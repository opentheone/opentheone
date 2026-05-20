// Package memory implements the long-term memory pyramid:
//
//	L0 Conversation  →  L1 Atom  →  L2 Scene  →  L3 User Profile
//
// All retrieval goes through BM25 (no vector embeddings — the user only
// needs a chat-capable LLM). The LLM itself does:
//
//   - L1 extraction (raw messages → persona / episodic / instruction atoms)
//   - L1 dedup (batch store / skip / update / merge decisions)
//   - L2 scene grouping (which atom belongs to which thematic block, with a
//     hard cap of 15 scenes per persona)
//   - L3 profile synthesis (a single markdown narrative of the user, ≤2000
//     chars, re-generated on threshold / cold-start / explicit request)
//
// `memory.Service` is the public surface used by the engine and HTTP
// handlers. The extractor / dedup / scene / profile helpers in their own
// files do the heavy LLM lifting and call back into Service for storage.
package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/llm"
	"github.com/opentheone/opentheone/backend/internal/model"
)

// candidateLimit caps the SQL pre-filter when no BM25 hit narrows the set.
// At ~100 bytes per row, 300 rows ≈ 30 KB working set — trivial.
const candidateLimit = 300

// dedupCandidateLimit is the per-new-atom cap when assembling the unified
// candidate pool we feed into the LLM dedup prompt. Keeps the prompt token
// budget bounded even if the user has thousands of accumulated atoms.
const dedupCandidateLimit = 8

// Service is the long-term memory layer.
type Service struct {
	db   *gorm.DB
	bm25 *BM25
}

// NewService returns a memory.Service. EnsureSchema must have been called on
// the same DB before any Ingest/Retrieve call (store.Open does this for us).
func NewService(db *gorm.DB) *Service {
	return &Service{db: db, bm25: NewBM25(db)}
}

// DB exposes the underlying GORM handle for helpers in this package
// (extractor / dedup / scene / profile) without forcing them all through
// Service methods. Not exported because we don't want external callers
// reaching around the abstraction.
func (s *Service) DB() *gorm.DB { return s.db }

// BM25 exposes the FTS5 index handle for the same reason.
func (s *Service) BM25() *BM25 { return s.bm25 }

// ============================================================================
// Retrieval
// ============================================================================

// RetrieveForConversation returns up to `topK` memory atoms most relevant to
// `query`, scoped to one persona. The scoring combines:
//
//   - BM25 keyword match (primary signal — comes from FTS5 over the
//     bigram-tokenized `tokens` column)
//   - importance boost (+0.05 × importance, 1-10 scale)
//   - conversation locality (+0.3 when the atom was last reinforced in this
//     conversation — promotes "what you just told me five turns ago")
//   - age decay (× 0.85 once the atom is > 30 days old; gentle, not punishing)
//
// When BM25 returns nothing (no overlap between query tokens and index),
// we fall back to importance/recency ordering — the user still gets a
// "highest-importance memories" injection, just not tailored to the query.
//
// `llmClient` is accepted for API compatibility with the old Mem0-style
// implementation but is no longer used at the retrieval path. We keep it on
// the signature so engine.go doesn't need to thread a different value in
// just for retrieval; future LLM-based reranking can drop in here without
// another signature churn.
func (s *Service) RetrieveForConversation(ctx context.Context, llmClient *llm.Client, personaID, conversationID, query string, topK int) ([]model.Memory, error) {
	_ = llmClient // see godoc above
	if topK <= 0 {
		topK = 5
	}
	hits, err := s.bm25.SearchMemories(ctx, personaID, query, topK*4)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		// Cold path: no keyword overlap. Return top-K by importance/recency
		// so the LLM still gets _something_ resembling the user's profile.
		var rows []model.Memory
		if err := s.db.WithContext(ctx).
			Where("persona_id = ? AND status = ?", personaID, "active").
			Order("importance desc, created_at desc").
			Limit(topK).
			Find(&rows).Error; err != nil {
			return nil, err
		}
		return rows, nil
	}

	// Hydrate hit IDs into full Memory rows. We need the boost-relevant
	// columns (importance, conversation_id, created_at).
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.MemoryID)
	}
	var rows []model.Memory
	if err := s.db.WithContext(ctx).
		Where("id IN ? AND status = ?", ids, "active").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	byID := make(map[string]*model.Memory, len(rows))
	for i := range rows {
		byID[rows[i].ID] = &rows[i]
	}

	now := time.Now()
	type scored struct {
		mem   *model.Memory
		score float64
	}
	out := make([]scored, 0, len(hits))
	for _, h := range hits {
		m := byID[h.MemoryID]
		if m == nil {
			// Index ↔ DB drift (deleted row not yet expunged from FTS).
			// Self-heal so the next query doesn't pay the same penalty.
			_ = s.bm25.DeleteMemory(ctx, h.MemoryID)
			continue
		}
		// FTS5 bm25() returns a NEGATIVE score where smaller (more
		// negative) = more relevant. Convert to "larger = better" before
		// boosting so the boost math reads naturally.
		score := -h.Score
		score += 0.05 * float64(m.Importance)
		if conversationID != "" && m.ConversationID == conversationID {
			score += 0.3
		}
		if age := now.Sub(m.CreatedAt); age > 30*24*time.Hour {
			score *= 0.85
		}
		out = append(out, scored{mem: m, score: score})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].score > out[j].score })
	if len(out) > topK {
		out = out[:topK]
	}
	result := make([]model.Memory, 0, len(out))
	for _, s := range out {
		result = append(result, *s.mem)
	}
	return result, nil
}

