package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// maxConns is the pool size. The cluster allows maxConnections connections and several lodan
// processes (one per MCP client) share it.
const maxConns = 4

// An idle process (an MCP client that is not using memory right now) gives its connections
// back after idleTimeout, checked every healthCheckPeriod, so idle clients do not take slots
// from the active ones.
const (
	idleTimeout       = 30 * time.Second
	healthCheckPeriod = 15 * time.Second
)

// Open creates a pgx pool for dsn and checks the connection with a ping.
//
// pgvector.HalfVector needs no type registration: it implements driver.Valuer and sql.Scanner,
// and pgx falls back to the text format for OIDs it does not know (halfvec has a dynamic OID).
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("DSN de PostgreSQL inválido: %w", err)
	}
	cfg.MaxConns = maxConns
	cfg.MinConns = 0
	cfg.MaxConnIdleTime = idleTimeout
	cfg.HealthCheckPeriod = healthCheckPeriod

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("no se pudo crear el pool de conexiones: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("no se pudo conectar con PostgreSQL: %w", err)
	}
	return pool, nil
}
