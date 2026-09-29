package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPlatform(t *testing.T) {
	tests := []struct {
		goos, goarch string
		want         string
		wantErr      bool
	}{
		{"linux", "amd64", "linux-64", false},
		{"linux", "arm64", "linux-aarch64", false},
		{"darwin", "amd64", "osx-64", false},
		{"darwin", "arm64", "osx-arm64", false},
		{"windows", "amd64", "win-64", false},
		{"windows", "arm64", "", true},
		{"freebsd", "amd64", "", true},
		{"linux", "386", "", true},
	}
	for _, tt := range tests {
		got, err := Platform(tt.goos, tt.goarch)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("Platform(%s,%s) = (%q, %v), quería (%q, err=%v)", tt.goos, tt.goarch, got, err, tt.want, tt.wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), "plataforma no soportada") {
			t.Errorf("mensaje inesperado: %v", err)
		}
	}
}

type fakeCall struct {
	name string
	args []string
}

// fakeRunner records commands and runs an optional hook that simulates them.
type fakeRunner struct {
	calls []fakeCall
	onRun func(name string, args []string) error
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string, out func(string)) error {
	f.calls = append(f.calls, fakeCall{name, args})
	if out != nil {
		out("línea de prueba")
	}
	if f.onRun != nil {
		return f.onRun(name, args)
	}
	return nil
}

// touch creates an empty file, creating parent directories.
func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o755); err != nil {
		t.Fatal(err)
	}
}

// populateEnv simulates what micromamba would install.
func populateEnv(t *testing.T, r Runtime) {
	t.Helper()
	suffix := exeSuffix(r.osName())
	for _, n := range []string{"initdb", "pg_ctl", "postgres"} {
		touch(t, filepath.Join(r.PostgresBinDir(), n+suffix))
	}
	touch(t, filepath.Join(r.extensionDir(), "vector.control"))
}

// mambaServer serves a fake micromamba binary and its .sha256 for the
// platform of r, and points micromambaBase to it.
func mambaServer(t *testing.T, r Runtime) *atomic.Int32 {
	t.Helper()
	plat, err := Platform(r.osName(), r.archName())
	if err != nil {
		t.Fatal(err)
	}
	file := "micromamba-" + plat + exeSuffix(r.osName())
	const content = "binario falso de micromamba"
	files := map[string]string{}
	files["/"+file] = content
	files["/"+file+".sha256"] = sumHex(content) + "  " + file + "\n"
	srv, hits := serveFiles(t, files)
	old := micromambaBase
	micromambaBase = srv.URL
	t.Cleanup(func() { micromambaBase = old })
	return hits
}

func TestEnsurePostgresCreatesEnv(t *testing.T) {
	tests := []struct {
		goos, goarch string
		pgSpec       string
		binRel       []string
	}{
		{"linux", "amd64", "postgresql", []string{"bin"}},
		{"darwin", "arm64", "postgresql", []string{"bin"}},
		{"windows", "amd64", "postgresql=16", []string{"Library", "bin"}},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			fr := &fakeRunner{}
			r := Runtime{Dir: t.TempDir(), goos: tt.goos, goarch: tt.goarch, run: fr}
			hits := mambaServer(t, r)
			fr.onRun = func(string, []string) error {
				populateEnv(t, r)
				return nil
			}

			var logs []string
			binDir, err := r.EnsurePostgres(context.Background(), func(s string) { logs = append(logs, s) })
			if err != nil {
				t.Fatalf("EnsurePostgres: %v", err)
			}

			wantBin := filepath.Join(append([]string{r.Dir, "pg"}, tt.binRel...)...)
			if binDir != wantBin {
				t.Errorf("binDir = %q, quería %q", binDir, wantBin)
			}
			if len(fr.calls) != 1 {
				t.Fatalf("quería 1 ejecución, hubo %d", len(fr.calls))
			}
			call := fr.calls[0]
			if call.name != r.MicromambaPath() {
				t.Errorf("ejecutable = %q, quería %q", call.name, r.MicromambaPath())
			}
			wantArgs := []string{
				"create", "-r", filepath.Join(r.Dir, "mamba"), "-p", filepath.Join(r.Dir, "pg"),
				"--no-rc", "--override-channels", "-c", "conda-forge", "-y", "-q", tt.pgSpec, "pgvector",
			}
			if !reflect.DeepEqual(call.args, wantArgs) {
				t.Errorf("args = %v\nquería  %v", call.args, wantArgs)
			}
			if _, err := os.Stat(r.MicromambaPath()); err != nil {
				t.Errorf("micromamba no se descargó: %v", err)
			}
			if len(logs) == 0 {
				t.Errorf("no hubo mensajes de log")
			}

			// Second call: environment complete, nothing runs or downloads.
			before := hits.Load()
			if _, err := r.EnsurePostgres(context.Background(), nil); err != nil {
				t.Fatalf("segunda llamada: %v", err)
			}
			if len(fr.calls) != 1 || hits.Load() != before {
				t.Errorf("la segunda llamada no debería ejecutar ni descargar nada")
			}
		})
	}
}

