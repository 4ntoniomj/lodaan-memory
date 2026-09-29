package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"lodan/internal/config"
	"lodan/internal/database"
	"lodan/internal/embedding"
	"lodan/internal/mcptools"
	"lodan/internal/memory"
	"lodan/internal/recall"
	"lodan/internal/session"
	"lodan/internal/topic"
)

// databaseName is the PostgreSQL database lodan stores its data in.
const databaseName = "lodan"

// signalContext returns a context cancelled by SIGINT or SIGTERM.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// openStack starts the local PostgreSQL cluster if needed, applies the
// migrations and builds every service. The returned cleanup closes the pool
// and must be called once. Logs go to logger (stderr), never to stdout.
func openStack(ctx context.Context, cfg config.Config, logger *log.Logger) (mcptools.Deps, func(), error) {
	cluster, err := database.NewCluster(cfg)
	if err != nil {
		return mcptools.Deps{}, nil, err
	}
	if err := cluster.EnsureRunning(ctx); err != nil {
		return mcptools.Deps{}, nil, err
	}
	dsn, err := cluster.DSN(databaseName)
	if err != nil {
		return mcptools.Deps{}, nil, err
	}
	pool, err := database.Open(ctx, dsn)
	if err != nil {
		return mcptools.Deps{}, nil, err
	}
	if err := database.Migrate(ctx, pool, database.Settings{Dims: cfg.EmbedDims, Model: cfg.EmbedModel}); err != nil {
		pool.Close()
		return mcptools.Deps{}, nil, err
	}

	emb, err := embedding.NewOllama(embedding.Options{
		BaseURL:   cfg.OllamaURL,
		Model:     cfg.EmbedModel,
		Dims:      cfg.EmbedDims,
		KeepAlive: cfg.EmbedKeepAlive,
	})
	if err != nil {
		pool.Close()
		return mcptools.Deps{}, nil, err
	}

	topics := topic.NewResolver(pool, emb, cfg.TopicSimilarity)
	if err := topics.Load(ctx); err != nil {
		logger.Printf("no se pudo cargar la caché de temas (se recargará más tarde): %v", err)
	}
	sessions := session.NewManager(pool, time.Duration(cfg.SessionIdleMinutes)*time.Minute)
	deps := mcptools.Deps{
		Pool:     pool,
		Memory:   memory.NewService(pool, emb, topics, sessions, cfg.DupSimilarity, time.Duration(cfg.EmbedTimeoutMs)*time.Millisecond),
		Recall:   recall.NewService(pool, emb, topics, recall.Options{MaxBytes: cfg.RecallMaxBytes, TopicDetectThreshold: cfg.TopicDetectSimilarity}),
		Sessions: sessions,
		Topics:   topics,
		Embedder: emb,
		Cfg:      cfg,
		Version:  version,
	}
	return deps, pool.Close, nil
}

// runDB implements `lodan db init|start|stop|status`.
func runDB(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("db", "lodan db init|start|stop|status", stderr)
	if code, done := parseFlags(fs, args); done {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "lodan db: falta la acción (init|start|stop|status)")
		return 2
	}
	action := fs.Arg(0)
	switch action {
	case "init", "start", "stop", "status":
	default:
		fmt.Fprintf(stderr, "lodan db: acción desconocida %q (usa init|start|stop|status)\n", action)
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "lodan db: %v\n", err)
		return 1
	}
	cluster, err := database.NewCluster(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "lodan db: %v\n", err)
		return 1
	}
	ctx, stop := signalContext()
	defer stop()

	switch action {
	case "init":
		err = cluster.EnsureRunning(ctx)
		if err == nil {
			fmt.Fprintf(stdout, "PostgreSQL listo: en marcha en el puerto %d, datos en %s\n", cfg.PGPort, cfg.DataDir)
		}
	case "start":
		err = cluster.Start(ctx)
		if err == nil {
			fmt.Fprintln(stdout, "PostgreSQL en marcha")
		}
	case "stop":
		err = cluster.Stop(ctx)
		if err == nil {
			fmt.Fprintln(stdout, "PostgreSQL parado")
		}
	case "status":
		var running bool
		if running, err = cluster.Status(ctx); err == nil {
			state := "parado"
			if running {
				state = "en marcha"
			} else if !cluster.Initialized() {
				state = "parado (sin inicializar)"
			}
			fmt.Fprintf(stdout, "PostgreSQL: %s\nDirectorio de datos: %s\nPuerto: %d\n", state, cfg.DataDir, cfg.PGPort)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "lodan db %s: %v\n", action, err)
		return 1
	}
	return 0
}

// runMigrate implements `lodan migrate`.
func runMigrate(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("migrate", "lodan migrate", stderr)
	if code, done := parseFlags(fs, args); done {
		return code
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "lodan migrate: %v\n", err)
		return 1
	}
	ctx, stop := signalContext()
	defer stop()

	_, cleanup, err := openStack(ctx, cfg, log.New(stderr, "lodan: ", log.LstdFlags))
	if err != nil {
		fmt.Fprintf(stderr, "lodan migrate: %v\n", err)
		return 1
	}
	defer cleanup()
	fmt.Fprintln(stdout, "migraciones al día")
	return 0
}

// runStatus implements `lodan status`.
func runStatus(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("status", "lodan status", stderr)
	if code, done := parseFlags(fs, args); done {
		return code
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "lodan status: %v\n", err)
		return 1
	}
	ctx, stop := signalContext()
	defer stop()

	deps, cleanup, err := openStack(ctx, cfg, log.New(stderr, "lodan: ", log.LstdFlags))
	if err != nil {
		fmt.Fprintf(stderr, "lodan status: %v\n", err)
		return 1
	}
	defer cleanup()
	fmt.Fprintln(stdout, mcptools.StatusLine(ctx, deps))
	return 0
}
