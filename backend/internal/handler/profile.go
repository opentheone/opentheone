package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/crypto"
	"github.com/opentheone/opentheone/backend/internal/llm"
	"github.com/opentheone/opentheone/backend/internal/memory"
	"github.com/opentheone/opentheone/backend/internal/model"
)

// ProfileHandler exposes the L3 user-profile read/regenerate endpoints.
type ProfileHandler struct {
	db     *gorm.DB
	mem    *memory.Service
	secret string
}

func NewProfileHandler(db *gorm.DB, mem *memory.Service, secret string) *ProfileHandler {
	return &ProfileHandler{db: db, mem: mem, secret: secret}
}

type profilePersonaReq struct {
	PersonaID string `json:"persona_id"`
}

type profileRegenReq struct {
	PersonaID string `json:"persona_id"`
	Reason    string `json:"reason"`
}

func (h *ProfileHandler) ownsPersona(c *gin.Context, personaID string) (*model.Persona, bool) {
	uid := currentUserID(c)
	var p model.Persona
	if err := h.db.Where("id = ? AND user_id = ?", personaID, uid).First(&p).Error; err != nil {
		return nil, false
	}
	return &p, true
}

// Get returns the current profile or {profile: null} when none exists.
func (h *ProfileHandler) Get(c *gin.Context) {
	var req profilePersonaReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if _, ok := h.ownsPersona(c, req.PersonaID); !ok {
		fail(c, http.StatusForbidden, 403, "forbidden")
		return
	}
	p, err := h.mem.GetProfile(c.Request.Context(), req.PersonaID)
	if err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{"profile": p, "max_chars": memory.MaxProfileChars})
}

// Regenerate kicks an LLM-backed L3 synthesis pass right now. Use sparingly
// from the UI — the pipeline will normally schedule this on its own.
func (h *ProfileHandler) Regenerate(c *gin.Context) {
	var req profileRegenReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	persona, owned := h.ownsPersona(c, req.PersonaID)
	if !owned {
		fail(c, http.StatusForbidden, 403, "forbidden")
		return
	}
	client, err := h.resolveLLMClient(persona, currentUserID(c))
	if err != nil || client == nil {
		fail(c, http.StatusFailedDependency, 424, "no usable llm config for persona — set chat_model + api_key first")
		return
	}
	reason := req.Reason
	if reason == "" {
		reason = "manual"
	}
	p, err := h.mem.RegenerateProfile(c.Request.Context(), client, req.PersonaID, reason)
	if err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{"profile": p})
}

// resolveLLMClient picks the persona's pinned LLM config first, then the
// user's default. Same fallback as the engine + manual-memory paths so
// behaviour stays predictable across surfaces.
func (h *ProfileHandler) resolveLLMClient(persona *model.Persona, userID string) (*llm.Client, error) {
	var cfg model.LLMConfig
	if persona.LLMConfigID != "" {
		_ = h.db.Where("id = ?", persona.LLMConfigID).First(&cfg).Error
	}
	if cfg.ID == "" {
		_ = h.db.Where("user_id = ? AND is_default = ?", userID, true).First(&cfg).Error
	}
	if cfg.ID == "" {
		return nil, nil
	}
	key, err := crypto.Decrypt(h.secret, cfg.APIKeyEnc)
	if err != nil || key == "" {
		return nil, nil
	}
	return llm.NewClient(&cfg, key), nil
}
