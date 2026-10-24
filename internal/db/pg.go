package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Pool = pgxpool.Pool

func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid postgres DSN: %w", err)
	}

	// A household needs a handful of connections, and a serverless host such as
	// Neon (free tier: compute sleeps after 5 idle minutes and bills by the
	// hour awake) is best served by a pool that holds none while idle:
	// connections closed after 2 minutes are never found dead after the compute
	// slept, and nothing redials in the background to wake it. (An idle
	// connection handed out after more than a second is pinged first, so a
	// stale one is replaced rather than failing a request.)
	cfg.MaxConns = 5
	cfg.MinConns = 0
	cfg.HealthCheckPeriod = time.Minute
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 2 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create postgres pool: %w", err)
	}

	// A serverless database that has gone to sleep (Neon free: after 5 idle
	// minutes) takes a moment to wake on the first connection; 3 seconds was
	// too tight and would have stopped the server from starting.
	ctxPing, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := pool.Ping(ctxPing); err != nil {
		pool.Close()
		return nil, fmt.Errorf("cannot connect to postgres: %w", err)
	}

	return pool, nil
}
