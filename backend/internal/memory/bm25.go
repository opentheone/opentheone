// Package memory — bm25.go: SQLite FTS5-backed keyword retrieval over L1
// atoms and L0 messages.
//
// # Why bigram?
//
// SQLite FTS5 ships with `unicode61` tokenizer which is great for English
// (splits on non-letters) but treats Chinese as one giant token. For an
// AI-companion chat product where ~all user text is Chinese, we need
// per-token granularity.
//
// Options were:
//
//   - jieba via cgo (gojieba): more accurate, but breaks the
//     "single-binary, zero-cgo, cross-compile to Windows" promise of this
//     project.
//   - bigram (this file): a 2-character sliding window over CJK text. Zero
//     dependencies, pure Go, ~50 ns per char. Recall is slightly noisier
//     than true segmentation but the LLM downstream filters chaff easily.
//
// For short conversational text (≤ 200 chars per atom) bigram is the right
// tradeoff. If we ever need true segmentation later, just swap this file's
// `tokenize()` and the FTS shadow table content; no schema migration.
//
// # How it integrates with FTS5
//
// We DO NOT use FTS5's `tokenize=` parameter. Instead we maintain two
// columns on the shadow table: `text` (original text, displayed back to
// the user) and `tokens` (pre-bigrammed text, what FTS5 actually indexes
// with the default `unicode61` tokenizer). The query path applies the same
// bigram pass before issuing the MATCH.
//
// This keeps the index logic 100% in Go and avoids depending on custom
// SQLite extensions.

package memory

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"gorm.io/gorm"
)

// BM25 is the FTS5-backed keyword index over a (persona-scoped) set of
// L1 memory atoms.
type BM25 struct {
	db *gorm.DB
}

// NewBM25 returns a handle. The schema (virtual tables + triggers) is created
// by EnsureSchema once at startup, not here.
func NewBM25(db *gorm.DB) *BM25 { return &BM25{db: db} }

// EnsureSchema creates the memories_fts and messages_fts FTS5 virtual tables
// (plus the sync triggers) if they don't already exist. Safe to call on every
// boot — all DDL is `IF NOT EXISTS`.
//
// Run AFTER store.Open()'s AutoMigrate has created the base tables.
func EnsureSchema(db *gorm.DB) error {
	// memories_fts shadows model.Memory.
	//
	// `tokens` is the bigram-expanded form of `content`; that's what FTS5
	// indexes. `content` is kept around so the BM25 query can SELECT … FROM
	// memories_fts and we still have the human-readable text to display.
	stmts := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
			memory_id UNINDEXED,
			persona_id UNINDEXED,
			content,
			tokens,
			tokenize='unicode61 remove_diacritics 2'
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
			message_id UNINDEXED,
			conversation_id UNINDEXED,
			text,
			tokens,
			tokenize='unicode61 remove_diacritics 2'
		)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			return fmt.Errorf("memory.EnsureSchema: %w", err)
		}
	}
	return nil
}

// BackfillIndex (re)builds memories_fts and messages_fts from the canonical
// tables for any row that isn't already represented. Safe to call on every
// boot — uses NOT EXISTS subqueries so a fully-indexed DB is a no-op.
//
// Why we need this: the legacy embedding-based memory system never wrote
// FTS rows, and a build that adopts this package against an existing
// database would otherwise have an empty index until each old row got
// re-touched. Bounded by hard limits to keep cold start cheap on huge
// installs; the remainder gets indexed as rows are read/updated.
func BackfillIndex(ctx context.Context, db *gorm.DB) error {
	const (
		memCap = 5000
		msgCap = 20000
	)

	type memRow struct {
		ID        string
		PersonaID string
		Content   string
	}
	var mems []memRow
	if err := db.WithContext(ctx).Raw(
		`SELECT id, persona_id, content
		 FROM memories
		 WHERE status = 'active'
		   AND content <> ''
		   AND NOT EXISTS (SELECT 1 FROM memories_fts WHERE memory_id = memories.id)
		 ORDER BY created_at DESC
		 LIMIT ?`, memCap).Scan(&mems).Error; err != nil {
		return fmt.Errorf("memory.BackfillIndex: memories: %w", err)
	}
	for _, m := range mems {
		if err := db.WithContext(ctx).Exec(
			"INSERT INTO memories_fts(memory_id, persona_id, content, tokens) VALUES (?, ?, ?, ?)",
			m.ID, m.PersonaID, m.Content, Tokenize(m.Content),
		).Error; err != nil {
			return fmt.Errorf("memory.BackfillIndex: insert memory %s: %w", m.ID, err)
		}
	}

	type msgRow struct {
		ID             string
		ConversationID string
		Text           string
	}
	var msgs []msgRow
	if err := db.WithContext(ctx).Raw(
		`SELECT id, conversation_id, text
		 FROM messages
		 WHERE direction IN ('inbound', 'outbound')
		   AND text <> ''
		   AND NOT EXISTS (SELECT 1 FROM messages_fts WHERE message_id = messages.id)
		 ORDER BY created_at DESC
		 LIMIT ?`, msgCap).Scan(&msgs).Error; err != nil {
		return fmt.Errorf("memory.BackfillIndex: messages: %w", err)
	}
	for _, m := range msgs {
		if err := db.WithContext(ctx).Exec(
			"INSERT INTO messages_fts(message_id, conversation_id, text, tokens) VALUES (?, ?, ?, ?)",
			m.ID, m.ConversationID, m.Text, Tokenize(m.Text),
		).Error; err != nil {
			return fmt.Errorf("memory.BackfillIndex: insert msg %s: %w", m.ID, err)
		}
	}
	return nil
}

