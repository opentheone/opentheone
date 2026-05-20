// Package memory — pipeline.go: per-(persona, conversation) async scheduler
// for L1/L2/L3 processing.
//
// # Triggers
//
// The pipeline runs in response to several signals (decided by the engine
// each time it persists a new message pair):
//
//   - warmup       Within the first N exchanges with a fresh conversation we
//                  extract on a doubling schedule (1, 2, 4, 8, 16, then
//                  every 16) so the user sees recall improving immediately.
//   - threshold    Once warm, we run once every 16 messages.
//   - idle         If the user has been silent for >= 5 minutes since the
//                  last unprocessed message, flush whatever is queued.
//   - cold-start   First exchange after the server boots — re-warm caches.
//   - explicit     The HTTP API can request a flush (manual button in UI).
//
// All work happens in goroutines; the engine returns to the user as soon
// as it has streamed the LLM reply, never blocking on memory processing.
//
// # State (two tables)
//
//   - memory_extract_checkpoints — per (persona, conversation) watermark,
//     warmup curve, and L1/L2 timestamps. One persona usually has many
//     simultaneous WeChat peers, each with its own message stream, so the
//     checkpoint MUST live at conversation granularity. Otherwise a busy
//     peer would constantly push the watermark forward and starve quieter
//     peers (their pending counts would be measured against the "wrong"
//     conversation's last message).
//
//   - memory_pipeline_states — per-persona counters that accumulate across
//     all conversations. The L3 user profile is per-persona and is
//     regenerated when total atoms / scenes since last profile cross
//     thresholds, with a 6h cool-down.

package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/llm"
	"github.com/opentheone/opentheone/backend/internal/model"
)

// Pipeline owns the async memory-processing goroutines.
type Pipeline struct {
	mem *Service
	log *zap.Logger

	// inflight gates one goroutine per (persona, conversation). The second
	// concurrent trigger for the same pair is dropped (it would just
	// process the same backlog twice).
	mu       sync.Mutex
	inflight map[string]bool

	// profileMu serialises L3 profile regeneration per-persona — multiple
	// conversations of the same persona can extract concurrently, but only
	// one of them gets to regenerate the (per-persona) profile at a time.
	profileMu       sync.Mutex
	profileInflight map[string]bool

	// clientResolver lets the engine plug in its own "decrypted llm.Client
	// for this persona" lookup without the pipeline package depending on
	// the engine. The engine assigns this at NewEngine time.
	clientResolver ClientResolver

	// idleAfter is the inactivity window for the idle trigger. Configurable
	// for tests; default 5min in production.
	idleAfter time.Duration

	// profileEvery controls how many newly-fitted scenes (or atoms) we
	// require before re-running L3 synthesis.
	profileEveryAtoms  int
	profileEveryScenes int

	// l3Cooldown caps L3 rebuild frequency regardless of triggers (LLM
	// cost guard).
	l3Cooldown time.Duration
}

// ClientResolver returns an LLM client suitable for processing a persona's
// memory — typically the engine's same persona-config-with-fallback lookup.
type ClientResolver func(ctx context.Context, personaID string) (*llm.Client, error)

// NewPipeline constructs a Pipeline. ClientResolver must be set by the
// caller via SetClientResolver before any trigger will perform LLM work.
func NewPipeline(mem *Service, log *zap.Logger) *Pipeline {
	if log == nil {
		log = zap.NewNop()
	}
	return &Pipeline{
		mem:                mem,
		log:                log,
		inflight:           make(map[string]bool),
		profileInflight:    make(map[string]bool),
		idleAfter:          5 * time.Minute,
		profileEveryAtoms:  20,
		profileEveryScenes: 3,
		l3Cooldown:         6 * time.Hour,
	}
}

// SetClientResolver wires the engine-side LLM client lookup. Must be called
// before triggers fire (typically right after engine construction).
func (p *Pipeline) SetClientResolver(r ClientResolver) {
	p.clientResolver = r
}

// Trigger is the public entry point the engine calls after persisting a
// new message pair. The decision of WHICH triggers to fire happens here,
// driven by the per-(persona, conv) checkpoint and the per-persona profile
// counters.
//
// Always returns immediately — actual LLM work runs in a goroutine.
func (p *Pipeline) Trigger(personaID, conversationID string) {
	if p == nil || p.mem == nil {
		return
	}
	go p.runIfNeeded(context.Background(), personaID, conversationID, "auto", false)
}

// TriggerExplicit forces a flush regardless of warmup / threshold. Used by
// the manual "rebuild memory" UI button. Runs synchronously so the HTTP
// response carries the outcome.
func (p *Pipeline) TriggerExplicit(ctx context.Context, personaID, conversationID string) error {
	return p.runIfNeeded(ctx, personaID, conversationID, "manual", true)
}