func TestEnsureMicromambaExecutable(t *testing.T) {
	r := Runtime{Dir: t.TempDir(), goos: "linux", goarch: "amd64"}
	mambaServer(t, r)
	p, err := r.EnsureMicromamba(context.Background())
	if err != nil {
		t.Fatalf("EnsureMicromamba: %v", err)
	}
	if p != filepath.Join(r.Dir, "bin", "micromamba") {
		t.Errorf("ruta = %q", p)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Errorf("permisos = %v, quería 0755", st.Mode().Perm())
	}
}

func TestEnsureMicromambaBadChecksum(t *testing.T) {
	r := Runtime{Dir: t.TempDir(), goos: "linux", goarch: "amd64"}
	files := map[string]string{}
	files["/micromamba-linux-64"] = "contenido"
	files["/micromamba-linux-64.sha256"] = sumHex("otro contenido") + "\n"
	srv, _ := serveFiles(t, files)
	old := micromambaBase
	micromambaBase = srv.URL
	t.Cleanup(func() { micromambaBase = old })

	if _, err := r.EnsureMicromamba(context.Background()); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("quería error de SHA-256, obtuve %v", err)
	}
	if _, err := os.Stat(r.MicromambaPath()); !os.IsNotExist(err) {
		t.Errorf("no debería quedar un binario sin verificar")
	}
}

func TestEnsurePostgresIncompleteEnv(t *testing.T) {
	fr := &fakeRunner{} // simulates a create that leaves nothing behind
	r := Runtime{Dir: t.TempDir(), goos: "linux", goarch: "amd64", run: fr}
	mambaServer(t, r)

	_, err := r.EnsurePostgres(context.Background(), nil)
	if err == nil {
		t.Fatal("quería error por entorno incompleto")
	}
	for _, want := range []string{"initdb", "pg_ctl", "postgres", "vector.control"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("el error no menciona %q: %v", want, err)
		}
	}
}

func TestEnsurePostgresMissingExtensionOnly(t *testing.T) {
	r := Runtime{Dir: t.TempDir(), goos: "linux", goarch: "amd64"}
	for _, n := range []string{"initdb", "pg_ctl", "postgres"} {
		touch(t, filepath.Join(r.PostgresBinDir(), n))
	}
	missing := r.MissingPostgresFiles()
	if len(missing) != 1 || !strings.HasSuffix(missing[0], "vector.control") {
		t.Errorf("missing = %v", missing)
	}
}

func TestEnsurePostgresRunnerError(t *testing.T) {
	boom := errors.New("fallo de red simulado")
	fr := &fakeRunner{onRun: func(string, []string) error { return boom }}
	r := Runtime{Dir: t.TempDir(), goos: "linux", goarch: "amd64", run: fr}
	mambaServer(t, r)

	if _, err := r.EnsurePostgres(context.Background(), nil); !errors.Is(err, boom) {
		t.Fatalf("quería envolver el error del runner, obtuve %v", err)
	}
}

func TestEnsurePostgresUnsupportedPlatform(t *testing.T) {
	r := Runtime{Dir: t.TempDir(), goos: "freebsd", goarch: "amd64", run: &fakeRunner{}}
	if _, err := r.EnsurePostgres(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "plataforma no soportada") {
		t.Fatalf("obtuve %v", err)
	}
}
