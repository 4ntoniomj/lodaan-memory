package database

import (
	"context"
	"fmt"
	"os"
	"time"
)

const (
	// SupervisorHeartbeatInterval is how often the service supervisor touches the heartbeat file
	// (config.Config.HeartbeatFile) while it is alive.
	SupervisorHeartbeatInterval = 5 * time.Second
	// supervisorHeartbeatMaxAge is how old the heartbeat file can be for the supervisor to be
	// considered active: three missed beats.
	supervisorHeartbeatMaxAge = 15 * time.Second
	// supervisorStartPoll is how often a backup or restore checks whether the supervisor has
	// started PostgreSQL again.
	supervisorStartPoll = 500 * time.Millisecond
)

// SupervisorActive reports whether the service supervisor (`lodan service run`) is alive: its
// heartbeat file exists and was touched less than supervisorHeartbeatMaxAge ago. A backup or a
// restore uses it to hand PostgreSQL back to the supervisor instead of starting it by itself.
func (c *Cluster) SupervisorActive() bool {
	info, err := os.Stat(c.cfg.HeartbeatFile())
	return err == nil && time.Since(info.ModTime()) < supervisorHeartbeatMaxAge
}

// LockHeld reports whether the cluster lock (see Lock) is taken by somebody: the file exists and
// is not stale, with the same criterion as acquireLock (older than lockStaleAfter is abandoned).
// A backup or restore holds it for as long as it keeps PostgreSQL stopped, so the supervisor
// takes a PostgreSQL that has stopped while the lock is held as a maintenance pause.
func (c *Cluster) LockHeld() bool {
	info, err := os.Stat(c.cfg.LockFile())
	return err == nil && time.Since(info.ModTime()) <= lockStaleAfter
}

// awaitSupervisorStart is called, without the cluster lock, after a backup or restore left
// PostgreSQL stopped for the supervisor to start it. It polls Status until PostgreSQL runs again.
// If that does not happen within restartTimeout (or ctx is cancelled), it starts PostgreSQL itself
// (plan B, under the cluster lock so it does not clash with the supervisor preparing the cluster)
// and returns an error that says so.
func (c *Cluster) awaitSupervisorStart(ctx context.Context) error {
	deadline := time.Now().Add(restartTimeout)
	for time.Now().Before(deadline) {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		running, err := c.Status(sctx)
		cancel()
		if err == nil && running {
			return nil
		}
		select {
		case <-ctx.Done():
			return c.startAfterSupervisorFailed(ctx, "se canceló la espera antes de que lo arrancara")
		case <-time.After(supervisorStartPoll):
		}
	}
	return c.startAfterSupervisorFailed(ctx, fmt.Sprintf("no lo arrancó en %s", restartTimeout))
}

// startAfterSupervisorFailed is plan B of awaitSupervisorStart: it starts PostgreSQL even if ctx
// has been cancelled and returns the error that reports why that was needed.
func (c *Cluster) startAfterSupervisorFailed(ctx context.Context, why string) error {
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restartTimeout)
	defer cancel()
	// Without the lock the start is still attempted: the lock is only there to avoid clashing
	// with the supervisor, and a stuck one must not leave PostgreSQL stopped.
	if unlock, err := c.Lock(bctx); err == nil {
		defer unlock()
	}
	if err := c.Start(bctx); err != nil {
		return fmt.Errorf("el servicio de lodan debía arrancar PostgreSQL y %s, y tampoco se pudo arrancar a mano: %w", why, err)
	}
	return fmt.Errorf("el servicio de lodan debía arrancar PostgreSQL y %s: se ha arrancado fuera del servicio (reinícialo con `lodan service restart` para que vuelva a gestionarlo)", why)
}
