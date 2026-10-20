package mcptools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/embedding"
)

const (
	statusDBTimeout     = 5 * time.Second
	statusOllamaTimeout = 3 * time.Second
	// maxStatusErrLen bounds the error text included in the status line.
	maxStatusErrLen = 160
)

// OllamaStatusLine reports the embedding service state as its own fragment, so
// a caller can tell "no Ollama because we run lite" from a real failure.
func OllamaStatusLine(ctx context.Context, d Deps) string {
	return ollamaStatus(ctx, d)
}

// statusInput is the (empty) input of the status tool.
type statusInput struct{}

func (s *Server) handleStatus(ctx context.Context, _ *mcp.CallToolRequest, _ statusInput) (*mcp.CallToolResult, any, error) {
	return textResult(StatusLine(ctx, s.d)), nil, nil
}

// StatusLine returns the one-line health report used by the status tool and by
// `lodan status`:
//
//	lodan lite: BD: ok · registros vigentes: N · pendientes de embedding: M · etiquetas: T · sesiones: S · alias: A ·
//	sugerencias de alias: G · veredictos pendientes: V · contradicciones abiertas: C · Ollama: ok (modelo, D dims)
//	  ¿alias?: #a ≈ #b (N registros)   (hasta 10 sugerencias pendientes)
func StatusLine(ctx context.Context, d Deps) string {
	line := ProductName + ": " + dbStatus(ctx, d) + " · " + ollamaStatus(ctx, d)
	if s := aliasSuggestions(ctx, d); s != "" {
		line += "\n" + s
	}
	return line
}

// maxStatusSuggestions is the maximum number of pending alias suggestions status lists.
const maxStatusSuggestions = 10

// aliasSuggestions lists the pending alias suggestions, one per line, or ""
// if there are none or they cannot be read.
func aliasSuggestions(ctx context.Context, d Deps) string {
	if d.Pool == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, statusDBTimeout)
	defer cancel()
	rows, err := d.Pool.Query(ctx, `SELECT a.alias, a.canonical,
			(SELECT count(DISTINCT m.id) FROM topics t JOIN memory_topics mt ON mt.topic_id = t.id
			 JOIN memories m ON m.id = mt.memory_id AND m.status = 'active' WHERE t.slug = a.canonical)::int
		FROM tag_aliases a WHERE a.state = 'suggested' ORDER BY a.created_at, a.alias, a.canonical LIMIT $1`, maxStatusSuggestions)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var alias, canonical string
		var n int
		if err := rows.Scan(&alias, &canonical, &n); err != nil {
			return ""
		}
		lines = append(lines, fmt.Sprintf("  ¿alias?: #%s ≈ #%s (%d registros)", alias, canonical, n))
	}
	if rows.Err() != nil {
		return ""
	}
	return strings.Join(lines, "\n")
}

func dbStatus(ctx context.Context, d Deps) string {
	if d.Pool == nil {
		return "BD: no disponible (sin conexión)"
	}
	ctx, cancel := context.WithTimeout(ctx, statusDBTimeout)
	defer cancel()

	var active, pending, topics, sessions, aliases, suggestions, verdicts, contradictions int64
	err := d.Pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM memories WHERE status = 'active'),
		(SELECT count(*) FROM memories WHERE embedding IS NULL),
		(SELECT count(*) FROM topics),
		(SELECT count(*) FROM sessions),
		(SELECT count(*) FROM tag_aliases WHERE state = 'confirmed'),
		(SELECT count(*) FROM tag_aliases WHERE state = 'suggested'),
		(SELECT count(*) FROM memory_relations WHERE state = 'suggested'),
		(SELECT count(*) FROM memory_relations r
			JOIN memories a ON a.id = r.source_id AND a.status = 'active'
			JOIN memories b ON b.id = r.target_id AND b.status = 'active'
			WHERE r.kind = 'contradicts' AND r.state <> 'rejected')`).Scan(
		&active, &pending, &topics, &sessions, &aliases, &suggestions, &verdicts, &contradictions)
	if err != nil {
		return "BD: error (" + shorten(err.Error()) + ")"
	}
	return fmt.Sprintf("BD: ok · registros vigentes: %d · pendientes de embedding: %d · etiquetas: %d · sesiones: %d"+
		" · alias: %d · sugerencias de alias: %d · veredictos pendientes: %d · contradicciones abiertas: %d",
		active, pending, topics, sessions, aliases, suggestions, verdicts, contradictions)
}

func ollamaStatus(ctx context.Context, d Deps) string {
	o, ok := d.Embedder.(*embedding.Ollama)
	if !ok {
		return "Ollama: no aplica"
	}
	ctx, cancel := context.WithTimeout(ctx, statusOllamaTimeout)
	defer cancel()
	if err := o.Ping(ctx); err != nil {
		return "Ollama: no disponible (" + shorten(err.Error()) + ")"
	}
	return fmt.Sprintf("Ollama: ok (%s, %d dims)", o.Model(), o.Dims())
}

// shorten collapses whitespace and cuts s to maxStatusErrLen runes.
func shorten(s string) string {
	s = oneLine(s)
	if r := []rune(s); len(r) > maxStatusErrLen {
		return string(r[:maxStatusErrLen]) + "…"
	}
	return s
}
