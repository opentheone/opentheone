package runner

import (
	"context"
	"errors"
	"runtime/debug"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/wzyjerry/opentheone/backend/internal/ilink"
	"github.com/wzyjerry/opentheone/backend/internal/model"
)

// QRLoginCoordinator drives QR scan polling in the background per binding.
type QRLoginCoordinator struct {
	db      *gorm.DB
	ilink   *ilink.Client
	mgr     *Manager
	log     *zap.Logger
	rootCtx context.Context
}

func NewQRLoginCoordinator(db *gorm.DB, ilinkClient *ilink.Client, mgr *Manager, log *zap.Logger) *QRLoginCoordinator {
	return &QRLoginCoordinator{
		db:      db,
		ilink:   ilinkClient,
		mgr:     mgr,
		log:     log,
		rootCtx: mgr.rootCtx,
	}
}

// StartScan creates (or refreshes) a binding row in `pending_scan` state with a fresh QR.
// As a side effect any other active binding for the same user is moved to `paused`
// (its long-poll runner is stopped) so the new persona truly becomes the one.
func (c *QRLoginCoordinator) StartScan(ctx context.Context, userID, personaID string) (*model.WeChatBinding, error) {
	if userID == "" || personaID == "" {
		return nil, errors.New("missing userID or personaID")
	}

	qr, err := c.ilink.GetBotQRCode(ctx)
	if err != nil {
		return nil, err
	}

	var conflicts []model.WeChatBinding
	if err := c.db.Where("user_id = ? AND state = ? AND persona_id <> ?", userID, "active", personaID).
		Find(&conflicts).Error; err == nil {
		for _, b := range conflicts {
			_ = c.db.Model(&model.WeChatBinding{}).
				Where("id = ?", b.ID).
				Update("state", "paused").Error
			c.mgr.Stop(b.ID)
		}
	}

	var binding model.WeChatBinding
	err = c.db.WithContext(ctx).
		Where("user_id = ? AND persona_id = ?", userID, personaID).
		First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		binding = model.WeChatBinding{
			UserID:         userID,
			PersonaID:      personaID,
			State:          "pending_scan",
			QRCodeToken:    qr.QRCode,
			QRCodeImageURL: qr.QRCodeImageURL,
			ScanPhase:      ilink.QRStatusWait,
		}
		if err := c.db.WithContext(ctx).Create(&binding).Error; err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		c.mgr.Stop(binding.ID)
		// NOTE: map keys are *physical column names* (snake_case as produced by
		// GORM's NamingStrategy on the Go field names), NOT the JSON tags.
		// QRCodeImageURL -> qr_code_image_url; QRCodeToken -> qr_code_token.
		// Verify with `sqlite3 data/oto.db ".schema we_chat_bindings"`.
		updates := map[string]interface{}{
			"state":              "pending_scan",
			"qr_code_token":      qr.QRCode,
			"qr_code_image_url":  qr.QRCodeImageURL,
			"scan_phase":         ilink.QRStatusWait,
			"bot_token":          "",
			"get_updates_buf":    "",
			"last_context_token": "",
			"typing_ticket":      "",
		}
		if err := c.db.WithContext(ctx).Model(&model.WeChatBinding{}).
			Where("id = ?", binding.ID).
			Updates(updates).Error; err != nil {
			return nil, err
		}
		binding.State = "pending_scan"
		binding.QRCodeToken = qr.QRCode
		binding.QRCodeImageURL = qr.QRCodeImageURL
		binding.ScanPhase = ilink.QRStatusWait
	}

	go c.pollScan(binding.ID, qr.QRCode)

	return &binding, nil
}

