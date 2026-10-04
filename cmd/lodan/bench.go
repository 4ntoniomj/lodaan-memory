package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"lodan/internal/benchmark"
	"lodan/internal/config"
)

// runBench implements `lodan bench` and returns the process exit code.
func runBench(args []string, stderr io.Writer) int {
	fs := newFlagSet("bench", "lodan bench [--rows n1,n2,...] [--candidates c1,c2,...] [--reuse] [--queries n] [--out fichero] [--seed n]", stderr)
	rowsFlag := fs.String("rows", "10000,100000,1000000", "tamaños del benchmark separados por comas, p. ej. 10000,100000")
	candidatesFlag := fs.String("candidates", "100", "candidatos del índice binario a medir, separados por comas, p. ej. 50,100,200")
	reuse := fs.Bool("reuse", false, "reutiliza los datos de lodan_bench si ya tiene max(--rows) filas: no carga ni reconstruye índices y mide solo la última escala")
	queries := fs.Int("queries", 500, "consultas medidas por escala")
	out := fs.String("out", benchmark.DefaultOut, "fichero markdown de salida")
	seed := fs.Int64("seed", 42, "semilla del generador de datos sintéticos")
	embedQueries := fs.Int("embed-queries", 30, "embeddings reales medidos contra Ollama (0 = no medir)")
	if code, done := parseFlags(fs, args); done {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "lodan bench: argumentos inesperados: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}

	rows, err := parseIntList("--rows", *rowsFlag)
	if err != nil {
		fmt.Fprintf(stderr, "lodan bench: %v\n", err)
		return 2
	}
	candidates, err := parseIntList("--candidates", *candidatesFlag)
	if err != nil {
		fmt.Fprintf(stderr, "lodan bench: %v\n", err)
		return 2
	}
	if *queries <= 0 {
		fmt.Fprintf(stderr, "lodan bench: --queries debe ser mayor que 0, y es %d\n", *queries)
		return 2
	}
	if *embedQueries < 0 {
		fmt.Fprintf(stderr, "lodan bench: --embed-queries no puede ser negativo, y es %d\n", *embedQueries)
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "lodan bench: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	_, err = benchmark.Run(ctx, cfg, benchmark.Options{
		Rows:         rows,
		Candidates:   candidates,
		Reuse:        *reuse,
		Queries:      *queries,
		EmbedQueries: *embedQueries,
		Seed:         *seed,
		Out:          *out,
		Log:          stderr,
	})
	if err != nil {
		fmt.Fprintf(stderr, "lodan bench: %v\n", err)
		return 1
	}
	return 0
}

// parseIntList parses a comma-separated list of positive integers. flag is the name of the
// flag, used in the error messages.
func parseIntList(flag, s string) ([]int, error) {
	var values []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("%s: %q no es un entero positivo", flag, part)
		}
		values = append(values, n)
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("%s: falta al menos un valor", flag)
	}
	return values, nil
}
