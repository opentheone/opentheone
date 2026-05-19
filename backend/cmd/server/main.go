package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/opentheone/opentheone/backend/internal/auth"
	"github.com/opentheone/opentheone/backend/internal/config"
	"github.com/opentheone/opentheone/backend/internal/engine"
	"github.com/opentheone/opentheone/backend/internal/ilink"
	"github.com/opentheone/opentheone/backend/internal/logger"
	"github.com/opentheone/opentheone/backend/internal/mcp"
	"github.com/opentheone/opentheone/backend/internal/memory"
	"github.com/opentheone/opentheone/backend/internal/proactive"
	"github.com/opentheone/opentheone/backend/internal/runner"
	"github.com/opentheone/opentheone/backend/internal/server"
	"github.com/opentheone/opentheone/backend/internal/settings"
	"github.com/opentheone/opentheone/backend/internal/store"
)

// These are populated at build time via -ldflags; see Makefile / Dockerfile.
var (
	Version   = "dev"
	Commit    = "none"
	BuildTime = "unknown"
)

func main() {
	cfgPath := flag.String("config", "", "path to config file (yaml)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("opentheone %s\ncommit:  %s\nbuilt:   %s\n", Version, Commit, BuildTime)
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		panic(err)
	}

	log, err := logger.New(cfg.Logging.Level, cfg.Logging.Format)
	if err != nil {
		panic(err)
	}
	defer log.Sync()

	log.Info("opentheone starting",
		zap.String("version", Version),
		zap.String("commit", Commit),
		zap.String("build_time", BuildTime),
	)

	db, err := store.Open(&cfg.Database)
	if err != nil {
		log.Fatal("open db", zap.Error(err))
	}
	log.Info("db ready", zap.String("driver", cfg.Database.Driver), zap.String("dsn", cfg.Database.DSN))

	secretPath := filepath.Join(cfg.Storage.DataDir, "secret.key")
	jwtSecret, generated, err := auth.ResolveJWTSecret(cfg.Auth.JWTSecret, secretPath)
	if err != nil {
		log.Fatal("resolve jwt secret", zap.Error(err))
	}
	if generated {
		log.Warn("no usable jwt_secret configured; generated a random one",
			zap.String("path", secretPath),
			zap.String("hint", "keep this file safe; deleting it invalidates all existing logins"))
	}
	if auth.IsInsecureSecret(cfg.Auth.JWTSecret) && cfg.Auth.JWTSecret != "" {
		// Do NOT log the configured secret value — even an insecure one can
		// leak into shared log aggregation. Log just the length, which is all
		// the operator needs to diagnose "I set a 4-char password by mistake".
		log.Warn("auth.jwt_secret looks insecure (placeholder or too short); falling back to generated key",
			zap.Int("configured_length", len(cfg.Auth.JWTSecret)))
	}
	cfg.Auth.JWTSecret = jwtSecret

	tm := auth.NewTokenManager(jwtSecret, cfg.Auth.JWTExpireHours)

	settingsSvc := settings.New(db)
	seedDefaults := map[string]string{
		settings.KeyAllowRegister: boolToText(cfg.Auth.AllowRegister),
	}
	if err := settingsSvc.Seed(seedDefaults); err != nil {
		log.Warn("seed settings", zap.Error(err))
	}

	ilinkClient := ilink.NewClient(ilink.ClientOptions{
		BaseURL:         cfg.ILink.BaseURL,
		CDNBaseURL:      cfg.ILink.CDNBaseURL,
		ChannelVersion:  cfg.ILink.ChannelVersion,
		UserAgent:       cfg.ILink.UserAgent,
		SKRouteTag:      cfg.ILink.SKRouteTag,
		LongPollTimeout: time.Duration(cfg.ILink.LongPollTimeout) * time.Millisecond,
		AppID:           cfg.ILink.AppID,
	})

	memSvc := memory.NewService(db)
	mcpMgr := mcp.NewManager(log)
	eng := engine.NewEngine(db, ilinkClient, memSvc, mcpMgr, log, engine.Options{
		Secret:         cfg.Auth.JWTSecret,
		MaxChunk:       1800,
		HistoryN:       16,
		RetrieveK:      5,
		AttachmentsDir: cfg.Storage.AttachmentsDir,
	})

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	mgr := runner.NewManager(rootCtx, db, ilinkClient, eng, log)
	qrlog := runner.NewQRLoginCoordinator(db, ilinkClient, mgr, log)

	if err := mgr.Bootstrap(); err != nil {
		log.Warn("bootstrap runners", zap.Error(err))
	}

	sched := proactive.NewScheduler(db, eng, log)
	if err := sched.Start(); err != nil {
		log.Warn("scheduler start", zap.Error(err))
	}

	srv := server.Build(server.Deps{
		Config:    cfg,
		DB:        db,
		Token:     tm,
		Engine:    eng,
		Memory:    memSvc,
		Manager:   mgr,
		QRLogin:   qrlog,
		Settings:  settingsSvc,
		MCP:       mcpMgr,
		Logger:    log,
		Version:   Version,
		Commit:    Commit,
		BuildTime: BuildTime,
		StartedAt: time.Now(),
	})

	errCh := make(chan error, 1)
	go func() {
		log.Info("http listening", zap.String("addr", cfg.Server.Listen))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-stop:
		log.Info("signal received, shutting down")
	case err := <-errCh:
		log.Error("http server error", zap.Error(err))
	}

	rootCancel()
	mgr.StopAll()
	mcpMgr.Shutdown()
	sched.Stop()
	if err := server.Shutdown(srv, 5*time.Second); err != nil {
		log.Error("graceful shutdown", zap.Error(err))
	}
	log.Info("bye")
}

func boolToText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
