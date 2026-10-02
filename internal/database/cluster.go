// Package database manages lodan's private PostgreSQL cluster (initdb, start, stop, status),
// the pgx connection pool and the embedded schema migrations.
package database

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/klauspost/compress/zstd"

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
	return combinedOutput(cmd)
}

// combinedOutput runs cmd with cmdWaitDelay and returns its trimmed combined output. On Windows
// `pg_ctl start` leaves the postmaster holding the output pipe, so Wait ends with
// exec.ErrWaitDelay even though pg_ctl succeeded: os/exec only returns that error when the
// command otherwise exited successfully, so it is not a failure.
func combinedOutput(cmd *exec.Cmd) (string, error) {
	cmd.WaitDelay = cmdWaitDelay
	out, err := cmd.CombinedOutput()
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
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

// statusPingTimeout bounds the connection that Status makes to confirm a server that
// pg_ctl status does not see (see Status).
const statusPingTimeout = 3 * time.Second

// pgCtlStatus runs `pg_ctl status` on the cluster. It is a variable so that tests can simulate
// a pg_ctl that cannot see a postmaster that is running.
var pgCtlStatus = func(ctx context.Context, c *Cluster) (string, error) {
	return c.run(ctx, "pg_ctl", "status", "-D", c.cfg.PGDataDir())
}

// Status reports whether the server is running (pg_ctl status: exit 0 = running, 3 = not
// running, 4 = no data directory, which is also reported as not running).
//
// pg_ctl is not always right. On Windows the service runs `lodan service run` as LocalSystem, and
// that process starts the postmaster; a pg_ctl launched by the regular user, without elevation,
// cannot inspect a process owned by LocalSystem, so it answers "not running" (or fails) even
// though postmaster.pid exists and the server accepts connections. So when pg_ctl does not say
// "running" but postmaster.pid exists, Status confirms with a real authenticated connection
// (user and password from SecretFile) to the cluster port: if it answers, the server is running.
// Authenticating avoids mistaking for lodan's PostgreSQL whatever else occupies the port (for
// example the WSL relay). It applies to every OS: a stale postmaster.pid with nothing listening
// still reports "not running".
func (c *Cluster) Status(ctx context.Context) (running bool, err error) {
	out, err := pgCtlStatus(ctx, c)
	if err == nil {
		return true, nil
	}
	notRunning := false
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		switch exitErr.ExitCode() {
		case 3, 4:
			notRunning = true
		}
	}
	if c.hasPostmasterPid() && c.acceptsConnections(ctx) {
		return true, nil
	}
	if notRunning {
		return false, nil
	}
	return false, fmt.Errorf("pg_ctl status falló: %w\n%s", err, out)
}

// hasPostmasterPid reports whether the data directory has a postmaster.pid (a running server
// or one that died without cleaning up).
func (c *Cluster) hasPostmasterPid() bool {
	_, err := os.Stat(filepath.Join(c.cfg.PGDataDir(), "postmaster.pid"))
	return err == nil
}

// acceptsConnections reports whether the cluster accepts an authenticated connection to the
// postgres database within statusPingTimeout.
func (c *Cluster) acceptsConnections(ctx context.Context) bool {
	dsn, err := c.DSN("postgres")
	if err != nil {
		return false
	}
	pctx, cancel := context.WithTimeout(ctx, statusPingTimeout)
	defer cancel()
	conn, err := pgx.Connect(pctx, dsn)
	if err != nil {
		return false
	}
	defer conn.Close(context.WithoutCancel(ctx))
	return conn.Ping(pctx) == nil
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

// Backup and restore.
//
// A backup is a cold copy: PostgreSQL is stopped while the files are read, so the archive is
// always consistent. It is a tar compressed with zstd (default level) named
// lodan_backup_<date>_<time>.<kind>.tar.zst (kind: full, incr or diff) with these entries, in this
// order: info.txt (metadata), manifest.txt (see below), pg/ (the cluster; only the files changed
// since the reference backup for incr and diff), config.json (if it exists) and secret.
//
// Downtime and disk space. PostgreSQL is stopped only while what has to be archived (the
// directories and the chosen files of pg/, plus config.json and secret) is copied, uncompressed,
// to a staging directory inside the destination (.lodan_backup_<date>.<kind>.staging, mode 0700,
// keeping the mode and modification time of every file). So the downtime lasts as long as that
// copy takes, and the destination needs free space for an uncompressed copy of what is backed up.
// Then PostgreSQL is started again and the cluster lock is released (the service supervisor takes
// that lock to prepare and start PostgreSQL, so it must not be held while compressing), and only
// then is the archive compressed from the staging copy, so compression happens with PostgreSQL
// already running. The archive is written to a .part file renamed at the end. The staging
// directory is always removed when the backup ends, successfully or not (and the .part file if it
// fails); one left by an interrupted backup is deleted at the start of the next one.
//
// Concurrent backups. A lock file inside the destination (.lodan_backup.lock, same mechanism as
// the cluster lock: refreshed while it is held, abandoned after lockStaleAfter) serializes the
// backups into the same destination from the start to the end of each one, compression included,
// so that a slow full backup and a cron incremental one cannot delete each other's staging
// directory. A backup waits up to lockWaitDeadline for it and then fails saying that another
// backup is in progress. Lock order: destination first, then cluster. Restore does not take it.
//
// There are three kinds, with the usual meaning:
//   - full: every file of the cluster.
//   - differential (diff): the files modified since the last full backup in the destination.
//   - incremental (incr): the files modified since the last backup of any kind (full, incr or
//     diff) in the destination.
//
// Both incr and diff fail if the destination has no full backup. A regular file is copied if any
// of these holds against the reference backup (the one whose creation time is stored in info.txt
// as "desde"):
//   - its modification time is after that creation time (minus backupSinceMargin);
//   - its path is not listed in the manifest of the reference backup;
//   - its size differs from the one in that manifest.
//
// The last two exist because rename(2) keeps the modification time: PostgreSQL recycles WAL
// segments by renaming old ones to future names, so a "new" file can have an old mtime and would
// be missed by the first rule alone. Restore then would find in the manifest a file that no
// backup of the chain holds. With the three rules, by induction every backup contains whatever
// its manifest lists that the reference manifest did not list (or listed with another size) or
// that changed, and the reference chain already holds the rest: the chain holds everything the
// manifest of its last backup lists.
//
// Restoring a chain means extracting the full backup and then every incr and diff after it, from
// oldest to newest, replacing the files that already exist.
//
// Files deleted since the reference cannot be seen from the modification times, so every backup
// (full, incr or diff) also carries manifest.txt: the complete list of what pg/ contained when it
// was made (not only what was copied). Restore uses the manifest of the newest backup of the
// chain to delete whatever should no longer be there and to check that nothing is missing.
//
// manifest.txt has one line per directory or regular file of pg/ (postmaster.pid excluded; pg/
// itself is not listed), sorted by path (byte order), each one as:
//
//	<d|f> <size> <relative/path>
//
// "d" is a directory (size 0) and "f" a regular file (size in bytes). The path comes last, slash
// separated and relative to pg/, and may contain spaces but never a line break.

const (
	// BackupFull makes a complete backup. BackupIncremental and BackupDifferential make a backup
	// of only what changed (see above). They are the values accepted by Backup.
	BackupFull         = "full"
	BackupIncremental  = "incremental"
	BackupDifferential = "differential"

	backupNameLayout   = "2006-01-02_150405"
	backupPrefix       = "lodan_backup_"
	backupExt          = ".tar.zst"
	backupInfoFile     = "info.txt"
	backupManifestFile = "manifest.txt"
	backupPGEntry      = "pg"
	backupFormat       = "lodan-backup/2"

	// manifestMaxBytes caps how much of manifest.txt is read (and how big one may be when
	// writing it). A line takes about 80 bytes (type, size and a relative path of a few dozen
	// characters), so 64 MiB leave room for roughly 800,000 entries, far more than the tens of
	// thousands that even a large lodan cluster has.
	manifestMaxBytes = 64 << 20

	// backupSinceMargin is subtracted from the reference time when looking for changed files:
	// it covers the coarse resolution of file timestamps. Including a file twice is harmless;
	// missing one is not.
	backupSinceMargin = 2 * time.Second
	// lockRefreshEvery is how often the lock is touched during a long operation, so that it is
	// never considered stale (lockStaleAfter) and removed by another process.
	lockRefreshEvery = 30 * time.Second
	// restartTimeout bounds the restart of PostgreSQL once a backup or restore is over.
	restartTimeout = 2 * time.Minute
	// verifyTimeout bounds the connection check after a restore.
	verifyTimeout = 15 * time.Second
)

// backupNameRe matches the file name of a backup; the group is its kind (full, incr, diff).
var backupNameRe = regexp.MustCompile(`^lodan_backup_\d{4}-\d{2}-\d{2}_\d{6}\.(full|incr|diff)\.tar\.zst$`)

// backupLockName is the lock file that a backup keeps inside its destination while it runs. It
// is not a backup, so it never matches backupNameRe and listBackups ignores it.
const backupLockName = ".lodan_backup.lock"

// backupLockWait is how long a backup waits for the lock of its destination before giving up
// with an error (the same limit as the cluster lock; a variable so that tests can shorten it).
var backupLockWait = lockWaitDeadline

// backupStagingSuffix ends the name of the staging directory of a backup in progress, which is
// ".lodan_backup_<date>.<kind>.staging" inside the destination.
const backupStagingSuffix = ".staging"

// backupStagingRe matches the name of the staging directory of a backup (see stagingDirName).
// It never matches backupNameRe, so listBackups cannot take one for a backup.
var backupStagingRe = regexp.MustCompile(`^\.lodan_backup_\d{4}-\d{2}-\d{2}_\d{6}\.(full|incr|diff)\.staging$`)

// backupInfo is the content of the info.txt of a backup.
type backupInfo struct {
	Kind          string    // full, incr or diff
	CreatedAt     time.Time // moment right after PostgreSQL was stopped
	Since         time.Time // CreatedAt of the reference backup (zero for full)
	OriginalBytes int64     // sum of the sizes of the archived files
	Files         int       // number of archived regular files
	PGVersion     string
}

// render returns the text of info.txt: one "key: value" per line.
func (i backupInfo) render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "formato: %s\n", backupFormat)
	fmt.Fprintf(&b, "tipo: %s\n", i.Kind)
	fmt.Fprintf(&b, "fecha: %s\n", i.CreatedAt.Format(time.RFC3339Nano))
	if !i.Since.IsZero() {
		fmt.Fprintf(&b, "desde: %s\n", i.Since.Format(time.RFC3339Nano))
	}
	fmt.Fprintf(&b, "tamano_original_bytes: %d\n", i.OriginalBytes)
	fmt.Fprintf(&b, "archivos: %d\n", i.Files)
	fmt.Fprintf(&b, "postgresql: %s\n", i.PGVersion)
	return b.String()
}

