package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"lodan/internal/config"
	"lodan/internal/gitsync"
	"lodan/internal/install"
)

const syncUsage = `Uso:
  lodan sync init --repo <url> [--key <fichero>] [--machine <id>]
      Prepara esta máquina: clona el repositorio en <datos>/sync-repo y crea la clave si no existe.
  lodan sync push
      Exporta los cambios locales cifrados, los confirma y los sube al repositorio.
  lodan sync pull
      Baja los cambios de las otras máquinas y los importa.
  lodan sync run
      Hace push y después pull (lo que ejecuta la tarea programada).
  lodan sync schedule [--every <minutos>]
      Crea una tarea de tu usuario que ejecuta "lodan sync run" cada <minutos> (por defecto 15;
      de 5 a 1440): Programador de tareas en Windows, timer de systemd --user en Linux y
      LaunchAgent en macOS. Usa tus credenciales de git.
  lodan sync unschedule
      Quita esa tarea.
`

// defaultSyncEvery is the default interval of `lodan sync schedule`, in minutes.
const defaultSyncEvery = 15

// syncTimeout bounds `lodan sync push|pull|run`.
const syncTimeout = 10 * time.Minute

// runSync implements `lodan sync init|push|pull|run|schedule|unschedule`.
func runSync(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, syncUsage)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "-help", "help":
		fmt.Fprint(stdout, syncUsage)
		return 0
	case "init":
		return runSyncInit(args[1:], stdout, stderr)
	case "push", "pull", "run":
		return runSyncTransfer(args[0], args[1:], stdout, stderr)
	case "schedule", "unschedule":
		return runSyncSchedule(args[0], args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "lodan sync: acción desconocida %q\n\n", args[0])
		fmt.Fprint(stderr, syncUsage)
		return 2
	}
}

func runSyncInit(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("sync init", "lodan sync init --repo <url> [--key <fichero>] [--machine <id>]", stderr)
	repo := fs.String("repo", "", "URL git del repositorio de sincronización (obligatoria)")
	key := fs.String("key", "", "fichero de la clave (por defecto, fuera del directorio de datos)")
	machine := fs.String("machine", "", "identificador de esta máquina (por defecto, el nombre del equipo)")
	if code, done := parseFlags(fs, args); done {
		return code
	}
	if *repo == "" {
		fmt.Fprintln(stderr, "lodan sync init: falta --repo <url>")
		fs.Usage()
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "lodan sync init: %v\n", err)
		return 1
	}
	cfg.SyncRepo = *repo
	if *machine != "" {
		cfg.SyncMachine = *machine
	}
	if *key != "" {
		cfg.SyncKeyFile = *key
	}
	c, _, err := gitsync.FromConfig(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "lodan sync init: %v\n", err)
		return 1
	}
	cfg.SyncMachine, cfg.SyncKeyFile = c.MachineID, c.KeyFile

	ctx, cancel := signalContext()
	defer cancel()
	res, err := gitsync.Init(ctx, c)
	if err != nil {
		fmt.Fprintf(stderr, "lodan sync init: %v\n", err)
		return 1
	}
	if err := cfg.Save(); err != nil {
		fmt.Fprintf(stderr, "lodan sync init: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Repositorio: %s\nCopia de trabajo: %s\nMáquina: %s\n", c.RepoURL, c.Dir, c.MachineID)
	if res.KeyCreated {
		fmt.Fprintf(stdout, "Clave creada: %s\n", c.KeyFile)
	} else {
		fmt.Fprintf(stdout, "Clave: %s\n", c.KeyFile)
	}
	fmt.Fprintln(stdout, "Guarda una copia de la clave fuera de este equipo: sin ella el contenido del repositorio no se puede recuperar.")
	return 0
}

