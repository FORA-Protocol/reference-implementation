// Package db provides shared database wiring: pgxpool construction with a
// slog-backed query tracer, context-aware shutdown, and a generic migration
// runner. Service-specific embedded migrations live under each service's
// internal/db package.
package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/tracelog"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/logging"
)

// Config captures pool-level tunables. Zero values fall back to sensible defaults.
type Config struct {
	DSN             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	HealthCheckFreq time.Duration
}

// Open constructs a pgxpool.Pool wired to a slog-based tracer.
// Caller owns the lifecycle; call pool.Close() on shutdown.
func Open(ctx context.Context, cfg Config, logger *slog.Logger) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	if cfg.MaxConns > 0 {
		pc.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		pc.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		pc.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.MaxConnIdleTime > 0 {
		pc.MaxConnIdleTime = cfg.MaxConnIdleTime
	}
	if cfg.HealthCheckFreq > 0 {
		pc.HealthCheckPeriod = cfg.HealthCheckFreq
	}
	pc.ConnConfig.Tracer = &tracelog.TraceLog{
		Logger:   slogAdapter{logger: logger},
		LogLevel: tracelog.LogLevelWarn,
	}

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

// slogAdapter bridges pgx's tracelog interface to the stdlib slog logger.
type slogAdapter struct {
	logger *slog.Logger
}

func (s slogAdapter) Log(ctx context.Context, level tracelog.LogLevel, msg string, data map[string]any) {
	attrs := make([]any, 0, 2*len(data))
	for k, v := range data {
		attrs = append(attrs, k, v)
	}
	// A statement that failed because its own context ended is its caller
	// going away, not a database fault, so logging.LevelFor records it at
	// DEBUG. An error the database itself returns keeps pgx's level: ERROR,
	// which is what database alerting counts. pgx passes the failure under
	// "err".
	err, _ := data["err"].(error)
	s.logger.Log(ctx, logging.LevelFor(ctx, err, slogLevel(level)), msg, attrs...)
}

// slogLevel maps a pgx trace level onto slog's.
func slogLevel(level tracelog.LogLevel) slog.Level {
	switch level {
	case tracelog.LogLevelTrace, tracelog.LogLevelDebug:
		return slog.LevelDebug
	case tracelog.LogLevelWarn:
		return slog.LevelWarn
	case tracelog.LogLevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Ping is a thin convenience for health endpoints.
func Ping(ctx context.Context, pool *pgxpool.Pool) error {
	return pool.Ping(ctx)
}

// WithTx runs fn inside a transaction. Commits on nil error, rolls back otherwise.
// Thin wrapper around pgx.BeginFunc to keep call sites terse.
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, pool, fn)
}

// TxRunner is the narrow port services depend on to open a transaction.
// Decouples service code from *pgxpool.Pool so the pool stays in wiring
// (cmd/server) and tests can substitute alternatives without booting a real
// pool. Gives explicit transactional boundaries and interface
// segregation at the port.
type TxRunner interface {
	WithTx(ctx context.Context, fn func(pgx.Tx) error) error
}

// PoolRunner adapts *pgxpool.Pool to the TxRunner interface. Used at wiring
// time so the service-side dependency reduces to the narrow TxRunner port.
type PoolRunner struct{ Pool *pgxpool.Pool }

// WithTx implements TxRunner by delegating to pgx.BeginFunc on the wrapped pool.
func (p PoolRunner) WithTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, p.Pool, fn)
}
