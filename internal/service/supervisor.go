package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"lodan/internal/config"
	"lodan/internal/database"
	"lodan/internal/embedding"
	"lodan/internal/mcptools"
	"lodan/internal/memory"
	"lodan/internal/session"
	"lodan/internal/topic"
)

const (
	// databaseName is the PostgreSQL database lodan stores its data in.
	databaseName = "lodan"
	// maintenanceInterval is how often pending embeddings are filled and idle sessions closed.
	maintenanceInterval = 30 * time.Second
	// pgReadyTimeout bounds the wait for PostgreSQL to accept connections after launching it.
	pgReadyTimeout = 60 * time.Second
	// pgReadyRetry is the pause between two connection attempts while PostgreSQL starts.
	pgReadyRetry = 250 * time.Millisecond
	// pgStopTimeout bounds the fast shutdown of the PostgreSQL launched by the supervisor.
	pgStopTimeout = 30 * time.Second
	// warmUpAttempts is how many times the model warm-up is tried: at boot Ollama may not be up yet.
	warmUpAttempts = 10
	// warmUpTimeout bounds each warm-up attempt (loading the model can be slow on CPU).
	warmUpTimeout = 2 * time.Minute
)

// Variables (not constants) so the tests can shorten them.
var (
	// pgWatchInterval is how often a PostgreSQL that the supervisor did not launch is checked.
	pgWatchInterval = 30 * time.Second
	// warmUpRetry is the pause between two warm-up attempts.
	warmUpRetry = 30 * time.Second
)

// RunSupervisor is the body of the system service (`lodan service run`). It prepares the
// cluster, keeps PostgreSQL running in the foreground as a child process (systemd, launchd
// and the Windows SCM need a process that does not daemonize), applies the migrations and runs
// the background maintenance (pending embeddings, idle sessions) until ctx is cancelled.
//
// On cancellation it stops the PostgreSQL it launched (fast shutdown) and returns nil. If that
// PostgreSQL exits on its own it returns an error, so the service manager restarts the service.
// If PostgreSQL was already running on the same data directory (started with `lodan db start`
// or by the lazy start of `lodan serve`), no second one is launched: only the maintenance runs,
// and the state is checked every 30 seconds.
func RunSupervisor(ctx context.Context, cfg config.Config, logger *log.Logger) error {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	cluster, err := database.NewCluster(cfg)
	if err != nil {
		return err
	}

	pg, err := prepareCluster(ctx, cfg, cluster, logger)
	if err == nil {
		err = superviseStack(ctx, cfg, cluster, pg, logger)
	}
	// An error that arrives once ctx is done is the shutdown itself (commands killed, waits
	// interrupted), not a failure.
	cancelled := err != nil && ctx.Err() != nil
	if cancelled {
		err = nil
	}

	if pg != nil {
		if stopErr := pg.stop(cluster, cfg, pgStopTimeout, logger); stopErr != nil {
			err = errors.Join(err, stopErr)
		}
	}
	return err
}

// prepareCluster initializes the cluster if needed, launches PostgreSQL in the foreground
// unless it is already running, waits until it accepts connections and creates the database.
// It returns the process it launched, or nil if PostgreSQL was already running. The process is
// returned even on error so the caller can stop it. A lockfile serializes the whole step
// against the lazy start of `lodan serve`.
func prepareCluster(ctx context.Context, cfg config.Config, cluster *database.Cluster, logger *log.Logger) (*postgresProc, error) {
	unlock, err := cluster.Lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()

	if err := cluster.Init(ctx); err != nil {
		return nil, err
	}
	running, err := cluster.Status(ctx)
	if err != nil {
		return nil, err
	}

	var pg *postgresProc
	if running {
		logger.Printf("PostgreSQL ya estaba en marcha sobre %s: no se lanza otro, se continúa solo con el mantenimiento", cfg.PGDataDir())
	} else {
		if pg, err = startPostgres(cluster, cfg); err != nil {
			return nil, err
		}
		logger.Printf("PostgreSQL arrancado en primer plano (pid %d); log en %s", pg.cmd.Process.Pid, pg.logPath)
	}

	if err := waitReady(ctx, cluster, pg, pgReadyTimeout); err != nil {
		return pg, err
	}
	if err := cluster.EnsureDatabase(ctx, databaseName); err != nil {
		return pg, err
	}
	return pg, nil
}

