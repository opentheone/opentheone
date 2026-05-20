package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/auth"
	"github.com/opentheone/opentheone/backend/internal/config"
	"github.com/opentheone/opentheone/backend/internal/engine"
	"github.com/opentheone/opentheone/backend/internal/handler"
	"github.com/opentheone/opentheone/backend/internal/mcp"
	"github.com/opentheone/opentheone/backend/internal/memory"
	"github.com/opentheone/opentheone/backend/internal/middleware"
	"github.com/opentheone/opentheone/backend/internal/runner"
	"github.com/opentheone/opentheone/backend/internal/settings"
	"github.com/opentheone/opentheone/backend/internal/web"
)

// Deps bundles the wired collaborators that Build needs to mount HTTP routes.
type Deps struct {
	Config    *config.Config
	DB        *gorm.DB
	Token     *auth.TokenManager
	Engine    *engine.Engine
	Memory    *memory.Service
	Manager   *runner.Manager
	QRLogin   *runner.QRLoginCoordinator
	Settings  *settings.Service
	MCP       *mcp.Manager
	Logger    *zap.Logger
	Version   string
	Commit    string
	BuildTime string
	StartedAt time.Time
}

func Build(deps Deps) *http.Server {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(requestLogger(deps.Logger))

	loginLimiter := middleware.NewSlidingWindowLimiter(10, time.Minute)
	registerLimiter := middleware.NewSlidingWindowLimiter(5, 10*time.Minute)
	// Janitor goroutines reclaim stale per-IP buckets so the map can't grow
	// forever across distinct IPs. They terminate on process exit; we don't
	// need to plumb the cancel through Shutdown for a single-binary deploy.
	loginLimiter.StartJanitor(10 * time.Minute)
	registerLimiter.StartJanitor(30 * time.Minute)

	api := r.Group("/api")
	{
		ah := handler.NewAuthHandler(deps.DB, deps.Token, deps.Settings)
		api.POST("/auth/register", middleware.LoginRateLimit(registerLimiter), ah.Register)
		api.POST("/auth/login", middleware.LoginRateLimit(loginLimiter), ah.Login)

		healthH := handler.NewHealthHandler(handler.HealthInfo{
			DB:        deps.DB,
			Version:   deps.Version,
			Commit:    deps.Commit,
			BuildTime: deps.BuildTime,
			StartedAt: deps.StartedAt,
		})
		api.GET("/health", healthH.Handle)
		api.POST("/health", healthH.Handle)

		authed := api.Group("/")
		authed.Use(middleware.JWTAuth(deps.Token))
		{
			authed.POST("/auth/me", ah.Me)
			authed.POST("/auth/update_profile", ah.UpdateProfile)
			authed.POST("/auth/update_password", ah.UpdatePassword)

			lh := handler.NewLLMHandler(deps.DB, deps.Config.Auth.JWTSecret)
			authed.POST("/llm/create", lh.Create)
			authed.POST("/llm/list", lh.List)
			authed.POST("/llm/update", lh.Update)
			authed.POST("/llm/delete", lh.Delete)
			authed.POST("/llm/test", lh.Test)
			authed.POST("/llm/providers", lh.Providers)

			ph := handler.NewPersonaHandler(deps.DB, deps.Manager, deps.Engine)
			authed.POST("/persona/create", ph.Create)
			authed.POST("/persona/list", ph.List)
			authed.POST("/persona/templates", ph.Templates)
			authed.POST("/persona/get", ph.Get)
			authed.POST("/persona/update", ph.Update)
			authed.POST("/persona/delete", ph.Delete)
			authed.POST("/persona/activate", ph.Activate)
			authed.POST("/persona/deactivate", ph.Deactivate)
			authed.POST("/persona/trigger_proactive", ph.TriggerProactive)

			bh := handler.NewBindingHandler(deps.DB, deps.Manager, deps.QRLogin)
			authed.POST("/binding/start", bh.Start)
			authed.POST("/binding/status", bh.Status)
			authed.POST("/binding/active", bh.Active)
			authed.POST("/binding/for_persona", bh.ForPersona)
			authed.POST("/binding/revoke", bh.Revoke)
			authed.POST("/binding/restart", bh.Restart)

			ch := handler.NewConversationHandler(deps.DB, deps.Engine)
			authed.POST("/conversation/list", ch.List)
			authed.POST("/conversation/messages", ch.Messages)
			authed.POST("/conversation/send_manual", ch.SendManual)
			authed.POST("/conversation/export", ch.Export)
			authed.POST("/conversation/delete", ch.Delete)
			authed.POST("/conversation/rebuild_summary", ch.RebuildSummary)

			ath := handler.NewAttachmentHandler(deps.DB)
			authed.POST("/attachment/get", ath.Get)

			mh := handler.NewMemoryHandler(deps.DB, deps.Memory)
			authed.POST("/memory/list", mh.List)
			authed.POST("/memory/delete", mh.Delete)
			authed.POST("/memory/upsert_manual", mh.UpsertManual)

			sh := handler.NewSceneHandler(deps.DB, deps.Memory)
			authed.POST("/scene/list", sh.List)
			authed.POST("/scene/get", sh.Get)
			authed.POST("/scene/delete", sh.Delete)

			prh := handler.NewProfileHandler(deps.DB, deps.Memory, deps.Config.Auth.JWTSecret)
			authed.POST("/profile/get", prh.Get)
			authed.POST("/profile/regenerate", prh.Regenerate)

			mcph := handler.NewMCPHandler(deps.DB, deps.MCP)
			authed.POST("/mcp/create", mcph.Create)
			authed.POST("/mcp/list", mcph.List)
			authed.POST("/mcp/update", mcph.Update)
			authed.POST("/mcp/delete", mcph.Delete)
			authed.POST("/mcp/test", mcph.Test)
			authed.POST("/mcp/tools", mcph.Tools)
			authed.POST("/mcp/import", mcph.Import)

			admin := authed.Group("/admin")
			admin.Use(middleware.AdminOnly())
			{
				adminH := handler.NewAdminHandler(deps.DB, deps.Settings, deps.Manager, deps.MCP)
				admin.POST("/users", adminH.ListUsers)
				admin.POST("/users/set_role", adminH.SetRole)
				admin.POST("/users/reset_password", adminH.ResetPassword)
				admin.POST("/users/delete", adminH.DeleteUser)
				admin.POST("/settings", adminH.GetSettings)
				admin.POST("/settings/update", adminH.UpdateSettings)
			}
		}
	}

	r.NoRoute(func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
			web.Handler().ServeHTTP(c.Writer, c.Request)
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "msg": "not found", "data": nil})
	})

	return &http.Server{
		Addr:              deps.Config.Server.Listen,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}
}

// Shutdown gracefully stops the http server.
func Shutdown(srv *http.Server, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := srv.Shutdown(ctx)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func requestLogger(log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Debug("http",
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("latency", time.Since(start)),
		)
	}
}
