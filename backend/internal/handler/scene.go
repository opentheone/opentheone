package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/memory"
	"github.com/opentheone/opentheone/backend/internal/model"
)

// SceneHandler exposes the L2 scene view to the frontend.
// All endpoints require the caller to own the persona.
type SceneHandler struct {
	db  *gorm.DB
	mem *memory.Service
}

func NewSceneHandler(db *gorm.DB, mem *memory.Service) *SceneHandler {
	return &SceneHandler{db: db, mem: mem}
}

type scenePersonaReq struct {
	PersonaID string `json:"persona_id"`
}

type sceneGetReq struct {
	PersonaID string `json:"persona_id"`
	SceneID   string `json:"scene_id"`
}

type sceneDeleteReq struct {
	PersonaID string `json:"persona_id"`
	SceneID   string `json:"scene_id"`
}

func (h *SceneHandler) ownsPersona(c *gin.Context, personaID string) bool {
	uid := currentUserID(c)
	var p model.Persona
	return h.db.Where("id = ? AND user_id = ?", personaID, uid).First(&p).Error == nil
}

// List returns every scene owned by the given persona.
func (h *SceneHandler) List(c *gin.Context) {
	var req scenePersonaReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if !h.ownsPersona(c, req.PersonaID) {
		fail(c, http.StatusForbidden, 403, "forbidden")
		return
	}
	scs, err := h.mem.ListScenes(c.Request.Context(), req.PersonaID)
	if err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{"items": scs, "max_scenes": memory.MaxScenesPerPersona})
}

// Get returns one scene plus the atoms it groups.
func (h *SceneHandler) Get(c *gin.Context) {
	var req sceneGetReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if !h.ownsPersona(c, req.PersonaID) {
		fail(c, http.StatusForbidden, 403, "forbidden")
		return
	}
	sc, atoms, err := h.mem.GetScene(c.Request.Context(), req.PersonaID, req.SceneID)
	if err != nil {
		fail(c, http.StatusNotFound, 404, err.Error())
		return
	}
	ok(c, gin.H{"scene": sc, "atoms": atoms})
}

// Delete removes a scene; the underlying atoms stay (just lose their
// scene_id) and will be re-routed on the next pipeline tick.
func (h *SceneHandler) Delete(c *gin.Context) {
	var req sceneDeleteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if !h.ownsPersona(c, req.PersonaID) {
		fail(c, http.StatusForbidden, 403, "forbidden")
		return
	}
	if err := h.mem.DeleteScene(c.Request.Context(), req.PersonaID, req.SceneID); err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{"id": req.SceneID})
}