// Retrieve is the unscoped flavour (no conversation locality boost) — kept
// for API surface compatibility with the prior implementation.
func (s *Service) Retrieve(ctx context.Context, llmClient *llm.Client, personaID, query string, topK int) ([]model.Memory, error) {
	return s.RetrieveForConversation(ctx, llmClient, personaID, "", query, topK)
}

// SearchMemories is the public BM25-search surface — used by the built-in
// `memory_search` tool the LLM can call mid-turn.
func (s *Service) SearchMemories(ctx context.Context, personaID, query string, limit int) ([]model.Memory, error) {
	if limit <= 0 {
		limit = 10
	}
	hits, err := s.bm25.SearchMemories(ctx, personaID, query, limit)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.MemoryID)
	}
	var rows []model.Memory
	if err := s.db.WithContext(ctx).
		Where("id IN ? AND status = ?", ids, "active").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	// Preserve BM25 ordering rather than DB row order.
	byID := make(map[string]*model.Memory, len(rows))
	for i := range rows {
		byID[rows[i].ID] = &rows[i]
	}
	out := make([]model.Memory, 0, len(hits))
	for _, h := range hits {
		if m := byID[h.MemoryID]; m != nil {
			out = append(out, *m)
		}
	}
	return out, nil
}

// ============================================================================
// Ingest (manual + extractor entry point)
// ============================================================================

// IngestManual stores a single atom from the manual-add HTTP endpoint.
// No LLM round-trip, just BM25-based "looks like a near-duplicate" check
// — fast and predictable for UI use.
func (s *Service) IngestManual(ctx context.Context, personaID, kind, content string, importance int) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return errors.New("memory: content required")
	}
	if importance <= 0 || importance > 10 {
		importance = 5
	}
	kind = normaliseKind(kind)

	// Cheap BM25 dedup: if the top hit's first 40 chars exactly match the
	// incoming text, treat as duplicate and bump importance instead of
	// creating a second row. (Strict-match is intentional — the LLM dedup
	// path in the auto-extract pipeline does the smarter version.)
	hits, _ := s.bm25.SearchMemories(ctx, personaID, content, 3)
	for _, h := range hits {
		if normaliseForDedup(h.Content) == normaliseForDedup(content) {
			return s.db.WithContext(ctx).Model(&model.Memory{}).
				Where("id = ?", h.MemoryID).
				Updates(map[string]interface{}{
					"importance": gormGreater("importance", importance),
				}).Error
		}
	}

	mem := model.Memory{
		PersonaID:  personaID,
		Kind:       kind,
		Content:    content,
		Importance: importance,
		Status:     "active",
		Metadata:   "{}",
	}
	if err := s.db.WithContext(ctx).Create(&mem).Error; err != nil {
		return err
	}
	if err := s.bm25.IndexMemory(ctx, mem.ID, personaID, content); err != nil {
		// Index failure mustn't lose the row — log via error return; caller
		// usually treats this as best-effort.
		return fmt.Errorf("memory: index manual atom: %w", err)
	}
	return nil
}

// InsertAtoms writes a batch of new atoms produced by the extractor/dedup
// pipeline. Each atom has already been routed through the LLM dedup decision
// — so this is a straight INSERT + FTS index pass, no per-row similarity
// check.
//
// Returns the IDs of the rows that were actually created (some may have
// been dropped due to empty content as a safety net).
func (s *Service) InsertAtoms(ctx context.Context, atoms []AtomInsert) ([]string, error) {
	out := make([]string, 0, len(atoms))
	for _, a := range atoms {
		content := strings.TrimSpace(a.Content)
		if content == "" {
			continue
		}
		kind := normaliseKind(a.Kind)
		imp := a.Importance
		if imp <= 0 || imp > 10 {
			imp = 5
		}
		mem := model.Memory{
			PersonaID:       a.PersonaID,
			ConversationID:  a.ConversationID,
			Kind:            kind,
			Content:         content,
			Importance:      imp,
			SourceMessageID: a.SourceMessageID,
			ActivityStart:   a.ActivityStart,
			ActivityEnd:     a.ActivityEnd,
			Metadata:        a.Metadata,
			Status:          "active",
		}
		if mem.Metadata == "" {
			mem.Metadata = "{}"
		}
		if err := s.db.WithContext(ctx).Create(&mem).Error; err != nil {
			return out, err
		}
		_ = s.bm25.IndexMemory(ctx, mem.ID, a.PersonaID, content)
		out = append(out, mem.ID)
	}
	return out, nil
}

