package service

import (
	"bytes"
	"context"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"lodan/internal/config"
	"lodan/internal/database"
)

// syncBuffer is a bytes.Buffer safe for the logger of RunSupervisor (another goroutine) and the test.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// supervisorEnv is a throwaway cluster configuration plus the helpers to drive RunSupervisor.
type supervisorEnv struct {
	cfg     config.Config
	cluster *database.Cluster
	logs    *syncBuffer
}

// newSupervisorEnv builds a configuration over a temporary data directory and a free TCP port.
// It skips the test if the PostgreSQL binaries cannot be found. The cluster is stopped when the
// test ends, whatever happens.
func newSupervisorEnv(t *testing.T) *supervisorEnv {
	t.Helper()

	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.EmbedDims = 4
	cfg.EmbedModel = "test-model"
	// Nothing listens here: the warm-up and the maintenance fail fast and are not fatal.
	cfg.OllamaURL = "http://127.0.0.1:1"
	cfg.PGBinDir = os.Getenv("LODAN_PG_BIN_DIR")

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("no se pudo reservar un puerto libre: %v", err)
	}
	cfg.PGPort = l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("no se pudo liberar el puerto reservado: %v", err)
	}

	cluster, err := database.NewCluster(cfg)
	if err != nil {
		t.Skipf("se omite el test: no hay binarios de PostgreSQL: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := cluster.Stop(ctx); err != nil {
			t.Logf("no se pudo parar el clúster de prueba: %v", err)
		}
	})
	return &supervisorEnv{cfg: cfg, cluster: cluster, logs: &syncBuffer{}}
}

// start runs RunSupervisor in a goroutine and returns its cancel function and the channel that
// receives its result.
func (e *supervisorEnv) start(t *testing.T) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errc := make(chan error, 1)
	go func() { errc <- RunSupervisor(ctx, e.cfg, log.New(e.logs, "", 0)) }()
	return cancel, errc
}

// awaitMigrated waits until the lodan database answers with the migrations applied. It skips
// the test if the supervisor fails because pgvector is not installed.
func (e *supervisorEnv) awaitMigrated(t *testing.T, errc <-chan error) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-errc:
			if err != nil && strings.Contains(err.Error(), "vector") {
				t.Skipf("se omite el test: pgvector no está disponible: %v", err)
			}
			t.Fatalf("RunSupervisor terminó antes de estar listo: %v\nlog:\n%s", err, e.logs.String())
		default:
		}
		if e.migrated() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("la base de datos no respondió a tiempo\nlog:\n%s", e.logs.String())
}

// migrated reports whether lodan_settings exists and holds the configured dimensions.
func (e *supervisorEnv) migrated() bool {
	dsn, err := e.cluster.DSN("lodan")
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, dsn)
	if err != nil {
		return false
	}
	defer pool.Close()
	var dims string
	if err := pool.QueryRow(ctx, "SELECT value FROM lodan_settings WHERE key = 'embed_dims'").Scan(&dims); err != nil {
		return false
	}
	return dims == "4"
}

// running reports whether pg_ctl sees the cluster as running.
func (e *supervisorEnv) running(t *testing.T) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ok, err := e.cluster.Status(ctx)
	if err != nil {
		t.Fatalf("Status falló: %v", err)
	}
	return ok
}

// postmasterPID returns the first line of postmaster.pid (the PID of the postmaster).
func (e *supervisorEnv) postmasterPID(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.cfg.PGDataDir(), "postmaster.pid"))
	if err != nil {
		t.Fatalf("no se pudo leer postmaster.pid: %v", err)
	}
	pid, _, _ := strings.Cut(string(data), "\n")
	return strings.TrimSpace(pid)
}

// awaitExit waits for the result of RunSupervisor.
func awaitExit(t *testing.T, errc <-chan error, within time.Duration) error {
	t.Helper()
	select {
	case err := <-errc:
		return err
	case <-time.After(within):
		t.Fatalf("RunSupervisor no terminó en %s", within)
		return nil
	}
}

