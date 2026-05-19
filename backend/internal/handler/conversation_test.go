package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/opentheone/opentheone/backend/internal/model"
)

// fixtureTwoUsersWithConversations creates two users each with one binding +
// one conversation. Returns (aliceUserID, aliceConvID, bobUserID, bobConvID).
func fixtureTwoUsersWithConversations(t *testing.T, h *ConversationHandler) (string, string, string, string) {
	t.Helper()
	alice := model.User{Username: "alice-conv", PasswordHash: "x", Role: "user"}
	bob := model.User{Username: "bob-conv", PasswordHash: "x", Role: "user"}
	if err := h.db.Create(&alice).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&bob).Error; err != nil {
		t.Fatal(err)
	}
	pAlice := model.Persona{UserID: alice.ID, Name: "pA"}
	pBob := model.Persona{UserID: bob.ID, Name: "pB"}
	if err := h.db.Create(&pAlice).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&pBob).Error; err != nil {
		t.Fatal(err)
	}
	bindA := model.WeChatBinding{UserID: alice.ID, PersonaID: pAlice.ID, ILinkUserID: "wxa", State: "active"}
	bindB := model.WeChatBinding{UserID: bob.ID, PersonaID: pBob.ID, ILinkUserID: "wxb", State: "active"}
	if err := h.db.Create(&bindA).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&bindB).Error; err != nil {
		t.Fatal(err)
	}
	convA := model.Conversation{BindingID: bindA.ID, ILinkUserID: "peerA"}
	convB := model.Conversation{BindingID: bindB.ID, ILinkUserID: "peerB"}
	if err := h.db.Create(&convA).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&convB).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&model.Message{ConversationID: convA.ID, Direction: "inbound", Text: "alice's secret"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&model.Message{ConversationID: convB.ID, Direction: "inbound", Text: "bob's secret"}).Error; err != nil {
		t.Fatal(err)
	}
	return alice.ID, convA.ID, bob.ID, convB.ID
}

func callAsUser(handler gin.HandlerFunc, userID string, body any) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("auth.user_id", userID)
	handler(c)
	return w
}

// SECURITY: a conversation owned by Bob must never be readable when Alice's
// JWT is presented. The current code uses userOwnsConversation() raw SQL,
// which depends on the gorm table name conventions matching reality —
// this test guards against any future schema rename silently breaking that
// check (which would be a hard data leak).
func TestConversation_Messages_CrossUserDenied(t *testing.T) {
	db := qaNewDB(t)
	h := NewConversationHandler(db, nil)
	aliceID, _, _, bobConvID := fixtureTwoUsersWithConversations(t, h)

	w := callAsUser(h.Messages, aliceID, map[string]any{"conversation_id": bobConvID})
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-user read: status %d want 404, body=%s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("bob's secret")) {
		t.Errorf("DATA LEAK: response contains bob's text\n%s", w.Body.String())
	}
}

// Sanity check: Alice CAN read her own conversation. Otherwise the previous
// test could pass for the wrong reason (e.g. a bug that 404s everything).
func TestConversation_Messages_OwnAllowed(t *testing.T) {
	db := qaNewDB(t)
	h := NewConversationHandler(db, nil)
	aliceID, aliceConvID, _, _ := fixtureTwoUsersWithConversations(t, h)

	w := callAsUser(h.Messages, aliceID, map[string]any{"conversation_id": aliceConvID})
	if w.Code != http.StatusOK {
		t.Errorf("own-conv read: status %d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("alice's secret")) {
		t.Errorf("expected own message in response: %s", w.Body.String())
	}
}

// Cross-user mutation attempts (Delete / SendManual / RebuildSummary) must
// 404 too. We only exercise Delete here because it's the destructive one;
// a regression that misses cross-user ownership on Delete would let a logged-in
// user nuke arbitrary conversations.
func TestConversation_Delete_CrossUserDenied(t *testing.T) {
	db := qaNewDB(t)
	h := NewConversationHandler(db, nil)
	aliceID, _, _, bobConvID := fixtureTwoUsersWithConversations(t, h)

	w := callAsUser(h.Delete, aliceID, map[string]any{"conversation_id": bobConvID})
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-user delete: status %d want 404, body=%s", w.Code, w.Body.String())
	}
	// Bob's conversation must still exist.
	var c model.Conversation
	if err := db.Where("id = ?", bobConvID).First(&c).Error; err != nil {
		t.Errorf("bob's conversation was deleted: %v", err)
	}
}

// SendManual requires a context_token; without one it should return 400 with
// a hint that the peer must message first. This is the only failure mode that
// the bot operator can't recover from on their own, so the error message
// matters for UX.
func TestConversation_SendManual_NoContextToken(t *testing.T) {
	db := qaNewDB(t)
	h := NewConversationHandler(db, nil)
	aliceID, aliceConvID, _, _ := fixtureTwoUsersWithConversations(t, h)
	w := callAsUser(h.SendManual, aliceID, map[string]any{
		"conversation_id": aliceConvID,
		"text":            "hello from staff",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("no context token: status %d want 400, body=%s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("context_token")) {
		t.Errorf("expected actionable error mentioning context_token, got %s", w.Body.String())
	}
}

// SendManual with whitespace-only text → 400 "text required". This catches
// the (common) bug where the frontend sends "\n  \t" because the textarea
// wasn't trimmed.
func TestConversation_SendManual_BlankText(t *testing.T) {
	db := qaNewDB(t)
	h := NewConversationHandler(db, nil)
	aliceID, aliceConvID, _, _ := fixtureTwoUsersWithConversations(t, h)
	w := callAsUser(h.SendManual, aliceID, map[string]any{
		"conversation_id": aliceConvID,
		"text":            "   \n\t  ",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("blank text: status %d want 400 body=%s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("text required")) {
		t.Errorf("expected 'text required' error, got %s", w.Body.String())
	}
}
