package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/memory"
	"github.com/opentheone/opentheone/backend/internal/model"
)

type MemoryHandler struct {
	db  *gorm.DB
	mem *memory.Service
}

func NewMemoryHandler(db *gorm.DB, mem *memory.Service) *MemoryHandler {
	return &MemoryHandler{db: db, mem: mem}
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
	// Drop the BM25 row too — otherwise retrieval still returns this
	// memory's content even though the underlying row is gone.
	_ = h.mem.BM25().DeleteMemory(c.Request.Context(), req.ID)
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
	// Default to `persona` (stable user attribute) — the most common manual
	// add ("user prefers Cantonese") and consistent with what the L1
	// extractor emits.
	if req.Kind == "" {
		req.Kind = "persona"
	}
	var p model.Persona
	if err := h.db.Where("id = ? AND user_id = ?", req.PersonaID, uid).First(&p).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "persona not found")
		return
	}
	// Manual upsert path is the simplest: BM25 dedup against existing atoms,
	// no LLM round-trip required. The pipeline will pick up new atoms and
	// fold them into L2 scenes asynchronously.
	if err := h.mem.IngestManual(c.Request.Context(), req.PersonaID, req.Kind, req.Content, req.Importance); err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{"ok": true})
}