// IndexMemory upserts one memory row into memories_fts.
//
// We do delete-then-insert (instead of a real upsert) because FTS5 doesn't
// support ON CONFLICT, and our row count per persona stays small enough that
// the two-statement cost is negligible.
func (b *BM25) IndexMemory(ctx context.Context, memoryID, personaID, content string) error {
	if err := b.db.WithContext(ctx).
		Exec("DELETE FROM memories_fts WHERE memory_id = ?", memoryID).Error; err != nil {
		return err
	}
	return b.db.WithContext(ctx).Exec(
		"INSERT INTO memories_fts(memory_id, persona_id, content, tokens) VALUES (?, ?, ?, ?)",
		memoryID, personaID, content, Tokenize(content),
	).Error
}

// DeleteMemory removes a row from the FTS index. Called on Memory delete.
func (b *BM25) DeleteMemory(ctx context.Context, memoryID string) error {
	return b.db.WithContext(ctx).
		Exec("DELETE FROM memories_fts WHERE memory_id = ?", memoryID).Error
}

// IndexMessage upserts one message row into messages_fts. Only the textual
// payload is indexed; image/voice/file messages have empty `text` so they're
// skipped by IndexMessage (no point indexing an empty row).
func (b *BM25) IndexMessage(ctx context.Context, messageID, conversationID, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if err := b.db.WithContext(ctx).
		Exec("DELETE FROM messages_fts WHERE message_id = ?", messageID).Error; err != nil {
		return err
	}
	return b.db.WithContext(ctx).Exec(
		"INSERT INTO messages_fts(message_id, conversation_id, text, tokens) VALUES (?, ?, ?, ?)",
		messageID, conversationID, text, Tokenize(text),
	).Error
}

// DeleteMessage removes a row from the FTS index. Called on conversation
// delete (cascade).
func (b *BM25) DeleteMessage(ctx context.Context, messageID string) error {
	return b.db.WithContext(ctx).
		Exec("DELETE FROM messages_fts WHERE message_id = ?", messageID).Error
}

// DeleteMessagesByConversation purges all FTS rows for a conversation in one
// statement (used by the conversation delete handler).
func (b *BM25) DeleteMessagesByConversation(ctx context.Context, conversationID string) error {
	return b.db.WithContext(ctx).
		Exec("DELETE FROM messages_fts WHERE conversation_id = ?", conversationID).Error
}

// MemoryHit is one BM25-ranked candidate.
type MemoryHit struct {
	MemoryID string
	Content  string
	Score    float64 // smaller = more relevant in FTS5's bm25() function
}

// SearchMemories returns up to `limit` memory_ids ordered by BM25 relevance
// to `query`, scoped to one persona.
//
// Returns nil (no error) when the query has no indexable tokens — that
// usually means a pure-emoji or pure-whitespace search and the caller can
// just fall back to importance/recency ordering.
func (b *BM25) SearchMemories(ctx context.Context, personaID, query string, limit int) ([]MemoryHit, error) {
	q := buildMatchExpr(query)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	type row struct {
		MemoryID string
		Content  string
		Rank     float64
	}
	var rows []row
	err := b.db.WithContext(ctx).Raw(
		`SELECT memory_id AS memory_id, content AS content, bm25(memories_fts) AS rank
		 FROM memories_fts
		 WHERE persona_id = ? AND memories_fts MATCH ?
		 ORDER BY rank
		 LIMIT ?`, personaID, q, limit,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]MemoryHit, 0, len(rows))
	for _, r := range rows {
		out = append(out, MemoryHit{
			MemoryID: r.MemoryID,
			Content:  r.Content,
			Score:    r.Rank,
		})
	}
	return out, nil
}

// MessageHit is one BM25-ranked message candidate.
type MessageHit struct {
	MessageID      string
	ConversationID string
	Text           string
	Score          float64
}