// parseBackupInfo reads the text written by render.
func parseBackupInfo(r io.Reader) (backupInfo, error) {
	data, err := io.ReadAll(io.LimitReader(r, 64<<10))
	if err != nil {
		return backupInfo{}, fmt.Errorf("no se pudo leer %s: %w", backupInfoFile, err)
	}
	var info backupInfo
	format := ""
	for _, line := range strings.Split(string(data), "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), ": ")
		if !ok {
			continue
		}
		switch key {
		case "formato":
			format = val
		case "tipo":
			info.Kind = val
		case "fecha":
			t, err := time.Parse(time.RFC3339Nano, val)
			if err != nil {
				return backupInfo{}, fmt.Errorf("fecha inválida en %s: %w", backupInfoFile, err)
			}
			info.CreatedAt = t
		case "desde":
			t, err := time.Parse(time.RFC3339Nano, val)
			if err != nil {
				return backupInfo{}, fmt.Errorf("fecha de referencia inválida en %s: %w", backupInfoFile, err)
			}
			info.Since = t
		case "tamano_original_bytes":
			info.OriginalBytes, _ = strconv.ParseInt(val, 10, 64)
		case "archivos":
			info.Files, _ = strconv.Atoi(val)
		case "postgresql":
			info.PGVersion = val
		}
	}
	if format != backupFormat {
		if format == "" {
			return backupInfo{}, fmt.Errorf("%s no tiene el formato esperado (%s)", backupInfoFile, backupFormat)
		}
		return backupInfo{}, fmt.Errorf("%s tiene el formato %q y solo se admite %s: el backup se hizo con otra versión de lodan", backupInfoFile, format, backupFormat)
	}
	switch info.Kind {
	case "full", "incr", "diff":
	default:
		return backupInfo{}, fmt.Errorf("tipo de backup desconocido %q en %s", info.Kind, backupInfoFile)
	}
	if info.CreatedAt.IsZero() {
		return backupInfo{}, fmt.Errorf("falta la fecha en %s", backupInfoFile)
	}
	return info, nil
}

// backupKind maps the public backup type to the kind used in file names.
func backupKind(backupType string) (string, error) {
	switch backupType {
	case BackupFull:
		return "full", nil
	case BackupIncremental:
		return "incr", nil
	case BackupDifferential:
		return "diff", nil
	}
	return "", fmt.Errorf("tipo de backup desconocido %q (usa %s, %s o %s)", backupType, BackupFull, BackupIncremental, BackupDifferential)
}

// keepLockAlive touches the lock file periodically until the returned function is called.
// acquireLock treats a lock older than lockStaleAfter as abandoned; a long backup or restore
// must not lose it, or a restarting service supervisor could start PostgreSQL in the middle.
func (c *Cluster) keepLockAlive() (stop func()) {
	return keepFileAlive(c.cfg.LockFile())
}

// keepFileAlive touches the lock file at path periodically until the returned function is called.
func keepFileAlive(path string) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(lockRefreshEvery)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				now := time.Now()
				_ = os.Chtimes(path, now, now)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-finished
	}
}

