// Package database manages lodan's private PostgreSQL cluster (initdb, start, stop, status),
// the pgx connection pool and the embedded schema migrations.
package database

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"lodan/internal/config"
)

const (
	// pgUser is the superuser created by initdb and the only user allowed in pg_hba.conf.
	pgUser = "lodan"
	// defaultDatabase is the database lodan stores its data in.
	defaultDatabase = "lodan"
	// confMarker marks the block lodan appends to postgresql.conf.
	confMarker = "# lodan"
	// cmdWaitDelay bounds how long we wait for output pipes after a command exits
	// (pg_ctl start leaves the postmaster running in the background).
	cmdWaitDelay = 5 * time.Second
)

var dbNameRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Cluster is lodan's local PostgreSQL cluster, stored under Config.PGDataDir().
type Cluster struct {
	cfg    config.Config
	binDir string
}

// NewCluster resolves the PostgreSQL binaries directory and returns a Cluster.
func NewCluster(cfg config.Config) (*Cluster, error) {
	binDir, err := config.DetectPGBinDir(cfg)
	if err != nil {
		return nil, err
	}
	return &Cluster{cfg: cfg, binDir: binDir}, nil
}

// exe returns the full path of a PostgreSQL executable, adding .exe on Windows.
func (c *Cluster) exe(name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(c.binDir, name)
}

// Exe returns the full path of a PostgreSQL executable (postgres, pg_ctl...), adding .exe on
// Windows. The service supervisor uses it to run postgres in the foreground.
func (c *Cluster) Exe(name string) string { return c.exe(name) }

// Lock takes the cross-process lock that serializes cluster startup (the same one that
// EnsureRunning uses) and returns the function that releases it, which is safe to call more
// than once.
func (c *Cluster) Lock(ctx context.Context) (unlock func(), err error) {
	if err := os.MkdirAll(c.cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("no se pudo crear el directorio de datos %s: %w", c.cfg.DataDir, err)
	}
	return acquireLock(ctx, c.cfg.LockFile())
}

// run executes a PostgreSQL binary and returns its combined output.
func (c *Cluster) run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.exe(name), args...)
	cmd.WaitDelay = cmdWaitDelay
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Initialized reports whether the cluster data directory has already been created by initdb.
func (c *Cluster) Initialized() bool {
	_, err := os.Stat(filepath.Join(c.cfg.PGDataDir(), "PG_VERSION"))
	return err == nil
}

// Init creates the cluster with initdb and applies lodan's configuration. It is idempotent:
// if the cluster already exists nothing is recreated (only a missing configuration block is
// re-applied).
func (c *Cluster) Init(ctx context.Context) error {
	if err := os.MkdirAll(c.cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("no se pudo crear el directorio de datos %s: %w", c.cfg.DataDir, err)
	}
	if err := os.MkdirAll(c.cfg.LogsDir(), 0o700); err != nil {
		return fmt.Errorf("no se pudo crear el directorio de logs %s: %w", c.cfg.LogsDir(), err)
	}

	if c.Initialized() {
		if !c.configured() {
			return c.configure()
		}
		return nil
	}

	password, err := randomPassword()
	if err != nil {
		return err
	}
	secret := c.cfg.SecretFile()
	if err := os.WriteFile(secret, []byte(password), 0o600); err != nil {
		return fmt.Errorf("no se pudo escribir la contraseña en %s: %w", secret, err)
	}
	// WriteFile keeps the mode of a pre-existing file: force it.
	if err := os.Chmod(secret, 0o600); err != nil {
		return fmt.Errorf("no se pudieron fijar los permisos de %s: %w", secret, err)
	}

	pwfile, err := writeTempPwfile(c.cfg.DataDir, password)
	if err != nil {
		return err
	}
	defer os.Remove(pwfile)

	out, err := c.run(ctx, "initdb",
		"-D", c.cfg.PGDataDir(),
		"-U", pgUser,
		"-E", "UTF8",
		"--locale=C",
		"--auth-host=scram-sha-256",
		"--auth-local=scram-sha-256",
		"--pwfile="+pwfile,
	)
	if err != nil {
		return fmt.Errorf("initdb falló: %w\n%s", err, out)
	}

	return c.configure()
}

// randomPassword returns 32 random bytes encoded as unpadded base64url.
func randomPassword() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("no se pudo generar la contraseña aleatoria: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// writeTempPwfile writes password into a new 0600 temporary file inside dir and returns its path.
func writeTempPwfile(dir, password string) (string, error) {
	f, err := os.CreateTemp(dir, ".pwfile-*") // CreateTemp creates the file with mode 0600
	if err != nil {
		return "", fmt.Errorf("no se pudo crear el fichero temporal de contraseña: %w", err)
	}
	name := f.Name()
	_, werr := f.WriteString(password + "\n")
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("no se pudo escribir el fichero temporal de contraseña: %w", err)
	}
	return name, nil
}

// configured reports whether postgresql.conf already contains lodan's block.
func (c *Cluster) configured() bool {
	data, err := os.ReadFile(filepath.Join(c.cfg.PGDataDir(), "postgresql.conf"))
	return err == nil && strings.Contains(string(data), confMarker)
}

