package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/engine"
	"github.com/opentheone/opentheone/backend/internal/model"
	"github.com/opentheone/opentheone/backend/internal/persona"
	"github.com/opentheone/opentheone/backend/internal/runner"
)

// encodeStringList serializes a []string into the JSON shape used by
// columns like Persona.EnabledMCPIDs. Empty / nil input → "".
//
// We deliberately allocate a fresh slice rather than reusing `in` so we
// never mutate caller-owned memory (the request body slice).
func encodeStringList(in []string) string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return ""
	}
	buf, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(buf)
}

var personaCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// validateProactiveCron returns nil for empty string (cron disabled), or a
// descriptive error if the expression is not parseable by the same parser the
// scheduler uses. Five-field standard cron only.
func validateProactiveCron(expr string) error {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil
	}
	if _, err := personaCronParser.Parse(expr); err != nil {
		return err
	}
	return nil
}

type PersonaHandler struct {
	db  *gorm.DB
	mgr *runner.Manager
	eng *engine.Engine
}

func NewPersonaHandler(db *gorm.DB, mgr *runner.Manager, eng *engine.Engine) *PersonaHandler {
	return &PersonaHandler{db: db, mgr: mgr, eng: eng}
}

type personaCreateReq struct {
	Name            string   `json:"name"`
	Avatar          string   `json:"avatar"`
	Description     string   `json:"description"`
	SystemPrompt    string   `json:"system_prompt"`
	Greeting        string   `json:"greeting"`
	SpeakingStyle   string   `json:"speaking_style"`
	ProactiveCron   string   `json:"proactive_cron"`
	ProactivePrompt string   `json:"proactive_prompt"`
	LLMConfigID     string   `json:"llm_config_id"`
	EnabledMCPIDs   []string `json:"enabled_mcp_ids"`
}

func (h *PersonaHandler) Create(c *gin.Context) {
	uid := currentUserID(c)
	var req personaCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if req.Name == "" {
		fail(c, http.StatusBadRequest, 400, "name required")
		return
	}
	if err := validateProactiveCron(req.ProactiveCron); err != nil {
		fail(c, http.StatusBadRequest, 400, "proactive_cron 无效（请使用 5 段标准 cron，例如「0 9 * * *」）："+err.Error())
		return
	}
	p := model.Persona{
		UserID:          uid,
		Name:            req.Name,
		Avatar:          req.Avatar,
		Description:     req.Description,
		SystemPrompt:    req.SystemPrompt,
		Greeting:        req.Greeting,
		SpeakingStyle:   req.SpeakingStyle,
		ProactiveCron:   req.ProactiveCron,
		ProactivePrompt: req.ProactivePrompt,
		LLMConfigID:     req.LLMConfigID,
		EnabledMCPIDs:   encodeStringList(req.EnabledMCPIDs),
	}
	if err := h.db.Create(&p).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{"id": p.ID})
}

// Templates returns the built-in persona template catalog. Frontend uses
// this to render one-click "use this template" buttons on the create form.
func (h *PersonaHandler) Templates(c *gin.Context) {
	ok(c, gin.H{"items": persona.Templates()})
}

func (h *PersonaHandler) List(c *gin.Context) {
	uid := currentUserID(c)
	var rows []model.Persona
	if err := h.db.Where("user_id = ?", uid).Order("created_at desc").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{"items": rows})
}

func (h *PersonaHandler) Get(c *gin.Context) {
	uid := currentUserID(c)
	var req idOnlyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	var p model.Persona
	if err := h.db.Where("id = ? AND user_id = ?", req.ID, uid).First(&p).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}
	ok(c, p)
}

type personaUpdateReq struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Avatar          string   `json:"avatar"`
	Description     string   `json:"description"`
	SystemPrompt    string   `json:"system_prompt"`
	Greeting        string   `json:"greeting"`
	SpeakingStyle   string   `json:"speaking_style"`
	ProactiveCron   string   `json:"proactive_cron"`
	ProactivePrompt string   `json:"proactive_prompt"`
	LLMConfigID     string   `json:"llm_config_id"`
	EnabledMCPIDs   []string `json:"enabled_mcp_ids"`
}