// startDetached starts PostgreSQL even if ctx has been cancelled (Ctrl-C in the middle of an
// operation must not leave the database stopped).
func (c *Cluster) startDetached(ctx context.Context) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restartTimeout)
	defer cancel()
	return c.Start(rctx)
}

// Backup writes a backup of the cluster, config.json and secret into destDir and returns the
// path of the archive. backupType is BackupFull, BackupDifferential (changes since the last full
// backup in destDir) or BackupIncremental (changes since the last backup of any kind in destDir);
// the last two fail if destDir has no full backup. PostgreSQL is stopped only while what has to be
// archived is copied to a staging directory inside destDir, and started again (if it was running)
// before the archive is compressed; the cluster lock is held until then and released before
// compressing. If PostgreSQL cannot be restarted, the archive is still written and the returned
// error reports the restart failure together with a non-empty path.
//
// With the service supervisor active (SupervisorActive) PostgreSQL is not started by Backup, so
// that it stays a child of the service: the lock is released and Backup waits (polling Status
// every 500 ms, up to restartTimeout) for the supervisor to start it. If that does not happen it
// starts PostgreSQL itself and returns an error saying so (the archive is still written).
func (c *Cluster) Backup(ctx context.Context, destDir, backupType string) (path string, err error) {
	kind, err := backupKind(backupType)
	if err != nil {
		return "", err
	}
	if !c.Initialized() {
		return "", fmt.Errorf("el clúster no está inicializado en %s: no hay nada que respaldar", c.cfg.PGDataDir())
	}
	if destDir == "" {
		return "", errors.New("falta el directorio de destino del backup")
	}
	destDir, err = filepath.Abs(destDir)
	if err != nil {
		return "", fmt.Errorf("ruta de destino inválida: %w", err)
	}
	pgReal, err := filepath.EvalSymlinks(c.cfg.PGDataDir())
	if err != nil {
		return "", fmt.Errorf("no se pudo resolver %s: %w", c.cfg.PGDataDir(), err)
	}
	// The destination may not exist yet: it is checked before creating it, so that a rejected
	// destination inside the data directory leaves nothing behind.
	destReal, err := resolveExistingPrefix(destDir)
	if err != nil {
		return "", fmt.Errorf("no se pudo resolver %s: %w", destDir, err)
	}
	if pathWithin(pgReal, destReal) {
		return "", fmt.Errorf("el destino %s está dentro del directorio de datos de PostgreSQL (%s): elige otro", destDir, pgReal)
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return "", fmt.Errorf("no se pudo crear el directorio de destino %s: %w", destDir, err)
	}

	// The destination lock serializes the backups into destDir for their whole duration,
	// compression included, so one cannot delete the staging directory of another or pick a
	// stale reference. Lock order: destination first, then cluster (taken in takeSnapshot).
	// Restore does not need it.
	lockPath := filepath.Join(destDir, backupLockName)
	unlockDest, err := acquireLockWait(ctx, lockPath, backupLockWait,
		fmt.Sprintf("hay otro backup en curso en el destino %s (si no es así, borra ese archivo)", destDir))
	if err != nil {
		return "", err
	}
	defer unlockDest()
	stopDestKeepAlive := keepFileAlive(lockPath)
	defer stopDestKeepAlive()

	// Leftovers of an interrupted backup. Safe to delete: nobody else is backing up into destDir.
	if err := removeOrphanStaging(destDir); err != nil {
		return "", err
	}

	// Phase 1, under the cluster lock: copy what has to be archived to the staging directory.
	// snap can be non-nil together with an error: PostgreSQL could not be restarted, but the
	// copy is complete and the archive is still worth writing.
	handoff := false
	snap, snapErr := c.takeSnapshot(ctx, destDir, kind, pgReal, &handoff)
	if handoff {
		// The service supervisor has to start PostgreSQL; the lock is already released.
		snapErr = errors.Join(snapErr, c.awaitSupervisorStart(ctx))
	}
	if snap == nil {
		return "", snapErr
	}
	// The staging directory is always removed, whatever happens next.
	defer snap.remove()

	// Phase 2, without the lock and with PostgreSQL running again: compress from the staging copy.
	dest := filepath.Join(destDir, backupPrefix+snap.info.CreatedAt.Format(backupNameLayout)+"."+kind+backupExt)
	if _, serr := os.Lstat(dest); serr == nil {
		return "", errors.Join(snapErr, fmt.Errorf("ya existe %s: espera un segundo y repite el backup", dest))
	}
	if werr := writeBackupArchive(dest, snap.info, snap.manifest, snap.entries); werr != nil {
		return "", errors.Join(snapErr, werr)
	}
	return dest, snapErr
}

// backupSnapshot is the consistent copy of what a backup archives, taken while PostgreSQL was
// stopped: a staging directory with the files, plus the metadata of the archive.
type backupSnapshot struct {
	staging  string        // directory inside the destination that holds the copy
	info     backupInfo    // content of info.txt
	manifest string        // text of manifest.txt
	entries  []backupEntry // what to archive, with Path pointing into staging
}

// remove deletes the staging directory. It is safe to call more than once.
func (s *backupSnapshot) remove() {
	_ = os.RemoveAll(s.staging)
}

// stagingDirName returns the name of the staging directory of a backup created at createdAt.
func stagingDirName(createdAt time.Time, kind string) string {
	return "." + backupPrefix + createdAt.Format(backupNameLayout) + "." + kind + backupStagingSuffix
}

// removeOrphanStaging deletes the staging directories that an interrupted backup left in dir.
func removeOrphanStaging(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("no se pudo leer %s: %w", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() || !backupStagingRe.MatchString(e.Name()) {
			continue
		}
		orphan := filepath.Join(dir, e.Name())
		if err := os.RemoveAll(orphan); err != nil {
			return fmt.Errorf("no se pudo borrar la copia temporal huérfana %s de un backup interrumpido: %w", orphan, err)
		}
	}
	return nil
}