// pollScan keeps long-polling get_qrcode_status until confirmed/expired/abandoned.
func (c *QRLoginCoordinator) pollScan(bindingID, qrToken string) {
	defer func() {
		if rec := recover(); rec != nil {
			c.log.Error("pollScan panicked",
				zap.String("binding_id", bindingID),
				zap.Any("panic", rec),
				zap.String("stack", string(debug.Stack())))
		}
	}()
	parent := c.rootCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	log := c.log.With(zap.String("binding_id", bindingID))

	for {
		var binding model.WeChatBinding
		if err := c.db.Where("id = ?", bindingID).First(&binding).Error; err != nil {
			log.Warn("binding gone during scan", zap.Error(err))
			return
		}
		if binding.QRCodeToken != qrToken || binding.State != "pending_scan" {
			log.Info("scan aborted (state changed)", zap.String("state", binding.State))
			return
		}

		statusCtx, statusCancel := context.WithTimeout(ctx, 40*time.Second)
		status, err := c.ilink.GetQRCodeStatus(statusCtx, qrToken)
		statusCancel()

		if err != nil {
			log.Warn("qr status poll error", zap.Error(err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}

		switch status.Status {
		case ilink.QRStatusWait, ilink.QRStatusScanned:
			if binding.ScanPhase != status.Status {
				_ = c.db.Model(&model.WeChatBinding{}).
					Where("id = ?", bindingID).
					Update("scan_phase", status.Status).Error
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(800 * time.Millisecond):
				continue
			}
		case ilink.QRStatusExpired:
			_ = c.db.Model(&model.WeChatBinding{}).
				Where("id = ?", bindingID).
				Updates(map[string]interface{}{
					"state":      "expired",
					"scan_phase": ilink.QRStatusExpired,
				}).Error
			return
		case ilink.QRStatusConfirmed:
			baseURL := status.BaseURL
			if baseURL == "" {
				baseURL = c.ilink.BaseURL
			}
			// NOTE: map keys are *physical column names*. ILinkBotID becomes
			// `i_link_bot_id` (GORM splits at every CamelCase boundary) — the
			// JSON-friendly `ilink_bot_id` is a separate concern handled in
			// handler responses.
			updates := map[string]interface{}{
				"state":          "active",
				"scan_phase":     ilink.QRStatusConfirmed,
				"bot_token":      status.BotToken,
				"base_url":       baseURL,
				"i_link_bot_id":  status.ILinkBotID,
				"i_link_user_id": status.ILinkUserID,
			}
			if err := c.db.Model(&model.WeChatBinding{}).
				Where("id = ?", bindingID).
				Updates(updates).Error; err != nil {
				log.Error("save active binding failed", zap.Error(err))
				return
			}
			var fresh model.WeChatBinding
			if err := c.db.Where("id = ?", bindingID).First(&fresh).Error; err != nil {
				log.Error("reload binding failed", zap.Error(err))
				return
			}
			// Auto-activate the persona this binding is for. A confirmed scan
			// is the clearest possible user intent that "this is the one";
			// without this step the dashboard wrongly reports "no active
			// persona" and the proactive scheduler (which filters on
			// is_active = true) refuses to fire.
			if err := c.activatePersona(fresh.UserID, fresh.PersonaID); err != nil {
				log.Warn("activate persona after scan failed", zap.Error(err))
			}
			if err := c.mgr.Start(&fresh); err != nil {
				log.Error("start runner failed", zap.Error(err))
			}
			log.Info("binding active")
			return
		default:
			log.Warn("unknown qr status", zap.String("status", status.Status))
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
	}
}

// activatePersona enforces the "one active persona per user" rule by flipping
// every persona owned by the user off and setting the target one on, in a
// transaction. Idempotent: re-running with the same target is a no-op.
func (c *QRLoginCoordinator) activatePersona(userID, personaID string) error {
	if userID == "" || personaID == "" {
		return errors.New("missing userID or personaID")
	}
	return c.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Persona{}).
			Where("user_id = ?", userID).
			Update("is_active", false).Error; err != nil {
			return err
		}
		return tx.Model(&model.Persona{}).
			Where("id = ? AND user_id = ?", personaID, userID).
			Update("is_active", true).Error
	})
}