func TestRunSupervisorArrancaMigraYResponde(t *testing.T) {
	env := newSupervisorEnv(t)
	cancel, errc := env.start(t)
	env.awaitMigrated(t, errc)

	if !env.running(t) {
		t.Fatal("PostgreSQL debería estar en marcha")
	}
	if !strings.Contains(env.logs.String(), "en primer plano") {
		t.Errorf("el log no indica el arranque en primer plano:\n%s", env.logs.String())
	}
	// postgres.log lo escribe el proceso hijo, no pg_ctl.
	if _, err := os.Stat(filepath.Join(env.cfg.LogsDir(), "postgres.log")); err != nil {
		t.Errorf("falta postgres.log: %v", err)
	}

	cancel()
	if err := awaitExit(t, errc, 60*time.Second); err != nil {
		t.Fatalf("RunSupervisor devolvió error al cancelarse: %v", err)
	}
}

func TestRunSupervisorParaPostgresAlCancelar(t *testing.T) {
	env := newSupervisorEnv(t)
	cancel, errc := env.start(t)
	env.awaitMigrated(t, errc)
	if !env.running(t) {
		t.Fatal("PostgreSQL debería estar en marcha antes de cancelar")
	}

	start := time.Now()
	cancel()
	if err := awaitExit(t, errc, 60*time.Second); err != nil {
		t.Fatalf("RunSupervisor devolvió error al cancelarse: %v", err)
	}
	if d := time.Since(start); d > pgStopTimeout+5*time.Second {
		t.Errorf("la parada tardó %s, por encima del máximo de %s", d, pgStopTimeout)
	}
	if env.running(t) {
		t.Error("PostgreSQL sigue en marcha tras cancelar el contexto")
	}
	if _, err := os.Stat(filepath.Join(env.cfg.PGDataDir(), "postmaster.pid")); err == nil {
		t.Error("postmaster.pid sigue existiendo: PostgreSQL no paró limpiamente")
	}
}

func TestRunSupervisorNoLanzaOtroPostgres(t *testing.T) {
	env := newSupervisorEnv(t)

	// PostgreSQL arrancado por otro medio (lodan db start o el arranque perezoso de serve).
	ctx, cancelSetup := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelSetup()
	if err := env.cluster.EnsureRunning(ctx); err != nil {
		t.Fatalf("EnsureRunning falló: %v", err)
	}
	pidBefore := env.postmasterPID(t)

	cancel, errc := env.start(t)
	env.awaitMigrated(t, errc)

	if !strings.Contains(env.logs.String(), "ya estaba en marcha") {
		t.Errorf("el log no dice que PostgreSQL ya estaba en marcha:\n%s", env.logs.String())
	}
	if strings.Contains(env.logs.String(), "en primer plano") {
		t.Errorf("el supervisor lanzó otro PostgreSQL:\n%s", env.logs.String())
	}
	if got := env.postmasterPID(t); got != pidBefore {
		t.Errorf("el postmaster cambió de PID (%s -> %s): se lanzó otro", pidBefore, got)
	}

	cancel()
	if err := awaitExit(t, errc, 30*time.Second); err != nil {
		t.Fatalf("RunSupervisor devolvió error al cancelarse: %v", err)
	}
	// No lo lanzó él, así que no debe pararlo.
	if !env.running(t) {
		t.Error("el supervisor paró un PostgreSQL que no había lanzado")
	}
	if got := env.postmasterPID(t); got != pidBefore {
		t.Errorf("el postmaster cambió de PID tras cancelar (%s -> %s)", pidBefore, got)
	}
}

// usePgCtlMode forces the launch mode that Windows uses (pg_ctl start) for one test.
func usePgCtlMode(t *testing.T) {
	t.Helper()
	oldUse, oldPoll := usePgCtl, pgCtlPollInterval
	usePgCtl = true
	pgCtlPollInterval = 200 * time.Millisecond
	t.Cleanup(func() { usePgCtl, pgCtlPollInterval = oldUse, oldPoll })
}

