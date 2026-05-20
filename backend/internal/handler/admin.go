package handler

import (
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/auth"
	"github.com/opentheone/opentheone/backend/internal/mcp"
	"github.com/opentheone/opentheone/backend/internal/model"
	"github.com/opentheone/opentheone/backend/internal/runner"
	"github.com/opentheone/opentheone/backend/internal/settings"
)

type AdminHandler struct {
	db       *gorm.DB
	settings *settings.Service
	mgr      *runner.Manager
	mcp      *mcp.Manager
}

func NewAdminHandler(db *gorm.DB, set *settings.Service, mgr *runner.Manager, mcpMgr *mcp.Manager) *AdminHandler {
	return &AdminHandler{db: db, settings: set, mgr: mgr, mcp: mcpMgr}
}

type adminUserItem struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	CreatedAt   string `json:"created_at"`
}

// ListUsers returns every user (admin only).
func (h *AdminHandler) ListUsers(c *gin.Context) {
	var rows []model.User
	if err := h.db.Order("created_at asc").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	out := make([]adminUserItem, 0, len(rows))
	for _, u := range rows {
		out = append(out, adminUserItem{
			ID:          u.ID,
			Username:    u.Username,
			DisplayName: u.DisplayName,
			Role:        u.Role,
			CreatedAt:   u.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	ok(c, gin.H{"items": out})
}

type adminSetRoleReq struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// SetRole sets a user's role. The last admin cannot be demoted.
func (h *AdminHandler) SetRole(c *gin.Context) {
	var req adminSetRoleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if req.Role != "admin" && req.Role != "user" {
		fail(c, http.StatusBadRequest, 400, "role must be admin|user")
		return
	}
	var target model.User
	if err := h.db.Where("id = ?", req.UserID).First(&target).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "user not found")
		return
	}
	if target.Role == "admin" && req.Role != "admin" {
		var admins int64
		_ = h.db.Model(&model.User{}).Where("role = ?", "admin").Count(&admins).Error
		if admins <= 1 {
			fail(c, http.StatusBadRequest, 400, "refuse to demote the last admin")
			return
		}
	}
	if err := h.db.Model(&target).Update("role", req.Role).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{"ok": true})
}

type adminResetPasswordReq struct {
	UserID      string `json:"user_id"`
	NewPassword string `json:"new_password"`
}

// ResetPassword overwrites another user's password (admin only).
func (h *AdminHandler) ResetPassword(c *gin.Context) {
	var req adminResetPasswordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if len(req.NewPassword) < minPasswordLen {
		fail(c, http.StatusBadRequest, 400, "password too short (min 6)")
		return
	}
	var target model.User
	if err := h.db.Where("id = ?", req.UserID).First(&target).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "user not found")
		return
	}
	pw, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	if err := h.db.Model(&target).Update("password_hash", pw).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{"ok": true})
}

type adminDeleteUserReq struct {
	UserID string `json:"user_id"`
}

