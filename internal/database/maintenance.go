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
	// maintenancePendingMaxAge is how old the maintenance mark can be to count: the longest a
	// backup or restore waits for the supervisor (restartTimeout) plus a margin. An older mark is
	// abandoned (its creator died) and ignored.
	maintenancePendingMaxAge = restartTimeout + 30*time.Second
)

// MarkMaintenancePending creates (or renews) the maintenance mark, config.Config.MaintenanceFile:
// a backup or restore that is going to stop PostgreSQL and hand its restart over to the service
// supervisor leaves it, under the cluster lock and before stopping PostgreSQL. The supervisor may
// notice the stop only after the lock is released (it polls PostgreSQL every few seconds), and the
// mark is what tells it, then, that this is a maintenance pause and not a failure. The file holds
// the creation time; its modification time is what MaintenancePending checks.
func (c *Cluster) MarkMaintenancePending() error {
	if err := os.MkdirAll(c.cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("no se pudo crear el directorio de datos %s: %w", c.cfg.DataDir, err)
	}
	path := c.cfg.MaintenanceFile()
	if err := os.WriteFile(path, []byte(time.Now().Format(time.RFC3339Nano)+"\n"), 0o600); err != nil {
		return fmt.Errorf("no se pudo crear la marca de mantenimiento %s: %w", path, err)
	}
	return nil
}

// ClearMaintenancePending removes the maintenance mark. It does nothing if it is not there.
// The supervisor calls it, under the cluster lock, once PostgreSQL is running again; a backup or
// restore calls it when it does not hand PostgreSQL over or when it had to start it itself.
func (c *Cluster) ClearMaintenancePending() {
	_ = os.Remove(c.cfg.MaintenanceFile())
}

// MaintenancePending reports whether the maintenance mark exists and is younger than
// maintenancePendingMaxAge. Together with LockHeld it tells the supervisor that a PostgreSQL that
// is not running was stopped on purpose.
func (c *Cluster) MaintenancePending() bool {
	info, err := os.Stat(c.cfg.MaintenanceFile())
	return err == nil && time.Since(info.ModTime()) < maintenancePendingMaxAge
}

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
	// Whatever the outcome, nobody is going to hand PostgreSQL over any more.
	defer c.ClearMaintenancePending()
	if err := c.Start(bctx); err != nil {
		return fmt.Errorf("el servicio de lodan debía arrancar PostgreSQL y %s, y tampoco se pudo arrancar a mano: %w", why, err)
	}
	return fmt.Errorf("el servicio de lodan debía arrancar PostgreSQL y %s: se ha arrancado fuera del servicio (reinícialo con `lodan service restart` para que vuelva a gestionarlo)", why)
}
