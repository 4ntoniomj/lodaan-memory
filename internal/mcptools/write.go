package mcptools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/memory"
	"lodan/internal/recall"
	"lodan/internal/topic"
)

// rememberInput is the input of the remember tool.
type rememberInput struct {
	Items []rememberItem `json:"items" jsonschema:"Registros a guardar (1-200)"`
}

type rememberItem struct {
	Title      string        `json:"title" jsonschema:"Título corto"`
	Content    string        `json:"content" jsonschema:"Contenido autosuficiente"`
	Kind       string        `json:"kind" jsonschema:"Tipo de registro"`
	Tags       []string      `json:"tags,omitempty" jsonschema:"Etiquetas (máx. 8): generales (#agridemur) y concretas (#coche-1234abc); reutiliza las existentes"`
	Topics     []string      `json:"topics,omitempty" jsonschema:"Igual que tags (se juntan)"`
	Key        string        `json:"key,omitempty" jsonschema:"Clave de un dato que cambia; sustituye al vigente con la misma clave"`
	OccurredAt string        `json:"occurred_at,omitempty" jsonschema:"Cuándo ocurrió: RFC3339 o AAAA-MM-DD, hora local (por defecto, ahora)"`
	Relate     []relateInput `json:"relate,omitempty" jsonschema:"Registros existentes con los que se relaciona (relación confirmada)"`
}

// relateInput is a relation requested when saving an item.
type relateInput struct {
	ID       int64  `json:"id" jsonschema:"Id del registro existente"`
	Relation string `json:"relation,omitempty" jsonschema:"related (por defecto), contradicts o part_of"`
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
		relate := make([]memory.RelateTo, len(it.Relate))
		for j, r := range it.Relate {
			relate[j] = memory.RelateTo{ID: r.ID, Kind: r.Relation}
		}
		items[i] = memory.Item{
			Title:      it.Title,
			Content:    it.Content,
			Kind:       memory.Kind(it.Kind),
			Tags:       it.Tags,
			Topics:     it.Topics,
			Key:        it.Key,
			OccurredAt: when,
			Relate:     relate,
		}
	}
	saved, err := s.d.Memory.Remember(ctx, tr, items)
	if err != nil {
		return errorResult(err), nil, nil
	}
	return textResult(formatReviewed(saved, s.connections(ctx, saved), s.reviewCandidates(ctx, items, saved))), nil, nil
}

// connections finds, for every newly saved item, the active records connected
// to it by rare tags. The search is best effort: if it fails, there are none.
func (s *Server) connections(ctx context.Context, saved []memory.Saved) [][]recall.Link {
	out := make([][]recall.Link, len(saved))
	for i, sv := range saved {
		if sv.Duplicate {
			continue
		}
		if links, err := s.d.Recall.Connected(ctx, []int64{sv.ID}, nil, maxLinks); err == nil {
			out[i] = links
		}
	}
	return out
}

// maxLinks is the maximum number of connected records listed per saved item.
const maxLinks = 5

// formatLinks renders links as "#12 (#tag, 5.3), #8 (#tag, 4.1)".
func formatLinks(links []recall.Link) string {
	parts := make([]string, len(links))
	for i, l := range links {
		parts[i] = fmt.Sprintf("#%d (#%s, %.1f)", l.ID, l.Tag, l.Strength)
	}
	return strings.Join(parts, ", ")
}

// reviewCandidates finds, for every newly saved item, the existing records it
// should be checked against (possible contradictions or relations). The search
// is best effort: if it fails for an item, that item gets no candidates.
func (s *Server) reviewCandidates(ctx context.Context, items []memory.Item, saved []memory.Saved) [][]recall.Item {
	// Records saved in this call, and the ones they replaced, are never candidates.
	var common []int64
	for _, sv := range saved {
		if !sv.Duplicate {
			common = append(common, sv.ID)
		}
		common = append(common, sv.Superseded...)
	}
	out := make([][]recall.Item, len(saved))
	for i, sv := range saved {
		if sv.Duplicate {
			continue
		}
		exclude := append(append([]int64(nil), common...), sv.Related...)
		topicIDs := make([]int32, len(sv.Topics))
		for j, r := range sv.Topics {
			topicIDs[j] = r.Topic.ID
		}
		cands, err := s.d.Recall.Candidates(ctx, recall.CandidateRequest{
			Text:     items[i].Title + " " + items[i].Content,
			Vector:   sv.Vector,
			TopicIDs: topicIDs,
			Exclude:  exclude,
			Limit:    candidateLimit,
		})
		if err == nil {
			out[i] = cands
		}
	}
	return out
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
	return formatReviewed(saved, nil, nil)
}

// formatReviewed renders one line per saved item, each followed by its review
// candidates ("  revisa: <recall line>"). candidates may be nil.
func formatReviewed(saved []memory.Saved, links [][]recall.Link, candidates [][]recall.Item) string {
	lines := make([]string, 0, len(saved))
	for i, sv := range saved {
		lines = append(lines, formatOneSaved(sv))
		for _, sg := range sv.Suggestions {
			lines = append(lines, fmt.Sprintf("  ¿alias?: #%s ≈ #%s (%d registros)", sg.New, sg.Existing, sg.Records))
		}
		if i < len(links) && len(links[i]) > 0 {
			lines = append(lines, "  conectado por etiquetas: "+formatLinks(links[i]))
		}
		if i < len(candidates) {
			for _, c := range candidates[i] {
				lines = append(lines, "  revisa: "+c.Line())
			}
		}
	}
	return strings.Join(lines, "\n")
}