func (h *PersonaHandler) Update(c *gin.Context) {
	uid := currentUserID(c)
	var req personaUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if err := validateProactiveCron(req.ProactiveCron); err != nil {
		fail(c, http.StatusBadRequest, 400, "proactive_cron 无效（请使用 5 段标准 cron，例如「0 9 * * *」）："+err.Error())
		return
	}
	var p model.Persona
	if err := h.db.Where("id = ? AND user_id = ?", req.ID, uid).First(&p).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}
	updates := map[string]interface{}{
		"name":             req.Name,
		"avatar":           req.Avatar,
		"description":      req.Description,
		"system_prompt":    req.SystemPrompt,
		"greeting":         req.Greeting,
		"speaking_style":   req.SpeakingStyle,
		"proactive_cron":   req.ProactiveCron,
		"proactive_prompt": req.ProactivePrompt,
		"llm_config_id":    req.LLMConfigID,
		"enabled_mcp_ids":  encodeStringList(req.EnabledMCPIDs),
	}
	if err := h.db.Model(&p).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{"id": p.ID})
}

func (h *PersonaHandler) Delete(c *gin.Context) {
	uid := currentUserID(c)
	var req idOnlyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}

	var p model.Persona
	if err := h.db.Where("id = ? AND user_id = ?", req.ID, uid).First(&p).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}

	var bindings []model.WeChatBinding
	_ = h.db.Where("user_id = ? AND persona_id = ?", uid, req.ID).Find(&bindings).Error

	for _, b := range bindings {
		h.mgr.Stop(b.ID)
	}

	var bindingIDs []string
	for _, b := range bindings {
		bindingIDs = append(bindingIDs, b.ID)
	}

	var convIDs []string
	if len(bindingIDs) > 0 {
		_ = h.db.Model(&model.Conversation{}).
			Where("binding_id IN ?", bindingIDs).
			Pluck("id", &convIDs).Error
	}

	var attachmentPaths []string
	if len(convIDs) > 0 {
		_ = h.db.Model(&model.Attachment{}).
			Joins("JOIN messages ON attachments.message_id = messages.id").
			Where("messages.conversation_id IN ?", convIDs).
			Pluck("attachments.local_path", &attachmentPaths).Error
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
		if err := tx.Where("conversation_id IN ?", convIDs).
			Delete(&model.Message{}).Error; err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
		if err := tx.Where("id IN ?", convIDs).
			Delete(&model.Conversation{}).Error; err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
	}
	if len(bindingIDs) > 0 {
		if err := tx.Where("id IN ?", bindingIDs).
			Delete(&model.WeChatBinding{}).Error; err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
	}
	// L1 atoms (also wipe their FTS index — memories_fts is a virtual
	// table, GORM's cascade rules don't reach it).
	if err := tx.Exec(`DELETE FROM memories_fts WHERE persona_id = ?`, req.ID).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	if err := tx.Where("persona_id = ?", req.ID).
		Delete(&model.Memory{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	// L2 scenes, L3 profile, per-persona pipeline + per-conv checkpoint
	// rows — all are persona-scoped and must die with the persona.
	if err := tx.Where("persona_id = ?", req.ID).
		Delete(&model.MemoryScene{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	if err := tx.Where("persona_id = ?", req.ID).
		Delete(&model.UserProfile{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	if err := tx.Where("persona_id = ?", req.ID).
		Delete(&model.MemoryPipelineState{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	if err := tx.Where("persona_id = ?", req.ID).
		Delete(&model.MemoryExtractCheckpoint{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	// FTS index for messages in deleted conversations.
	if len(convIDs) > 0 {
		if err := tx.Exec(`DELETE FROM messages_fts WHERE conversation_id IN ?`, convIDs).Error; err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
	}
	if err := tx.Where("id = ? AND user_id = ?", req.ID, uid).
		Delete(&model.Persona{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	// See conversation.Delete: short-circuit on Commit failure so we don't
	// rip files off disk while the persona/conversation/message rows are
	// still live.
	if err := tx.Commit().Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}

	for _, p := range attachmentPaths {
		if p != "" {
			_ = os.Remove(p)
		}
	}

	ok(c, gin.H{"id": req.ID})
}

// TriggerProactive is a debug endpoint that immediately fires one proactive message
// for the given persona without waiting for the cron tick or the 6h cooldown.
// Requires the persona to have an active binding with a cached context_token.
func (h *PersonaHandler) TriggerProactive(c *gin.Context) {
	uid := currentUserID(c)
	var req idOnlyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	var p model.Persona
	if err := h.db.Where("id = ? AND user_id = ?", req.ID, uid).First(&p).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "persona not found")
		return
	}
	var b model.WeChatBinding
	if err := h.db.Where("persona_id = ? AND state = ?", p.ID, "active").First(&b).Error; err != nil {
		fail(c, http.StatusBadRequest, 400, "no active binding for this persona")
		return
	}
	var cfg model.LLMConfig
	cfgID := p.LLMConfigID
	if cfgID != "" {
		if err := h.db.Where("id = ?", cfgID).First(&cfg).Error; err != nil {
			fail(c, http.StatusBadRequest, 400, "llm config missing")
			return
		}
	} else {
		if err := h.db.Where("user_id = ? AND is_default = ?", uid, true).First(&cfg).Error; err != nil {
			fail(c, http.StatusBadRequest, 400, "no default llm config")
			return
		}
	}
	if err := h.eng.SendProactive(c.Request.Context(), &b, &p, &cfg, ""); err != nil {
		fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	ok(c, gin.H{"ok": true})
}

// Deactivate clears the user's active persona entirely. Any active binding is
// moved to "paused" and its runner stopped, so no AI is currently online for
// this user. This is the "AI 下线/休息" switch.
func (h *PersonaHandler) Deactivate(c *gin.Context) {
	uid := currentUserID(c)

	var toPause []model.WeChatBinding
	if err := h.db.Where("user_id = ? AND state = ?", uid, "active").Find(&toPause).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}

	tx := h.db.Begin()
	defer func() { _ = tx.Rollback() }()
	if err := tx.Model(&model.Persona{}).
		Where("user_id = ?", uid).
		Update("is_active", false).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	if len(toPause) > 0 {
		ids := make([]string, len(toPause))
		for i, b := range toPause {
			ids[i] = b.ID
		}
		if err := tx.Model(&model.WeChatBinding{}).
			Where("id IN ?", ids).
			Update("state", "paused").Error; err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
	}
	// If Commit fails we MUST NOT stop the runners — otherwise the bindings
	// stay "active" in the DB but their long-poll loops are dead, which
	// presents as a silent "AI is online but never replies" state until the
	// next process restart.
	if err := tx.Commit().Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}

	for _, b := range toPause {
		h.mgr.Stop(b.ID)
	}
	ok(c, gin.H{"ok": true})
}

// Activate toggles which persona is "the one" for this user.
// Only one persona may be active per user at any time.
// As a side effect, any other persona's active binding is moved to "paused"
// (its long-poll runner is stopped) and any paused binding of the target
// persona is resumed automatically.
func (h *PersonaHandler) Activate(c *gin.Context) {
	uid := currentUserID(c)
	var req idOnlyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	var p model.Persona
	if err := h.db.Where("id = ? AND user_id = ?", req.ID, uid).First(&p).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}

	var toPause []model.WeChatBinding
	if err := h.db.Where("user_id = ? AND state = ? AND persona_id <> ?", uid, "active", p.ID).
		Find(&toPause).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}

	tx := h.db.Begin()
	defer func() { _ = tx.Rollback() }()
	if err := tx.Model(&model.Persona{}).
		Where("user_id = ?", uid).
		Update("is_active", false).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	if err := tx.Model(&model.Persona{}).
		Where("id = ?", p.ID).
		Update("is_active", true).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	if len(toPause) > 0 {
		ids := make([]string, len(toPause))
		for i, b := range toPause {
			ids[i] = b.ID
		}
		if err := tx.Model(&model.WeChatBinding{}).
			Where("id IN ?", ids).
			Update("state", "paused").Error; err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
	}
	// See Deactivate above: failing to commit then stopping runners would
	// desync DB state ("active") from runtime state (no goroutine) and
	// silently break inbound delivery for the user.
	if err := tx.Commit().Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}

	for _, b := range toPause {
		h.mgr.Stop(b.ID)
	}

	var toResume model.WeChatBinding
	err := h.db.Where("user_id = ? AND persona_id = ? AND state = ?", uid, p.ID, "paused").
		Order("updated_at desc").First(&toResume).Error
	if err == nil {
		if err := h.db.Model(&model.WeChatBinding{}).
			Where("id = ?", toResume.ID).
			Update("state", "active").Error; err != nil {
			fail(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
		toResume.State = "active"
		if startErr := h.mgr.Start(&toResume); startErr != nil {
			fail(c, http.StatusInternalServerError, 500, startErr.Error())
			return
		}
	}

	ok(c, gin.H{"id": p.ID})
}
