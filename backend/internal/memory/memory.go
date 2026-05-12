package memory

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wzyjerry/opentheone/backend/internal/llm"
	"github.com/wzyjerry/opentheone/backend/internal/model"
)

// Service is the long-term memory layer (extract / dedupe / retrieve).
type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// encodeVector serializes []float32 to little-endian bytes.
func encodeVector(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(x))
	}
	return buf
}

func decodeVector(b []byte) []float32 {
	n := len(b) / 4
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// candidateLimit caps the SQL pre-filter so brute-force cosine stays cheap.
// At 1536-dim float32, 300 rows ≈ 1.8MB working set — plenty for typical user
// scale without needing sqlite-vec / faiss.
const candidateLimit = 300

// Retrieve returns up to topK memories most relevant to query, scoped to the
// given persona. Same as RetrieveForConversation with no conversation boost.
func (s *Service) Retrieve(ctx context.Context, llmClient *llm.Client, personaID, query string, topK int) ([]model.Memory, error) {
	return s.RetrieveForConversation(ctx, llmClient, personaID, "", query, topK)
}

// RetrieveForConversation does Mem0-style hybrid retrieval:
//  1. SQL pre-filter by persona_id, ordered by importance + recency (no full table scan).
//  2. In-memory cosine on the up-to-`candidateLimit` candidates, with two boosts:
//     - importance: +0.02 * importance (already validated in prior rounds)
//     - same-conversation locality: +0.05 when m.conversation_id == currentConversationID
//     - age decay: ×0.95 once a memory is older than 30 days (gentle, not punishing)
//
// When the LLM client has no embedding model, falls back to importance + recency.
func (s *Service) RetrieveForConversation(ctx context.Context, llmClient *llm.Client, personaID, conversationID, query string, topK int) ([]model.Memory, error) {
	if topK <= 0 {
		topK = 5
	}
	// Pre-filter at SQL level. Order: importance desc, then created_at desc.
	// The composite sort lets the index on (persona_id, created_at) still help
	// because we filter by persona_id; the importance tie-breaker is cheap.
	var rows []model.Memory
	if err := s.db.WithContext(ctx).
		Where("persona_id = ?", personaID).
		Order("importance desc, created_at desc").
		Limit(candidateLimit).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if llmClient == nil {
		return rows[:minInt(topK, len(rows))], nil
	}
	qv, err := llmClient.Embed(ctx, query)
	if err != nil {
		return rows[:minInt(topK, len(rows))], nil
	}

	now := time.Now()
	type scored struct {
		idx   int
		score float64
	}
	ranked := make([]scored, 0, len(rows))
	for i := range rows {
		if len(rows[i].Embedding) == 0 {
			continue
		}
		v := decodeVector(rows[i].Embedding)
		sc := cosine(qv, v)
		sc += 0.02 * float64(rows[i].Importance)
		if conversationID != "" && rows[i].ConversationID == conversationID {
			sc += 0.05
		}
		if age := now.Sub(rows[i].CreatedAt); age > 30*24*time.Hour {
			sc *= 0.95
		}
		ranked = append(ranked, scored{i, sc})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	limit := minInt(topK, len(ranked))
	out := make([]model.Memory, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, rows[ranked[i].idx])
	}
	return out, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Ingest pushes a memory bullet point: embed → dedupe → upsert.
func (s *Service) Ingest(ctx context.Context, llmClient *llm.Client, personaID, conversationID, sourceMessageID, kind, content string, importance int) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	if importance <= 0 {
		importance = 5
	}
	var emb []byte
	if llmClient != nil {
		v, err := llmClient.Embed(ctx, content)
		if err == nil {
			emb = encodeVector(v)
		}
	}

	if len(emb) > 0 {
		// Bound the dedupe candidate set the same way Retrieve does. Beyond
		// `candidateLimit`, a brand-new fact is statistically very unlikely to
		// be a near-duplicate of an item we'd find only by scanning the long
		// tail of low-importance / old memories.
		var existing []model.Memory
		if err := s.db.WithContext(ctx).
			Where("persona_id = ?", personaID).
			Order("importance desc, created_at desc").
			Limit(candidateLimit).
			Find(&existing).Error; err != nil {
			return err
		}
		nv := decodeVector(emb)
		for _, m := range existing {
			if len(m.Embedding) == 0 {
				continue
			}
			sim := cosine(nv, decodeVector(m.Embedding))
			switch {
			case sim >= 0.92:
				return nil
			case sim >= 0.78:
				updated := m.Content + "\n• " + content
				return s.db.WithContext(ctx).Model(&model.Memory{}).
					Where("id = ?", m.ID).
					Updates(map[string]interface{}{
						"content":    updated,
						"importance": maxInt(m.Importance, importance),
					}).Error
			}
		}
	}

	mem := model.Memory{
		PersonaID:       personaID,
		ConversationID:  conversationID,
		Kind:            kind,
		Content:         content,
		Embedding:       emb,
		Importance:      importance,
		SourceMessageID: sourceMessageID,
	}
	return s.db.WithContext(ctx).Create(&mem).Error
}

// FactItem is one bullet produced by ExtractFacts: a short third-person fact
// (or preference / event / summary) plus an importance score 1–10.
type FactItem struct {
	Kind       string `json:"kind"`
	Content    string `json:"content"`
	Importance int    `json:"importance"`
}

const extractSystem = `You are an information-extraction module for a long-term memory.
Given a short snippet of conversation between USER and ASSISTANT, output ONLY a JSON array
of memory bullets that should be remembered LONG-TERM about the USER (preferences, facts,
events, relationships, plans). DO NOT include trivial chit-chat.
Each bullet:
{"kind":"fact|preference|event|summary", "content":"... short third-person sentence ...", "importance": 1-10}
If nothing is worth remembering output [].`

func (s *Service) ExtractFacts(ctx context.Context, llmClient *llm.Client, snippet string) ([]FactItem, error) {
	if llmClient == nil {
		return nil, errors.New("memory: llm client required for extraction")
	}
	reply, err := llmClient.Chat(ctx, []llm.ChatMessage{
		{Role: "system", Content: extractSystem},
		{Role: "user", Content: snippet},
	})
	if err != nil {
		return nil, err
	}
	reply = strings.TrimSpace(reply)
	reply = stripCodeFence(reply)
	if reply == "" || reply == "[]" {
		return nil, nil
	}
	var items []FactItem
	if err := json.Unmarshal([]byte(reply), &items); err != nil {
		return nil, fmt.Errorf("memory: cannot parse facts %q: %w", reply, err)
	}
	return items, nil
}

// stripCodeFence removes a single leading/trailing ``` block.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		nl := strings.Index(s, "\n")
		if nl >= 0 {
			s = s[nl+1:]
		}
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	return s
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
