package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/crypto"
	"github.com/opentheone/opentheone/backend/internal/llm"
	"github.com/opentheone/opentheone/backend/internal/memory"
	"github.com/opentheone/opentheone/backend/internal/model"
)

type MemoryHandler struct {
	db     *gorm.DB
	mem    *memory.Service
	secret string
}

func NewMemoryHandler(db *gorm.DB, mem *memory.Service, secret string) *MemoryHandler {
	return &MemoryHandler{db: db, mem: mem, secret: secret}
}

type memListReq struct {
	PersonaID string `json:"persona_id"`
	Limit     int    `json:"limit"`
}

func (h *MemoryHandler) List(c *gin.Context) {
	uid := currentUserID(c)
	var req memListReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	var p model.Persona
	if err := h.db.Where("id = ? AND user_id = ?", req.PersonaID, uid).First(&p).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "persona not found")
		return
	}
	if req.Limit <= 0 || req.Limit > 500 {
		req.Limit = 100
	}
	var rows []model.Memory
	if err := h.db.Where("persona_id = ?", req.PersonaID).
		Order("importance desc, created_at desc").
		Limit(req.Limit).Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{"items": rows})
}

func (h *MemoryHandler) Delete(c *gin.Context) {
	uid := currentUserID(c)
	var req idOnlyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	var m model.Memory
	if err := h.db.Where("id = ?", req.ID).First(&m).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}
	var p model.Persona
	if err := h.db.Where("id = ? AND user_id = ?", m.PersonaID, uid).First(&p).Error; err != nil {
		fail(c, http.StatusForbidden, 403, "forbidden")
		return
	}
	if err := h.db.Delete(&m).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{"id": req.ID})
}

type memUpsertReq struct {
	PersonaID  string `json:"persona_id"`
	Content    string `json:"content"`
	Kind       string `json:"kind"`
	Importance int    `json:"importance"`
}

func (h *MemoryHandler) UpsertManual(c *gin.Context) {
	uid := currentUserID(c)
	var req memUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	req.Content = strings.TrimSpace(req.Content)
	if req.Content == "" {
		fail(c, http.StatusBadRequest, 400, "content required")
		return
	}
	if req.Kind == "" {
		req.Kind = "fact"
	}
	var p model.Persona
	if err := h.db.Where("id = ? AND user_id = ?", req.PersonaID, uid).First(&p).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "persona not found")
		return
	}
	// Resolve the LLM client used to embed the new memory. Order:
	//   1. The persona's explicitly configured llm_config_id
	//   2. The user's is_default = true config
	// Without the fallback, any persona that hadn't been pinned to a specific
	// model would silently bypass embedding — and the manual memory would
	// then be invisible to vector-similarity retrieval (only reachable via
	// the importance/recency pre-filter), defeating the point of adding it.
	var llmClient *llm.Client
	var cfg model.LLMConfig
	if p.LLMConfigID != "" {
		_ = h.db.Where("id = ?", p.LLMConfigID).First(&cfg).Error
	}
	if cfg.ID == "" {
		_ = h.db.Where("user_id = ? AND is_default = ?", uid, true).First(&cfg).Error
	}
	if cfg.ID != "" {
		if key, err := crypto.Decrypt(h.secret, cfg.APIKeyEnc); err == nil && key != "" {
			llmClient = llm.NewClient(&cfg, key)
		}
	}
	if err := h.mem.Ingest(c.Request.Context(), llmClient, req.PersonaID, "", "", req.Kind, req.Content, req.Importance); err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{"ok": true})
}