func TestRunSupervisorConPgCtlArrancaYParaAlCancelar(t *testing.T) {
	usePgCtlMode(t)
	env := newSupervisorEnv(t)
	cancel, errc := env.start(t)
	env.awaitMigrated(t, errc)

	if !env.running(t) {
		t.Fatal("PostgreSQL debería estar en marcha")
	}
	if !strings.Contains(env.logs.String(), "con pg_ctl") {
		t.Errorf("el log no indica el arranque con pg_ctl:\n%s", env.logs.String())
	}

	start := time.Now()
	cancel()
	if err := awaitExit(t, errc, 60*time.Second); err != nil {
		t.Fatalf("RunSupervisor devolvió error al cancelarse: %v", err)
	}
	if d := time.Since(start); d > pgStopTimeout+5*time.Second {
		t.Errorf("la parada tardó %s, por encima del máximo de %s", d, pgStopTimeout)
	}
	if env.running(t) {
		t.Error("PostgreSQL sigue en marcha tras cancelar el contexto")
	}
}

func TestRunSupervisorConPgCtlFallaSiPostgresSeCae(t *testing.T) {
	usePgCtlMode(t)
	env := newSupervisorEnv(t)
	_, errc := env.start(t)
	env.awaitMigrated(t, errc)

	ctx, cancelStop := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelStop()
	if err := env.cluster.Stop(ctx); err != nil {
		t.Fatalf("no se pudo parar PostgreSQL: %v", err)
	}
	err := awaitExit(t, errc, 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "terminó por su cuenta") {
		t.Fatalf("se esperaba un error porque PostgreSQL se cayó, y es: %v", err)
	}
}

// fastMaintenancePoll makes a maintenance pause notice the release of the lock quickly.
func fastMaintenancePoll(t *testing.T) {
	t.Helper()
	old := maintenancePollInterval
	maintenancePollInterval = 100 * time.Millisecond
	t.Cleanup(func() { maintenancePollInterval = old })
}

// assertAlive fails the test if RunSupervisor has already returned.
func assertAlive(t *testing.T, env *supervisorEnv, errc <-chan error) {
	t.Helper()
	select {
	case err := <-errc:
		t.Fatalf("RunSupervisor terminó y no debía: %v\nlog:\n%s", err, env.logs.String())
	default:
	}
}

