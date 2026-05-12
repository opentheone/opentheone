package handler

import (
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// HealthInfo carries the cached build/runtime info for /api/health.
type HealthInfo struct {
	DB        *gorm.DB
	Version   string
	Commit    string
	BuildTime string
	StartedAt time.Time
}

type HealthHandler struct {
	info HealthInfo
}

func NewHealthHandler(info HealthInfo) *HealthHandler {
	return &HealthHandler{info: info}
}

// Handle responds with build metadata, uptime, and a cheap DB liveness probe.
// The response is intentionally cheap (single SELECT 1) so a monitor can hit it
// every few seconds without pressure.
func (h *HealthHandler) Handle(c *gin.Context) {
	dbOK := true
	dbErr := ""
	if h.info.DB != nil {
		sqlDB, err := h.info.DB.DB()
		if err != nil {
			dbOK = false
			dbErr = err.Error()
		} else if err := sqlDB.PingContext(c.Request.Context()); err != nil {
			dbOK = false
			dbErr = err.Error()
		}
	}

	uptime := time.Since(h.info.StartedAt).Truncate(time.Second).String()
	status := "ok"
	if !dbOK {
		status = "degraded"
	}

	ok(c, gin.H{
		"status":     status,
		"version":    h.info.Version,
		"commit":     h.info.Commit,
		"build_time": h.info.BuildTime,
		"uptime":     uptime,
		"started_at": h.info.StartedAt.UTC().Format(time.RFC3339),
		"db_ok":      dbOK,
		"db_error":   dbErr,
	})
}
