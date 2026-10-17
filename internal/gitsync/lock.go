package gitsync

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LockKey is the PostgreSQL advisory lock that Push and Pull hold for their whole run, so two
// syncs of the same database (the scheduled task and the session-start pulls of several AI
// clients) never work on the sync working copy at the same time.
const LockKey int64 = 0x6c6f64616e73796e // "lodansyn"

// withSyncLock runs fn holding LockKey on a dedicated connection. It waits for the lock and
// gives up with the context error when ctx ends while waiting.
func withSyncLock(ctx context.Context, pool *pgxpool.Pool, fn func() error) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("no se pudo obtener una conexión para la sincronización: %w", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, LockKey); err != nil {
		// A cancelled wait may leave the connection in an unknown state: close it.
		_ = conn.Conn().Close(context.Background())
		conn.Release()
		if ctx.Err() != nil {
			return fmt.Errorf("otra sincronización está en marcha y no terminó a tiempo: %w", ctx.Err())
		}
		return fmt.Errorf("no se pudo tomar el bloqueo de sincronización: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, LockKey); err != nil {
			// Closing the session releases its locks.
			_ = conn.Conn().Close(unlockCtx)
		}
		conn.Release()
	}()
	return fn()
}