// pauseAndResume does what a backup or restore does to PostgreSQL: it takes the cluster lock and
// stops PostgreSQL under it. The supervisor must stay alive and leave PostgreSQL stopped while
// the lock is held; once it is released, PostgreSQL must be back and the supervisor still alive.
// launchLog is the log line of a launch by the supervisor, which must have happened wantLaunches
// times when it is over.
func (e *supervisorEnv) pauseAndResume(t *testing.T, errc <-chan error, launchLog string, wantLaunches int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	unlock, err := e.cluster.Lock(ctx)
	if err != nil {
		t.Fatalf("no se pudo tomar el lock del clúster: %v", err)
	}
	defer unlock() // safe to call twice
	if err := e.cluster.Stop(ctx); err != nil {
		t.Fatalf("no se pudo parar PostgreSQL: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(e.logs.String(), "parado por una operación de mantenimiento") {
		if time.Now().After(deadline) {
			t.Fatalf("el supervisor no registró la pausa de mantenimiento\nlog:\n%s", e.logs.String())
		}
		select {
		case err := <-errc:
			t.Fatalf("RunSupervisor terminó en vez de esperar al mantenimiento: %v\nlog:\n%s", err, e.logs.String())
		case <-time.After(100 * time.Millisecond):
		}
	}

	// With the lock held the supervisor neither fails nor starts PostgreSQL.
	select {
	case err := <-errc:
		t.Fatalf("RunSupervisor terminó durante el mantenimiento: %v\nlog:\n%s", err, e.logs.String())
	case <-time.After(2 * time.Second):
	}
	if e.running(t) {
		t.Fatal("el supervisor arrancó PostgreSQL con el lock tomado")
	}

	unlock()
	e.awaitMigrated(t, errc)
	assertAlive(t, e, errc)
	if !e.running(t) {
		t.Fatal("PostgreSQL debería estar en marcha tras soltar el lock")
	}
	if got := strings.Count(e.logs.String(), launchLog); got != wantLaunches {
		t.Errorf("el log tiene %d veces %q y se esperaban %d:\n%s", got, launchLog, wantLaunches, e.logs.String())
	}
}

func TestRunSupervisorEsperaAlMantenimiento(t *testing.T) {
	fastMaintenancePoll(t)
	env := newSupervisorEnv(t)
	cancel, errc := env.start(t)
	env.awaitMigrated(t, errc)

	env.pauseAndResume(t, errc, "arrancado en primer plano", 2)

	cancel()
	if err := awaitExit(t, errc, 60*time.Second); err != nil {
		t.Fatalf("RunSupervisor devolvió error al cancelarse: %v", err)
	}
	// Lo lanzó él otra vez, así que lo para al salir.
	if env.running(t) {
		t.Error("PostgreSQL sigue en marcha tras cancelar: no era hijo del supervisor")
	}
}

func TestRunSupervisorConPgCtlEsperaAlMantenimiento(t *testing.T) {
	usePgCtlMode(t)
	fastMaintenancePoll(t)
	env := newSupervisorEnv(t)
	cancel, errc := env.start(t)
	env.awaitMigrated(t, errc)

	env.pauseAndResume(t, errc, "arrancado con pg_ctl", 2)

	cancel()
	if err := awaitExit(t, errc, 60*time.Second); err != nil {
		t.Fatalf("RunSupervisor devolvió error al cancelarse: %v", err)
	}
}

func TestRunSupervisorConPostgresExternoEsperaAlMantenimiento(t *testing.T) {
	old := pgWatchInterval
	pgWatchInterval = 200 * time.Millisecond
	t.Cleanup(func() { pgWatchInterval = old })
	fastMaintenancePoll(t)

	env := newSupervisorEnv(t)
	ctx, cancelSetup := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelSetup()
	if err := env.cluster.EnsureRunning(ctx); err != nil {
		t.Fatalf("EnsureRunning falló: %v", err)
	}
	cancel, errc := env.start(t)
	env.awaitMigrated(t, errc)

	// It adopted the external PostgreSQL; after the pause it launches its own.
	env.pauseAndResume(t, errc, "arrancado en primer plano", 1)

	cancel()
	if err := awaitExit(t, errc, 60*time.Second); err != nil {
		t.Fatalf("RunSupervisor devolvió error al cancelarse: %v", err)
	}
}

// awaitMarkGone waits for the supervisor to remove the maintenance mark.
func (e *supervisorEnv) awaitMarkGone(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(e.cfg.MaintenanceFile()); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("el supervisor no borró la marca de mantenimiento\nlog:\n%s", e.logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// handOverStop does what a backup does when it hands PostgreSQL over to the supervisor: it leaves
// the maintenance mark, takes the lock, stops PostgreSQL and releases the lock right away,
// before a supervisor that polls slowly can notice anything.
func (e *supervisorEnv) handOverStop(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := e.cluster.MarkMaintenancePending(); err != nil {
		t.Fatalf("no se pudo crear la marca de mantenimiento: %v", err)
	}
	unlock, err := e.cluster.Lock(ctx)
	if err != nil {
		t.Fatalf("no se pudo tomar el lock del clúster: %v", err)
	}
	defer unlock()
	if err := e.cluster.Stop(ctx); err != nil {
		t.Fatalf("no se pudo parar PostgreSQL: %v", err)
	}
	unlock()
}

// assertResumedAfterHandOver checks the outcome of handOverStop with a slow poll: the supervisor
// did not fail, PostgreSQL is running again, the mark is gone and the supervisor launched
// PostgreSQL wantLaunches times (launchLog is its log line).
func (e *supervisorEnv) assertResumedAfterHandOver(t *testing.T, errc <-chan error, launchLog string, wantLaunches int) {
	t.Helper()
	e.awaitMigrated(t, errc) // fails if RunSupervisor returned
	assertAlive(t, e, errc)
	if !e.running(t) {
		t.Fatal("PostgreSQL debería estar en marcha tras el mantenimiento")
	}
	if !strings.Contains(e.logs.String(), "parado por una operación de mantenimiento") {
		t.Errorf("el log no registra la pausa de mantenimiento:\n%s", e.logs.String())
	}
	e.awaitMarkGone(t)
	if got := strings.Count(e.logs.String(), launchLog); got != wantLaunches {
		t.Errorf("el log tiene %d veces %q y se esperaban %d:\n%s", got, launchLog, wantLaunches, e.logs.String())
	}
}

// The real case on Windows: the lock is released long before the next poll of the supervisor,
// and only the maintenance mark tells it that PostgreSQL was stopped on purpose.
func TestRunSupervisorConPostgresExternoYSondeoLentoUsaLaMarca(t *testing.T) {
	old := pgWatchInterval
	pgWatchInterval = 3 * time.Second
	t.Cleanup(func() { pgWatchInterval = old })
	fastMaintenancePoll(t)

	env := newSupervisorEnv(t)
	ctx, cancelSetup := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelSetup()
	if err := env.cluster.EnsureRunning(ctx); err != nil {
		t.Fatalf("EnsureRunning falló: %v", err)
	}
	cancel, errc := env.start(t)
	env.awaitMigrated(t, errc)

	env.handOverStop(t)
	// It adopted the external PostgreSQL; after the pause it launches its own.
	env.assertResumedAfterHandOver(t, errc, "arrancado en primer plano", 1)

	cancel()
	if err := awaitExit(t, errc, 60*time.Second); err != nil {
		t.Fatalf("RunSupervisor devolvió error al cancelarse: %v", err)
	}
}

func TestRunSupervisorConPgCtlYSondeoLentoUsaLaMarca(t *testing.T) {
	usePgCtlMode(t)
	pgCtlPollInterval = 3 * time.Second // restored by the cleanup of usePgCtlMode
	fastMaintenancePoll(t)
	env := newSupervisorEnv(t)
	cancel, errc := env.start(t)
	env.awaitMigrated(t, errc)

	env.handOverStop(t)
	env.assertResumedAfterHandOver(t, errc, "arrancado con pg_ctl", 2)

	cancel()
	if err := awaitExit(t, errc, 60*time.Second); err != nil {
		t.Fatalf("RunSupervisor devolvió error al cancelarse: %v", err)
	}
}

// A mark that nobody renewed (its creator died) must not hide a real failure.
func TestRunSupervisorIgnoraUnaMarcaCaducadaSinLock(t *testing.T) {
	old := pgWatchInterval
	pgWatchInterval = 200 * time.Millisecond
	t.Cleanup(func() { pgWatchInterval = old })

	env := newSupervisorEnv(t)
	ctx, cancelSetup := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelSetup()
	if err := env.cluster.EnsureRunning(ctx); err != nil {
		t.Fatalf("EnsureRunning falló: %v", err)
	}
	_, errc := env.start(t)
	env.awaitMigrated(t, errc)

	if err := env.cluster.MarkMaintenancePending(); err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(env.cfg.MaintenanceFile(), expired, expired); err != nil {
		t.Fatal(err)
	}
	if env.cluster.MaintenancePending() {
		t.Fatal("precondición: la marca debería estar caducada")
	}
	if err := env.cluster.Stop(ctx); err != nil {
		t.Fatalf("no se pudo parar el PostgreSQL externo: %v", err)
	}

	err := awaitExit(t, errc, 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "se ha parado") {
		t.Fatalf("con la marca caducada y sin lock se esperaba el fallo de siempre, y es: %v", err)
	}
	if strings.Contains(env.logs.String(), "parado por una operación de mantenimiento") {
		t.Errorf("el supervisor trató como mantenimiento una marca caducada:\n%s", env.logs.String())
	}
}

func TestRunSupervisorLatido(t *testing.T) {
	env := newSupervisorEnv(t)
	if env.cluster.SupervisorActive() {
		t.Fatal("antes de arrancar no debería haber supervisor activo")
	}
	cancel, errc := env.start(t)
	env.awaitMigrated(t, errc)

	if !env.cluster.SupervisorActive() {
		t.Error("con el supervisor en marcha, SupervisorActive debería ser true")
	}
	cancel()
	if err := awaitExit(t, errc, 60*time.Second); err != nil {
		t.Fatalf("RunSupervisor devolvió error al cancelarse: %v", err)
	}
	if _, err := os.Stat(env.cfg.HeartbeatFile()); !os.IsNotExist(err) {
		t.Errorf("el latido debería borrarse al salir (err = %v)", err)
	}
	if env.cluster.SupervisorActive() {
		t.Error("tras salir, SupervisorActive debería ser false")
	}
}

// execSQL runs one statement on the "lodan" database of the test cluster.
func (e *supervisorEnv) execSQL(t *testing.T, query string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsn, err := e.cluster.DSN("lodan")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("no se pudo conectar: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, query); err != nil {
		t.Fatalf("%s falló: %v", query, err)
	}
}

// countRows returns the result of a query that yields one integer.
func (e *supervisorEnv) countRows(t *testing.T, query string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsn, err := e.cluster.DSN("lodan")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("no se pudo conectar: %v", err)
	}
	defer pool.Close()
	var n int
	if err := pool.QueryRow(ctx, query).Scan(&n); err != nil {
		t.Fatalf("%s falló: %v", query, err)
	}
	return n
}

// With the supervisor running, a backup and a restore leave it alive and PostgreSQL running as
// its own child (so stopping the supervisor stops PostgreSQL).
func TestRunSupervisorSobreviveABackupYRestore(t *testing.T) {
	fastMaintenancePoll(t)
	env := newSupervisorEnv(t)
	cancel, errc := env.start(t)
	env.awaitMigrated(t, errc)

	ctx, cancelOps := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancelOps()
	dir := t.TempDir()
	const launched = "arrancado en primer plano"

	env.execSQL(t, "CREATE TABLE maint (step text)")
	env.execSQL(t, "INSERT INTO maint VALUES ('antes')")

	path, err := env.cluster.Backup(ctx, dir, database.BackupFull)
	if err != nil {
		t.Fatalf("Backup con el supervisor en marcha falló: %v\nlog:\n%s", err, env.logs.String())
	}
	assertAlive(t, env, errc)
	if !env.running(t) {
		t.Fatal("PostgreSQL debería estar en marcha tras el backup")
	}
	env.awaitMarkGone(t) // the supervisor removes it once PostgreSQL is up again
	if got := strings.Count(env.logs.String(), launched); got != 2 {
		t.Errorf("tras el backup el supervisor debería haber lanzado PostgreSQL 2 veces y son %d:\n%s", got, env.logs.String())
	}

	env.execSQL(t, "INSERT INTO maint VALUES ('despues')")
	previous, err := env.cluster.Restore(ctx, []string{path})
	if err != nil {
		t.Fatalf("Restore con el supervisor en marcha falló: %v\nlog:\n%s", err, env.logs.String())
	}
	if previous != "" {
		if err := os.RemoveAll(previous); err != nil {
			t.Errorf("no se pudo borrar %s: %v", previous, err)
		}
	}
	assertAlive(t, env, errc)
	if !env.running(t) {
		t.Fatal("PostgreSQL debería estar en marcha tras la restauración")
	}
	env.awaitMarkGone(t)
	if got := strings.Count(env.logs.String(), launched); got != 3 {
		t.Errorf("tras la restauración el supervisor debería haber lanzado PostgreSQL 3 veces y son %d:\n%s", got, env.logs.String())
	}
	env.awaitMigrated(t, errc)
	if n := env.countRows(t, "SELECT count(*)::int FROM maint"); n != 1 {
		t.Errorf("tras restaurar maint debería tener 1 fila (la anterior al backup) y tiene %d", n)
	}

	cancel()
	if err := awaitExit(t, errc, 60*time.Second); err != nil {
		t.Fatalf("RunSupervisor devolvió error al cancelarse: %v", err)
	}
	if env.running(t) {
		t.Error("PostgreSQL sigue en marcha tras parar el supervisor: no lo gestionaba él")
	}
}

func TestRunSupervisorFallaSiElPostgresExternoSePara(t *testing.T) {
	old := pgWatchInterval
	pgWatchInterval = 200 * time.Millisecond
	t.Cleanup(func() { pgWatchInterval = old })

	env := newSupervisorEnv(t)
	ctx, cancelSetup := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelSetup()
	if err := env.cluster.EnsureRunning(ctx); err != nil {
		t.Fatalf("EnsureRunning falló: %v", err)
	}

	_, errc := env.start(t)
	env.awaitMigrated(t, errc)

	if err := env.cluster.Stop(ctx); err != nil {
		t.Fatalf("no se pudo parar el PostgreSQL externo: %v", err)
	}
	err := awaitExit(t, errc, 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "se ha parado") {
		t.Fatalf("se esperaba un error por la parada del PostgreSQL externo, y es: %v", err)
	}
}