// superviseStack migrates the database, starts the maintenance and the model warm-up and blocks
// until ctx is cancelled (nil) or PostgreSQL goes away (error).
func superviseStack(ctx context.Context, cfg config.Config, cluster *database.Cluster, pg *postgresProc, logger *log.Logger) error {
	dsn, err := cluster.DSN(databaseName)
	if err != nil {
		return err
	}
	pool, err := database.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool, database.Settings{Dims: cfg.EmbedDims, Model: cfg.EmbedModel}); err != nil {
		return err
	}

	emb, err := embedding.NewOllama(embedding.Options{
		BaseURL:   cfg.OllamaURL,
		Model:     cfg.EmbedModel,
		Dims:      cfg.EmbedDims,
		KeepAlive: cfg.EmbedKeepAlive,
	})
	if err != nil {
		return err
	}
	topics := topic.NewResolver(pool, emb, cfg.TopicSimilarity)
	sessions := session.NewManager(pool, time.Duration(cfg.SessionIdleMinutes)*time.Minute)
	deps := mcptools.Deps{
		Pool:     pool,
		Memory:   memory.NewService(pool, emb, topics, sessions, cfg.DupSimilarity, time.Duration(cfg.EmbedTimeoutMs)*time.Millisecond),
		Sessions: sessions,
		Topics:   topics,
		Embedder: emb,
		Cfg:      cfg,
	}

	// The background goroutines stop, and are awaited, before the pool is closed (defers run
	// in reverse order).
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	wg.Add(2)
	go func() {
		defer wg.Done()
		mcptools.NewMaintenance(deps, logger).Run(runCtx, maintenanceInterval)
	}()
	go func() {
		defer wg.Done()
		warmUp(runCtx, emb, logger)
	}()

	logger.Printf("servicio listo: base de datos %q migrada, mantenimiento cada %s", databaseName, maintenanceInterval)
	return watch(ctx, cluster, pg, logger)
}

// watch blocks until ctx is done (nil) or PostgreSQL stops (error). For the process launched
// by the supervisor it waits for it to exit; for an external one it polls its status.
func watch(ctx context.Context, cluster *database.Cluster, pg *postgresProc, logger *log.Logger) error {
	if pg != nil {
		select {
		case <-ctx.Done():
			return nil
		case <-pg.done:
			return fmt.Errorf("PostgreSQL terminó por su cuenta (%v): revisa %s", pg.err, pg.logPath)
		}
	}

	ticker := time.NewTicker(pgWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			running, err := cluster.Status(ctx)
			if err != nil {
				if ctx.Err() == nil {
					logger.Printf("no se pudo consultar el estado de PostgreSQL: %v", err)
				}
				continue
			}
			if !running {
				return errors.New("el PostgreSQL externo se ha parado: el gestor de servicios reiniciará el servicio, que lo lanzará en primer plano")
			}
		}
	}
}