// takeSnapshot is phase 1 of a backup. Under the cluster lock it stops PostgreSQL (if it was
// running), copies the entries to archive into a new staging directory in destDir, restarts
// PostgreSQL and releases the lock. If it returns a snapshot, the caller owns the staging
// directory and must remove it; if it fails before the copy is complete it removes it itself.
//
// A failure to restart PostgreSQL is returned together with the (complete) snapshot.
//
// If PostgreSQL was running and the service supervisor is active (SupervisorActive), it is not
// started here: *handoff is set to true and the caller, once the lock is released, waits for the
// supervisor to start it (awaitSupervisorStart). This holds also if the copy failed.
func (c *Cluster) takeSnapshot(ctx context.Context, destDir, kind, pgReal string, handoff *bool) (snap *backupSnapshot, err error) {
	unlock, err := c.Lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	stopKeepAlive := c.keepLockAlive()
	defer stopKeepAlive()

	// Reference backup of an incremental or differential one.
	var since time.Time
	var refFiles map[string]int64 // regular files of the reference backup: path -> size
	if kind != "full" {
		ref, err := referenceBackup(destDir, kind)
		if err != nil {
			return nil, err
		}
		refManifest, err := readBackupManifest(ref.Path)
		if err != nil {
			return nil, err
		}
		since = ref.Info.CreatedAt
		refFiles = make(map[string]int64, len(refManifest))
		for _, e := range refManifest {
			if !e.Dir {
				refFiles[e.Path] = e.Size
			}
		}
	}

	wasRunning, err := c.Status(ctx)
	if err != nil {
		return nil, err
	}
	if wasRunning {
		if err := c.Stop(ctx); err != nil {
			return nil, err
		}
		// Deferred functions run in reverse order: this one runs after the staging cleanup
		// below and before the lock is released.
		defer func() {
			if c.SupervisorActive() {
				// The supervisor saw PostgreSQL stop under our lock and is waiting for it
				// to be released to start PostgreSQL again as its own child.
				*handoff = true
				return
			}
			if serr := c.startDetached(ctx); serr != nil {
				err = errors.Join(err, fmt.Errorf("no se pudo reiniciar PostgreSQL tras copiar los datos del backup: %w", serr))
			}
		}()
	}

	// From here on PostgreSQL is stopped: no file changes until the deferred restart.
	createdAt := time.Now()
	staging := filepath.Join(destDir, stagingDirName(createdAt, kind))
	if err := os.Mkdir(staging, 0o700); err != nil {
		return nil, fmt.Errorf("no se pudo crear el directorio temporal %s: %w", staging, err)
	}
	defer func() {
		if snap == nil {
			_ = os.RemoveAll(staging)
		}
	}()

	cut := time.Time{}
	if !since.IsZero() {
		cut = since.Add(-backupSinceMargin)
	}
	entries, manifest, err := collectChangedFiles(pgReal, cut, refFiles)
	if err != nil {
		return nil, err
	}
	manifestText := renderManifest(manifest)
	if len(manifestText) > manifestMaxBytes {
		return nil, fmt.Errorf("el manifiesto del clúster (%d entradas) ocupa %d bytes y supera el límite de %d: no se podría restaurar", len(manifest), len(manifestText), manifestMaxBytes)
	}
	extras, err := c.backupExtras()
	if err != nil {
		return nil, err
	}
	entries = append(entries, extras...)

	info := backupInfo{Kind: kind, CreatedAt: createdAt, Since: since}
	for _, e := range entries {
		if e.Info.Mode().IsRegular() {
			info.Files++
			info.OriginalBytes += e.Info.Size()
		}
	}
	if version, rerr := os.ReadFile(filepath.Join(pgReal, "PG_VERSION")); rerr == nil {
		info.PGVersion = strings.TrimSpace(string(version))
	}

	staged, err := copyToStaging(ctx, staging, entries)
	if err != nil {
		return nil, err
	}
	return &backupSnapshot{staging: staging, info: info, manifest: manifestText, entries: staged}, nil
}

// copyToStaging copies entries (directories and regular files) into staging, under their archive
// names, keeping the mode and modification time of each file and checking that every copy has the
// expected size. Directories are created 0700 so that the staging directory can always be
// removed. It returns the entries to archive: same names and Info as the originals, with Path
// pointing to the copies. Directories come before their contents, as collectChangedFiles returns
// them.
func copyToStaging(ctx context.Context, staging string, entries []backupEntry) ([]backupEntry, error) {
	staged := make([]backupEntry, 0, len(entries))
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		target := filepath.Join(staging, filepath.FromSlash(e.Name))
		switch {
		case e.Info.IsDir():
			if err := os.MkdirAll(target, 0o700); err != nil {
				return nil, fmt.Errorf("no se pudo crear %s: %w", target, err)
			}
		case e.Info.Mode().IsRegular():
			if err := copyStagedEntry(e, target); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%s no es un archivo regular ni un directorio: no se puede respaldar", e.Path)
		}
		staged = append(staged, backupEntry{Path: target, Name: e.Name, Info: e.Info})
	}
	return staged, nil
}

// copyStagedEntry copies the regular file e to target with the mode and modification time of e.
func copyStagedEntry(e backupEntry, target string) (err error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("no se pudo crear %s: %w", filepath.Dir(target), err)
	}
	src, err := os.Open(e.Path)
	if err != nil {
		return fmt.Errorf("no se pudo abrir %s: %w", e.Path, err)
	}
	defer src.Close()
	dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("no se pudo crear %s: %w", target, err)
	}
	want := e.Info.Size()
	// One byte more than expected, to notice a file that grew.
	n, err := io.Copy(dst, io.LimitReader(src, want+1))
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("no se pudo copiar %s: %w", e.Path, err)
	}
	if n != want {
		return fmt.Errorf("%s cambió de tamaño durante el backup (esperado %d bytes, leídos %d)", e.Path, want, n)
	}
	if err := os.Chmod(target, e.Info.Mode().Perm()); err != nil {
		return fmt.Errorf("no se pudieron fijar los permisos de %s: %w", target, err)
	}
	if err := os.Chtimes(target, e.Info.ModTime(), e.Info.ModTime()); err != nil {
		return fmt.Errorf("no se pudo fijar la fecha de %s: %w", target, err)
	}
	fi, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("no se pudo comprobar %s: %w", target, err)
	}
	if fi.Size() != want {
		return fmt.Errorf("la copia %s mide %d bytes y se esperaban %d", target, fi.Size(), want)
	}
	return nil
}

// backupExtras returns the entries for config.json (optional) and secret (required).
func (c *Cluster) backupExtras() ([]backupEntry, error) {
	files := []struct {
		path, name string
		required   bool
	}{
		{c.cfg.ConfigFile(), "config.json", false},
		{c.cfg.SecretFile(), "secret", true},
	}
	var out []backupEntry
	for _, f := range files {
		fi, err := os.Stat(f.path)
		if errors.Is(err, os.ErrNotExist) && !f.required {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("no se pudo leer %s: %w", f.path, err)
		}
		out = append(out, backupEntry{Path: f.path, Name: f.name, Info: fi})
	}
	return out, nil
}

