package main

import (
	"fmt"
	"io"
	"log"

	"lodan/internal/config"
	"lodan/internal/gitsync"
)

const syncUsage = `Uso:
  lodan sync init --repo <url> [--key <fichero>] [--machine <id>]
      Prepara esta máquina: clona el repositorio en <datos>/sync-repo y crea la clave si no existe.
  lodan sync push
      Exporta los cambios locales cifrados, los confirma y los sube al repositorio.
  lodan sync pull
      Baja los cambios de las otras máquinas y los importa.
`

// runSync implements `lodan sync init|push|pull`.
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
	case "push", "pull":
		return runSyncTransfer(args[0], args[1:], stdout, stderr)
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

	ctx, cancel := signalContext()
	defer cancel()
	logger := log.New(stderr, "", 0)
	deps, closeStack, err := openStack(ctx, cfg, logger)
	if err != nil {
		fmt.Fprintf(stderr, "lodan sync %s: %v\n", action, err)
		return 1
	}
	defer closeStack()

	if action == "push" {
		res, err := gitsync.Push(ctx, deps.Pool, c)
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

	res, err := gitsync.Pull(ctx, deps.Pool, c)
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
