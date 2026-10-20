package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"lodan/internal/service"
)

// HTTPServiceName is the name of the Windows service that exposes the MCP over
// HTTP, for clients that cannot speak stdio (a tunnel, a phone, another
// machine). It is separate from the `lodan` service, which only supervises
// PostgreSQL: that separation keeps the supervisor's backup/restore handoff
// untouched and makes this service independently startable and removable.
const HTTPServiceName = "lodan-http"

// httpOptions are the parsed flags of the internal `lodan service http`.
type httpOptions struct {
	// Addr is the TCP address the MCP HTTP server binds to (host:port).
	Addr string
	// Env holds "K=V" variables for the HTTP process (LODAN_DATA_DIR, ...).
	Env []string
	// LogPath is the file that receives the output of the server; empty means
	// stderr in the foreground and nothing under the Windows service.
	LogPath string
}

// parseHTTPArgs parses the flags of `lodan service http`. When done is true the
// caller must return code.
func parseHTTPArgs(args []string, stderr io.Writer) (h httpOptions, code int, done bool) {
	flags := newFlagSet("service http", "lodan service http --addr <host:puerto> [--log <archivo>] [--env K=V]...", stderr)
	var envs stringList
	flags.StringVar(&h.Addr, "addr", "", "dirección TCP donde sirve el MCP por HTTP")
	flags.StringVar(&h.LogPath, "log", "", "archivo donde se escribe la salida del servidor HTTP")
	flags.Var(&envs, "env", "variable de entorno K=V para el servidor HTTP (repetible)")
	if code, done := parseFlags(flags, args); done {
		return h, code, true
	}
	if h.Addr == "" {
		fmt.Fprintln(stderr, "lodan service http: falta --addr")
		return h, 2, true
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "lodan service http: argumentos inesperados: %s\n", strings.Join(flags.Args(), " "))
		return h, 2, true
	}
	for _, kv := range envs {
		if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
			fmt.Fprintf(stderr, "lodan service http: --env mal formado %q (se espera K=V)\n", kv)
			return h, 2, true
		}
	}
	h.Env = envs
	return h, 0, false
}

// runServiceHTTP implements the internal `lodan service http`: the process the
// SCM keeps alive for lodan-http. The MCP server over HTTP is not a Windows
// service itself, so this command answers the SCM and runs
// `lodan serve --http --addr <addr>` as its child. Outside a Windows service it
// does the same in the foreground and stops the child on Ctrl-C.
func runServiceHTTP(args []string, stdout, stderr io.Writer) int {
	h, code, done := parseHTTPArgs(args, stderr)
	if done {
		return code
	}
	isWinSvc, err := service.IsWindowsService()
	if err != nil {
		fmt.Fprintf(stderr, "lodan service http: %v\n", err)
		return 1
	}

	if isWinSvc {
		// A Windows service has no console: everything goes to the log file, if there is one.
		err = service.RunAsWindowsService(HTTPServiceName, func(ctx context.Context) error {
			return runHTTPChild(ctx, h, nil)
		})
	} else {
		ctx, stop := signalContext()
		defer stop()
		err = runHTTPChild(ctx, h, stderr)
	}
	if err != nil {
		fmt.Fprintf(stderr, "lodan service http: %v\n", err)
		return 1
	}
	return 0
}

// runHTTPChild runs `<this binary> serve --http --addr <addr>` until ctx is
// cancelled (nil) or the server dies (error). It re-executes the same
// executable, so the child is the same build (and the same edition name) that
// installed the service.
func runHTTPChild(ctx context.Context, h httpOptions, fallback io.Writer) error {
	out := fallback
	if h.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(h.LogPath), 0o700); err != nil {
			return fmt.Errorf("no se pudo crear el directorio de logs de %s: %w", h.LogPath, err)
		}
		f, err := os.OpenFile(h.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("no se pudo abrir %s: %w", h.LogPath, err)
		}
		defer f.Close()
		out = f
	}
	logOut := out
	if logOut == nil {
		logOut = io.Discard
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("no se pudo localizar el ejecutable: %w", err)
	}
	logger := log.New(logOut, "lodan-http: ", log.LstdFlags)
	return service.RunChild(ctx, service.ChildSpec{
		Exe:  self,
		Args: []string{"serve", "--http", "--addr", h.Addr},
		Env:  h.Env,
		Out:  out,
	}, logger)
}