// runIfNeeded is the workhorse. `force` skips the warmup / threshold gate
// — every other branch (locking, error logging, L3 cool-down) still applies.
func (p *Pipeline) runIfNeeded(ctx context.Context, personaID, conversationID, reason string, force bool) error {
	if personaID == "" {
		return errors.New("pipeline: persona_id required")
	}

	// One goroutine per (persona, conversation). Other peers of the same
	// persona can still extract concurrently — the profile step is guarded
	// separately by profileMu below.
	key := personaID + "|" + conversationID
	p.mu.Lock()
	if p.inflight[key] {
		p.mu.Unlock()
		return nil
	}
	p.inflight[key] = true
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.inflight, key)
		p.mu.Unlock()
	}()

	chk, err := p.mem.loadOrInitCheckpoint(ctx, personaID, conversationID)
	if err != nil {
		p.log.Warn("pipeline.loadCheckpoint", zap.String("persona", personaID), zap.String("conv", conversationID), zap.Error(err))
		return err
	}

	pending, err := p.mem.countPendingMessages(ctx, conversationID, chk.LastExtractedMessageID)
	if err != nil {
		return err
	}

	now := time.Now()
	shouldExtract := force ||
		pending >= chk.NextThreshold ||
		(pending > 0 && now.Sub(chk.LastL1At) >= p.idleAfter) ||
		(pending > 0 && chk.TotalProcessed == 0) // cold start

	if !shouldExtract {
		return nil
	}

	client, err := p.resolveClient(ctx, personaID)
	if err != nil || client == nil {
		p.log.Debug("pipeline: no LLM client; deferring",
			zap.String("persona", personaID), zap.Error(err))
		return nil
	}

	createdAtomIDs, sceneTouched, err := p.runOneCycle(ctx, client, chk, conversationID, reason)
	if err != nil {
		p.log.Warn("pipeline.runOneCycle", zap.String("persona", personaID), zap.String("conv", conversationID), zap.Error(err))
		return err
	}

	chk.LastL1At = now
	if sceneTouched > 0 {
		chk.LastL2At = now
	}
	chk.TotalProcessed += pending
	// Doubling warmup curve: 1, 2, 4, 8, 16, 16, 16…
	if chk.NextThreshold < 16 {
		chk.NextThreshold *= 2
		if chk.NextThreshold > 16 {
			chk.NextThreshold = 16
		}
	}
	if err := p.mem.saveCheckpoint(ctx, chk); err != nil {
		p.log.Warn("pipeline.saveCheckpoint", zap.String("persona", personaID), zap.Error(err))
	}

	// L3 profile is per-persona; counters and decisions live in the
	// pipeline-state row. Multiple conversations may try to update at once
	// — profileMu serialises them.
	p.maybeRegenerateProfile(ctx, client, personaID, len(createdAtomIDs), sceneTouched, force, now)
	return nil
}

// maybeRegenerateProfile bumps the per-persona profile counters and, if the
// triggers + cool-down agree, runs the (LLM-expensive) L3 synthesis. The
// counter bump itself is always persisted, even when the regeneration
// itself is skipped, so the next caller sees the accumulated work.
func (p *Pipeline) maybeRegenerateProfile(ctx context.Context, client *llm.Client, personaID string, atomsDelta, scenesDelta int, force bool, now time.Time) {
	p.profileMu.Lock()
	if p.profileInflight[personaID] {
		p.profileMu.Unlock()
		return
	}
	p.profileInflight[personaID] = true
	p.profileMu.Unlock()
	defer func() {
		p.profileMu.Lock()
		delete(p.profileInflight, personaID)
		p.profileMu.Unlock()
	}()

	state, err := p.mem.loadOrInitState(ctx, personaID)
	if err != nil {
		p.log.Warn("pipeline.loadState", zap.String("persona", personaID), zap.Error(err))
		return
	}

	state.AtomsSinceLastProfile += atomsDelta
	state.ScenesSinceLastProfile += scenesDelta

	shouldProfile := force ||
		state.AtomsSinceLastProfile >= p.profileEveryAtoms ||
		state.ScenesSinceLastProfile >= p.profileEveryScenes ||
		state.RequestProfileUpdate
	cooledDown := now.Sub(state.LastL3At) >= p.l3Cooldown || state.LastL3At.IsZero()

	if shouldProfile && cooledDown {
		reasonStr := profileReason(state, force)
		if _, err := p.mem.RegenerateProfile(ctx, client, personaID, reasonStr); err != nil {
			p.log.Warn("pipeline.profile", zap.String("persona", personaID), zap.Error(err))
			// Fall through — partial success on the L1/L2 path still gets persisted.
		} else {
			state.LastL3At = now
			state.AtomsSinceLastProfile = 0
			state.ScenesSinceLastProfile = 0
			state.RequestProfileUpdate = false
			state.ProfileUpdateReason = ""
		}
	}

	if err := p.mem.saveState(ctx, state); err != nil {
		p.log.Warn("pipeline.saveState", zap.String("persona", personaID), zap.Error(err))
	}
}

