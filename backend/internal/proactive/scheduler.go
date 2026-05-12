package proactive

import (
	"context"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/wzyjerry/opentheone/backend/internal/engine"
	"github.com/wzyjerry/opentheone/backend/internal/model"
)

// Scheduler periodically inspects active personas and triggers proactive messages.
type Scheduler struct {
	db     *gorm.DB
	eng    *engine.Engine
	log    *zap.Logger
	cron   *cron.Cron
	stopCh chan struct{}
}

func NewScheduler(db *gorm.DB, eng *engine.Engine, log *zap.Logger) *Scheduler {
	c := cron.New(cron.WithSeconds())
	return &Scheduler{db: db, eng: eng, log: log, cron: c, stopCh: make(chan struct{})}
}

// Start runs a once-per-minute tick that re-evaluates every persona's proactive_cron.
func (s *Scheduler) Start() error {
	_, err := s.cron.AddFunc("0 * * * * *", s.tick)
	if err != nil {
		return err
	}
	s.cron.Start()
	s.log.Info("proactive scheduler started")
	return nil
}

func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
	close(s.stopCh)
}

func (s *Scheduler) tick() {
	var personas []model.Persona
	if err := s.db.Where("is_active = ? AND proactive_cron <> ''", true).Find(&personas).Error; err != nil {
		s.log.Warn("scan personas failed", zap.Error(err))
		return
	}
	now := time.Now()
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	for _, p := range personas {
		expr := strings.TrimSpace(p.ProactiveCron)
		if expr == "" {
			continue
		}
		sched, err := parser.Parse(expr)
		if err != nil {
			s.log.Warn("invalid proactive_cron", zap.String("persona_id", p.ID), zap.Error(err))
			continue
		}
		mostRecent := sched.Next(now.Add(-1 * time.Hour))
		for {
			n := sched.Next(mostRecent)
			if n.After(now.Add(2 * time.Second)) {
				break
			}
			mostRecent = n
		}
		if now.Sub(mostRecent) > 90*time.Second || now.Sub(mostRecent) < -2*time.Second {
			continue
		}
		s.firePersona(p)
	}
}

func (s *Scheduler) firePersona(p model.Persona) {
	var binding model.WeChatBinding
	err := s.db.Where("persona_id = ? AND state = ?", p.ID, "active").First(&binding).Error
	if err != nil {
		return
	}
	if binding.LastContextToken == "" {
		s.log.Info("persona has no context_token cached, skipping proactive",
			zap.String("persona_id", p.ID))
		return
	}
	if !binding.LastProactiveAt.IsZero() && time.Since(binding.LastProactiveAt) < 6*time.Hour {
		return
	}

	llmCfgID := p.LLMConfigID
	var llmCfg model.LLMConfig
	if llmCfgID != "" {
		if err := s.db.Where("id = ?", llmCfgID).First(&llmCfg).Error; err != nil {
			s.log.Warn("llm_config missing", zap.Error(err))
			return
		}
	} else {
		if err := s.db.Where("user_id = ? AND is_default = ?", p.UserID, true).First(&llmCfg).Error; err != nil {
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := s.eng.SendProactive(ctx, &binding, &p, &llmCfg, ""); err != nil {
		s.log.Warn("proactive send failed",
			zap.String("persona_id", p.ID),
			zap.Error(err))
	}
}
