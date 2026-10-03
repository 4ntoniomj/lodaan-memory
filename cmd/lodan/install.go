package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"lodan/internal/config"
	"lodan/internal/install"
	"lodan/internal/service"
)

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// runInstall implements `lodan install`.
func runInstall(args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("install", "lodan install [--yes] [--dry-run] [--skip-ollama] [--skip-service] [--skip-clients] [--only <cliente>]...", stderr)
	var o install.Options
	var only stringList
	flags.BoolVar(&o.Yes, "yes", false, "responde sí a todas las confirmaciones")
	flags.BoolVar(&o.DryRun, "dry-run", false, "muestra lo que haría sin tocar nada")
	flags.BoolVar(&o.SkipOllama, "skip-ollama", false, "no instala Ollama si no hay ninguno")
	flags.BoolVar(&o.SkipService, "skip-service", false, "no registra los servicios de sistema")
	flags.BoolVar(&o.SkipClients, "skip-clients", false, "no configura los clientes MCP")
	flags.Var(&only, "only", "configura solo este cliente (repetible)")
	if code, done := parseFlags(flags, args); done {
		return code
	}
	o.Only = only
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "lodan install: %v\n", err)
		return 1
	}
	ctx, stop := signalContext()
	defer stop()
	if err := install.NewInstaller(cfg, os.Stdin, stdout).Install(ctx, o); err != nil {
		fmt.Fprintf(stderr, "lodan install: %v\n", err)
		return 1
	}
	return 0
}

// runUninstall implements `lodan uninstall`.
func runUninstall(args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("uninstall", "lodan uninstall [--yes] [--dry-run] [--purge]", stderr)
	var o install.UninstallOptions
	flags.BoolVar(&o.Yes, "yes", false, "responde sí a las confirmaciones (salvo --purge)")
	flags.BoolVar(&o.DryRun, "dry-run", false, "muestra lo que haría sin tocar nada")
	flags.BoolVar(&o.Purge, "purge", false, "borra también todos los datos (pide escribir «borrar»)")
	if code, done := parseFlags(flags, args); done {
		return code
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "lodan uninstall: %v\n", err)
		return 1
	}
	ctx, stop := signalContext()
	defer stop()
	if err := install.NewInstaller(cfg, os.Stdin, stdout).Uninstall(ctx, o); err != nil {
		fmt.Fprintf(stderr, "lodan uninstall: %v\n", err)
		return 1
	}
	return 0
}

// runDoctor implements `lodan doctor`.
func runDoctor(args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("doctor", "lodan doctor", stderr)
	if code, done := parseFlags(flags, args); done {
		return code
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "lodan doctor: %v\n", err)
		return 1
	}
	ctx, stop := signalContext()
	defer stop()
	// Doctor prints its own report; here only the exit code is decided.
	results := install.NewInstaller(cfg, os.Stdin, stdout).Doctor(ctx)
	if install.DoctorHasErrors(results) {
		return 1
	}
	return 0
}

// teeLog duplicates stdout and stderr into the file at path (appending), for the elevated
// commands whose window closes when they end: the installer reads the file afterwards. With an
// empty path nothing changes. If the file cannot be opened it warns on stderr and goes on without
// it, because the work of the command matters more than its log. The returned function closes
// the file.
func teeLog(path string, stdout, stderr io.Writer) (io.Writer, io.Writer, func()) {
	if path == "" {
		return stdout, stderr, func() {}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil && errors.Is(err, fs.ErrNotExist) {
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr == nil {
			f, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "aviso: no se pudo abrir el log %s: %v\n", path, err)
		return stdout, stderr, func() {}
	}
	// The file goes first: if the console fails, the log still gets the text.
	return io.MultiWriter(f, stdout), io.MultiWriter(f, stderr), func() { _ = f.Close() }
}

// runServiceInstall implements the internal `lodan service install`, run elevated by `lodan install`.
func runServiceInstall(args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("service install", "lodan service install --data-dir <dir> [--user <u>] [--home <dir>] [--ollama-exe <ruta> --ollama-dir <dir>] [--log <archivo>]", stderr)
	var p install.ServiceParams
	logFile := flags.String("log", "", "archivo donde se escribe también la salida y el error")
	flags.StringVar(&p.User, "user", "", "cuenta con la que se ejecutan los servicios")
	flags.StringVar(&p.Home, "home", "", "HOME de esa cuenta")
	flags.StringVar(&p.DataDir, "data-dir", "", "directorio de datos de lodan")
	flags.StringVar(&p.OllamaExe, "ollama-exe", "", "ejecutable de Ollama instalado por lodan")
	flags.StringVar(&p.OllamaDir, "ollama-dir", "", "directorio de Ollama instalado por lodan")
	if code, done := parseFlags(flags, args); done {
		return code
	}
	stdout, stderr, closeLog := teeLog(*logFile, stdout, stderr)
	defer closeLog()
	if p.DataDir == "" {
		fmt.Fprintln(stderr, "lodan service install: falta --data-dir")
		return 2
	}
	mgr := service.NewManager()
	for _, s := range install.ServiceSpecs(p, goos()) {
		if err := mgr.Install(s); err != nil {
			fmt.Fprintf(stderr, "lodan service install: %s: %v\n", s.Name, err)
			if hint := permissionHint(err); hint != "" {
				fmt.Fprintln(stderr, hint)
			}
			return 1
		}
		if err := mgr.Start(s.Name); err != nil {
			fmt.Fprintf(stderr, "lodan service install: arrancar %s: %v\n", s.Name, err)
			return 1
		}
		fmt.Fprintf(stdout, "Servicio %s instalado y en marcha\n", s.Name)
	}
	return 0
}

// runServiceUninstall implements the internal `lodan service uninstall`.
func runServiceUninstall(args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("service uninstall", "lodan service uninstall [--ollama] [--log <archivo>]", stderr)
	withOllama := flags.Bool("ollama", false, "quita también el servicio lodan-ollama")
	logFile := flags.String("log", "", "archivo donde se escribe también la salida y el error")
	if code, done := parseFlags(flags, args); done {
		return code
	}
	stdout, stderr, closeLog := teeLog(*logFile, stdout, stderr)
	defer closeLog()
	mgr := service.NewManager()
	names := []string{service.DefaultName}
	if *withOllama {
		names = append(names, install.OllamaServiceName)
	}
	code := 0
	for _, n := range names {
		if err := mgr.Uninstall(n); err != nil {
			fmt.Fprintf(stderr, "lodan service uninstall: %s: %v\n", n, err)
			if hint := permissionHint(err); hint != "" {
				fmt.Fprintln(stderr, hint)
			}
			code = 1
			continue
		}
		fmt.Fprintf(stdout, "Servicio %s eliminado\n", n)
	}
	return code
}
