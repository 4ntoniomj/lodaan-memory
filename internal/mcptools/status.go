package mcptools

import (
	"context"
	"fmt"
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

// statusInput is the (empty) input of the status tool.
type statusInput struct{}

func (s *Server) handleStatus(ctx context.Context, _ *mcp.CallToolRequest, _ statusInput) (*mcp.CallToolResult, any, error) {
	return textResult(StatusLine(ctx, s.d)), nil, nil
}

// StatusLine returns the one-line health report used by the status tool and by
// `lodan status`:
//
//	BD: ok · registros vigentes: N · pendientes de embedding: M · temas: T · sesiones: S · Ollama: ok (modelo, D dims)
func StatusLine(ctx context.Context, d Deps) string {
	return dbStatus(ctx, d) + " · " + ollamaStatus(ctx, d)
}

func dbStatus(ctx context.Context, d Deps) string {
	if d.Pool == nil {
		return "BD: no disponible (sin conexión)"
	}
	ctx, cancel := context.WithTimeout(ctx, statusDBTimeout)
	defer cancel()

	var active, pending, topics, sessions int64
	err := d.Pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM memories WHERE status = 'active'),
		(SELECT count(*) FROM memories WHERE embedding IS NULL),
		(SELECT count(*) FROM topics),
		(SELECT count(*) FROM sessions)`).Scan(&active, &pending, &topics, &sessions)
	if err != nil {
		return "BD: error (" + shorten(err.Error()) + ")"
	}
	return fmt.Sprintf("BD: ok · registros vigentes: %d · pendientes de embedding: %d · temas: %d · sesiones: %d",
		active, pending, topics, sessions)
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
