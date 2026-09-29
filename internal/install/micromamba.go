package install

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// micromambaBase is the release download base; a variable so tests can point
// it to a local server.
var micromambaBase = "https://github.com/mamba-org/micromamba-releases/releases/latest/download"

// Platform maps GOOS/GOARCH to the micromamba platform name.
func Platform(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "linux/amd64":
		return "linux-64", nil
	case "linux/arm64":
		return "linux-aarch64", nil
	case "darwin/amd64":
		return "osx-64", nil
	case "darwin/arm64":
		return "osx-arm64", nil
	case "windows/amd64":
		return "win-64", nil
	}
	return "", fmt.Errorf("plataforma no soportada: %s/%s", goos, goarch)
}

// runner executes external commands; injectable for tests. Each output line
// (stdout and stderr merged) is passed to out, which may be nil.
type runner interface {
	Run(ctx context.Context, name string, args []string, out func(line string)) error
}

// Runtime is the private runtime directory holding micromamba and the
// PostgreSQL environment: <Dir>/bin, <Dir>/mamba (root) and <Dir>/pg (env).
type Runtime struct {
	Dir string

	// Overrides for tests; zero values mean the current platform / real exec.
	goos   string
	goarch string
	run    runner
}

func (r Runtime) osName() string {
	if r.goos != "" {
		return r.goos
	}
	return runtime.GOOS
}

func (r Runtime) archName() string {
	if r.goarch != "" {
		return r.goarch
	}
	return runtime.GOARCH
}

func (r Runtime) cmdRunner() runner {
	if r.run != nil {
		return r.run
	}
	return execRunner{}
}

// MambaRoot is the micromamba root prefix (package cache and so on).
func (r Runtime) MambaRoot() string { return filepath.Join(r.Dir, "mamba") }

// EnvDir is the conda environment that holds PostgreSQL and pgvector.
func (r Runtime) EnvDir() string { return filepath.Join(r.Dir, "pg") }

// MicromambaPath is where the micromamba binary lives.
func (r Runtime) MicromambaPath() string {
	return filepath.Join(r.Dir, "bin", "micromamba"+exeSuffix(r.osName()))
}

// PostgresBinDir is the directory with initdb, pg_ctl and postgres:
// <env>/bin on Unix and <env>\Library\bin on Windows.
func (r Runtime) PostgresBinDir() string {
	if r.osName() == "windows" {
		return filepath.Join(r.EnvDir(), "Library", "bin")
	}
	return filepath.Join(r.EnvDir(), "bin")
}

// extensionDir is where PostgreSQL extension control files live.
func (r Runtime) extensionDir() string {
	if r.osName() == "windows" {
		return filepath.Join(r.EnvDir(), "Library", "share", "extension")
	}
	return filepath.Join(r.EnvDir(), "share", "extension")
}

// MissingPostgresFiles lists the required files that do not exist (empty
// means the environment is complete).
func (r Runtime) MissingPostgresFiles() []string {
	var missing []string
	suffix := exeSuffix(r.osName())
	for _, name := range []string{"initdb", "pg_ctl", "postgres"} {
		p := filepath.Join(r.PostgresBinDir(), name+suffix)
		if !isRegularFile(p) {
			missing = append(missing, p)
		}
	}
	if p := filepath.Join(r.extensionDir(), "vector.control"); !isRegularFile(p) {
		missing = append(missing, p)
	}
	return missing
}

func isRegularFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// EnsureMicromamba downloads the micromamba binary (SHA-256 verified) into
// <Dir>/bin and returns its path.
func (r Runtime) EnsureMicromamba(ctx context.Context) (string, error) {
	plat, err := Platform(r.osName(), r.archName())
	if err != nil {
		return "", err
	}
	url := micromambaBase + "/micromamba-" + plat + exeSuffix(r.osName())
	want, err := fetchSHA256(ctx, url+".sha256")
	if err != nil {
		return "", fmt.Errorf("no se pudo obtener la suma de verificación de micromamba: %w", err)
	}
	dest := r.MicromambaPath()
	if err := download(ctx, url, dest, want, nil); err != nil {
		return "", fmt.Errorf("no se pudo descargar micromamba: %w", err)
	}
	if err := os.Chmod(dest, 0o755); err != nil {
		return "", fmt.Errorf("no se pudo hacer ejecutable %s: %w", dest, err)
	}
	return dest, nil
}

// EnsurePostgres makes sure the environment with PostgreSQL and pgvector
// exists and returns the directory with its binaries. It does nothing if the
// environment is already complete. log may be nil.
func (r Runtime) EnsurePostgres(ctx context.Context, log func(string)) (string, error) {
	if log == nil {
		log = func(string) {}
	}
	binDir := r.PostgresBinDir()
	if len(r.MissingPostgresFiles()) == 0 {
		return binDir, nil
	}

	log("Descargando micromamba…")
	mm, err := r.EnsureMicromamba(ctx)
	if err != nil {
		return "", err
	}

	pgSpec := "postgresql"
	if r.osName() == "windows" {
		// pgvector on conda-forge for Windows is built against PostgreSQL 16.
		pgSpec = "postgresql=16"
	}
	args := []string{
		"create", "-r", r.MambaRoot(), "-p", r.EnvDir(),
		"--no-rc", "--override-channels", "-c", "conda-forge",
		"-y", "-q", pgSpec, "pgvector",
	}
	log("Creando el entorno de PostgreSQL con pgvector (puede tardar un minuto)…")
	if err := r.cmdRunner().Run(ctx, mm, args, log); err != nil {
		return "", fmt.Errorf("no se pudo crear el entorno de PostgreSQL: %w", err)
	}

	if missing := r.MissingPostgresFiles(); len(missing) > 0 {
		return "", fmt.Errorf("el entorno de PostgreSQL quedó incompleto; faltan:\n  %s\nBorra %s y vuelve a ejecutar la instalación",
			strings.Join(missing, "\n  "), r.EnvDir())
	}
	return binDir, nil
}

// execRunner runs real processes.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args []string, out func(string)) error {
	cmd := exec.CommandContext(ctx, name, args...)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	tailCh := make(chan []string, 1)
	go func() {
		var tail []string
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if out != nil {
				out(line)
			}
			tail = append(tail, line)
			if len(tail) > 20 {
				tail = tail[1:]
			}
		}
		// Drain in case the scanner stopped early, so the process never blocks.
		_, _ = io.Copy(io.Discard, pr)
		tailCh <- tail
	}()

	err := cmd.Run()
	_ = pw.Close()
	tail := <-tailCh
	if err != nil {
		return fmt.Errorf("%s: %w\n%s", filepath.Base(name), err, strings.Join(tail, "\n"))
	}
	return nil
}
