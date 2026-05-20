package store

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/opentheone/opentheone/backend/internal/config"
	"github.com/opentheone/opentheone/backend/internal/memory"
	"github.com/opentheone/opentheone/backend/internal/model"
)

// sqliteDSN attaches the recommended pragmas (WAL journal, busy timeout, FKs)
// to a user-supplied DSN without breaking it. We use `?` as separator only if
// the DSN doesn't already carry a query string — otherwise we append with `&`.
// Also injects each pragma only when the user hasn't already specified it, so
// an operator can override anything via config.
func sqliteDSN(raw string) string {
	wanted := map[string]string{
		"_journal_mode": "WAL",
		"_busy_timeout": "5000",
		"_foreign_keys": "on",
	}
	sep := "?"
	if strings.Contains(raw, "?") {
		sep = "&"
	}
	var b strings.Builder
	b.WriteString(raw)
	first := true
	for k, v := range wanted {
		// Quick check: if the key is already mentioned anywhere after the `?`,
		// skip it. Good enough for the small static set above.
		if strings.Contains(raw, k+"=") {
			continue
		}
		if first {
			b.WriteString(sep)
			first = false
		} else {
			b.WriteString("&")
		}
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(v)
	}
	return b.String()
}

// Open establishes the database connection and runs AutoMigrate.
func Open(cfg *config.DatabaseConfig) (*gorm.DB, error) {
	var dialector gorm.Dialector
	switch cfg.Driver {
	case "sqlite", "":
		dialector = sqlite.Open(sqliteDSN(cfg.DSN))
	default:
		return nil, fmt.Errorf("unsupported database driver: %s", cfg.Driver)
	}

	// We rely on `errors.Is(err, gorm.ErrRecordNotFound)` as a control-flow
	// signal in plenty of places (first user / first setting / first binding).
	// Without IgnoreRecordNotFoundError, every such First() floods stdout with
	// "record not found" WARN lines that are not actually warnings.
	gormLog := gormlogger.New(
		log.New(os.Stderr, "\r\n", log.LstdFlags),
		gormlogger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	)
	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: gormLog,
	})
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		return nil, fmt.Errorf("auto migrate: %w", err)
	}
	// FTS5 virtual tables can't be expressed in GORM models — bring them up
	// (idempotent CREATE IF NOT EXISTS) right after the base schema so the
	// memory service can immediately start indexing.
	if err := memory.EnsureSchema(db); err != nil {
		return nil, fmt.Errorf("fts schema: %w", err)
	}
	// Backfill the FTS index for any rows that pre-date this package (e.g.
	// the legacy embedding-based memory system). Bounded so a huge legacy
	// install doesn't stall boot — the remainder gets indexed organically
	// as rows are read/updated through the live memory pipeline.
	if err := memory.BackfillIndex(context.Background(), db); err != nil {
		return nil, fmt.Errorf("fts backfill: %w", err)
	}
	return db, nil
}