// configure appends lodan's settings to postgresql.conf and rewrites pg_hba.conf so that only
// the lodan user can connect, over TCP on 127.0.0.1, with scram-sha-256.
func (c *Cluster) configure() error {
	pgdata := c.cfg.PGDataDir()

	block := strings.Join([]string{
		"",
		confMarker + ": ajustes locales (generados por lodan)",
		"listen_addresses = '127.0.0.1'",
		"port = " + strconv.Itoa(c.cfg.PGPort),
		"unix_socket_directories = ''",
		"shared_buffers = 256MB",
		"work_mem = 16MB",
		"maintenance_work_mem = 512MB",
		"max_connections = 20",
		"",
	}, "\n")

	confPath := filepath.Join(pgdata, "postgresql.conf")
	f, err := os.OpenFile(confPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("no se pudo abrir %s: %w", confPath, err)
	}
	_, werr := f.WriteString(block)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		return fmt.Errorf("no se pudo escribir %s: %w", confPath, err)
	}

	hba := "# Generado por lodan: solo el usuario lodan, por TCP local y con contraseña.\n" +
		"host all " + pgUser + " 127.0.0.1/32 scram-sha-256\n"
	hbaPath := filepath.Join(pgdata, "pg_hba.conf")
	if err := os.WriteFile(hbaPath, []byte(hba), 0o600); err != nil {
		return fmt.Errorf("no se pudo escribir %s: %w", hbaPath, err)
	}
	return nil
}

// Status reports whether the server is running (pg_ctl status: exit 0 = running, 3 = not
// running, 4 = no data directory, which is also reported as not running).
func (c *Cluster) Status(ctx context.Context) (running bool, err error) {
	out, err := c.run(ctx, "pg_ctl", "status", "-D", c.cfg.PGDataDir())
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		switch exitErr.ExitCode() {
		case 3, 4:
			return false, nil
		}
	}
	return false, fmt.Errorf("pg_ctl status falló: %w\n%s", err, out)
}

// Start starts the server and waits until it accepts connections. It does nothing if the
// server is already running.
func (c *Cluster) Start(ctx context.Context) error {
	if !c.Initialized() {
		return fmt.Errorf("el clúster no está inicializado en %s: ejecuta Init antes de arrancarlo", c.cfg.PGDataDir())
	}
	running, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if running {
		return nil
	}
	if err := os.MkdirAll(c.cfg.LogsDir(), 0o700); err != nil {
		return fmt.Errorf("no se pudo crear el directorio de logs %s: %w", c.cfg.LogsDir(), err)
	}

	logFile := filepath.Join(c.cfg.LogsDir(), "postgres.log")
	out, err := c.run(ctx, "pg_ctl", "start", "-w", "-t", "60", "-D", c.cfg.PGDataDir(), "-l", logFile)
	if err != nil {
		return fmt.Errorf("pg_ctl start falló: %w\n%s\n(consulta el log en %s)", err, out, logFile)
	}
	return nil
}

// Stop stops the server with a fast shutdown and waits for it. It does nothing if the
// server is not running.
func (c *Cluster) Stop(ctx context.Context) error {
	running, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if !running {
		return nil
	}
	out, err := c.run(ctx, "pg_ctl", "stop", "-m", "fast", "-w", "-D", c.cfg.PGDataDir())
	if err != nil {
		return fmt.Errorf("pg_ctl stop falló: %w\n%s", err, out)
	}
	return nil
}

// EnsureRunning brings the cluster to a usable state: it initializes it if needed, starts it
// and creates the "lodan" database. A lockfile serializes concurrent lodan processes.
func (c *Cluster) EnsureRunning(ctx context.Context) error {
	if err := os.MkdirAll(c.cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("no se pudo crear el directorio de datos %s: %w", c.cfg.DataDir, err)
	}
	unlock, err := acquireLock(ctx, c.cfg.LockFile())
	if err != nil {
		return err
	}
	defer unlock()

	if err := c.Init(ctx); err != nil {
		return err
	}
	if err := c.Start(ctx); err != nil {
		return err
	}
	return c.EnsureDatabase(ctx, defaultDatabase)
}

// EnsureDatabase creates the database name if it does not exist. The name must match
// ^[a-z_][a-z0-9_]*$.
func (c *Cluster) EnsureDatabase(ctx context.Context, name string) error {
	if !dbNameRe.MatchString(name) {
		return fmt.Errorf("nombre de base de datos inválido %q: debe cumplir %s", name, dbNameRe)
	}
	dsn, err := c.DSN("postgres")
	if err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("no se pudo conectar a la base postgres: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))

	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return fmt.Errorf("no se pudo consultar pg_database: %w", err)
	}
	if exists {
		return nil
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		return fmt.Errorf("no se pudo crear la base de datos %q: %w", name, err)
	}
	return nil
}

// DSN builds the connection string for database dbname using the password stored in SecretFile.
func (c *Cluster) DSN(dbname string) (string, error) {
	data, err := os.ReadFile(c.cfg.SecretFile())
	if err != nil {
		return "", fmt.Errorf("no se pudo leer la contraseña en %s (¿está inicializado el clúster?): %w", c.cfg.SecretFile(), err)
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(pgUser, strings.TrimSpace(string(data))),
		Host:     net.JoinHostPort("127.0.0.1", strconv.Itoa(c.cfg.PGPort)),
		Path:     "/" + dbname,
		RawQuery: "sslmode=disable",
	}
	return u.String(), nil
}
