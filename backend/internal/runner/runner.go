package runner

import (
	"context"
	"errors"
	"runtime/debug"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/engine"
	"github.com/opentheone/opentheone/backend/internal/ilink"
	"github.com/opentheone/opentheone/backend/internal/model"
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

// Stop terminates the runner and best-effort announces shutdown to the
// upstream server via /ilink/bot/msg/notifystop so it can release long-poll
// state. The notification is sent on a fresh detached context so the
// already-canceled runner ctx doesn't immediately abort it.
func (m *Manager) Stop(bindingID string) {
	m.mu.Lock()
	r, ok := m.runners[bindingID]
	if ok {
		r.cancel()
		delete(m.runners, bindingID)
	}
	m.mu.Unlock()
	if ok {
		m.notifyStopAsync(bindingID)
	}
}

// StopAll cancels every runner and best-effort fires notifyStop in parallel.
// Returns once the notifyStop calls have either succeeded, errored, or
// timed out — bounded to ~10s overall so process shutdown isn't blocked.
func (m *Manager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.runners))
	for id, r := range m.runners {
		r.cancel()
		ids = append(ids, id)
	}
	m.runners = map[string]*bindingRunner{}
	m.mu.Unlock()

	if len(ids) == 0 {
		return
	}
	done := make(chan struct{}, len(ids))
	for _, id := range ids {
		go func(bindingID string) {
			defer func() {
				_ = recover()
				done <- struct{}{}
			}()
			m.notifyStopSync(bindingID)
		}(id)
	}
	timeout := time.After(10 * time.Second)
	for range ids {
		select {
		case <-done:
		case <-timeout:
			return
		}
	}
}

// notifyStopAsync issues a best-effort notifyStop for a single binding from
// a fresh goroutine. Used by Stop, where we don't want to block the caller.
func (m *Manager) notifyStopAsync(bindingID string) {
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				m.log.Warn("notifyStop goroutine panic recovered",
					zap.String("binding_id", bindingID),
					zap.Any("panic", rec))
			}
		}()
		m.notifyStopSync(bindingID)
	}()
}

// notifyStopSync sends a single notifyStop for the given binding using a
// fresh 8s context detached from the (canceled) runner ctx.
func (m *Manager) notifyStopSync(bindingID string) {
	var b model.WeChatBinding
	if err := m.db.Where("id = ?", bindingID).First(&b).Error; err != nil {
		return
	}
	if b.BotToken == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	sess := ilink.Session{
		BotToken:    b.BotToken,
		BaseURL:     b.BaseURL,
		ILinkBotID:  b.ILinkBotID,
		ILinkUserID: b.ILinkUserID,
	}
	if _, err := m.ilink.NotifyStop(ctx, sess); err != nil {
		m.log.Debug("notifyStop failed (ignored)",
			zap.String("binding_id", bindingID),
			zap.Error(err))
	}
}

// bindingRunner is the per-binding long-poll loop.
type bindingRunner struct {
	mgr       *Manager
	bindingID string
	ctx       context.Context
	cancel    context.CancelFunc
	log       *zap.Logger

	// nextLongPollMS is the client-side timeout to use on the next getupdates
	// call (in ms). The server's `longpolling_timeout_ms` response field is
	// authoritative; we initialize to the configured default and update it
	// whenever the server tells us a different window. Matches the
	// `nextTimeoutMs` variable in monitorWeixinProvider.
	nextLongPollMS int

	// notifyStartDone tracks whether we have successfully announced this
	// runner to the server via /ilink/bot/msg/notifystart. Failures are
	// retried on the next loop iteration; once it succeeds we never call it
	// again for the lifetime of this runner.
	notifyStartDone bool
}

