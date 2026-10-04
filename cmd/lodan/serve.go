package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"lodan/internal/config"
	"lodan/internal/mcptools"
)

// maintenanceInterval is how often pending embeddings are filled and idle sessions closed.
const maintenanceInterval = 30 * time.Second

// runServe implements `lodan serve`. In stdio mode nothing but the MCP protocol
// may be written to stdout: every log goes to stderr.
func runServe(args []string, stderr io.Writer) int {
	fs := newFlagSet("serve", "lodan serve [--http] [--addr dirección]", stderr)
	useHTTP := fs.Bool("http", false, "usar transporte HTTP en lugar de stdio")
	addr := fs.String("addr", "", "dirección de escucha HTTP (vacío = la de la configuración)")
	if code, done := parseFlags(fs, args); done {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "lodan serve: argumentos inesperados: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}
	if *addr != "" && !*useHTTP {
		fmt.Fprintln(stderr, "lodan serve: --addr solo se usa junto con --http")
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "lodan serve: %v\n", err)
		return 1
	}
	httpAddr := cfg.HTTPAddr
	if *addr != "" {
		httpAddr = *addr
	}
	if *useHTTP {
		// Fail before starting PostgreSQL if the address is not local.
		if err := mcptools.ValidateHTTPAddr(httpAddr); err != nil {
			fmt.Fprintf(stderr, "lodan serve: %v\n", err)
			return 2
		}
	}

	sigCtx, stop := signalContext()
	defer stop()
	logger := log.New(stderr, "lodan: ", log.LstdFlags)

	deps, cleanup, err := openStack(sigCtx, cfg, logger)
	if err != nil {
		fmt.Fprintf(stderr, "lodan serve: %v\n", err)
		return 1
	}
	defer cleanup()

	// runCtx ends with the signal or when the server returns, so that the
	// background goroutines stop before the pool is closed.
	runCtx, cancel := context.WithCancel(sigCtx)
	srv := mcptools.NewServer(deps)
	maintDone := make(chan struct{})
	go func() {
		defer close(maintDone)
		srv.RunMaintenance(runCtx, maintenanceInterval)
	}()
	warmDone := make(chan struct{})
	go func() {
		defer close(warmDone)
		// Loads the model in Ollama (keep_alive = -1 keeps it loaded); errors are irrelevant here.
		wctx, wcancel := context.WithTimeout(runCtx, 2*time.Minute)
		defer wcancel()
		_, _ = deps.Embedder.EmbedQuery(wctx, "calentamiento")
	}()

	if *useHTTP {
		logger.Printf("servidor MCP escuchando en http://%s", httpAddr)
		err = mcptools.ServeHTTP(runCtx, srv, httpAddr)
	} else {
		err = mcptools.ServeStdio(runCtx, srv)
	}
	cancel()
	<-maintDone
	<-warmDone

	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
		fmt.Fprintf(stderr, "lodan serve: %v\n", err)
		return 1
	}
	return 0
}