// runOneCycle is the L0 → L1 → L2 sequence: pull recent messages, ask the
// LLM to extract atoms, dedup against existing memory, fit into scenes, and
// retry any orphan atoms whose previous scene-fit failed.
func (p *Pipeline) runOneCycle(ctx context.Context, client *llm.Client, chk *model.MemoryExtractCheckpoint, conversationID, reason string) (createdAtomIDs []string, sceneTouched int, err error) {
	persona := chk.PersonaID
	// Pull the unprocessed window. We deliberately cap at 32 messages per
	// cycle to keep extractor prompts compact; if the user dumps a 200-
	// message wall of text the pipeline will catch up over several ticks.
	const windowCap = 32
	msgs, err := p.mem.loadPendingMessages(ctx, conversationID, chk.LastExtractedMessageID, windowCap)
	if err != nil {
		return nil, 0, err
	}

	touchedSet := map[string]bool{}

	// Step 1: extract + dedup + insert + scene-fit the fresh window.
	if len(msgs) > 0 {
		snippet := formatSnippet(msgs)
		r, err := p.mem.ExtractAtoms(ctx, client, snippet)
		if err != nil {
			return nil, 0, fmt.Errorf("extract: %w", err)
		}
		lastMsgID := msgs[len(msgs)-1].ID
		chk.LastExtractedMessageID = lastMsgID

		if r != nil && len(r.Atoms) > 0 {
			contents := make([]string, 0, len(r.Atoms))
			for _, a := range r.Atoms {
				contents = append(contents, a.Content)
			}
			candidates, err := p.mem.CandidatesForDedup(ctx, persona, contents)
			if err != nil {
				return nil, 0, fmt.Errorf("dedup-candidates: %w", err)
			}
			actions, err := p.mem.DedupExtracted(ctx, client, r.Atoms, candidates)
			if err != nil {
				return nil, 0, fmt.Errorf("dedup: %w", err)
			}
			meta := IngestMeta{
				PersonaID:       persona,
				ConversationID:  conversationID,
				SourceMessageID: lastMsgID,
				ActivityAt:      time.Now(),
			}
			createdIDs, err := p.mem.ApplyDedupActions(ctx, actions, r.Atoms, meta)
			if err != nil {
				return nil, 0, fmt.Errorf("apply-dedup: %w", err)
			}
			createdAtomIDs = createdIDs

			if len(createdIDs) > 0 {
				touched, ferr := p.mem.FitAtomsToScenes(ctx, client, persona, createdIDs)
				if ferr != nil {
					// Don't fail the whole cycle — atoms are written; orphan
					// rescue below (or the next pipeline tick) will retry.
					p.log.Warn("pipeline.scene-fit (fresh)",
						zap.String("persona", persona), zap.Error(ferr))
				}
				for _, id := range touched {
					touchedSet[id] = true
				}
			}
		}
	}

	// Step 2: orphan rescue. Atoms whose scene-fit failed on a previous
	// pass (LLM error, malformed JSON, transient outage) sit with
	// `scene_id = ''`. Pick a bounded batch and re-fit them so they don't
	// stay orphan forever. Bounded to keep prompts small; remainder waits
	// for the next pipeline tick.
	const orphanCap = 16
	orphanIDs, err := p.mem.findOrphanAtoms(ctx, persona, orphanCap)
	if err != nil {
		p.log.Warn("pipeline.find-orphans", zap.String("persona", persona), zap.Error(err))
	} else if len(orphanIDs) > 0 {
		touched, ferr := p.mem.FitAtomsToScenes(ctx, client, persona, orphanIDs)
		if ferr != nil {
			p.log.Warn("pipeline.scene-fit (orphans)",
				zap.String("persona", persona), zap.Error(ferr))
		}
		for _, id := range touched {
			touchedSet[id] = true
		}
	}

	return createdAtomIDs, len(touchedSet), nil
}

func (p *Pipeline) resolveClient(ctx context.Context, personaID string) (*llm.Client, error) {
	if p.clientResolver == nil {
		return nil, nil
	}
	return p.clientResolver(ctx, personaID)
}