// pathWithin reports whether p is base or lives under it (lexically).
func pathWithin(base, p string) bool {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// resolveExistingPrefix resolves the symbolic links of p even if the end of the path does not
// exist yet: it resolves the longest existing prefix and appends the rest as it is.
func resolveExistingPrefix(p string) (string, error) {
	rest := ""
	cur := p
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(resolved, rest), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// backupEntry is a file or directory to put in the archive.
type backupEntry struct {
	Path string      // location on disk
	Name string      // name inside the archive (slash separated, no trailing slash)
	Info fs.FileInfo // from Lstat
}

// manifestEntry is one line of manifest.txt: a directory or a regular file of pg/.
type manifestEntry struct {
	Path string // relative to pg/, slash separated
	Dir  bool
	Size int64 // bytes; zero for a directory
}

// renderManifest returns the text of manifest.txt for entries, which must be sorted by path.
func renderManifest(entries []manifestEntry) string {
	var b strings.Builder
	for _, e := range entries {
		kind := 'f'
		if e.Dir {
			kind = 'd'
		}
		fmt.Fprintf(&b, "%c %d %s\n", kind, e.Size, e.Path)
	}
	return b.String()
}

// parseManifest reads the text written by renderManifest. It reads at most manifestMaxBytes and
// rejects anything that renderManifest could not have produced: bad lines, paths that are not
// clean relative ones, and entries out of order or repeated.
func parseManifest(r io.Reader) ([]manifestEntry, error) {
	data, err := io.ReadAll(io.LimitReader(r, manifestMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("no se pudo leer %s: %w", backupManifestFile, err)
	}
	if len(data) > manifestMaxBytes {
		return nil, fmt.Errorf("%s supera el límite de %d bytes", backupManifestFile, manifestMaxBytes)
	}
	text := string(data)
	if text == "" {
		return nil, fmt.Errorf("%s está vacío", backupManifestFile)
	}
	if !strings.HasSuffix(text, "\n") {
		return nil, fmt.Errorf("%s está truncado: la última línea no termina en salto de línea", backupManifestFile)
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	out := make([]manifestEntry, 0, len(lines))
	for i, line := range lines {
		kind, rest, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("%s, línea %d: formato inválido", backupManifestFile, i+1)
		}
		sizeText, path, ok := strings.Cut(rest, " ")
		if !ok {
			return nil, fmt.Errorf("%s, línea %d: formato inválido", backupManifestFile, i+1)
		}
		size, err := strconv.ParseInt(sizeText, 10, 64)
		if err != nil || size < 0 {
			return nil, fmt.Errorf("%s, línea %d: tamaño inválido %q", backupManifestFile, i+1, sizeText)
		}
		e := manifestEntry{Path: path, Size: size}
		switch kind {
		case "d":
			e.Dir = true
			if size != 0 {
				return nil, fmt.Errorf("%s, línea %d: un directorio no puede tener tamaño", backupManifestFile, i+1)
			}
		case "f":
		default:
			return nil, fmt.Errorf("%s, línea %d: tipo desconocido %q", backupManifestFile, i+1, kind)
		}
		if path == "." || !fs.ValidPath(path) {
			return nil, fmt.Errorf("%s, línea %d: ruta inválida %q", backupManifestFile, i+1, path)
		}
		if len(out) > 0 && path <= out[len(out)-1].Path {
			return nil, fmt.Errorf("%s, línea %d: %q está repetida o fuera de orden", backupManifestFile, i+1, path)
		}
		out = append(out, e)
	}
	return out, nil
}

// collectChangedFiles walks pgDataDir once and returns what a backup needs from it: the entries
// to copy (every directory, whose modes matter because PostgreSQL needs pg/ to be 0700, and the
// regular files that changed; a zero sinceTime selects every file) and the manifest, which lists
// every directory (except pg/ itself) and regular file with its size regardless of sinceTime,
// sorted by path. postmaster.pid is never included. Symbolic links are rejected (tablespaces are
// not supported) and other special files are skipped.
//
// With a non-zero sinceTime a regular file has changed if its modification time is after
// sinceTime, or its path is not in reference (path -> size of the regular files of the reference
// backup), or its size differs from the one in reference (see the "Backup and restore" comment
// for why the last two exist).
func collectChangedFiles(pgDataDir string, sinceTime time.Time, reference map[string]int64) (entries []backupEntry, manifest []manifestEntry, err error) {
	err = filepath.WalkDir(pgDataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(pgDataDir, path)
		if err != nil {
			return err
		}
		name := backupPGEntry
		slashRel := ""
		if rel != "." {
			slashRel = filepath.ToSlash(rel)
			name = backupPGEntry + "/" + slashRel
		}
		fi, err := d.Info()
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if strings.Contains(slashRel, "\n") {
			return fmt.Errorf("el nombre %q contiene un salto de línea y no cabe en el manifiesto", path)
		}
		switch {
		case d.IsDir():
			entries = append(entries, backupEntry{Path: path, Name: name, Info: fi})
			if rel != "." {
				manifest = append(manifest, manifestEntry{Path: slashRel, Dir: true})
			}
		case fi.Mode().IsRegular():
			if rel == "postmaster.pid" {
				return nil
			}
			manifest = append(manifest, manifestEntry{Path: slashRel, Size: fi.Size()})
			if !sinceTime.IsZero() && !fi.ModTime().After(sinceTime) {
				if refSize, known := reference[slashRel]; known && refSize == fi.Size() {
					return nil
				}
			}
			entries = append(entries, backupEntry{Path: path, Name: name, Info: fi})
		case fi.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("el enlace simbólico %s no se puede respaldar (¿tablespaces?)", path)
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("no se pudo recorrer %s: %w", pgDataDir, err)
	}
	sort.Slice(manifest, func(i, j int) bool { return manifest[i].Path < manifest[j].Path })
	return entries, manifest, nil
}

// writeBackupArchive writes the tar.zst to destPath through a .part file renamed at the end, so
// that an interrupted backup is never mistaken for a valid one. info.txt goes first, then
// manifest.txt (manifest is its text, see renderManifest) and then the entries.
func writeBackupArchive(destPath string, info backupInfo, manifest string, entries []backupEntry) (err error) {
	tmp := destPath + ".part"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("no se pudo crear %s: %w", tmp, err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()

	zw, err := zstd.NewWriter(f)
	if err != nil {
		return fmt.Errorf("no se pudo iniciar la compresión zstd: %w", err)
	}
	// On an error path the encoder is still closed to release its resources. This runs before
	// the deferred function above (which closes f), because defers run in reverse order.
	zwClosed := false
	defer func() {
		if !zwClosed {
			_ = zw.Close()
		}
	}()
	tw := tar.NewWriter(zw)

	if err = writeTextEntry(tw, backupInfoFile, info.render(), info.CreatedAt); err != nil {
		return err
	}
	if err = writeTextEntry(tw, backupManifestFile, manifest, info.CreatedAt); err != nil {
		return err
	}
	for _, e := range entries {
		if err = addBackupEntry(tw, e); err != nil {
			return err
		}
	}
	if err = tw.Close(); err != nil {
		return fmt.Errorf("no se pudo cerrar el tar: %w", err)
	}
	zwClosed = true
	if err = zw.Close(); err != nil {
		return fmt.Errorf("no se pudo cerrar la compresión zstd: %w", err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("no se pudo sincronizar %s: %w", tmp, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("no se pudo cerrar %s: %w", tmp, err)
	}
	if err = os.Rename(tmp, destPath); err != nil {
		return fmt.Errorf("no se pudo renombrar %s a %s: %w", tmp, destPath, err)
	}
	return nil
}

// writeTextEntry writes a small text file (info.txt, manifest.txt) as the next tar entry.
func writeTextEntry(tw *tar.Writer, name, text string, modTime time.Time) error {
	hdr := &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(text)), ModTime: modTime}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("no se pudo escribir %s: %w", name, err)
	}
	if _, err := io.WriteString(tw, text); err != nil {
		return fmt.Errorf("no se pudo escribir %s: %w", name, err)
	}
	return nil
}

// addBackupEntry writes the header of e and, for a regular file, its content.
func addBackupEntry(tw *tar.Writer, e backupEntry) error {
	hdr, err := tar.FileInfoHeader(e.Info, "")
	if err != nil {
		return fmt.Errorf("no se pudo describir %s: %w", e.Path, err)
	}
	hdr.Name = e.Name
	if e.Info.IsDir() {
		hdr.Name += "/"
	}
	// Ownership does not travel: the restore creates the files as the current user.
	hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
	hdr.AccessTime, hdr.ChangeTime = time.Time{}, time.Time{}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("no se pudo escribir la cabecera de %s: %w", e.Name, err)
	}
	if !e.Info.Mode().IsRegular() {
		return nil
	}
	src, err := os.Open(e.Path)
	if err != nil {
		return fmt.Errorf("no se pudo abrir %s: %w", e.Path, err)
	}
	defer src.Close()
	n, err := io.Copy(tw, io.LimitReader(src, hdr.Size))
	if err != nil {
		return fmt.Errorf("no se pudo copiar %s: %w", e.Path, err)
	}
	if n != hdr.Size {
		return fmt.Errorf("%s cambió de tamaño durante el backup", e.Path)
	}
	return nil
}

// openBackup opens a .tar.zst; closeFn must be called when done: it closes the zstd decoder (which
// runs goroutines) and the file, and is safe to call more than once.
func openBackup(path string) (tr *tar.Reader, closeFn func(), err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	zr, err := zstd.NewReader(f)
	if err != nil {
		// NewReader can return a decoder together with the error: release it too.
		if zr != nil {
			zr.Close()
		}
		_ = f.Close()
		return nil, nil, fmt.Errorf("%s no es un archivo zstd válido: %w", filepath.Base(path), err)
	}
	return tar.NewReader(zr), func() {
		zr.Close()
		_ = f.Close()
	}, nil
}

// readBackupInfo reads the info.txt (always the first entry) of a backup.
func readBackupInfo(path string) (backupInfo, error) {
	tr, closeFn, err := openBackup(path)
	if err != nil {
		return backupInfo{}, err
	}
	defer closeFn()
	hdr, err := tr.Next()
	if err != nil {
		return backupInfo{}, fmt.Errorf("no se pudo leer %s: %w", filepath.Base(path), err)
	}
	if hdr.Name != backupInfoFile {
		return backupInfo{}, fmt.Errorf("%s no es un backup de lodan: su primera entrada no es %s", filepath.Base(path), backupInfoFile)
	}
	info, err := parseBackupInfo(tr)
	if err != nil {
		return backupInfo{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return info, nil
}

// readBackupManifest reads the manifest.txt (always the second entry, after info.txt) of a backup.
func readBackupManifest(path string) ([]manifestEntry, error) {
	tr, closeFn, err := openBackup(path)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	base := filepath.Base(path)
	for _, want := range []string{backupInfoFile, backupManifestFile} {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s no contiene %s: no es un backup válido", base, want)
		}
		if err != nil {
			return nil, fmt.Errorf("no se pudo leer %s: %w", base, err)
		}
		if hdr.Name != want {
			return nil, fmt.Errorf("%s no es un backup válido: se esperaba %s y hay %s", base, want, hdr.Name)
		}
	}
	manifest, err := parseManifest(tr)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", base, err)
	}
	return manifest, nil
}

// backupFile is a backup found in a directory.
type backupFile struct {
	Path string
	Kind string // full, incr or diff
	Info backupInfo
}

// listBackups returns the backups in dir (files named lodan_backup_*.<kind>.tar.zst), sorted from
// oldest to newest by the creation time stored in their info.txt. Directories are skipped, so the
// staging directories of a backup in progress (or interrupted) are never listed.
func listBackups(dir string) ([]backupFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []backupFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := backupNameRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := readBackupInfo(path)
		if err != nil {
			return nil, err
		}
		if info.Kind != m[1] {
			return nil, fmt.Errorf("%s: el tipo de su %s (%s) no coincide con el de su nombre (%s)", e.Name(), backupInfoFile, info.Kind, m[1])
		}
		out = append(out, backupFile{Path: path, Kind: m[1], Info: info})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Info.CreatedAt.Equal(out[j].Info.CreatedAt) {
			return out[i].Info.CreatedAt.Before(out[j].Info.CreatedAt)
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// referenceBackup returns the backup that a new backup of kind "incr" or "diff" is relative to:
// the newest full backup in dir for a differential one, the newest backup of any kind for an
// incremental one. It fails if dir has no full backup.
func referenceBackup(dir, kind string) (backupFile, error) {
	list, err := listBackups(dir)
	if err != nil {
		return backupFile{}, err
	}
	lastFull := -1
	for i, b := range list {
		if b.Kind == "full" {
			lastFull = i
		}
	}
	if lastFull < 0 {
		return backupFile{}, fmt.Errorf("no hay ningún backup full en %s: haz primero uno con `lodan db backup --full`", dir)
	}
	if kind == "diff" {
		return list[lastFull], nil
	}
	return list[len(list)-1], nil
}

// AutoDetectBackups scans dirPath for backups and returns the files to restore, in order: the
// newest full backup followed by every incremental and differential backup made after it, from
// oldest to newest. It fails if there is no full backup.
func AutoDetectBackups(dirPath string) ([]string, error) {
	list, err := listBackups(dirPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("el directorio %s no existe", dirPath)
	}
	if err != nil {
		return nil, err
	}
	lastFull := -1
	for i, b := range list {
		if b.Kind == "full" {
			lastFull = i
		}
	}
	if lastFull < 0 {
		return nil, fmt.Errorf("no hay ningún backup full (lodan_backup_*.full.tar.zst) en %s: sin él no se puede restaurar", dirPath)
	}
	paths := make([]string, 0, len(list)-lastFull)
	for _, b := range list[lastFull:] {
		paths = append(paths, b.Path)
	}
	return paths, nil
}

// validateRestoreChain checks that files is a restorable sequence and returns the info of each
// one: a full backup first and then incremental or differential backups from oldest to newest,
// each one with its reference backup earlier in the list.
func validateRestoreChain(files []string) ([]backupInfo, error) {
	infos := make([]backupInfo, len(files))
	for i, f := range files {
		base := filepath.Base(f)
		m := backupNameRe.FindStringSubmatch(base)
		if m == nil {
			return nil, fmt.Errorf("%s no parece un backup de lodan (se espera lodan_backup_AAAA-MM-DD_HHMMSS.<full|incr|diff>.tar.zst)", base)
		}
		info, err := readBackupInfo(f)
		if err != nil {
			return nil, err
		}
		if info.Kind != m[1] {
			return nil, fmt.Errorf("%s: el tipo de su %s (%s) no coincide con el de su nombre (%s)", base, backupInfoFile, info.Kind, m[1])
		}
		infos[i] = info
	}
	if infos[0].Kind != "full" {
		return nil, fmt.Errorf("el primer archivo debe ser un backup full (.full.tar.zst) y es %s: sin un full no se puede restaurar", filepath.Base(files[0]))
	}
	for i := 1; i < len(infos); i++ {
		base := filepath.Base(files[i])
		if infos[i].Kind == "full" {
			return nil, fmt.Errorf("%s es un backup full: solo el primer archivo puede serlo", base)
		}
		if infos[i].CreatedAt.Before(infos[i-1].CreatedAt) {
			return nil, fmt.Errorf("orden incorrecto: %s es anterior a %s; pásalos de más antiguo a más reciente", base, filepath.Base(files[i-1]))
		}
		found := false
		for j := 0; j < i; j++ {
			if infos[j].CreatedAt.Equal(infos[i].Since) {
				found = true
				break
			}
		}
		if !found {
			// The reference may be in the list but later: then the order is wrong.
			for k := i + 1; k < len(infos); k++ {
				if infos[k].CreatedAt.Equal(infos[i].Since) {
					return nil, fmt.Errorf("orden incorrecto: %s parte de %s, que aparece más adelante en la lista; pásalos de más antiguo a más reciente", base, filepath.Base(files[k]))
				}
			}
			return nil, fmt.Errorf("%s parte de un backup (%s) que no está en la lista: falta un archivo en la cadena", base, infos[i].Since.Format(time.RFC3339))
		}
	}
	return infos, nil
}

// Restore replaces the cluster with the one stored in files: a full backup followed by the
// incremental and differential ones, from oldest to newest (see validateRestoreChain). config.json
// and secret are taken from the full backup. The cluster does not need to exist.
//
// The backups are extracted to a temporary directory and swapped in only if they are complete:
// whatever the manifest of the newest backup does not list is deleted from it (files that were
// deleted since the full backup), and every entry of that manifest must be there with the same
// type and size, or the restore is aborted before touching the current cluster (see
// stageBackups). The cluster that was there is kept as pg.pre-restore-<date> next to it and its path is
// returned (empty if there was none): the caller decides when to delete it. If PostgreSQL does
// not start or accept connections after the swap, the previous cluster, secret and config.json
// are put back and an error is returned. PostgreSQL is left running.
//
// With the service supervisor active (SupervisorActive when the restore starts), PostgreSQL stays
// under its management: the restore starts its own PostgreSQL and verifies it as above (still
// under the lock), then stops it, releases the lock and waits (up to restartTimeout) for the
// supervisor to start it again as its child. If it does not, Restore starts it itself and returns
// an error saying so. The same hand-over applies when a failed restore went back to the previous
// state with PostgreSQL having been running.
func (c *Cluster) Restore(ctx context.Context, files []string) (previous string, err error) {
	if len(files) == 0 {
		return "", errors.New("no se ha indicado ningún backup que restaurar")
	}
	if _, err := validateRestoreChain(files); err != nil {
		return "", err
	}
	handoff := false
	previous, err = c.restoreLocked(ctx, files, &handoff)
	if handoff {
		// The lock is already released.
		err = errors.Join(err, c.awaitSupervisorStart(ctx))
	}
	return previous, err
}

// restoreLocked is Restore under the cluster lock, which it releases when it returns. It sets
// *handoff when PostgreSQL has been left stopped for the service supervisor to start.
func (c *Cluster) restoreLocked(ctx context.Context, files []string, handoff *bool) (previous string, err error) {
	unlock, err := c.Lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	stopKeepAlive := c.keepLockAlive()
	defer stopKeepAlive()

	supervised := c.SupervisorActive()
	wasRunning, err := c.Status(ctx)
	if err != nil {
		return "", err
	}
	if wasRunning {
		if err := c.Stop(ctx); err != nil {
			return "", err
		}
	}
	succeeded := false
	defer func() {
		// Any failure leaves things as they were, including a PostgreSQL that was running.
		if !succeeded && wasRunning {
			if supervised {
				*handoff = true
				return
			}
			if serr := c.startDetached(ctx); serr != nil {
				err = errors.Join(err, fmt.Errorf("no se pudo reiniciar PostgreSQL: %w", serr))
			}
		}
	}()

	// The staging directory lives in DataDir so the final swap is a rename within one file system.
	staging, err := os.MkdirTemp(c.cfg.DataDir, ".restore-")
	if err != nil {
		return "", fmt.Errorf("no se pudo crear el directorio temporal en %s: %w", c.cfg.DataDir, err)
	}
	defer os.RemoveAll(staging)

	if err := stageBackups(files, staging); err != nil {
		return "", err
	}
	stagedPG := filepath.Join(staging, backupPGEntry)
	if _, err := os.Stat(filepath.Join(stagedPG, "PG_VERSION")); err != nil {
		return "", errors.New("los backups no contienen un clúster de PostgreSQL válido (falta pg/PG_VERSION)")
	}
	if _, err := os.Stat(filepath.Join(staging, "secret")); err != nil {
		return "", errors.New("el backup full no contiene el archivo secret")
	}
	// PostgreSQL refuses to start if its data directory is accessible to other users.
	if err := os.Chmod(stagedPG, 0o700); err != nil {
		return "", fmt.Errorf("no se pudieron fijar los permisos de %s: %w", stagedPG, err)
	}

	// Keep what is going to be replaced, to be able to go back.
	pgDir := c.cfg.PGDataDir()
	oldSecret, secretExisted, err := readOptional(c.cfg.SecretFile())
	if err != nil {
		return "", err
	}
	oldConfig, configExisted, err := readOptional(c.cfg.ConfigFile())
	if err != nil {
		return "", err
	}
	if _, serr := os.Lstat(pgDir); serr == nil {
		previous = pgDir + ".pre-restore-" + time.Now().Format("20060102-150405")
		if err := os.Rename(pgDir, previous); err != nil {
			return "", fmt.Errorf("no se pudo apartar el clúster actual a %s: %w", previous, err)
		}
	} else if !errors.Is(serr, os.ErrNotExist) {
		return "", fmt.Errorf("no se pudo comprobar %s: %w", pgDir, serr)
	}

	// rollback puts the previous state back; it returns the problems it found doing so.
	rollback := func() error {
		var errs []error
		if err := os.RemoveAll(pgDir); err != nil {
			errs = append(errs, err)
		}
		if previous != "" {
			if err := os.Rename(previous, pgDir); err != nil {
				errs = append(errs, fmt.Errorf("no se pudo devolver %s a %s: %w", previous, pgDir, err))
			}
		}
		errs = append(errs, restoreOptional(c.cfg.SecretFile(), oldSecret, secretExisted, 0o600))
		errs = append(errs, restoreOptional(c.cfg.ConfigFile(), oldConfig, configExisted, 0o600))
		return errors.Join(errs...)
	}

	err = os.Rename(stagedPG, pgDir)
	if err == nil {
		err = installFile(filepath.Join(staging, "secret"), c.cfg.SecretFile())
	}
	if err == nil {
		if _, serr := os.Stat(filepath.Join(staging, "config.json")); serr == nil {
			err = installFile(filepath.Join(staging, "config.json"), c.cfg.ConfigFile())
		}
	}
	if err == nil {
		if err = c.Start(ctx); err == nil {
			err = c.verifyConnection(ctx)
		}
	}
	if err != nil {
		bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restartTimeout)
		defer cancel()
		_ = c.Stop(bctx)
		if rerr := rollback(); rerr != nil {
			return "", fmt.Errorf("la restauración falló (%w) y además no se pudo devolver el estado anterior: %v; el clúster anterior está en %s", err, rerr, previous)
		}
		return "", fmt.Errorf("la restauración falló y se ha vuelto al estado anterior: %w (log de PostgreSQL en %s)", err, filepath.Join(c.cfg.LogsDir(), "postgres.log"))
	}
	succeeded = true
	if supervised {
		// Verified: PostgreSQL goes back to the supervisor, which starts it as its child once
		// the lock is released (Restore waits for that).
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restartTimeout)
		defer cancel()
		if serr := c.Stop(sctx); serr != nil {
			return previous, fmt.Errorf("la restauración terminó bien, pero no se pudo parar PostgreSQL para devolvérselo al servicio de lodan: %w", serr)
		}
		*handoff = true
	}
	return previous, nil
}

// verifyConnection checks that the restored cluster accepts connections with the restored secret
// and has lodan's database.
func (c *Cluster) verifyConnection(ctx context.Context) error {
	dsn, err := c.DSN(defaultDatabase)
	if err != nil {
		return err
	}
	vctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	conn, err := pgx.Connect(vctx, dsn)
	if err != nil {
		return fmt.Errorf("PostgreSQL arrancó pero no acepta conexiones a la base %q: %w", defaultDatabase, err)
	}
	defer conn.Close(context.WithoutCancel(ctx))
	var one int
	if err := conn.QueryRow(vctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Errorf("PostgreSQL arrancó pero no responde consultas: %w", err)
	}
	return nil
}

// stageBackups unpacks the chain files (already validated) into staging, from oldest to newest,
// and then makes pg/ match the manifest of the last backup (see applyManifest).
func stageBackups(files []string, staging string) error {
	// The manifest is read first: a backup without a valid one fails before extracting anything.
	last := files[len(files)-1]
	manifest, err := readBackupManifest(last)
	if err != nil {
		return err
	}
	for i, f := range files {
		if err := extractBackup(f, staging, i == 0); err != nil {
			return fmt.Errorf("no se pudo extraer %s: %w", filepath.Base(f), err)
		}
	}
	stagedPG := filepath.Join(staging, backupPGEntry)
	if _, err := os.Stat(stagedPG); err != nil {
		return errors.New("los backups no contienen un clúster de PostgreSQL válido (falta pg/)")
	}
	if err := applyManifest(stagedPG, manifest); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(last), err)
	}
	return nil
}

// applyManifest makes the extracted cluster stagedPG match manifest: it deletes every file and
// directory the manifest does not list and then checks that every entry of the manifest exists
// with the same type and, for files, the same size. If something is missing or does not match it
// returns an error describing the first problems; stagedPG is a temporary copy, so deleting from
// it is harmless.
func applyManifest(stagedPG string, manifest []manifestEntry) error {
	want := make(map[string]manifestEntry, len(manifest))
	for _, e := range manifest {
		want[e.Path] = e
	}

	err := filepath.WalkDir(stagedPG, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(stagedPG, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if _, ok := want[filepath.ToSlash(rel)]; ok {
			return nil
		}
		if d.IsDir() {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			return filepath.SkipDir
		}
		return os.Remove(path)
	})
	if err != nil {
		return fmt.Errorf("no se pudo depurar el clúster extraído: %w", err)
	}

	const maxReported = 5
	problems := 0
	var first []string
	report := func(format string, args ...any) {
		problems++
		if len(first) < maxReported {
			first = append(first, fmt.Sprintf(format, args...))
		}
	}
	for _, e := range manifest {
		fi, err := os.Lstat(filepath.Join(stagedPG, filepath.FromSlash(e.Path)))
		switch {
		case errors.Is(err, os.ErrNotExist):
			report("falta %s", e.Path)
		case err != nil:
			return fmt.Errorf("no se pudo comprobar %s: %w", e.Path, err)
		case e.Dir && !fi.IsDir():
			report("%s debería ser un directorio", e.Path)
		case !e.Dir && !fi.Mode().IsRegular():
			report("%s debería ser un archivo", e.Path)
		case !e.Dir && fi.Size() != e.Size:
			report("%s mide %d bytes y el manifiesto dice %d", e.Path, fi.Size(), e.Size)
		}
	}
	if problems > 0 {
		more := ""
		if problems > len(first) {
			more = fmt.Sprintf(" (y %d más)", problems-len(first))
		}
		return fmt.Errorf("los backups no cuadran con su manifiesto (%s), la cadena está incompleta o dañada: %s%s", backupManifestFile, strings.Join(first, "; "), more)
	}
	return nil
}

// extractBackup unpacks one backup into staging (pg/ and, if withExtras, config.json and secret).
// Files that already exist are replaced, which is how incremental and differential backups are
// merged over the full one. Modes and modification times are kept.
func extractBackup(archive, staging string, withExtras bool) error {
	tr, closeFn, err := openBackup(archive)
	if err != nil {
		return err
	}
	defer closeFn()
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("archivo dañado: %w", err)
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		for _, part := range strings.Split(name, "/") {
			if part == ".." || part == "" {
				return fmt.Errorf("entrada insegura en el backup: %q", hdr.Name)
			}
		}
		switch {
		case name == backupInfoFile || name == backupManifestFile:
			continue
		case name == "config.json" || name == "secret":
			if !withExtras {
				continue
			}
		case name == backupPGEntry || strings.HasPrefix(name, backupPGEntry+"/"):
		default:
			return fmt.Errorf("entrada inesperada en el backup: %q", hdr.Name)
		}

		target := filepath.Join(staging, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			if err := os.Chmod(target, hdr.FileInfo().Mode().Perm()); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeStagedFile(target, tr, hdr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("tipo de entrada no soportado en el backup: %q", hdr.Name)
		}
	}
}

// writeStagedFile replaces target with the content of r, with the mode and time of hdr.
func writeStagedFile(target string, r io.Reader, hdr *tar.Header) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return fmt.Errorf("no se pudo extraer %s: %w", target, err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(target, hdr.FileInfo().Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(target, hdr.ModTime, hdr.ModTime)
}

// readOptional reads a small file; existed is false if it is not there.
func readOptional(path string) (data []byte, existed bool, err error) {
	data, err = os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("no se pudo leer %s: %w", path, err)
	}
	return data, true, nil
}

// restoreOptional puts a file back as readOptional found it: rewritten if it existed, removed
// if it did not.
func restoreOptional(path string, data []byte, existed bool, perm fs.FileMode) error {
	if !existed {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeFileAtomic(path, data, perm)
}

// installFile copies src over dst atomically, keeping the mode of src.
func installFile(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeFileAtomic(dst, data, fi.Mode().Perm())
}

// writeFileAtomic writes data to a temporary file next to path and renames it over path.
func writeFileAtomic(path string, data []byte, perm fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".restore-file-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("no se pudo escribir %s: %w", path, err)
	}
	if err := os.Chmod(name, perm); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
