package logger

import (
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New creates a zap logger from level (debug/info/warn/error) and format (console/json).
func New(level, format string) (*zap.Logger, error) {
	cfg := zap.NewProductionConfig()
	if strings.EqualFold(format, "console") {
		cfg = zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	}
	// Stack traces only for ERROR+. The zap DevelopmentConfig default of WarnLevel
	// floods stdout with unactionable noise on perfectly fine WARN events
	// (e.g. "auto-generated JWT secret", "no llm config configured").
	cfg.DisableStacktrace = true
	switch strings.ToLower(level) {
	case "debug":
		cfg.Level = zap.NewAtomicLevelAt(zapcore.DebugLevel)
	case "warn", "warning":
		cfg.Level = zap.NewAtomicLevelAt(zapcore.WarnLevel)
	case "error":
		cfg.Level = zap.NewAtomicLevelAt(zapcore.ErrorLevel)
	default:
		cfg.Level = zap.NewAtomicLevelAt(zapcore.InfoLevel)
	}
	return cfg.Build()
}