// DeleteUser removes a user.
// All of their personas / bindings / conversations / messages / memories / llm configs are cascaded.
// The caller cannot delete themselves; the last admin cannot be deleted.
func (h *AdminHandler) DeleteUser(c *gin.Context) {
	uid := currentUserID(c)
	var req adminDeleteUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if req.UserID == uid {
		fail(c, http.StatusBadRequest, 400, "cannot delete yourself")
		return
	}
	var target model.User
	if err := h.db.Where("id = ?", req.UserID).First(&target).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "user not found")
		return
	}
	if target.Role == "admin" {
		var admins int64
		_ = h.db.Model(&model.User{}).Where("role = ?", "admin").Count(&admins).Error
		if admins <= 1 {
			fail(c, http.StatusBadRequest, 400, "refuse to delete the last admin")
			return
		}
	}

	var personaIDs []string
	_ = h.db.Model(&model.Persona{}).Where("user_id = ?", req.UserID).Pluck("id", &personaIDs).Error
	var bindingIDs []string
	_ = h.db.Model(&model.WeChatBinding{}).Where("user_id = ?", req.UserID).Pluck("id", &bindingIDs).Error
	var convIDs []string
	if len(bindingIDs) > 0 {
		_ = h.db.Model(&model.Conversation{}).Where("binding_id IN ?", bindingIDs).Pluck("id", &convIDs).Error
	}
	// Collect MCP server ids so we can invalidate their cached client
	// connections after the DB rows are gone (a stdio MCP subprocess that
	// outlives its row would happily keep running otherwise).
	var mcpIDs []string
	_ = h.db.Model(&model.MCPServer{}).Where("user_id = ?", req.UserID).Pluck("id", &mcpIDs).Error
	var attachmentPaths []string
	if len(convIDs) > 0 {
		_ = h.db.Model(&model.Attachment{}).
			Joins("JOIN messages ON attachments.message_id = messages.id").
			Where("messages.conversation_id IN ?", convIDs).
			Pluck("attachments.local_path", &attachmentPaths).Error
	}

	for _, bid := range bindingIDs {
		h.mgr.Stop(bid)
	}

	tx := h.db.Begin()
	// See conversation.Delete: defer-rollback guarantees the tx unwinds on
	// panic or any future early-return path; it's a no-op after a successful
	// Commit (sql.ErrTxDone is swallowed).
	defer func() { _ = tx.Rollback() }()
	if len(convIDs) > 0 {
		if err := tx.Exec(`DELETE FROM attachments WHERE message_id IN (
			SELECT id FROM messages WHERE conversation_id IN ?
		)`, convIDs).Error; err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
		if err := tx.Where("conversation_id IN ?", convIDs).Delete(&model.Message{}).Error; err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
		if err := tx.Where("id IN ?", convIDs).Delete(&model.Conversation{}).Error; err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
	}
	if len(bindingIDs) > 0 {
		if err := tx.Where("id IN ?", bindingIDs).Delete(&model.WeChatBinding{}).Error; err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
	}
	if len(personaIDs) > 0 {
		// FTS5 virtual table — GORM cascade rules don't reach it, must be
		// purged with an explicit DELETE before / alongside the canonical
		// memories rows. Same goes for messages_fts further down (scoped to
		// the deleted conversations).
		if err := tx.Exec(`DELETE FROM memories_fts WHERE persona_id IN ?`, personaIDs).Error; err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
		if err := tx.Where("persona_id IN ?", personaIDs).Delete(&model.Memory{}).Error; err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
		// L2 scenes, L3 profile, per-persona pipeline state, per-(persona,
		// conv) checkpoint — all persona-scoped, all need to die with the
		// user's personas. Otherwise the next user (or a re-created persona
		// with the same id, however unlikely) would inherit zombie memory
		// state.
		if err := tx.Where("persona_id IN ?", personaIDs).Delete(&model.MemoryScene{}).Error; err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
		if err := tx.Where("persona_id IN ?", personaIDs).Delete(&model.UserProfile{}).Error; err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
		if err := tx.Where("persona_id IN ?", personaIDs).Delete(&model.MemoryPipelineState{}).Error; err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
		if err := tx.Where("persona_id IN ?", personaIDs).Delete(&model.MemoryExtractCheckpoint{}).Error; err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
		if err := tx.Where("id IN ?", personaIDs).Delete(&model.Persona{}).Error; err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
	}
	// messages_fts is keyed by conversation_id; purge here (after the message
	// rows are gone above) so the `oto_conversation_search` tool can't return
	// stale hits pointing at non-existent messages.
	if len(convIDs) > 0 {
		if err := tx.Exec(`DELETE FROM messages_fts WHERE conversation_id IN ?`, convIDs).Error; err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
	}
	if err := tx.Where("user_id = ?", req.UserID).Delete(&model.LLMConfig{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	if err := tx.Where("user_id = ?", req.UserID).Delete(&model.MCPServer{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	if err := tx.Where("id = ?", req.UserID).Delete(&model.User{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	// See conversation.Delete: a Commit failure here MUST short-circuit the
	// post-commit side effects (file deletion, MCP cache invalidation),
	// otherwise we tear down attachments and live subprocesses while the DB
	// rows still exist — strictly worse than rolling back cleanly.
	if err := tx.Commit().Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}

	for _, p := range attachmentPaths {
		if p != "" {
			_ = os.Remove(p)
		}
	}
	if h.mcp != nil {
		for _, mid := range mcpIDs {
			h.mcp.Invalidate(mid)
		}
	}

	ok(c, gin.H{"ok": true})
}

// GetSettings returns all surfaced settings.
func (h *AdminHandler) GetSettings(c *gin.Context) {
	ok(c, gin.H{
		"allow_register": h.settings.GetBool(settings.KeyAllowRegister, true),
	})
}

type adminUpdateSettingsReq struct {
	AllowRegister *bool `json:"allow_register"`
}

// UpdateSettings updates one or more known settings.
func (h *AdminHandler) UpdateSettings(c *gin.Context) {
	var req adminUpdateSettingsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if req.AllowRegister != nil {
		if err := h.settings.SetBool(settings.KeyAllowRegister, *req.AllowRegister); err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
	}
	ok(c, gin.H{
		"allow_register": h.settings.GetBool(settings.KeyAllowRegister, true),
	})
}