// warmUp loads the embedding model in Ollama (keep_alive = -1 keeps it loaded). At boot Ollama
// may not be listening yet, so it retries a few times; failures are not fatal.
func warmUp(ctx context.Context, emb *embedding.Ollama, logger *log.Logger) {
	for attempt := 1; attempt <= warmUpAttempts; attempt++ {
		wctx, cancel := context.WithTimeout(ctx, warmUpTimeout)
		_, err := emb.EmbedQuery(wctx, "calentamiento")
		cancel()
		if err == nil {
			logger.Printf("modelo de embeddings %s precalentado", emb.Model())
			return
		}
		if attempt == warmUpAttempts {
			logger.Printf("no se pudo precalentar el modelo de embeddings tras %d intentos: %v", attempt, err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(warmUpRetry):
		}
	}
}

// postgresProc is the PostgreSQL process launched in the foreground by the supervisor.
type postgresProc struct {
	cmd     *exec.Cmd
	logPath string
	// done is closed when the process has exited; err (its Wait result) is valid after that.
	done chan struct{}
	err  error
}

// startPostgres launches `postgres -D <data>` without pg_ctl, so it stays a child of the
// supervisor. Its output is appended to <LogsDir>/postgres.log.
func startPostgres(cluster *database.Cluster, cfg config.Config) (*postgresProc, error) {
	if err := os.MkdirAll(cfg.LogsDir(), 0o700); err != nil {
		return nil, fmt.Errorf("no se pudo crear el directorio de logs %s: %w", cfg.LogsDir(), err)
	}
	logPath := filepath.Join(cfg.LogsDir(), "postgres.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("no se pudo abrir %s: %w", logPath, err)
	}

	// Not CommandContext: cancelling ctx must trigger a clean shutdown, not a kill.
	cmd := exec.Command(cluster.Exe("postgres"), "-D", cfg.PGDataDir())
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("no se pudo lanzar postgres: %w", err)
	}

	p := &postgresProc{cmd: cmd, logPath: logPath, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		_ = logFile.Close()
		close(p.done)
	}()
	return p, nil
}

// stop asks PostgreSQL for a fast shutdown and waits for it to exit. If it does not exit within
// timeout, it is killed and an error is returned. It does nothing if the process already exited.
func (p *postgresProc) stop(cluster *database.Cluster, cfg config.Config, timeout time.Duration, logger *log.Logger) error {
	select {
	case <-p.done:
		return nil
	default:
	}

	logger.Printf("parando PostgreSQL (parada rápida)")
	deadline := time.Now().Add(timeout)
	if runtime.GOOS == "windows" {
		// Windows has no signals: pg_ctl talks to the postmaster through its own channel.
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		out, err := exec.CommandContext(ctx, cluster.Exe("pg_ctl"), "stop", "-m", "fast", "-D", cfg.PGDataDir()).CombinedOutput()
		cancel()
		if err != nil {
			logger.Printf("pg_ctl stop falló: %v\n%s", err, out)
		}
	} else if err := p.cmd.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		// SIGINT is PostgreSQL's fast shutdown.
		logger.Printf("no se pudo enviar SIGINT a postgres: %v", err)
	}

	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-timer.C:
	}
	_ = p.cmd.Process.Kill()
	<-p.done
	return fmt.Errorf("PostgreSQL no paró en %s: se forzó su cierre", timeout)
}

// waitReady retries a connection to the "postgres" database until it succeeds, timeout expires,
// ctx is cancelled or the process launched by the supervisor (pg, may be nil) exits.
func waitReady(ctx context.Context, cluster *database.Cluster, pg *postgresProc, timeout time.Duration) error {
	dsn, err := cluster.DSN("postgres")
	if err != nil {
		return err
	}
	var exited <-chan struct{} // nil (blocks forever) when PostgreSQL is not ours
	if pg != nil {
		exited = pg.done
	}

	deadline := time.Now().Add(timeout)
	for {
		err := ping(ctx, dsn)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("PostgreSQL no aceptó conexiones en %s: %w", timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-exited:
			return fmt.Errorf("PostgreSQL terminó durante el arranque (%v): consulta %s", pg.err, pg.logPath)
		case <-time.After(pgReadyRetry):
		}
	}
}

// ping opens and closes one connection to dsn.
func ping(ctx context.Context, dsn string) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(cctx, dsn)
	if err != nil {
		return err
	}
	closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer closeCancel()
	return conn.Close(closeCtx)
}
