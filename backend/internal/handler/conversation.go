package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/engine"
	"github.com/opentheone/opentheone/backend/internal/model"
)

type ConversationHandler struct {
	db  *gorm.DB
	eng *engine.Engine
}

func NewConversationHandler(db *gorm.DB, eng *engine.Engine) *ConversationHandler {
	return &ConversationHandler{db: db, eng: eng}
}

type convListReq struct {
	BindingID string `json:"binding_id"`
	Limit     int    `json:"limit"`
}

func (h *ConversationHandler) List(c *gin.Context) {
	uid := currentUserID(c)
	var req convListReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if req.Limit <= 0 || req.Limit > 200 {
		req.Limit = 50
	}
	var bindingIDs []string
	if req.BindingID != "" {
		var b model.WeChatBinding
		if err := h.db.Where("id = ? AND user_id = ?", req.BindingID, uid).First(&b).Error; err != nil {
			fail(c, http.StatusNotFound, 404, "binding not found")
			return
		}
		bindingIDs = []string{b.ID}
	} else {
		var bs []model.WeChatBinding
		_ = h.db.Where("user_id = ?", uid).Find(&bs).Error
		for _, b := range bs {
			bindingIDs = append(bindingIDs, b.ID)
		}
	}
	if len(bindingIDs) == 0 {
		ok(c, gin.H{"items": []any{}})
		return
	}
	var rows []model.Conversation
	if err := h.db.Where("binding_id IN ?", bindingIDs).
		Order("last_message_at desc").
		Limit(req.Limit).
		Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{"items": rows})
}

type convMessagesReq struct {
	ConversationID string    `json:"conversation_id"`
	Before         time.Time `json:"before"`
	Limit          int       `json:"limit"`
}

func (h *ConversationHandler) Messages(c *gin.Context) {
	uid := currentUserID(c)
	var req convMessagesReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if !h.userOwnsConversation(uid, req.ConversationID) {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}
	if req.Limit <= 0 || req.Limit > 200 {
		req.Limit = 50
	}
	q := h.db.Where("conversation_id = ?", req.ConversationID)
	if !req.Before.IsZero() {
		q = q.Where("created_at < ?", req.Before)
	}
	var rows []model.Message
	if err := q.Order("created_at desc").Limit(req.Limit + 1).Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	hasMore := len(rows) > req.Limit
	if hasMore {
		rows = rows[:req.Limit]
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	// Attach the conversation-level rolling summary so the frontend can render
	// "before this window there was a summary" header without a second round trip.
	var conv model.Conversation
	_ = h.db.Where("id = ?", req.ConversationID).First(&conv).Error
	ok(c, gin.H{
		"messages":           rows,
		"has_more":           hasMore,
		"summary":            conv.Summary,
		"summary_updated_at": conv.SummaryUpdatedAt,
	})
}

type convRebuildSummaryReq struct {
	ConversationID string `json:"conversation_id"`
}

// RebuildSummary drops the existing summary watermark and re-summarizes the
// full conversation in one shot. Useful when the summary went stale or wrong.
func (h *ConversationHandler) RebuildSummary(c *gin.Context) {
	uid := currentUserID(c)
	var req convRebuildSummaryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if !h.userOwnsConversation(uid, req.ConversationID) {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}
	var conv model.Conversation
	if err := h.db.Where("id = ?", req.ConversationID).First(&conv).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}
	var binding model.WeChatBinding
	if err := h.db.Where("id = ?", conv.BindingID).First(&binding).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	var persona model.Persona
	if err := h.db.Where("id = ?", binding.PersonaID).First(&persona).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	var llmCfg model.LLMConfig
	if persona.LLMConfigID != "" {
		_ = h.db.Where("id = ?", persona.LLMConfigID).First(&llmCfg).Error
	}
	if llmCfg.ID == "" {
		if err := h.db.Where("user_id = ? AND is_default = ?", uid, true).First(&llmCfg).Error; err != nil {
			fail(c, http.StatusBadRequest, 400, "no llm config available")
			return
		}
	}
	if err := h.eng.RebuildSummary(c.Request.Context(), &conv, &llmCfg); err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	var fresh model.Conversation
	_ = h.db.Where("id = ?", req.ConversationID).First(&fresh).Error
	ok(c, gin.H{
		"summary":            fresh.Summary,
		"summary_updated_at": fresh.SummaryUpdatedAt,
	})
}

