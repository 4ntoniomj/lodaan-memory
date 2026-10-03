package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"lodan/internal/config"
	"lodan/internal/service"
)

const serviceUsage = "lodan service run | start | stop | restart | enable | disable | status [--name lodan]"

// runService implements `lodan service run|start|stop|restart|enable|disable|status`.
func runService(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "lodan service: falta la acción\nUso: %s\n", serviceUsage)
		return 2
	}
	action, rest := args[0], args[1:]

	switch action {
	case "-h", "--help", "-help", "help":
		fmt.Fprintf(stdout, "Uso: %s\n", serviceUsage)
		return 0
	case "run":
		return runServiceRun(rest, stderr)
	case "ollama":
		// Internal: the process of the lodan-ollama Windows service.
		return runServiceOllama(rest, stderr)
	case "install":
		return runServiceInstall(rest, stdout, stderr)
	case "uninstall":
		return runServiceUninstall(rest, stdout, stderr)
	case "start", "stop", "restart", "enable", "disable", "status":
	default:
		fmt.Fprintf(stderr, "lodan service: acción desconocida %q\nUso: %s\n", action, serviceUsage)
		return 2
	}

	flags := newFlagSet("service "+action, "lodan service "+action+" [--name lodan]", stderr)
	name := flags.String("name", service.DefaultName, "nombre del servicio")
	if code, done := parseFlags(flags, rest); done {
		return code
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "lodan service %s: argumentos inesperados: %s\n", action, strings.Join(flags.Args(), " "))
		return 2
	}

	mgr := service.NewManager()
	var err error
	switch action {
	case "start":
		if err = mgr.Start(*name); err == nil {
			fmt.Fprintf(stdout, "Servicio %s arrancado\n", *name)
		}
	case "stop":
		if err = mgr.Stop(*name); err == nil {
			fmt.Fprintf(stdout, "Servicio %s parado\n", *name)
		}
	case "restart":
		if err = mgr.Restart(*name); err == nil {
			fmt.Fprintf(stdout, "Servicio %s reiniciado\n", *name)
		}
	case "enable":
		if err = mgr.Enable(*name); err == nil {
			fmt.Fprintf(stdout, "Servicio %s habilitado: arrancará con el sistema\n", *name)
		}
	case "disable":
		if err = mgr.Disable(*name); err == nil {
			fmt.Fprintf(stdout, "Servicio %s deshabilitado: no arrancará con el sistema\n", *name)
		}
	case "status":
		var st service.Status
		if st, err = mgr.Status(*name); err == nil {
			fmt.Fprintln(stdout, statusLine(*name, st))
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "lodan service %s: %v\n", action, err)
		if hint := permissionHint(err); hint != "" {
			fmt.Fprintln(stderr, hint)
		}
		return 1
	}
	return 0
}

// runServiceRun implements `lodan service run`: the process that the service manager keeps alive.
func runServiceRun(args []string, stderr io.Writer) int {
	flags := newFlagSet("service run", "lodan service run [--env K=V]...", stderr)
	var envs stringList
	flags.Var(&envs, "env", "variable de entorno K=V aplicada antes de cargar la configuración (repetible)")
	if code, done := parseFlags(flags, args); done {
		return code
	}
	for _, kv := range envs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			fmt.Fprintf(stderr, "lodan service run: --env mal formado %q (se espera K=V)\n", kv)
			return 2
		}
		os.Setenv(k, v)
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "lodan service run: argumentos inesperados: %s\n", strings.Join(flags.Args(), " "))
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "lodan service run: %v\n", err)
		return 1
	}

	isWinSvc, err := service.IsWindowsService()
	if err != nil {
		fmt.Fprintf(stderr, "lodan service run: %v\n", err)
		return 1
	}

	logOut := stderr
	if isWinSvc {
		// A Windows service has no console: the log goes to a file.
		f, err := openServiceLog(cfg)
		if err != nil {
			fmt.Fprintf(stderr, "lodan service run: %v\n", err)
			return 1
		}
		defer f.Close()
		logOut = f
	}
	logger := log.New(logOut, "lodan: ", log.LstdFlags)

	if isWinSvc {
		err = service.RunAsWindowsService(service.DefaultName, func(ctx context.Context) error {
			return service.RunSupervisor(ctx, cfg, logger)
		})
	} else {
		ctx, stop := signalContext()
		defer stop()
		err = service.RunSupervisor(ctx, cfg, logger)
	}
	if err != nil {
		logger.Printf("el servicio terminó con error: %v", err)
		return 1
	}
	return 0
}

// openServiceLog opens (appending) <LogsDir>/service.log.
func openServiceLog(cfg config.Config) (*os.File, error) {
	if err := os.MkdirAll(cfg.LogsDir(), 0o700); err != nil {
		return nil, fmt.Errorf("no se pudo crear el directorio de logs %s: %w", cfg.LogsDir(), err)
	}
	path := filepath.Join(cfg.LogsDir(), "service.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("no se pudo abrir %s: %w", path, err)
	}
	return f, nil
}

// statusLine formats the state of a service on a single line.
func statusLine(name string, st service.Status) string {
	state := map[service.State]string{
		"running":       "en marcha",
		"stopped":       "parado",
		"not-installed": "no instalado",
		"unknown":       "desconocido",
	}[st.State]
	if state == "" {
		state = string(st.State)
	}
	enabled := "no"
	if st.Enabled {
		enabled = "sí"
	}
	line := fmt.Sprintf("Servicio %s: estado %s · arranque automático: %s", name, state, enabled)
	if st.Detail != "" {
		line += " · " + st.Detail
	}
	return line
}

// permissionHint returns a Spanish hint when err looks like a lack of privileges, or "".
func permissionHint(err error) string {
	msg := strings.ToLower(err.Error())
	if !errors.Is(err, fs.ErrPermission) &&
		!strings.Contains(msg, "permiso") && !strings.Contains(msg, "access") && !strings.Contains(msg, "denied") {
		return ""
	}
	if runtime.GOOS == "windows" {
		return "Pista: ejecútalo como administrador."
	}
	return "Pista: ejecútalo con sudo."
}

// goos returns the operating system the binary runs on.
func goos() string { return runtime.GOOS }
