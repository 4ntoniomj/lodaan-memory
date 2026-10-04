package mcptools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/memory"
)

// rememberInput is the input of the remember tool.
type rememberInput struct {
	Items []rememberItem `json:"items" jsonschema:"Registros a guardar (1-20)"`
}

type rememberItem struct {
	Title      string   `json:"title" jsonschema:"Título corto"`
	Content    string   `json:"content" jsonschema:"Contenido autosuficiente"`
	Kind       string   `json:"kind" jsonschema:"Tipo de registro"`
	Topics     []string `json:"topics,omitempty" jsonschema:"1-3 temas libres; reutiliza los existentes"`
	Key        string   `json:"key,omitempty" jsonschema:"Clave de un dato que cambia; sustituye al vigente con la misma clave"`
	OccurredAt string   `json:"occurred_at,omitempty" jsonschema:"Cuándo ocurrió: RFC3339 o AAAA-MM-DD, hora local (por defecto, ahora)"`
}

func (s *Server) handleRemember(ctx context.Context, req *mcp.CallToolRequest, in rememberInput) (*mcp.CallToolResult, any, error) {
	tr, err := s.begin(ctx, req)
	if err != nil {
		return errorResult(err), nil, nil
	}
	items := make([]memory.Item, len(in.Items))
	for i, it := range in.Items {
		when, err := parseWhen(it.OccurredAt)
		if err != nil {
			return errorResult(fmt.Errorf("ítem %d: %w", i+1, err)), nil, nil
		}
		items[i] = memory.Item{
			Title:      it.Title,
			Content:    it.Content,
			Kind:       memory.Kind(it.Kind),
			Topics:     it.Topics,
			Key:        it.Key,
			OccurredAt: when,
		}
	}
	saved, err := s.d.Memory.Remember(ctx, tr, items)
	if err != nil {
		return errorResult(err), nil, nil
	}
	return textResult(formatSaved(saved)), nil, nil
}

// parseWhen parses an RFC3339 timestamp or a AAAA-MM-DD date (local midnight).
// An empty string returns nil (meaning "now").
func parseWhen(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return &t, nil
	}
	if t, err := parseDay(s); err == nil {
		return t, nil
	}
	return nil, fmt.Errorf("occurred_at %q no es válida: usa RFC3339 (2026-09-28T10:30:00+02:00) o AAAA-MM-DD", s)
}

// parseDay parses AAAA-MM-DD as local midnight.
func parseDay(s string) (*time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(s), time.Local)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// formatSaved renders one line per saved item.
func formatSaved(saved []memory.Saved) string {
	lines := make([]string, len(saved))
	for i, sv := range saved {
		lines[i] = formatOneSaved(sv)
	}
	return strings.Join(lines, "\n")
}

func formatOneSaved(sv memory.Saved) string {
	if sv.Duplicate {
		return fmt.Sprintf("#%d ya existía (duplicado): «%s»", sv.ID, oneLine(sv.Title))
	}
	parts := []string{fmt.Sprintf("#%d guardado", sv.ID)}
	if len(sv.Topics) > 0 {
		names := make([]string, len(sv.Topics))
		for i, r := range sv.Topics {
			names[i] = r.Topic.Slug
			if r.Equivalent {
				names[i] += fmt.Sprintf(" (%s→%s)", r.Requested, r.Topic.Slug)
			}
		}
		parts = append(parts, "temas: "+strings.Join(names, ", "))
	}
	if sv.Pending {
		parts = append(parts, "embedding y parecidos pendientes (se calculan en segundo plano)")
	}
	if len(sv.Superseded) > 0 {
		ids := make([]string, len(sv.Superseded))
		for i, id := range sv.Superseded {
			ids[i] = fmt.Sprintf("#%d", id)
		}
		parts = append(parts, "sustituye "+strings.Join(ids, ", "))
	}
	if len(sv.Similar) > 0 {
		sim := make([]string, len(sv.Similar))
		for i, n := range sv.Similar {
			sim[i] = fmt.Sprintf("#%d «%s» (%.2f)", n.ID, oneLine(n.Title), n.Similarity)
		}
		parts = append(parts, "parecidos: "+strings.Join(sim, ", "))
	}
	return strings.Join(parts, " · ")
}

// reviseInput is the input of the revise tool.
type reviseInput struct {
	ID       int64    `json:"id" jsonschema:"Id del registro"`
	Action   string   `json:"action" jsonschema:"Acción a aplicar"`
	WithID   int64    `json:"with_id,omitempty" jsonschema:"Otro registro: el que sustituye (supersede) o con el que se relaciona"`
	Relation string   `json:"relation,omitempty" jsonschema:"Tipo de relación (por defecto related)"`
	Title    string   `json:"title,omitempty" jsonschema:"Título nuevo (update)"`
	Content  string   `json:"content,omitempty" jsonschema:"Contenido nuevo (update)"`
	Topics   []string `json:"topics,omitempty" jsonschema:"Temas nuevos (update); reemplazan a los actuales"`
	Confirm  bool     `json:"confirm,omitempty" jsonschema:"true para confirmar delete"`
}

func (s *Server) handleRevise(ctx context.Context, req *mcp.CallToolRequest, in reviseInput) (*mcp.CallToolResult, any, error) {
	// Memory.Revise records the activity on the session itself, so no Touch here.
	tr := s.tracker(req)
	line, err := s.d.Memory.Revise(ctx, tr, memory.Revision{
		ID:       in.ID,
		Action:   in.Action,
		WithID:   in.WithID,
		Relation: in.Relation,
		Title:    in.Title,
		Content:  in.Content,
		Topics:   in.Topics,
		Confirm:  in.Confirm,
	})
	if err != nil {
		return errorResult(err), nil, nil
	}
	return textResult(line), nil, nil
}