func (r *bindingRunner) loop() {
	r.log.Info("runner started")
	defer r.log.Info("runner stopped")

	if r.nextLongPollMS <= 0 {
		if d := r.mgr.ilink.LongPollTimeout; d > 0 {
			r.nextLongPollMS = int(d / time.Millisecond)
		} else {
			r.nextLongPollMS = 35000
		}
	}

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

// announceStart fires /ilink/bot/msg/notifystart for the current binding the
// first time the runner observes the binding is active. Best-effort: the
// official client treats failures here as ignorable, but skipping it has
// been observed to leave the session in a state where the server never
// pushes inbound messages.
func (r *bindingRunner) announceStart(binding *model.WeChatBinding) {
	if r.notifyStartDone {
		return
	}
	sess := ilink.Session{
		BotToken:    binding.BotToken,
		BaseURL:     binding.BaseURL,
		ILinkBotID:  binding.ILinkBotID,
		ILinkUserID: binding.ILinkUserID,
	}
	ctx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
	defer cancel()
	resp, err := r.mgr.ilink.NotifyStart(ctx, sess)
	if err != nil {
		r.log.Warn("notifystart failed (ignored)", zap.Error(err))
		return
	}
	if resp.Ret != 0 {
		r.log.Warn("notifystart non-zero ret (ignored)",
			zap.Int("ret", resp.Ret),
			zap.String("errmsg", resp.ErrMsg))
	} else {
		r.log.Info("notifystart ok")
	}
	r.notifyStartDone = true
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

	// Best-effort: announce this client before the first long-poll. The
	// official gateway does this in startAccount; failure is ignored but
	// skipping it entirely has been seen to silently mute inbound delivery.
	r.announceStart(&binding)

	sess := ilink.Session{
		BotToken:    binding.BotToken,
		BaseURL:     binding.BaseURL,
		ILinkBotID:  binding.ILinkBotID,
		ILinkUserID: binding.ILinkUserID,
	}

	// Per-call deadline: long-poll budget plus a small buffer for TCP
	// teardown. This is intentionally larger than the server-side timeout
	// (advertised via longpolling_timeout_ms in each response) so a normal
	// idle poll doesn't look like a client-side abort.
	timeoutMS := r.nextLongPollMS
	if timeoutMS < 5000 {
		timeoutMS = 5000
	}
	callCtx, cancelCall := context.WithTimeout(r.ctx, time.Duration(timeoutMS)*time.Millisecond+5*time.Second)
	r.log.Debug("getupdates start",
		zap.Int("timeout_ms", timeoutMS),
		zap.Int("buf_len", len(binding.GetUpdatesBuf)))
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

	// Adopt the server's preferred long-poll window for next iteration.
	if resp.LongPollingTimeoutMS > 0 {
		r.nextLongPollMS = resp.LongPollingTimeoutMS
	}

	if ilink.IsSessionExpired(resp.Ret, resp.ErrCode) {
		r.log.Warn("session expired, marking binding",
			zap.Int("ret", resp.Ret),
			zap.Int("errcode", resp.ErrCode),
			zap.String("errmsg", resp.ErrMsg))
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

	// Match the official client: ANY non-zero ret OR non-zero errcode is a
	// protocol-level failure. Treating only ret would silently drop calls
	// where the server signaled via errcode alone.
	if resp.Ret != 0 || resp.ErrCode != 0 {
		*consecutiveFailures++
		r.log.Warn("getupdates non-zero status",
			zap.Int("ret", resp.Ret),
			zap.Int("errcode", resp.ErrCode),
			zap.String("errmsg", resp.ErrMsg),
			zap.Int("fails", *consecutiveFailures))
		r.backoff(*consecutiveFailures)
		return false
	}

	*consecutiveFailures = 0

	r.log.Debug("getupdates ok",
		zap.Int("msg_count", len(resp.Msgs)),
		zap.Int("next_long_poll_ms", r.nextLongPollMS),
		zap.Int("new_buf_len", len(resp.GetUpdatesBuf)))

	if resp.GetUpdatesBuf != "" && resp.GetUpdatesBuf != binding.GetUpdatesBuf {
		_ = r.mgr.db.Model(&model.WeChatBinding{}).
			Where("id = ?", r.bindingID).
			Update("get_updates_buf", resp.GetUpdatesBuf).Error
	}

	for i := range resp.Msgs {
		msg := resp.Msgs[i]
		// Skip our own echoed bot messages — but DO NOT filter by
		// message_state. The protocol marks the field optional and several
		// inbound user messages have been observed without it; Go's zero
		// value would otherwise equal MessageStateNew and the message would
		// be dropped on the floor. Mirrors monitorWeixinProvider, which
		// forwards every message verbatim to processOneMessage.
		if msg.MessageType == ilink.MessageTypeBot {
			r.log.Debug("skip bot echo",
				zap.Int64("ilink_message_id", msg.MessageID))
			continue
		}
		// GENERATING is a transient intermediate state for streaming bot
		// replies; we never want to react to a half-finished message.
		if msg.MessageState == ilink.MessageStateGenerating {
			continue
		}
		r.log.Debug("dispatch inbound",
			zap.Int64("ilink_message_id", msg.MessageID),
			zap.String("from", msg.FromUserID),
			zap.Int("state", msg.MessageState),
			zap.Int("items", len(msg.ItemList)))
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