func profileReason(state *model.MemoryPipelineState, force bool) string {
	if force {
		return "manual"
	}
	if state.RequestProfileUpdate && state.ProfileUpdateReason != "" {
		return state.ProfileUpdateReason
	}
	if state.AtomsSinceLastProfile >= 20 {
		return "atom-threshold"
	}
	return "scene-threshold"
}

func formatSnippet(msgs []model.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		body := strings.TrimSpace(m.Text)
		if body == "" {
			continue
		}
		role := "USER"
		switch m.Direction {
		case "outbound":
			role = "ASSISTANT"
		case "inbound":
			role = "USER"
		default:
			// tool_call/tool_result/system — skip from the LLM-facing snippet
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", role, body)
	}
	return strings.TrimSpace(b.String())
}

// ============================================================================
// Helpers on Service (kept here because they're only used by the pipeline)
// ============================================================================

func (s *Service) loadOrInitState(ctx context.Context, personaID string) (*model.MemoryPipelineState, error) {
	var state model.MemoryPipelineState
	err := s.db.WithContext(ctx).Where("persona_id = ?", personaID).First(&state).Error
	if err == nil {
		return &state, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	state = model.MemoryPipelineState{
		PersonaID: personaID,
		UpdatedAt: time.Now(),
	}
	if err := s.db.WithContext(ctx).Create(&state).Error; err != nil {
		return nil, err
	}
	return &state, nil
}

func (s *Service) saveState(ctx context.Context, state *model.MemoryPipelineState) error {
	state.UpdatedAt = time.Now()
	return s.db.WithContext(ctx).Save(state).Error
}

func (s *Service) loadOrInitCheckpoint(ctx context.Context, personaID, conversationID string) (*model.MemoryExtractCheckpoint, error) {
	var chk model.MemoryExtractCheckpoint
	err := s.db.WithContext(ctx).
		Where("persona_id = ? AND conversation_id = ?", personaID, conversationID).
		First(&chk).Error
	if err == nil {
		return &chk, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	chk = model.MemoryExtractCheckpoint{
		PersonaID:      personaID,
		ConversationID: conversationID,
		NextThreshold:  1, // warmup starts here
		UpdatedAt:      time.Now(),
	}
	if err := s.db.WithContext(ctx).Create(&chk).Error; err != nil {
		return nil, err
	}
	return &chk, nil
}

func (s *Service) saveCheckpoint(ctx context.Context, chk *model.MemoryExtractCheckpoint) error {
	chk.UpdatedAt = time.Now()
	return s.db.WithContext(ctx).Save(chk).Error
}

func (s *Service) countPendingMessages(ctx context.Context, conversationID, lastID string) (int, error) {
	if conversationID == "" {
		return 0, nil
	}
	q := s.db.WithContext(ctx).Model(&model.Message{}).
		Where("conversation_id = ?", conversationID).
		Where("direction IN ?", []string{"inbound", "outbound"})
	if lastID != "" {
		// Compare by created_at via subquery to avoid ID ordering assumptions
		// (UUIDs aren't time-sortable). If the lastID row was deleted (e.g.
		// conversation got pruned), the subquery returns NULL and the
		// `> NULL` predicate is FALSE — count would stick at 0. We coalesce
		// to epoch-0 so a dangling watermark behaves like "no watermark".
		q = q.Where("created_at > COALESCE((SELECT created_at FROM messages WHERE id = ?), '1970-01-01')", lastID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return 0, err
	}
	return int(n), nil
}

func (s *Service) loadPendingMessages(ctx context.Context, conversationID, lastID string, limit int) ([]model.Message, error) {
	if conversationID == "" {
		return nil, nil
	}
	q := s.db.WithContext(ctx).
		Where("conversation_id = ?", conversationID).
		Where("direction IN ?", []string{"inbound", "outbound"})
	if lastID != "" {
		q = q.Where("created_at > COALESCE((SELECT created_at FROM messages WHERE id = ?), '1970-01-01')", lastID)
	}
	var rows []model.Message
	err := q.Order("created_at asc").Limit(limit).Find(&rows).Error
	return rows, err
}

// findOrphanAtoms returns active atom IDs whose scene_id is empty. Capped
// to keep the LLM prompt for FitAtomsToScenes bounded; the next pipeline
// tick will pick up any remainder.
func (s *Service) findOrphanAtoms(ctx context.Context, personaID string, limit int) ([]string, error) {
	if personaID == "" || limit <= 0 {
		return nil, nil
	}
	var ids []string
	err := s.db.WithContext(ctx).Model(&model.Memory{}).
		Where("persona_id = ? AND status = ? AND (scene_id = '' OR scene_id IS NULL)", personaID, "active").
		Order("created_at asc").
		Limit(limit).
		Pluck("id", &ids).Error
	return ids, err
}
