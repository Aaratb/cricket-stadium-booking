// Package store implements the ADR-001 CAS patterns as raw SQL over pgxpool.
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"stadiumbooking/internal/config"
)

// NewPool opens a bounded connection pool. MaxConns is a hard guardrail
// (spec.md: "no unbounded connection growth") — never raise this without
// also reasoning about Postgres's own max_connections ceiling.
func NewPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("store: parse pool config: %w", err)
	}
	poolCfg.MaxConns = cfg.PoolMaxConns

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("store: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return pool, nil
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}
