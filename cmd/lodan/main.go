// Command lodan is the entry point of the lodan memory server.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"lodan/internal/config"
)

const usageText = `lodan: servidor de memoria para asistentes de IA

Uso:
  lodan <subcomando> [opciones]

Subcomandos:
  serve     Arranca el servidor MCP (stdio por defecto, --http para HTTP local)
  db        Gestiona el PostgreSQL local: init | start | stop | status
  migrate   Aplica las migraciones pendientes
  status    Muestra el estado de la base de datos y de Ollama
  bench     Ejecuta el benchmark (--rows, --out)

Usa "lodan <subcomando> -h" para ver las opciones de cada uno.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the CLI and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}

	name, rest := args[0], args[1:]
	switch name {
	case "-h", "--help", "-help", "help":
		fmt.Fprint(stdout, usageText)
		return 0
	case "serve":
		fs := newFlagSet(name, "lodan serve [--http] [--addr dirección]", stderr)
		fs.Bool("http", false, "usar transporte HTTP en lugar de stdio")
		fs.String("addr", "", "dirección de escucha HTTP (vacío = la de la configuración)")
		if code, done := parseFlags(fs, rest); done {
			return code
		}
	case "db":
		fs := newFlagSet(name, "lodan db init|start|stop|status", stderr)
		if code, done := parseFlags(fs, rest); done {
			return code
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "lodan db: falta la acción (init|start|stop|status)")
			return 2
		}
		switch fs.Arg(0) {
		case "init", "start", "stop", "status":
		default:
			fmt.Fprintf(stderr, "lodan db: acción desconocida %q (usa init|start|stop|status)\n", fs.Arg(0))
			return 2
		}
	case "migrate":
		fs := newFlagSet(name, "lodan migrate", stderr)
		if code, done := parseFlags(fs, rest); done {
			return code
		}
	case "status":
		fs := newFlagSet(name, "lodan status", stderr)
		if code, done := parseFlags(fs, rest); done {
			return code
		}
	case "bench":
		return runBench(rest, stderr)
	default:
		fmt.Fprintf(stderr, "lodan: subcomando desconocido %q\n\n", name)
		fmt.Fprint(stderr, usageText)
		return 2
	}

	if _, err := config.Load(); err != nil {
		fmt.Fprintf(stderr, "lodan %s: %v\n", name, err)
		return 1
	}
	fmt.Fprintf(stderr, "lodan %s: no implementado todavía\n", name)
	return 2
}

// newFlagSet creates a FlagSet that reports errors and help to w.
func newFlagSet(name, usage string, w io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(w)
	fs.Usage = func() {
		fmt.Fprintf(w, "Uso: %s\n", usage)
		fs.PrintDefaults()
	}
	return fs
}

// parseFlags parses args. When done is true, the caller must return code.
func parseFlags(fs *flag.FlagSet, args []string) (code int, done bool) {
	err := fs.Parse(args)
	switch {
	case err == nil:
		return 0, false
	case errors.Is(err, flag.ErrHelp):
		return 0, true
	default:
		return 2, true
	}
}
