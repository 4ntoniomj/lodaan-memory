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
	fs := newFlagSet("bench", "lodan bench [--rows n1,n2,...] [--queries n] [--out fichero] [--seed n]", stderr)
	rowsFlag := fs.String("rows", "10000,100000,1000000", "tamaños del benchmark separados por comas, p. ej. 10000,100000")
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

	rows, err := parseRows(*rowsFlag)
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

// parseRows parses a comma-separated list of positive integers.
func parseRows(s string) ([]int, error) {
	var rows []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("--rows: %q no es un entero positivo", part)
		}
		rows = append(rows, n)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("--rows: falta al menos un tamaño")
	}
	return rows, nil
}
