package runner

import (
	"context"
	"errors"
	"runtime/debug"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/wzyjerry/opentheone/backend/internal/engine"
	"github.com/wzyjerry/opentheone/backend/internal/ilink"
	"github.com/wzyjerry/opentheone/backend/internal/model"
)

// Manager owns one long-poll goroutine per active WeChatBinding.
type Manager struct {
	db     *gorm.DB
	ilink  *ilink.Client
	engine *engine.Engine
	log    *zap.Logger

	mu      sync.Mutex
	runners map[string]*bindingRunner
	rootCtx context.Context
}

func NewManager(rootCtx context.Context, db *gorm.DB, ilinkClient *ilink.Client, eng *engine.Engine, log *zap.Logger) *Manager {
	return &Manager{
		db:      db,
		ilink:   ilinkClient,
		engine:  eng,
		log:     log,
		runners: map[string]*bindingRunner{},
		rootCtx: rootCtx,
	}
}

// Bootstrap loads every active binding and starts a runner for each.
func (m *Manager) Bootstrap() error {
	var bindings []model.WeChatBinding
	if err := m.db.Where("state = ?", "active").Find(&bindings).Error; err != nil {
		return err
	}
	for i := range bindings {
		b := bindings[i]
		if err := m.Start(&b); err != nil {
			m.log.Warn("start runner failed", zap.String("binding_id", b.ID), zap.Error(err))
		}
	}
	return nil
}

// Start launches (or replaces) a runner for the given binding.
func (m *Manager) Start(b *model.WeChatBinding) error {
	if b == nil || b.ID == "" {
		return errors.New("nil binding")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.runners[b.ID]; ok {
		existing.cancel()
		delete(m.runners, b.ID)
	}

	ctx, cancel := context.WithCancel(m.rootCtx)
	r := &bindingRunner{
		mgr:       m,
		bindingID: b.ID,
		ctx:       ctx,
		cancel:    cancel,
		log:       m.log.With(zap.String("binding_id", b.ID)),
	}
	m.runners[b.ID] = r
	go r.loop()
	return nil
}

// Stop terminates the runner.
func (m *Manager) Stop(bindingID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runners[bindingID]; ok {
		r.cancel()
		delete(m.runners, bindingID)
	}
}

// StopAll waits for every runner to exit.
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.runners {
		r.cancel()
	}
	m.runners = map[string]*bindingRunner{}
}

// bindingRunner is the per-binding long-poll loop.
type bindingRunner struct {
	mgr       *Manager
	bindingID string
	ctx       context.Context
	cancel    context.CancelFunc
	log       *zap.Logger
}

func (r *bindingRunner) loop() {
	r.log.Info("runner started")
	defer r.log.Info("runner stopped")

	consecutiveFailures := 0

	for {
		select {
		case <-r.ctx.Done():
			return
		default:
		}

		// Each iteration is wrapped so a panic in one inbound message does not
		// kill the entire long-poll loop — without this a single panic would
		// silently take the user's AI offline until process restart.
		if stop := r.iterateOnce(&consecutiveFailures); stop {
			return
		}
	}
}