type convSendManualReq struct {
	ConversationID string `json:"conversation_id"`
	Text           string `json:"text"`
}

// SendManual lets the user post a message *as* the AI (debug / staff override).
func (h *ConversationHandler) SendManual(c *gin.Context) {
	uid := currentUserID(c)
	var req convSendManualReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		fail(c, http.StatusBadRequest, 400, "text required")
		return
	}
	if !h.userOwnsConversation(uid, req.ConversationID) {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}
	var conv model.Conversation
	if err := h.db.Where("id = ?", req.ConversationID).First(&conv).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}
	var binding model.WeChatBinding
	if err := h.db.Where("id = ?", conv.BindingID).First(&binding).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	if conv.LastContextToken == "" && binding.LastContextToken == "" {
		fail(c, http.StatusBadRequest, 400, "no context_token cached; ask the peer to message the bot first")
		return
	}
	if err := h.eng.SendLiteralText(c.Request.Context(), &binding, conv.ILinkUserID, text); err != nil {
		fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	ok(c, gin.H{"ok": true})
}

type convExportReq struct {
	ConversationID string `json:"conversation_id"`
	Format         string `json:"format"`
}

func (h *ConversationHandler) Export(c *gin.Context) {
	uid := currentUserID(c)
	var req convExportReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if !h.userOwnsConversation(uid, req.ConversationID) {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}
	// Export the readable transcript: skip agent-loop audit rows. JSON export
	// is intentionally raw (includes tool rows) so power users can grep them.
	var rows []model.Message
	if err := h.db.Where("conversation_id = ?", req.ConversationID).
		Order("created_at asc").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	switch strings.ToLower(req.Format) {
	case "markdown", "md":
		var b strings.Builder
		for _, m := range rows {
			if m.Direction != "inbound" && m.Direction != "outbound" {
				continue
			}
			role := "User"
			if m.Direction == "outbound" {
				role = "AI"
			}
			fmt.Fprintf(&b, "**%s** — %s\n\n", role, m.CreatedAt.Format(time.RFC3339))
			b.WriteString(m.Text)
			b.WriteString("\n\n")
		}
		ok(c, gin.H{"format": "markdown", "content": b.String()})
	default:
		buf, _ := json.Marshal(rows)
		ok(c, gin.H{"format": "json", "content": string(buf)})
	}
}

type convDeleteReq struct {
	ConversationID string `json:"conversation_id"`
}

// Delete removes a conversation along with its messages and attachments (files included).
func (h *ConversationHandler) Delete(c *gin.Context) {
	uid := currentUserID(c)
	var req convDeleteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if !h.userOwnsConversation(uid, req.ConversationID) {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}

	var attachmentPaths []string
	_ = h.db.Model(&model.Attachment{}).
		Joins("JOIN messages ON attachments.message_id = messages.id").
		Where("messages.conversation_id = ?", req.ConversationID).
		Pluck("attachments.local_path", &attachmentPaths).Error

	tx := h.db.Begin()
	if err := tx.Exec(`DELETE FROM attachments WHERE message_id IN (
		SELECT id FROM messages WHERE conversation_id = ?
	)`, req.ConversationID).Error; err != nil {
		tx.Rollback()
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	if err := tx.Where("conversation_id = ?", req.ConversationID).
		Delete(&model.Message{}).Error; err != nil {
		tx.Rollback()
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	if err := tx.Where("id = ?", req.ConversationID).
		Delete(&model.Conversation{}).Error; err != nil {
		tx.Rollback()
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	tx.Commit()

	for _, p := range attachmentPaths {
		if p != "" {
			_ = os.Remove(p)
		}
	}
	ok(c, gin.H{"id": req.ConversationID})
}

func (h *ConversationHandler) userOwnsConversation(userID, convID string) bool {
	if convID == "" {
		return false
	}
	var count int64
	h.db.Raw(`SELECT count(1) FROM conversations c
		JOIN we_chat_bindings b ON c.binding_id = b.id
		WHERE c.id = ? AND b.user_id = ?`, convID, userID).Scan(&count)
	return count > 0
}