// AtomInsert is the value shape used by InsertAtoms — kept in this package
// (not the model package) so it doesn't leak to non-memory callers.
type AtomInsert struct {
	PersonaID       string
	ConversationID  string
	SourceMessageID string
	Kind            string
	Content         string
	Importance      int
	ActivityStart   *time.Time
	ActivityEnd     *time.Time
	Metadata        string
}

// UpdateAtomContent overwrites an atom's content (used by dedup `merge` /
// `update` actions) and re-indexes the FTS row.
func (s *Service) UpdateAtomContent(ctx context.Context, memoryID, newContent string, newImportance int, newKind string) error {
	newContent = strings.TrimSpace(newContent)
	if newContent == "" {
		return errors.New("memory: cannot update to empty content")
	}
	updates := map[string]interface{}{
		"content": newContent,
	}
	if newImportance > 0 && newImportance <= 10 {
		updates["importance"] = newImportance
	}
	if newKind != "" {
		updates["kind"] = normaliseKind(newKind)
	}
	var existing model.Memory
	if err := s.db.WithContext(ctx).Where("id = ?", memoryID).First(&existing).Error; err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Model(&existing).Updates(updates).Error; err != nil {
		return err
	}
	return s.bm25.IndexMemory(ctx, memoryID, existing.PersonaID, newContent)
}

// SupersedeAtoms marks a set of old atoms as superseded by a new one. The
// FTS rows for the superseded atoms are dropped so retrieval ignores them
// even if the SQL query forgets the status filter.
func (s *Service) SupersedeAtoms(ctx context.Context, supersededIDs []string, newID string) error {
	if len(supersededIDs) == 0 {
		return nil
	}
	if err := s.db.WithContext(ctx).Model(&model.Memory{}).
		Where("id IN ?", supersededIDs).
		Updates(map[string]interface{}{
			"status":        "superseded",
			"superseded_by": newID,
		}).Error; err != nil {
		return err
	}
	for _, id := range supersededIDs {
		_ = s.bm25.DeleteMemory(ctx, id)
	}
	return nil
}

// CandidatesForDedup builds the unified candidate pool for the LLM dedup
// prompt: union of BM25 hits across all new-atom contents, deduplicated by
// memory_id, capped at `dedupCandidateLimit × len(newAtoms)`.
func (s *Service) CandidatesForDedup(ctx context.Context, personaID string, newContents []string) ([]model.Memory, error) {
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	for _, c := range newContents {
		hits, err := s.bm25.SearchMemories(ctx, personaID, c, dedupCandidateLimit)
		if err != nil {
			return nil, err
		}
		for _, h := range hits {
			if _, ok := seen[h.MemoryID]; ok {
				continue
			}
			seen[h.MemoryID] = struct{}{}
			ids = append(ids, h.MemoryID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []model.Memory
	err := s.db.WithContext(ctx).
		Where("id IN ? AND status = ?", ids, "active").
		Find(&rows).Error
	return rows, err
}

// ============================================================================
// Helpers
// ============================================================================

// normaliseKind maps legacy / shorthand kinds onto the canonical set
// {persona, episodic, instruction}. Unknown values fall through to
// `persona` (the safest "generic stable attribute" bucket).
func normaliseKind(k string) string {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "persona", "preference", "fact":
		return "persona"
	case "episodic", "event":
		return "episodic"
	case "instruction", "rule":
		return "instruction"
	case "summary":
		// Legacy summaries are kept as `persona` since the manual taxonomy
		// is the closest semantic match (general user-related notes).
		return "persona"
	}
	return "persona"
}

// normaliseForDedup squeezes a memory string into a "loose-equality"
// canonical form for the manual-add dedup path. We strip whitespace and
// punctuation, lowercase ASCII, and truncate to 80 runes so two atoms
// that differ only in trailing chitchat are still caught.
func normaliseForDedup(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		if r == '.' || r == ',' || r == '!' || r == '?' || r == ';' ||
			r == '：' || r == '，' || r == '。' || r == '！' || r == '？' {
			continue
		}
		b.WriteRune(toLowerASCII(r))
	}
	out := b.String()
	if len([]rune(out)) > 80 {
		runes := []rune(out)
		out = string(runes[:80])
	}
	return out
}

func toLowerASCII(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}

// gormGreater returns a GORM expression that updates a column to the greater
// of (its current value, candidate). Used by IngestManual to "bump importance
// up, never down" when re-adding the same atom.
func gormGreater(col string, candidate int) interface{} {
	return gorm.Expr("CASE WHEN ? > "+col+" THEN ? ELSE "+col+" END", candidate, candidate)
}