func formatOneSaved(sv memory.Saved) string {
	if sv.Duplicate {
		return fmt.Sprintf("#%d ya existía (duplicado): «%s»", sv.ID, oneLine(sv.Title))
	}
	parts := []string{fmt.Sprintf("#%d guardado", sv.ID)}
	if len(sv.Topics) > 0 {
		parts = append(parts, "etiquetas: "+formatTags(sv.Topics))
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
	for _, id := range sv.MissingRelations {
		parts = append(parts, fmt.Sprintf("relación con #%d no creada: no existe", id))
	}
	return strings.Join(parts, " · ")
}

// formatTags renders resolved tags as "#a, #requested→#canonical, #new (nueva)".
func formatTags(rs []topic.Resolution) string {
	names := make([]string, len(rs))
	for i, r := range rs {
		names[i] = "#" + r.Topic.Slug
		if r.Equivalent {
			names[i] = fmt.Sprintf("#%s→#%s", r.Requested, r.Topic.Slug)
		}
		if r.Created {
			names[i] += " (nueva)"
		}
	}
	return strings.Join(names, ", ")
}

// reviseInput is the input of the revise tool: one operation in the top-level
// fields, several in ops, or both (the top-level one goes first).
type reviseInput struct {
	ID       int64      `json:"id,omitempty" jsonschema:"Id del registro"`
	Action   string     `json:"action,omitempty" jsonschema:"Acción a aplicar"`
	WithID   int64      `json:"with_id,omitempty" jsonschema:"Otro registro: el que sustituye (supersede) o con el que se relaciona"`
	Relation string     `json:"relation,omitempty" jsonschema:"Tipo de relación (por defecto related)"`
	Title    string     `json:"title,omitempty" jsonschema:"Título nuevo (update)"`
	Content  string     `json:"content,omitempty" jsonschema:"Contenido nuevo (update)"`
	Tag      string     `json:"tag,omitempty" jsonschema:"Etiqueta que es alias (alias, reject_alias)"`
	WithTag  string     `json:"with_tag,omitempty" jsonschema:"Etiqueta canónica (alias) o distinta (reject_alias)"`
	Tags     []string   `json:"tags,omitempty" jsonschema:"Etiquetas nuevas (update, máx. 8); reemplazan a las actuales"`
	Topics   []string   `json:"topics,omitempty" jsonschema:"Igual que tags (se juntan)"`
	Confirm  bool       `json:"confirm,omitempty" jsonschema:"true para confirmar delete"`
	Ops      []reviseOp `json:"ops,omitempty" jsonschema:"Varias operaciones en una sola llamada (máx. 200 en total), con los mismos campos; se aplican en orden y una que falle no frena las demás"`
}

// reviseOp is one operation of the revise tool.
type reviseOp struct {
	ID       int64    `json:"id,omitempty" jsonschema:"Id del registro (no hace falta en alias ni reject_alias)"`
	Action   string   `json:"action" jsonschema:"update, supersede, invalidate, delete, relate, confirm_relation, reject_relation, alias o reject_alias"`
	WithID   int64    `json:"with_id,omitempty" jsonschema:"Otro registro: el que sustituye (supersede) o con el que se relaciona"`
	Relation string   `json:"relation,omitempty" jsonschema:"related, contradicts, part_of o supersedes (por defecto related)"`
	Title    string   `json:"title,omitempty" jsonschema:"Título nuevo (update)"`
	Content  string   `json:"content,omitempty" jsonschema:"Contenido nuevo (update)"`
	Tag      string   `json:"tag,omitempty" jsonschema:"Etiqueta que es alias (alias, reject_alias)"`
	WithTag  string   `json:"with_tag,omitempty" jsonschema:"Etiqueta canónica (alias) o distinta (reject_alias)"`
	Tags     []string `json:"tags,omitempty" jsonschema:"Etiquetas nuevas (update, máx. 8)"`
	Topics   []string `json:"topics,omitempty" jsonschema:"Igual que tags (se juntan)"`
	Confirm  bool     `json:"confirm,omitempty" jsonschema:"true para confirmar delete"`
}

func (s *Server) handleRevise(ctx context.Context, req *mcp.CallToolRequest, in reviseInput) (*mcp.CallToolResult, any, error) {
	var ops []reviseOp
	if in.ID != 0 || in.Action != "" || in.Tag != "" || in.WithTag != "" {
		ops = append(ops, reviseOp{ID: in.ID, Action: in.Action, WithID: in.WithID, Relation: in.Relation,
			Title: in.Title, Content: in.Content, Tag: in.Tag, WithTag: in.WithTag, Tags: in.Tags, Topics: in.Topics, Confirm: in.Confirm})
	}
	ops = append(ops, in.Ops...)
	if len(ops) == 0 {
		return errorResult(errors.New("hay que enviar una operación (id y action) u ops")), nil, nil
	}
	if len(ops) > memory.MaxBatch {
		return errorResult(batchSizeError(len(ops), "operaciones")), nil, nil
	}

	// Memory.Revise records the activity on the session itself, so no Touch here.
	tr := s.tracker(req)
	results := make([]opResult, len(ops))
	for i, op := range ops {
		line, err := s.d.Memory.Revise(ctx, tr, memory.Revision{
			ID:       op.ID,
			Action:   op.Action,
			WithID:   op.WithID,
			Relation: op.Relation,
			Title:    op.Title,
			Content:  op.Content,
			Tag:      op.Tag,
			WithTag:  op.WithTag,
			Tags:     op.Tags,
			Topics:   op.Topics,
			Confirm:  op.Confirm,
		})
		results[i] = opResult{text: line, err: err}
	}
	return batchResult(results), nil, nil
}
