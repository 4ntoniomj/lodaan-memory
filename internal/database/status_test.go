package database

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"lodan/internal/config"
)

// exitError returns a real *exec.ExitError with the given exit code.
func exitError(t *testing.T, code int) error {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", "exit "+strconv.Itoa(code))
	} else {
		cmd = exec.Command("sh", "-c", "exit "+strconv.Itoa(code))
	}
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Skipf("no se pudo fabricar un código de salida %d: %v", code, err)
	}
	return err
}

// fakePgCtlStatus replaces pgCtlStatus until the end of the test.
func fakePgCtlStatus(t *testing.T, err error) {
	t.Helper()
	orig := pgCtlStatus
	pgCtlStatus = func(context.Context, *Cluster) (string, error) { return "simulado", err }
	t.Cleanup(func() { pgCtlStatus = orig })
}

// bareClusterWithSecret returns a Cluster over a temporary directory with a secret file and a
// free port where nothing listens. It needs no PostgreSQL binaries.
func bareClusterWithSecret(t *testing.T) *Cluster {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("no se pudo reservar un puerto libre: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DataDir: t.TempDir(), PGPort: port}
	if err := os.WriteFile(cfg.SecretFile(), []byte("secreto\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.PGDataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	return &Cluster{cfg: cfg}
}

func writePostmasterPid(t *testing.T, c *Cluster) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(c.cfg.PGDataDir(), "postmaster.pid"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// With a live server, a pg_ctl that cannot see it (Windows, unelevated, postmaster owned by
// LocalSystem) must not make Status say "not running".
func TestStatusConfirmaConConexionSiPgCtlNoLoVe(t *testing.T) {
	cl, _ := newLocalCluster(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if err := cl.Init(ctx); err != nil {
		t.Fatalf("Init falló: %v", err)
	}
	if err := cl.Start(ctx); err != nil {
		t.Fatalf("Start falló: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testTimeout)
		defer stopCancel()
		if err := cl.Stop(stopCtx); err != nil {
			t.Logf("no se pudo parar el clúster de prueba: %v", err)
		}
	})
	if !cl.hasPostmasterPid() {
		t.Fatal("con el servidor en marcha debe existir postmaster.pid")
	}

	cases := []struct {
		name string
		err  error
	}{
		{"pg_ctl dice parado (3)", exitError(t, 3)},
		{"pg_ctl falla con otro código (1)", exitError(t, 1)},
		{"pg_ctl no se puede ejecutar", errors.New("acceso denegado")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakePgCtlStatus(t, tc.err)
			running, err := cl.Status(ctx)
			if err != nil || !running {
				t.Fatalf("Status = (%v, %v); con postmaster.pid y el servidor aceptando conexiones debe ser (true, nil)", running, err)
			}
		})
	}
}

func TestStatusPostmasterPidHuerfanoNoCuentaComoEnMarcha(t *testing.T) {
	cl := bareClusterWithSecret(t)
	writePostmasterPid(t, cl)
	fakePgCtlStatus(t, exitError(t, 3))

	running, err := cl.Status(context.Background())
	if err != nil || running {
		t.Fatalf("Status = (%v, %v); con un postmaster.pid huérfano y nada escuchando debe ser (false, nil)", running, err)
	}
}

func TestStatusSinPostmasterPidNoConecta(t *testing.T) {
	cl := bareClusterWithSecret(t)
	fakePgCtlStatus(t, exitError(t, 3))

	running, err := cl.Status(context.Background())
	if err != nil || running {
		t.Fatalf("Status = (%v, %v); sin postmaster.pid y con pg_ctl diciendo parado debe ser (false, nil)", running, err)
	}
}

// An unexpected pg_ctl failure with nothing to confirm keeps being an error.
func TestStatusErrorInesperadoDePgCtlSeMantiene(t *testing.T) {
	cl := bareClusterWithSecret(t)
	writePostmasterPid(t, cl)
	fakePgCtlStatus(t, exitError(t, 1))

	running, err := cl.Status(context.Background())
	if err == nil || running {
		t.Fatalf("Status = (%v, %v); un fallo de pg_ctl que no se puede confirmar debe devolver error", running, err)
	}
}

func TestStatusPgCtlEnMarcha(t *testing.T) {
	cl := bareClusterWithSecret(t)
	fakePgCtlStatus(t, nil)

	running, err := cl.Status(context.Background())
	if err != nil || !running {
		t.Fatalf("Status = (%v, %v); con pg_ctl diciendo en marcha debe ser (true, nil)", running, err)
	}
}
