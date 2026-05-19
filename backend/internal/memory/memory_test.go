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
	if err := db.AutoMigrate(&model.Memory{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestCosine_KnownValues(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	c := []float32{0, 1, 0}
	d := []float32{-1, 0, 0}

	if v := cosine(a, b); v < 0.999 || v > 1.001 {
		t.Errorf("cosine(a,b) = %v, want 1.0", v)
	}
	if v := cosine(a, c); v < -0.001 || v > 0.001 {
		t.Errorf("cosine(a,c) = %v, want 0", v)
	}
	if v := cosine(a, d); v > -0.999 || v < -1.001 {
		t.Errorf("cosine(a,d) = %v, want -1", v)
	}
}

func TestCosine_SafeOnDegenerate(t *testing.T) {
	if cosine(nil, nil) != 0 {
		t.Error("cosine of two empty vectors should be 0")
	}
	if cosine([]float32{0, 0}, []float32{1, 1}) != 0 {
		t.Error("cosine of zero vector should be 0")
	}
	if cosine([]float32{1, 0}, []float32{1, 0, 0}) != 0 {
		t.Error("cosine of mismatched dims should be 0")
	}
}

func TestEncodeDecodeVectorRoundTrip(t *testing.T) {
	in := []float32{1.5, -2.25, 0, 3.0e-3}
	out := decodeVector(encodeVector(in))
	if len(out) != len(in) {
		t.Fatalf("len mismatch: %d vs %d", len(out), len(in))
	}
	for i := range in {
		if in[i] != out[i] {
			t.Errorf("idx %d: %v vs %v", i, in[i], out[i])
		}
	}
}

// TestRetrieve_FallsBackToRecencyWithoutLLM verifies that when llmClient==nil
// we get importance+recency ordering and respect topK without crashing.
func TestRetrieve_FallsBackToRecencyWithoutLLM(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	rows := []model.Memory{
		{PersonaID: "p1", Content: "old low", Importance: 1, BaseModel: model.BaseModel{CreatedAt: now.Add(-72 * time.Hour)}},
		{PersonaID: "p1", Content: "new high", Importance: 9, BaseModel: model.BaseModel{CreatedAt: now}},
		{PersonaID: "p1", Content: "new low", Importance: 1, BaseModel: model.BaseModel{CreatedAt: now.Add(-time.Hour)}},
		{PersonaID: "p2", Content: "other persona", Importance: 9, BaseModel: model.BaseModel{CreatedAt: now}},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	svc := NewService(db)
	got, err := svc.Retrieve(context.Background(), nil, "p1", "ignored", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d, want 3 (p2 must be excluded)", len(got))
	}
	if got[0].Content != "new high" {
		t.Errorf("first should be high-importance, got %q", got[0].Content)
	}
	for _, m := range got {
		if m.PersonaID != "p1" {
			t.Errorf("leak: %q from other persona", m.Content)
		}
	}
}

func TestRetrieve_RespectsTopK(t *testing.T) {
	db := newTestDB(t)
	for i := 0; i < 10; i++ {
		_ = db.Create(&model.Memory{
			PersonaID:  "p1",
			Content:    "row",
			Importance: i,
		}).Error
	}
	svc := NewService(db)
	got, err := svc.Retrieve(context.Background(), nil, "p1", "q", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("topK ignored: got %d, want 3", len(got))
	}
}

func TestMinIntHelper(t *testing.T) {
	if minInt(1, 2) != 1 {
		t.Error("minInt(1,2)")
	}
	if minInt(2, 1) != 1 {
		t.Error("minInt(2,1)")
	}
	if minInt(5, 5) != 5 {
		t.Error("minInt(5,5)")
	}
}