func runSyncTransfer(action string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("sync "+action, "lodan sync "+action, stderr)
	if code, done := parseFlags(fs, args); done {
		return code
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "lodan sync %s: %v\n", action, err)
		return 1
	}
	c, ok, err := gitsync.FromConfig(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "lodan sync %s: %v\n", action, err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stderr, gitsync.ErrNotConfigured)
		return 1
	}

	sigCtx, stop := signalContext()
	defer stop()
	// A sync never runs forever (the scheduled task would stop starting new runs).
	ctx, cancel := context.WithTimeout(sigCtx, syncTimeout)
	defer cancel()
	logger := log.New(stderr, "", 0)
	deps, closeStack, err := openStack(ctx, cfg, logger)
	if err != nil {
		fmt.Fprintf(stderr, "lodan sync %s: %v\n", action, err)
		return 1
	}
	defer closeStack()

	switch action {
	case "push":
		return syncPush(ctx, deps.Pool, c, stdout, stderr)
	case "pull":
		return syncPull(ctx, deps.Pool, c, stdout, stderr)
	default: // run: push first, so local edits are sent before merging remote ones
		code := syncPush(ctx, deps.Pool, c, stdout, stderr)
		if pc := syncPull(ctx, deps.Pool, c, stdout, stderr); pc != 0 {
			code = pc
		}
		return code
	}
}

func syncPush(ctx context.Context, pool *pgxpool.Pool, c gitsync.Config, stdout, stderr io.Writer) int {
	res, err := gitsync.Push(ctx, pool, c)
	if err != nil {
		fmt.Fprintf(stderr, "lodan sync push: %v\n", err)
		return 1
	}
	if res.File == "" {
		fmt.Fprintln(stdout, "Sin cambios que exportar.")
	} else {
		fmt.Fprintf(stdout, "Enviado %s: %d registros, %d relaciones, %d sesiones, %d borrados.\n",
			res.File, res.Memories, res.Relations, res.Sessions, res.Deletions)
	}
	return 0
}

func syncPull(ctx context.Context, pool *pgxpool.Pool, c gitsync.Config, stdout, stderr io.Writer) int {
	res, err := gitsync.Pull(ctx, pool, c)
	if err != nil {
		fmt.Fprintf(stderr, "lodan sync pull: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Importados %d ficheros.\n", res.Files)
	for _, k := range res.Contradictions {
		fmt.Fprintln(stdout, k)
	}
	for _, e := range res.Errors {
		fmt.Fprintln(stderr, e)
	}
	if len(res.Errors) > 0 {
		return 1
	}
	return 0
}

// runSyncSchedule implements `lodan sync schedule|unschedule`: a task of the current user, so
// it runs with the user's git credentials.
func runSyncSchedule(action string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("sync "+action, "lodan sync "+action, stderr)
	every := defaultSyncEvery
	if action == "schedule" {
		fs.IntVar(&every, "every", defaultSyncEvery, "minutos entre sincronizaciones (de 5 a 1440)")
	}
	if code, done := parseFlags(fs, args); done {
		return code
	}
	if every < 5 || every > 1440 {
		fmt.Fprintln(stderr, "lodan sync schedule: el intervalo debe estar entre 5 y 1440 minutos")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "lodan sync %s: %v\n", action, err)
		return 1
	}
	if _, ok, err := gitsync.FromConfig(cfg); err != nil {
		fmt.Fprintf(stderr, "lodan sync %s: %v\n", action, err)
		return 1
	} else if !ok {
		fmt.Fprintln(stderr, gitsync.ErrNotConfigured)
		return 1
	}
	bin, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "lodan sync %s: no se pudo localizar el binario: %v\n", action, err)
		return 1
	}
	if stable := install.StableBinary(cfg.DataDir, runtime.GOOS); fileExistsCLI(stable) {
		bin = stable // the installed copy survives updates of the downloaded one
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "lodan sync %s: %v\n", action, err)
		return 1
	}
	s := gitsync.Scheduler{GOOS: runtime.GOOS, Home: home, Bin: bin}
	ctx, cancel := signalContext()
	defer cancel()

	if action == "unschedule" {
		if err := s.Unschedule(ctx); err != nil {
			fmt.Fprintf(stderr, "lodan sync unschedule: %v\n", err)
			return 1
		}
		cfg.SyncIntervalMinutes = 0
		if err := cfg.Save(); err != nil {
			fmt.Fprintf(stderr, "lodan sync unschedule: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "Sincronización automática quitada.")
		return 0
	}
	if err := s.Schedule(ctx, every); err != nil {
		fmt.Fprintf(stderr, "lodan sync schedule: %v\n", err)
		return 1
	}
	cfg.SyncIntervalMinutes = every
	if err := cfg.Save(); err != nil {
		fmt.Fprintf(stderr, "lodan sync schedule: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Sincronización automática cada %d minutos (tarea %s de tu usuario).\n", every, gitsync.TaskName)
	return 0
}

func fileExistsCLI(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
