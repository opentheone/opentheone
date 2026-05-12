package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/wzyjerry/opentheone/backend/internal/model"
	"github.com/wzyjerry/opentheone/backend/internal/runner"
)

type BindingHandler struct {
	db    *gorm.DB
	mgr   *runner.Manager
	qrlog *runner.QRLoginCoordinator
}

func NewBindingHandler(db *gorm.DB, mgr *runner.Manager, qrlog *runner.QRLoginCoordinator) *BindingHandler {
	return &BindingHandler{db: db, mgr: mgr, qrlog: qrlog}
}

type bindingStartReq struct {
	PersonaID string `json:"persona_id"`
}

// Start asks the iLink server for a fresh QR and creates/refreshes a binding row.
func (h *BindingHandler) Start(c *gin.Context) {
	uid := currentUserID(c)
	var req bindingStartReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if req.PersonaID == "" {
		fail(c, http.StatusBadRequest, 400, "persona_id required")
		return
	}
	var p model.Persona
	if err := h.db.Where("id = ? AND user_id = ?", req.PersonaID, uid).First(&p).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "persona not found")
		return
	}

	binding, err := h.qrlog.StartScan(c.Request.Context(), uid, req.PersonaID)
	if err != nil {
		fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	ok(c, gin.H{
		"binding_id":       binding.ID,
		"qrcode_token":     binding.QRCodeToken,
		"qrcode_image_url": binding.QRCodeImageURL,
		"state":            binding.State,
	})
}

type bindingStatusReq struct {
	BindingID string `json:"binding_id"`
}

func (h *BindingHandler) Status(c *gin.Context) {
	uid := currentUserID(c)
	var req bindingStatusReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	var b model.WeChatBinding
	if err := h.db.Where("id = ? AND user_id = ?", req.BindingID, uid).First(&b).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}
	ok(c, gin.H{
		"binding_id":       b.ID,
		"state":            b.State,
		"phase":            phaseOf(&b),
		"qrcode_image_url": b.QRCodeImageURL,
		"ilink_bot_id":     b.ILinkBotID,
		"ilink_user_id":    b.ILinkUserID,
		"persona_id":       b.PersonaID,
	})
}

// phaseOf computes the surfaced phase string for the API:
// for `pending_scan` it reflects the live QR scan progress (`wait` / `scanned`),
// for `active` it returns `confirmed`, and otherwise it returns the state itself.
func phaseOf(b *model.WeChatBinding) string {
	switch b.State {
	case "active":
		return "confirmed"
	case "pending_scan":
		if b.ScanPhase != "" {
			return b.ScanPhase
		}
		return "wait"
	default:
		return b.State
	}
}

type bindingForPersonaReq struct {
	PersonaID string `json:"persona_id"`
}

// ForPersona returns the binding (any state) associated with the given persona.
func (h *BindingHandler) ForPersona(c *gin.Context) {
	uid := currentUserID(c)
	var req bindingForPersonaReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if req.PersonaID == "" {
		fail(c, http.StatusBadRequest, 400, "persona_id required")
		return
	}
	var b model.WeChatBinding
	err := h.db.Where("user_id = ? AND persona_id = ?", uid, req.PersonaID).
		Order("updated_at desc").First(&b).Error
	if err != nil {
		ok(c, gin.H{"binding": nil})
		return
	}
	ok(c, gin.H{
		"binding": gin.H{
			"binding_id":       b.ID,
			"state":            b.State,
			"phase":            phaseOf(&b),
			"qrcode_image_url": b.QRCodeImageURL,
			"ilink_bot_id":     b.ILinkBotID,
			"ilink_user_id":    b.ILinkUserID,
			"persona_id":       b.PersonaID,
		},
	})
}

// Active returns the currently active binding for the user (if any).
func (h *BindingHandler) Active(c *gin.Context) {
	uid := currentUserID(c)
	var b model.WeChatBinding
	err := h.db.Where("user_id = ? AND state = ?", uid, "active").Order("updated_at desc").First(&b).Error
	if err != nil {
		ok(c, gin.H{"active": nil})
		return
	}
	ok(c, gin.H{
		"active": gin.H{
			"binding_id":    b.ID,
			"persona_id":    b.PersonaID,
			"ilink_bot_id":  b.ILinkBotID,
			"ilink_user_id": b.ILinkUserID,
			"base_url":      b.BaseURL,
		},
	})
}

func (h *BindingHandler) Revoke(c *gin.Context) {
	uid := currentUserID(c)
	var req bindingStatusReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	var b model.WeChatBinding
	if err := h.db.Where("id = ? AND user_id = ?", req.BindingID, uid).First(&b).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}
	h.mgr.Stop(b.ID)
	if err := h.db.Model(&b).Updates(map[string]interface{}{
		"state":              "revoked",
		"bot_token":          "",
		"get_updates_buf":    "",
		"typing_ticket":      "",
		"last_context_token": "",
		"last_proactive_at":  time.Time{},
		"qrcode_token":       "",
		"qrcode_image_url":   "",
		"scan_phase":         "",
	}).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	// Wipe per-conversation context tokens too. Otherwise a future re-scan
	// would inherit stale tokens that are guaranteed to fail at sendmessage
	// time, masquerading as "AI silently doesn't reply".
	_ = h.db.Model(&model.Conversation{}).
		Where("binding_id = ?", b.ID).
		Update("last_context_token", "").Error
	ok(c, gin.H{"id": b.ID})
}

func (h *BindingHandler) Restart(c *gin.Context) {
	uid := currentUserID(c)
	var req bindingStatusReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	var b model.WeChatBinding
	if err := h.db.Where("id = ? AND user_id = ?", req.BindingID, uid).First(&b).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}
	if b.State != "active" {
		fail(c, http.StatusBadRequest, 400, "binding not active")
		return
	}
	if err := h.mgr.Start(&b); err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{"id": b.ID})
}
