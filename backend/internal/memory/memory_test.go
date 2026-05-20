package memory

import (
	"context"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/model"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSchema(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestTokenize_Bigram(t *testing.T) {
	got := Tokenize("早上好abc")
	// expect unigrams + bigrams + the ascii word
	want := map[string]bool{"早": true, "上": true, "好": true, "早上": true, "上好": true, "abc": true}
	for tok := range want {
		if !contains(got, tok) {
			t.Errorf("missing token %q in %q", tok, got)
		}
	}
}

func TestTokenize_EmptyAndShort(t *testing.T) {
	if Tokenize("") != "" {
		t.Error("empty in, empty out")
	}
	// single ASCII letter is dropped (< 2 chars), single CJK char is kept
	if got := Tokenize("a"); got != "" {
		t.Errorf("single ascii dropped: got %q", got)
	}
	if got := Tokenize("好"); got != "好" {
		t.Errorf("single CJK kept as unigram: got %q", got)
	}
}

func TestBuildMatchExpr_Quotes(t *testing.T) {
	q := buildMatchExpr("早上好")
	// should contain quoted CJK bigrams joined with OR
	if !contains(q, `"早上"`) || !contains(q, `"上好"`) {
		t.Errorf("missing bigrams in %q", q)
	}
	if !contains(q, " OR ") {
		t.Errorf("expected OR connector in %q", q)
	}
}

func TestNormaliseKind(t *testing.T) {
	cases := map[string]string{
		"persona":     "persona",
		"PERSONA":     "persona",
		"preference":  "persona",
		"fact":        "persona",
		"summary":     "persona",
		"event":       "episodic",
		"episodic":    "episodic",
		"instruction": "instruction",
		"rule":        "instruction",
		"unknown":     "persona",
		"":            "persona",
	}
	for in, want := range cases {
		if got := normaliseKind(in); got != want {
			t.Errorf("normaliseKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormaliseForDedup(t *testing.T) {
	a := normaliseForDedup("用户喜欢手冲咖啡。")
	b := normaliseForDedup("用户喜欢手冲咖啡")
	if a != b {
		t.Errorf("punctuation should not differ: %q vs %q", a, b)
	}
}

// TestRetrieve_FallsBackToImportanceOnNoBM25Hit verifies the cold path:
// query whose tokens don't overlap any indexed memory should still return
// the top-K by importance/recency (not error out).
func TestRetrieve_FallsBackToImportanceOnNoBM25Hit(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	now := time.Now()
	rows := []model.Memory{
		{PersonaID: "p1", Content: "用户喜欢手冲咖啡", Importance: 9, Status: "active", BaseModel: model.BaseModel{CreatedAt: now}},
		{PersonaID: "p1", Content: "用户养了一只猫", Importance: 5, Status: "active", BaseModel: model.BaseModel{CreatedAt: now.Add(-time.Hour)}},
		{PersonaID: "p1", Content: "用户是产品经理", Importance: 7, Status: "active", BaseModel: model.BaseModel{CreatedAt: now.Add(-2 * time.Hour)}},
		{PersonaID: "p2", Content: "另一个 persona", Importance: 10, Status: "active", BaseModel: model.BaseModel{CreatedAt: now}},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
		_ = svc.bm25.IndexMemory(context.Background(), rows[i].ID, rows[i].PersonaID, rows[i].Content)
	}

	// Query in English — no overlap with the CJK bigram index → fallback.
	got, err := svc.RetrieveForConversation(context.Background(), nil, "p1", "", "english query", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	if got[0].PersonaID != "p1" {
		t.Errorf("leaked persona: %+v", got[0])
	}
	if got[0].Importance < got[2].Importance {
		t.Errorf("not sorted by importance desc: %+v", got)
	}
}

// TestRetrieve_BM25HitWins verifies the keyword path: a query that hits
// the FTS index should return that atom even if its importance is lower
// than other candidates.
func TestRetrieve_BM25HitWins(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	high := model.Memory{PersonaID: "p1", Content: "用户养了一只猫", Importance: 9, Status: "active"}
	low := model.Memory{PersonaID: "p1", Content: "用户喜欢手冲咖啡", Importance: 3, Status: "active"}
	if err := db.Create(&high).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&low).Error; err != nil {
		t.Fatal(err)
	}
	_ = svc.bm25.IndexMemory(context.Background(), high.ID, high.PersonaID, high.Content)
	_ = svc.bm25.IndexMemory(context.Background(), low.ID, low.PersonaID, low.Content)

	got, err := svc.RetrieveForConversation(context.Background(), nil, "p1", "", "手冲咖啡", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least 1 hit")
	}
	if got[0].ID != low.ID {
		t.Errorf("BM25 hit should win, got %+v", got[0])
	}
}

// TestIngestManual_DedupsExactMatch ensures re-adding the same atom bumps
// importance instead of creating a duplicate row.
func TestIngestManual_DedupsExactMatch(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	if err := svc.IngestManual(ctx, "p1", "persona", "用户喜欢手冲咖啡", 4); err != nil {
		t.Fatal(err)
	}
	if err := svc.IngestManual(ctx, "p1", "persona", "用户喜欢手冲咖啡", 9); err != nil {
		t.Fatal(err)
	}

	var rows []model.Memory
	if err := db.Where("persona_id = ?", "p1").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 dedup'd row, got %d", len(rows))
	}
	if rows[0].Importance != 9 {
		t.Errorf("expected importance bumped to 9, got %d", rows[0].Importance)
	}
}

// TestIngestManual_ImportanceClampedOutOfRange covers the 1-10 clamp.
func TestIngestManual_ImportanceClampedOutOfRange(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	if err := svc.IngestManual(context.Background(), "p1", "persona", "用户喜欢徒步", -5); err != nil {
		t.Fatal(err)
	}
	if err := svc.IngestManual(context.Background(), "p1", "persona", "用户养鱼", 99); err != nil {
		t.Fatal(err)
	}
	var rows []model.Memory
	_ = db.Where("persona_id = ?", "p1").Order("content").Find(&rows).Error
	if len(rows) != 2 {
		t.Fatalf("got %d", len(rows))
	}
	for _, r := range rows {
		if r.Importance < 1 || r.Importance > 10 {
			t.Errorf("not clamped: %+v", r)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
