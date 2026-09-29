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
