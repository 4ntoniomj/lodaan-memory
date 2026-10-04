package database

import (
	"os"
	"testing"
	"time"

	"lodan/internal/config"
)

// newBareCluster returns a Cluster over a temporary data directory that is only good for the
// checks that read files in it (no PostgreSQL binaries are needed).
func newBareCluster(t *testing.T) *Cluster {
	t.Helper()
	return &Cluster{cfg: config.Config{DataDir: t.TempDir()}}
}

// setAge writes path (if it is not there) and sets its modification time to age ago.
func setAge(t *testing.T, path string, age time.Duration) {
	t.Helper()
	if err := os.WriteFile(path, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-age)
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorActive(t *testing.T) {
	cl := newBareCluster(t)
	path := cl.cfg.HeartbeatFile()

	if cl.SupervisorActive() {
		t.Error("sin archivo de latido no hay supervisor activo")
	}
	setAge(t, path, 0)
	if !cl.SupervisorActive() {
		t.Error("un latido recién tocado debería contar como supervisor activo")
	}
	setAge(t, path, supervisorHeartbeatMaxAge-3*time.Second)
	if !cl.SupervisorActive() {
		t.Error("un latido por debajo del máximo debería contar como activo")
	}
	setAge(t, path, supervisorHeartbeatMaxAge+3*time.Second)
	if cl.SupervisorActive() {
		t.Error("un latido más viejo que el máximo no debería contar como activo")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if cl.SupervisorActive() {
		t.Error("borrado el latido no hay supervisor activo")
	}
}

func TestMaintenancePending(t *testing.T) {
	cl := newBareCluster(t)
	path := cl.cfg.MaintenanceFile()

	if cl.MaintenancePending() {
		t.Error("sin marca no hay mantenimiento pendiente")
	}
	cl.ClearMaintenancePending() // does nothing, without error

	if err := cl.MarkMaintenancePending(); err != nil {
		t.Fatalf("MarkMaintenancePending falló: %v", err)
	}
	if !cl.MaintenancePending() {
		t.Error("una marca recién creada debería contar como mantenimiento pendiente")
	}
	if data, err := os.ReadFile(path); err != nil || len(data) == 0 {
		t.Errorf("la marca debería contener la hora de creación (data = %q, err = %v)", data, err)
	}

	setAge(t, path, maintenancePendingMaxAge-10*time.Second)
	if !cl.MaintenancePending() {
		t.Error("una marca por debajo del máximo debería contar")
	}
	// An abandoned mark (its creator died) is ignored.
	setAge(t, path, maintenancePendingMaxAge+10*time.Second)
	if cl.MaintenancePending() {
		t.Error("una marca caducada no debería contar como mantenimiento pendiente")
	}
	// Marking again renews it.
	if err := cl.MarkMaintenancePending(); err != nil {
		t.Fatal(err)
	}
	if !cl.MaintenancePending() {
		t.Error("volver a marcar debería renovar la marca caducada")
	}

	cl.ClearMaintenancePending()
	if cl.MaintenancePending() {
		t.Error("tras borrar la marca no debería haber mantenimiento pendiente")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("la marca debería haberse borrado (err = %v)", err)
	}
}

func TestLockHeld(t *testing.T) {
	cl := newBareCluster(t)
	ctx := backupCtx(t)

	if cl.LockHeld() {
		t.Error("sin archivo de lock no hay lock tomado")
	}
	unlock, err := cl.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cl.LockHeld() {
		t.Error("con el lock tomado, LockHeld debería ser true")
	}
	unlock()
	if cl.LockHeld() {
		t.Error("liberado el lock, LockHeld debería ser false")
	}

	// A lock that nobody refreshes is abandoned once it is older than lockStaleAfter, which is
	// also when acquireLock removes it.
	path := cl.cfg.LockFile()
	setAge(t, path, lockStaleAfter-30*time.Second)
	if !cl.LockHeld() {
		t.Error("un lock más reciente que lockStaleAfter debería contar como tomado")
	}
	setAge(t, path, lockStaleAfter+30*time.Second)
	if cl.LockHeld() {
		t.Error("un lock más viejo que lockStaleAfter está abandonado: LockHeld debería ser false")
	}
}
