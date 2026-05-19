package handler

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/opentheone/opentheone/backend/internal/model"
)

func qaNewLLMHandler(t *testing.T) (*LLMHandler, string) {
	t.Helper()
	db := qaNewDB(t)
	u := model.User{Username: "alice-llm", PasswordHash: "x", Role: "user"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	return NewLLMHandler(db, "test-secret-key-1234567890abc"), u.ID
}

// Delete must refuse to drop an LLM config that a persona is still pointing to,
// otherwise the persona ends up with a dangling llm_config_id and silent
// runtime failures on the next reply.
func TestLLM_Delete_RejectsReferenced(t *testing.T) {
	h, uid := qaNewLLMHandler(t)
	cfg := model.LLMConfig{UserID: uid, Name: "n", BaseURL: "https://x", ChatModel: "m", IsDefault: true}
	if err := h.db.Create(&cfg).Error; err != nil {
		t.Fatal(err)
	}
	p := model.Persona{UserID: uid, Name: "p", LLMConfigID: cfg.ID}
	if err := h.db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	w := callAsUser(h.Delete, uid, map[string]any{"id": cfg.ID})
	if w.Code != http.StatusConflict {
		t.Errorf("delete referenced: status %d want 409 body=%s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(p.Name)) {
		t.Errorf("expected persona name in error message, got %s", w.Body.String())
	}
}

// Promoting one config to is_default must demote every other row for the same
// user, so the "default lookup" invariant (`is_default = TRUE` is unique per
// user) holds. Without this, RebuildSummary / proactive sends could pick a
// stale provider.
func TestLLM_Update_TogglesDefault(t *testing.T) {
	h, uid := qaNewLLMHandler(t)
	a := model.LLMConfig{UserID: uid, Name: "A", BaseURL: "https://a", ChatModel: "m", IsDefault: true}
	b := model.LLMConfig{UserID: uid, Name: "B", BaseURL: "https://b", ChatModel: "m"}
	if err := h.db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&b).Error; err != nil {
		t.Fatal(err)
	}
	w := callAsUser(h.Update, uid, map[string]any{"id": b.ID, "is_default": true})
	if w.Code != http.StatusOK {
		t.Fatalf("update: status %d body=%s", w.Code, w.Body.String())
	}
	var defaults int64
	if err := h.db.Model(&model.LLMConfig{}).Where("user_id = ? AND is_default = ?", uid, true).Count(&defaults).Error; err != nil {
		t.Fatal(err)
	}
	if defaults != 1 {
		t.Errorf("expected exactly 1 default after toggle, got %d", defaults)
	}
	var freshA model.LLMConfig
	_ = h.db.Where("id = ?", a.ID).First(&freshA).Error
	if freshA.IsDefault {
		t.Errorf("config A should be demoted")
	}
}

// Create rejects missing required fields.
func TestLLM_Create_Required(t *testing.T) {
	h, uid := qaNewLLMHandler(t)
	cases := []struct {
		body map[string]any
		hint string
	}{
		{map[string]any{"base_url": "https://x", "chat_model": "m"}, "missing name"},
		{map[string]any{"name": "n", "chat_model": "m"}, "missing base_url"},
		{map[string]any{"name": "n", "base_url": "https://x"}, "missing chat_model"},
	}
	for _, tc := range cases {
		w := callAsUser(h.Create, uid, tc.body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d want 400 body=%s", tc.hint, w.Code, w.Body.String())
		}
	}
}

// Cross-user Delete: Bob's config must not be deletable from Alice's session.
// Without the user_id scope, an attacker with any account could enumerate IDs
// and unblock conflict checks for other tenants.
func TestLLM_Delete_CrossUserDenied(t *testing.T) {
	h, aliceID := qaNewLLMHandler(t)
	bob := model.User{Username: "bob-llm", PasswordHash: "x", Role: "user"}
	if err := h.db.Create(&bob).Error; err != nil {
		t.Fatal(err)
	}
	bobCfg := model.LLMConfig{UserID: bob.ID, Name: "bob-cfg", BaseURL: "https://x", ChatModel: "m"}
	if err := h.db.Create(&bobCfg).Error; err != nil {
		t.Fatal(err)
	}
	_ = callAsUser(h.Delete, aliceID, map[string]any{"id": bobCfg.ID})
	// We don't assert status (the handler returns 200 because no rows match the
	// WHERE clause — that's actually fine, callers can't tell the difference);
	// we DO assert that Bob's row survives.
	var c model.LLMConfig
	if err := h.db.Where("id = ?", bobCfg.ID).First(&c).Error; err != nil {
		t.Errorf("bob's config got deleted via cross-user call: %v", err)
	}
}
