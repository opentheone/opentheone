package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/crypto"
	"github.com/opentheone/opentheone/backend/internal/llm"
	"github.com/opentheone/opentheone/backend/internal/model"
)

type LLMHandler struct {
	db     *gorm.DB
	secret string
}

func NewLLMHandler(db *gorm.DB, secret string) *LLMHandler {
	return &LLMHandler{db: db, secret: secret}
}

type llmCreateReq struct {
	Name           string `json:"name"`
	BaseURL        string `json:"base_url"`
	APIKey         string `json:"api_key"`
	ChatModel      string `json:"chat_model"`
	EmbeddingModel string `json:"embedding_model"`
	// Temperature is a pointer so the zero value (`0`, deterministic
	// decoding — a perfectly valid request) is distinguishable from
	// "field absent in body". The previous `float32` plus `== 0 ⇒ 0.8`
	// shortcut silently overrode any user who intended exactly zero.
	Temperature *float32 `json:"temperature"`
	MaxTokens   int      `json:"max_tokens"`
	IsDefault   bool     `json:"is_default"`
}

func (h *LLMHandler) Create(c *gin.Context) {
	uid := currentUserID(c)
	var req llmCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if req.Name == "" || req.BaseURL == "" || req.ChatModel == "" {
		fail(c, http.StatusBadRequest, 400, "name/base_url/chat_model required")
		return
	}
	enc, err := crypto.Encrypt(h.secret, req.APIKey)
	if err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	// Default to 0.8 only when the caller didn't pass anything; an explicit
	// `temperature: 0` flows through verbatim.
	temp := float32(0.8)
	if req.Temperature != nil && *req.Temperature >= 0 {
		temp = *req.Temperature
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = 1024
	}

	cfg := model.LLMConfig{
		UserID:         uid,
		Name:           req.Name,
		BaseURL:        req.BaseURL,
		APIKeyEnc:      enc,
		ChatModel:      req.ChatModel,
		EmbeddingModel: req.EmbeddingModel,
		Temperature:    temp,
		MaxTokens:      req.MaxTokens,
		IsDefault:      req.IsDefault,
	}
	if err := h.db.Create(&cfg).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	if req.IsDefault {
		_ = h.db.Model(&model.LLMConfig{}).
			Where("user_id = ? AND id <> ?", uid, cfg.ID).
			Update("is_default", false).Error
	}
	ok(c, gin.H{"id": cfg.ID})
}

type llmListItem struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	BaseURL        string  `json:"base_url"`
	ChatModel      string  `json:"chat_model"`
	EmbeddingModel string  `json:"embedding_model"`
	Temperature    float32 `json:"temperature"`
	MaxTokens      int     `json:"max_tokens"`
	IsDefault      bool    `json:"is_default"`
	APIKeySet      bool    `json:"api_key_set"`
	CreatedAt      string  `json:"created_at"`
}

func (h *LLMHandler) List(c *gin.Context) {
	uid := currentUserID(c)
	var rows []model.LLMConfig
	if err := h.db.Where("user_id = ?", uid).Order("created_at desc").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	out := make([]llmListItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, llmListItem{
			ID:             r.ID,
			Name:           r.Name,
			BaseURL:        r.BaseURL,
			ChatModel:      r.ChatModel,
			EmbeddingModel: r.EmbeddingModel,
			Temperature:    r.Temperature,
			MaxTokens:      r.MaxTokens,
			IsDefault:      r.IsDefault,
			APIKeySet:      r.APIKeyEnc != "",
			CreatedAt:      r.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	ok(c, gin.H{"items": out})
}

// Providers returns the built-in provider preset catalog.
// Used by the frontend to render one-click "fill from template" buttons.
func (h *LLMHandler) Providers(c *gin.Context) {
	ok(c, gin.H{"items": llm.Presets()})
}

type llmUpdateReq struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	BaseURL        string `json:"base_url"`
	APIKey         string `json:"api_key"`
	ChatModel      string `json:"chat_model"`
	EmbeddingModel string `json:"embedding_model"`
	// Temperature and MaxTokens are pointers so the zero value is
	// distinguishable from "field absent". A user setting temperature=0
	// (deterministic decoding) is a perfectly valid request; the previous
	// `> 0` guard silently dropped it on the floor.
	Temperature *float32 `json:"temperature"`
	MaxTokens   *int     `json:"max_tokens"`
	IsDefault   bool     `json:"is_default"`
}

func (h *LLMHandler) Update(c *gin.Context) {
	uid := currentUserID(c)
	var req llmUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	var cfg model.LLMConfig
	if err := h.db.Where("id = ? AND user_id = ?", req.ID, uid).First(&cfg).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}
	updates := map[string]interface{}{}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.BaseURL != "" {
		updates["base_url"] = req.BaseURL
	}
	if req.ChatModel != "" {
		updates["chat_model"] = req.ChatModel
	}
	updates["embedding_model"] = req.EmbeddingModel
	if req.Temperature != nil && *req.Temperature >= 0 {
		updates["temperature"] = *req.Temperature
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		updates["max_tokens"] = *req.MaxTokens
	}
	updates["is_default"] = req.IsDefault
	if req.APIKey != "" {
		enc, err := crypto.Encrypt(h.secret, req.APIKey)
		if err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
		updates["api_key_enc"] = enc
	}
	if err := h.db.Model(&cfg).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	if req.IsDefault {
		_ = h.db.Model(&model.LLMConfig{}).
			Where("user_id = ? AND id <> ?", uid, cfg.ID).
			Update("is_default", false).Error
	}
	ok(c, gin.H{"id": cfg.ID})
}

type idOnlyReq struct {
	ID string `json:"id"`
}

func (h *LLMHandler) Delete(c *gin.Context) {
	uid := currentUserID(c)
	var req idOnlyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	var refs []model.Persona
	if err := h.db.Where("user_id = ? AND llm_config_id = ?", uid, req.ID).Find(&refs).Error; err == nil && len(refs) > 0 {
		names := make([]string, 0, len(refs))
		for _, p := range refs {
			names = append(names, p.Name)
		}
		fail(c, http.StatusConflict, 409, "该配置正被以下角色使用，请先在角色详情中切换模型："+strings.Join(names, "、"))
		return
	}
	if err := h.db.Where("id = ? AND user_id = ?", req.ID, uid).
		Delete(&model.LLMConfig{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{"id": req.ID})
}

func (h *LLMHandler) Test(c *gin.Context) {
	uid := currentUserID(c)
	var req idOnlyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	var cfg model.LLMConfig
	if err := h.db.Where("id = ? AND user_id = ?", req.ID, uid).First(&cfg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, 404, "not found")
			return
		}
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	key, err := crypto.Decrypt(h.secret, cfg.APIKeyEnc)
	if err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	client := llm.NewClient(&cfg, key)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	ok(c, gin.H{"ok": true})
}
