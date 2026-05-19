package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/auth"
	"github.com/opentheone/opentheone/backend/internal/llm"
	"github.com/opentheone/opentheone/backend/internal/model"
	"github.com/opentheone/opentheone/backend/internal/settings"
)

// AuthHandler manages register / login / me.
type AuthHandler struct {
	db       *gorm.DB
	tm       *auth.TokenManager
	settings *settings.Service
}

func NewAuthHandler(db *gorm.DB, tm *auth.TokenManager, set *settings.Service) *AuthHandler {
	return &AuthHandler{db: db, tm: tm, settings: set}
}

type registerReq struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

// Register creates a new user. First user is auto-promoted to admin.
func (h *AuthHandler) Register(c *gin.Context) {
	var req registerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		fail(c, http.StatusBadRequest, 400, "username & password required")
		return
	}
	allow := true
	if h.settings != nil {
		allow = h.settings.GetBool(settings.KeyAllowRegister, true)
	}
	if !allow {
		var count int64
		_ = h.db.Model(&model.User{}).Count(&count).Error
		if count > 0 {
			fail(c, http.StatusForbidden, 403, "registration disabled")
			return
		}
	}

	var existing model.User
	err := h.db.Where("username = ?", req.Username).First(&existing).Error
	if err == nil {
		fail(c, http.StatusBadRequest, 400, "username taken")
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}

	pwHash, err := auth.HashPassword(req.Password)
	if err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}

	role := "user"
	var total int64
	_ = h.db.Model(&model.User{}).Count(&total).Error
	if total == 0 {
		role = "admin"
	}

	u := model.User{
		Username:     req.Username,
		PasswordHash: pwHash,
		DisplayName:  req.DisplayName,
		Role:         role,
	}
	if err := h.db.Create(&u).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}

	seedDefaultLLMConfig(h.db, u.ID)

	token, exp, err := h.tm.Issue(u.ID, u.Username, u.Role)
	if err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{
		"token":      token,
		"expires_at": exp,
		"user": gin.H{
			"id":           u.ID,
			"username":     u.Username,
			"display_name": u.DisplayName,
			"role":         u.Role,
		},
	})
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Login returns a JWT on success.
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	var u model.User
	if err := h.db.Where("username = ?", req.Username).First(&u).Error; err != nil {
		fail(c, http.StatusUnauthorized, 401, "invalid credentials")
		return
	}
	if !auth.VerifyPassword(u.PasswordHash, req.Password) {
		fail(c, http.StatusUnauthorized, 401, "invalid credentials")
		return
	}
	token, exp, err := h.tm.Issue(u.ID, u.Username, u.Role)
	if err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{
		"token":      token,
		"expires_at": exp,
		"user": gin.H{
			"id":           u.ID,
			"username":     u.Username,
			"display_name": u.DisplayName,
			"role":         u.Role,
		},
	})
}

type updateProfileReq struct {
	DisplayName string `json:"display_name"`
}

// UpdateProfile updates display_name on the current user.
func (h *AuthHandler) UpdateProfile(c *gin.Context) {
	uid := currentUserID(c)
	var req updateProfileReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if err := h.db.Model(&model.User{}).
		Where("id = ?", uid).
		Update("display_name", req.DisplayName).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{"ok": true})
}

type updatePasswordReq struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// UpdatePassword changes the current user's password.
// Requires the old password to match.
func (h *AuthHandler) UpdatePassword(c *gin.Context) {
	uid := currentUserID(c)
	var req updatePasswordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if req.OldPassword == "" || req.NewPassword == "" {
		fail(c, http.StatusBadRequest, 400, "old_password & new_password required")
		return
	}
	if len(req.NewPassword) < 6 {
		fail(c, http.StatusBadRequest, 400, "new_password too short (min 6)")
		return
	}
	var u model.User
	if err := h.db.Where("id = ?", uid).First(&u).Error; err != nil {
		fail(c, http.StatusUnauthorized, 401, "user gone")
		return
	}
	if !auth.VerifyPassword(u.PasswordHash, req.OldPassword) {
		fail(c, http.StatusUnauthorized, 401, "wrong password")
		return
	}
	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	if err := h.db.Model(&u).Update("password_hash", newHash).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{"ok": true})
}

// seedDefaultLLMConfig inserts the default provider preset (DeepSeek)
// with an empty API key, so the user has a placeholder row to fill in.
// It is a best-effort no-op on any DB error or if the user already has any config.
func seedDefaultLLMConfig(db *gorm.DB, userID string) {
	var count int64
	_ = db.Model(&model.LLMConfig{}).Where("user_id = ?", userID).Count(&count).Error
	if count > 0 {
		return
	}
	preset := llm.DefaultPreset()
	cfg := model.LLMConfig{
		UserID:         userID,
		Name:           preset.Name,
		BaseURL:        preset.BaseURL,
		APIKeyEnc:      "",
		ChatModel:      preset.ChatModel,
		EmbeddingModel: preset.EmbeddingModel,
		Temperature:    0.8,
		MaxTokens:      1024,
		IsDefault:      true,
	}
	_ = db.Create(&cfg).Error
}

// Me returns the current user info.
func (h *AuthHandler) Me(c *gin.Context) {
	uid := currentUserID(c)
	var u model.User
	if err := h.db.Where("id = ?", uid).First(&u).Error; err != nil {
		fail(c, http.StatusUnauthorized, 401, "user gone")
		return
	}
	ok(c, gin.H{
		"id":           u.ID,
		"username":     u.Username,
		"display_name": u.DisplayName,
		"role":         u.Role,
		"created_at":   u.CreatedAt,
	})
}
