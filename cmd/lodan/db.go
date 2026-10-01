package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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

// runDB implements `lodan db init|start|stop|status|backup|restore`.
func runDB(args []string, stdout, stderr io.Writer) int {
	// backup and restore take their own flags after the action, which the generic flag set
	// below (flags before the action) cannot parse.
	if len(args) > 0 {
		switch args[0] {
		case "backup":
			return runDBBackup(args[1:], stdout, stderr)
		case "restore":
			return runDBRestore(args[1:], stdout, stderr)
		}
	}

	fs := newFlagSet("db", "lodan db init|start|stop|status|backup|restore", stderr)
	if code, done := parseFlags(fs, args); done {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "lodan db: falta la acción (init|start|stop|status|backup|restore)")
		return 2
	}
	action := fs.Arg(0)
	switch action {
	case "init", "start", "stop", "status":
	default:
		fmt.Fprintf(stderr, "lodan db: acción desconocida %q (usa init|start|stop|status|backup|restore)\n", action)
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

// runDBBackup implements `lodan db backup [--to destino] [--full|--incremental|--differential]`.
func runDBBackup(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("db backup", "lodan db backup [--to destino] [--full|--incremental|--differential]", stderr)
	to := fs.String("to", os.TempDir(), "directorio donde se crea el backup (por defecto el temporal del sistema, /tmp en Linux)")
	full := fs.Bool("full", false, "backup completo (es el tipo por defecto)")
	incr := fs.Bool("incremental", false, "solo los cambios desde el último backup full del destino")
	diff := fs.Bool("differential", false, "solo los cambios desde el último backup full o differential del destino")
	if code, done := parseFlags(fs, args); done {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "lodan db backup: argumentos inesperados: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}

	backupType := database.BackupFull
	chosen := 0
	if *full {
		chosen++
	}
	if *incr {
		chosen++
		backupType = database.BackupIncremental
	}
	if *diff {
		chosen++
		backupType = database.BackupDifferential
	}
	if chosen > 1 {
		fmt.Fprintln(stderr, "lodan db backup: usa solo una de --full, --incremental o --differential")
		return 2
	}

	dest, err := filepath.Abs(*to)
	if err != nil {
		fmt.Fprintf(stderr, "lodan db backup: ruta de destino inválida: %v\n", err)
		return 1
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "lodan db backup: %v\n", err)
		return 1
	}
	cluster, err := database.NewCluster(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "lodan db backup: %v\n", err)
		return 1
	}
	ctx, stop := signalContext()
	defer stop()

	path, err := cluster.Backup(ctx, dest, backupType)
	if path != "" {
		// The archive exists even if PostgreSQL could not be restarted afterwards.
		fmt.Fprintf(stdout, "Backup creado: %s\n", path)
	}
	if err != nil {
		fmt.Fprintf(stderr, "lodan db backup: %v\n", err)
		return 1
	}
	return 0
}

// pathList is a flag.Value that collects every occurrence of a repeated flag.
type pathList []string

func (p *pathList) String() string { return strings.Join(*p, ", ") }

func (p *pathList) Set(v string) error {
	*p = append(*p, v)
	return nil
}

// runDBRestore implements `lodan db restore --from archivo1.tar.xz [archivo2.tar.xz ...]` and
// `lodan db restore --from /carpeta`.
func runDBRestore(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("db restore", "lodan db restore --from archivo1.tar.xz [archivo2.tar.xz ...] | --from /carpeta", stderr)
	var from pathList
	fs.Var(&from, "from", "backup .tar.xz (pueden seguir más archivos, del más antiguo al más reciente) o carpeta: se restaura el último full y los backups posteriores")
	if code, done := parseFlags(fs, args); done {
		return code
	}
	inputs := append([]string(from), fs.Args()...)
	if len(inputs) == 0 {
		fmt.Fprintln(stderr, "lodan db restore: falta --from (un archivo .tar.xz, varios o una carpeta)")
		return 2
	}

	dirs := 0
	for i, in := range inputs {
		abs, err := filepath.Abs(in)
		if err != nil {
			fmt.Fprintf(stderr, "lodan db restore: ruta inválida %q: %v\n", in, err)
			return 1
		}
		st, err := os.Stat(abs)
		if err != nil {
			fmt.Fprintf(stderr, "lodan db restore: %v\n", err)
			return 1
		}
		if st.IsDir() {
			dirs++
		}
		inputs[i] = abs
	}

	files := inputs
	switch {
	case dirs > 0 && len(inputs) > 1:
		fmt.Fprintln(stderr, "lodan db restore: --from acepta una carpeta o una lista de archivos, no ambas cosas")
		return 2
	case dirs == 1:
		detected, err := database.AutoDetectBackups(inputs[0])
		if err != nil {
			fmt.Fprintf(stderr, "lodan db restore: %v\n", err)
			return 1
		}
		files = detected
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "lodan db restore: %v\n", err)
		return 1
	}
	cluster, err := database.NewCluster(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "lodan db restore: %v\n", err)
		return 1
	}
	ctx, stop := signalContext()
	defer stop()

	previous, err := cluster.Restore(ctx, files)
	if err != nil {
		fmt.Fprintf(stderr, "lodan db restore: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Restauración completada desde: %s\n", strings.Join(files, ", "))
	if previous != "" {
		fmt.Fprintf(stdout, "El clúster anterior se ha conservado en %s: bórralo cuando hayas comprobado la restauración\n", previous)
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