// iterateOnce performs exactly one getupdates round-trip plus message
// dispatch. Returns true if the caller should stop the loop entirely
// (binding gone / expired / state != active). Panics are recovered into a
// non-terminal failure so a buggy message never takes the AI offline.
func (r *bindingRunner) iterateOnce(consecutiveFailures *int) (stop bool) {
	defer func() {
		if rec := recover(); rec != nil {
			r.log.Error("runner panicked, continuing",
				zap.Any("panic", rec),
				zap.String("stack", string(debug.Stack())))
			*consecutiveFailures++
			r.backoff(*consecutiveFailures)
			stop = false
		}
	}()

	var binding model.WeChatBinding
	if err := r.mgr.db.Where("id = ?", r.bindingID).First(&binding).Error; err != nil {
		r.log.Warn("binding gone", zap.Error(err))
		return true
	}
	if binding.State != "active" {
		r.log.Info("binding not active, exiting", zap.String("state", binding.State))
		return true
	}

	sess := ilink.Session{
		BotToken:    binding.BotToken,
		BaseURL:     binding.BaseURL,
		ILinkBotID:  binding.ILinkBotID,
		ILinkUserID: binding.ILinkUserID,
	}

	callCtx, cancelCall := context.WithTimeout(r.ctx, 45*time.Second)
	resp, err := r.mgr.ilink.GetUpdates(callCtx, sess, binding.GetUpdatesBuf)
	cancelCall()

	if err != nil {
		if r.ctx.Err() != nil {
			return true
		}
		*consecutiveFailures++
		r.log.Warn("getupdates error", zap.Error(err), zap.Int("fails", *consecutiveFailures))
		r.backoff(*consecutiveFailures)
		return false
	}

	if ilink.IsSessionExpired(resp.Ret, resp.ErrCode) {
		r.log.Warn("session expired, marking binding")
		_ = r.mgr.db.Model(&model.WeChatBinding{}).
			Where("id = ?", r.bindingID).
			Updates(map[string]interface{}{
				"state":              "expired",
				"get_updates_buf":    "",
				"last_context_token": "",
				"typing_ticket":      "",
			}).Error
		return true
	}

	if resp.Ret != 0 {
		*consecutiveFailures++
		r.log.Warn("getupdates non-zero ret",
			zap.Int("ret", resp.Ret),
			zap.Int("errcode", resp.ErrCode),
			zap.String("errmsg", resp.ErrMsg))
		r.backoff(*consecutiveFailures)
		return false
	}

	*consecutiveFailures = 0

	if resp.GetUpdatesBuf != "" && resp.GetUpdatesBuf != binding.GetUpdatesBuf {
		_ = r.mgr.db.Model(&model.WeChatBinding{}).
			Where("id = ?", r.bindingID).
			Update("get_updates_buf", resp.GetUpdatesBuf).Error
	}

	for i := range resp.Msgs {
		msg := resp.Msgs[i]
		if msg.MessageType == ilink.MessageTypeBot {
			continue
		}
		if msg.MessageState == ilink.MessageStateGenerating || msg.MessageState == ilink.MessageStateNew {
			continue
		}
		r.safeHandleMessage(&binding, msg)
	}
	return false
}

// safeHandleMessage wraps handleMessage with a panic recovery so one bad
// message can't crash the whole loop's iteration body.
func (r *bindingRunner) safeHandleMessage(binding *model.WeChatBinding, msg ilink.WeixinMessage) {
	defer func() {
		if rec := recover(); rec != nil {
			r.log.Error("handleMessage panicked",
				zap.Any("panic", rec),
				zap.Int64("ilink_message_id", msg.MessageID),
				zap.String("stack", string(debug.Stack())))
		}
	}()
	r.handleMessage(binding, msg)
}

func (r *bindingRunner) handleMessage(binding *model.WeChatBinding, msg ilink.WeixinMessage) {
	var persona model.Persona
	if err := r.mgr.db.Where("id = ?", binding.PersonaID).First(&persona).Error; err != nil {
		r.log.Warn("persona missing", zap.Error(err))
		return
	}
	var llmCfg model.LLMConfig
	var llmCfgPtr *model.LLMConfig
	if persona.LLMConfigID != "" {
		if err := r.mgr.db.Where("id = ?", persona.LLMConfigID).First(&llmCfg).Error; err == nil {
			llmCfgPtr = &llmCfg
		}
	}
	if llmCfgPtr == nil {
		var fallback model.LLMConfig
		if err := r.mgr.db.Where("user_id = ? AND is_default = ?", persona.UserID, true).First(&fallback).Error; err == nil {
			llmCfgPtr = &fallback
		}
	}

	ctx, cancel := context.WithTimeout(r.ctx, 120*time.Second)
	defer cancel()
	if err := r.mgr.engine.HandleInbound(ctx, binding, &persona, llmCfgPtr, msg); err != nil {
		r.log.Error("handle inbound failed", zap.Error(err))
	}
}

func (r *bindingRunner) backoff(fails int) {
	d := time.Duration(fails) * 2 * time.Second
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	select {
	case <-r.ctx.Done():
	case <-time.After(d):
	}
}