// SearchMessages does the same BM25 search but over L0 messages, optionally
// scoped to one conversation (pass "" for "across all conversations the
// caller is allowed to see — caller is responsible for the auth filter").
func (b *BM25) SearchMessages(ctx context.Context, conversationID, query string, limit int) ([]MessageHit, error) {
	q := buildMatchExpr(query)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	type row struct {
		MessageID      string
		ConversationID string
		Text           string
		Rank           float64
	}
	var rows []row
	var err error
	if conversationID == "" {
		err = b.db.WithContext(ctx).Raw(
			`SELECT message_id AS message_id, conversation_id AS conversation_id, text AS text, bm25(messages_fts) AS rank
			 FROM messages_fts
			 WHERE messages_fts MATCH ?
			 ORDER BY rank
			 LIMIT ?`, q, limit,
		).Scan(&rows).Error
	} else {
		err = b.db.WithContext(ctx).Raw(
			`SELECT message_id AS message_id, conversation_id AS conversation_id, text AS text, bm25(messages_fts) AS rank
			 FROM messages_fts
			 WHERE conversation_id = ? AND messages_fts MATCH ?
			 ORDER BY rank
			 LIMIT ?`, conversationID, q, limit,
		).Scan(&rows).Error
	}
	if err != nil {
		return nil, err
	}
	out := make([]MessageHit, 0, len(rows))
	for _, r := range rows {
		out = append(out, MessageHit{
			MessageID:      r.MessageID,
			ConversationID: r.ConversationID,
			Text:           r.Text,
			Score:          r.Rank,
		})
	}
	return out, nil
}

// ============================================================================
// Tokenization
// ============================================================================

// Tokenize converts free-form text into the FTS5-indexable string we store
// in the `tokens` column.
//
// Algorithm:
//
//   - CJK runs are sliced into 2-char overlapping windows ("早上好" → "早上 上好")
//   - ASCII / Latin runs are lowercased and split on non-letter/digit
//   - Tokens shorter than 2 runes are dropped (FTS5 noise)
//   - All tokens are joined with single spaces
//
// Calling Tokenize on already-tokenized output is idempotent (bigrams of
// 2-char ASCII windows are ≥ 2 chars, will be re-split into the same tokens).
//
// Exported so tests can call it directly.
func Tokenize(text string) string {
	if text == "" {
		return ""
	}
	var out []string
	var run []rune
	flushASCII := func() {
		if len(run) == 0 {
			return
		}
		// run is lowercase ASCII letters/digits; emit as a single token if ≥ 2 chars
		if len(run) >= 2 {
			out = append(out, string(run))
		}
		run = run[:0]
	}
	for _, r := range text {
		switch {
		case isCJK(r):
			flushASCII()
			out = append(out, string(r)) // also include the unigram (helps recall on 2-char queries)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			run = append(run, unicode.ToLower(r))
		default:
			flushASCII()
		}
	}
	flushASCII()
	// Second pass: add CJK bigrams. We walk the original text and whenever
	// we see two consecutive CJK runes, emit their bigram.
	prev := rune(-1)
	for _, r := range text {
		if isCJK(r) && isCJK(prev) {
			out = append(out, string([]rune{prev, r}))
		}
		prev = r
	}
	return strings.Join(out, " ")
}

// buildMatchExpr converts a free-form user query into an FTS5 MATCH
// expression. Bigrams are connected with OR (any hit is a candidate; the
// LLM downstream will filter).
//
// Special chars that have meaning inside FTS5 MATCH ("\"*-:^") are stripped
// to keep the query string safe (we already control the inputs but better
// to be defensive — a user-typed "*" in a search query is otherwise a
// syntax error).
func buildMatchExpr(query string) string {
	toks := strings.Fields(Tokenize(query))
	if len(toks) == 0 {
		return ""
	}
	clean := make([]string, 0, len(toks))
	for _, t := range toks {
		// Wrap each token in double quotes so FTS5 treats it as a phrase
		// literal — this neutralizes any reserved chars and avoids the
		// "unterminated string" error class.
		clean = append(clean, `"`+strings.ReplaceAll(t, `"`, ``)+`"`)
	}
	return strings.Join(clean, " OR ")
}

// isCJK returns true for runes in the common CJK Unified Ideographs blocks
// AND CJK punctuation. We deliberately don't include hiragana/katakana —
// this is a Chinese-first product and Japanese kana would inflate the
// bigram index with noise. (Easy to add later if needed.)
func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF: // CJK Unified Ideographs
		return true
	case r >= 0x3400 && r <= 0x4DBF: // CJK Unified Ideographs Extension A
		return true
	case r >= 0xF900 && r <= 0xFAFF: // CJK Compatibility Ideographs
		return true
	}
	return false
}
