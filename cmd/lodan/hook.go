package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"lodan/internal/config"
	"lodan/internal/database"
	"lodan/internal/embedding"
	"lodan/internal/hook"
	"lodan/internal/memory"
	"lodan/internal/session"
	"lodan/internal/topic"
)

// stdin is where hooks read the client's event; tests replace it.
var stdin io.Reader = os.Stdin

const hookUsage = `Uso:
  lodan hook compact --client <nombre> [--format text|antigravity]
      Tras compactar: escribe las instrucciones de recuperación y el resumen de la última sesión;
      con el evento PostCompact guarda el resumen de la compactación en la sesión del cliente.
  lodan hook subagent-stop --client <nombre>
      Al terminar un subagente: guarda cada punto de su bloque "## Key Learnings" como nota.

Leen el JSON del evento por la entrada estándar, nunca arrancan PostgreSQL y siempre salen con 0.
`

// hookTimeout bounds a whole hook run, so a hook never holds up the client.
const hookTimeout = 8 * time.Second

// runHookCommand implements `lodan hook compact|subagent-stop`. Apart from a wrong command line it
// always returns 0: a hook must never block or break the AI client.
func runHookCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, hookUsage)
		return 2
	}
	action := args[0]
	switch action {
	case "-h", "--help", "-help", "help":
		fmt.Fprint(stdout, hookUsage)
		return 0
	case "compact", "subagent-stop":
	default:
		fmt.Fprintf(stderr, "lodan hook: acción desconocida %q\n\n", action)
		fmt.Fprint(stderr, hookUsage)
		return 2
	}
	fs := newFlagSet("hook "+action, "lodan hook "+action+" --client <nombre>", stderr)
	client := fs.String("client", "desconocido", "nombre del cliente de IA (el mismo que usa en MCP)")
	format := "text"
	if action == "compact" {
		fs.StringVar(&format, "format", "text", "salida: text, o antigravity (JSON injectSteps del SessionStart de Antigravity)")
	}
	if code, done := parseFlags(fs, args[1:]); done {
		return code
	}
	if format != "text" && format != "antigravity" {
		fmt.Fprintf(stderr, "lodan hook compact: formato desconocido %q (usa text o antigravity)\n", format)
		return 2
	}

	// The event is read first, so the client is never left waiting on a full pipe.
	input, _ := io.ReadAll(io.LimitReader(stdin, 16<<20))

	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	deps, closeDeps := connectHook(ctx, stderr)
	defer closeDeps()

	switch {
	case action == "compact" && format == "antigravity":
		writeAntigravityStart(ctx, deps, *client, stdout, stderr)
	case action == "compact":
		if err := hook.Compact(ctx, deps, *client, bytes.NewReader(input), stdout); err != nil {
			fmt.Fprintf(stderr, "lodan hook compact: %v\n", err)
		}
	case action == "subagent-stop":
		if _, err := hook.SubagentStop(ctx, deps, *client, bytes.NewReader(input)); err != nil {
			fmt.Fprintf(stderr, "lodan hook subagent-stop: %v\n", err)
		}
	}
	return 0
}

// writeAntigravityStart prints the JSON Antigravity expects from a SessionStart hook: the
// start instructions and the last session, as a system message. On any problem it prints {}.
func writeAntigravityStart(ctx context.Context, deps *hook.Deps, client string, stdout, stderr io.Writer) {
	text := hook.StartInstructions
	if line, err := hook.LastSessionLine(ctx, deps, client); err != nil {
		fmt.Fprintf(stderr, "lodan hook compact: %v\n", err)
	} else if line != "" {
		text += "\n" + line
	}
	out := map[string]any{"injectSteps": []any{map[string]any{"systemMessage": map[string]any{"systemMessage": text}}}}
	data, err := json.Marshal(out)
	if err != nil {
		fmt.Fprintln(stdout, "{}")
		return
	}
	fmt.Fprintln(stdout, string(data))
}

// connectHook connects to lodan's database only if PostgreSQL is already running; it never
// initialises or starts it. It returns nil deps when the database is unavailable.
func connectHook(ctx context.Context, stderr io.Writer) (*hook.Deps, func()) {
	noop := func() {}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "lodan hook: %v\n", err)
		return nil, noop
	}
	if _, err := os.Stat(filepath.Join(cfg.PGDataDir(), "PG_VERSION")); err != nil {
		return nil, noop // no cluster here: nothing to connect to
	}
	cluster, err := database.NewCluster(cfg)
	if err != nil {
		return nil, noop
	}
	dsn, err := cluster.DSN(databaseName)
	if err != nil {
		return nil, noop
	}
	openCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	pool, err := database.Open(openCtx, dsn)
	if err != nil {
		fmt.Fprintf(stderr, "lodan hook: base de datos no disponible: %v\n", err)
		return nil, noop
	}
	emb, err := embedding.NewOllama(embedding.Options{
		BaseURL:   cfg.OllamaURL,
		Model:     cfg.EmbedModel,
		Dims:      cfg.EmbedDims,
		KeepAlive: cfg.EmbedKeepAlive,
	})
	if err != nil {
		pool.Close()
		return nil, noop
	}
	topics := topic.NewResolver(pool, emb, cfg.TopicSimilarity)
	if err := topics.Load(ctx); err != nil {
		fmt.Fprintf(stderr, "lodan hook: no se pudo cargar la caché de temas: %v\n", err)
	}
	sessions := session.NewManager(pool, time.Duration(cfg.SessionIdleMinutes)*time.Minute)
	deps := &hook.Deps{
		Pool:     pool,
		Memory:   memory.NewService(pool, emb, topics, sessions, cfg.DupSimilarity, time.Duration(cfg.EmbedTimeoutMs)*time.Millisecond),
		Sessions: sessions,
	}
	return deps, pool.Close
}
