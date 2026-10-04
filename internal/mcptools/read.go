package mcptools

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/memory"
	"lodan/internal/recall"
)

// recallInput is the input of the recall tool.
type recallInput struct {
	Query          string   `json:"query" jsonschema:"Pregunta o tema en lenguaje natural"`
	Topics         []string `json:"topics,omitempty" jsonschema:"Temas conocidos a priorizar (opcional; si no, se detectan)"`
	Limit          int      `json:"limit,omitempty" jsonschema:"Resultados (por defecto 8, máx. 20)"`
	IncludeHistory bool     `json:"include_history,omitempty" jsonschema:"Incluir registros sustituidos e invalidados"`
}

func (s *Server) handleRecall(ctx context.Context, req *mcp.CallToolRequest, in recallInput) (*mcp.CallToolResult, any, error) {
	if _, err := s.begin(ctx, req); err != nil {
		return errorResult(err), nil, nil
	}
	res, err := s.d.Recall.Recall(ctx, recall.Request{
		Query:          in.Query,
		Topics:         in.Topics,
		Limit:          in.Limit,
		IncludeHistory: in.IncludeHistory,
	})
	if err != nil {
		return errorResult(err), nil, nil
	}
	return textResult(res.Format(s.d.Cfg.RecallMaxBytes)), nil, nil
}

// getInput is the input of the get tool.
type getInput struct {
	IDs []int64 `json:"ids" jsonschema:"Ids de los registros (máx. 20)"`
}

func (s *Server) handleGet(ctx context.Context, req *mcp.CallToolRequest, in getInput) (*mcp.CallToolResult, any, error) {
	if _, err := s.begin(ctx, req); err != nil {
		return errorResult(err), nil, nil
	}
	records, err := s.d.Memory.Get(ctx, in.IDs)
	if err != nil {
		return errorResult(err), nil, nil
	}
	return textResult(formatGet(in.IDs, records)), nil, nil
}

// formatGet renders the records separated by a blank line, followed by the
// requested ids that do not exist.
func formatGet(ids []int64, records []memory.Full) string {
	found := make(map[int64]bool, len(records))
	blocks := make([]string, 0, len(records)+1)
	for _, f := range records {
		found[f.ID] = true
		blocks = append(blocks, formatFull(f))
	}
	var missing []string
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if !found[id] && !seen[id] {
			missing = append(missing, fmt.Sprintf("#%d", id))
		}
		seen[id] = true
	}
	if len(missing) > 0 {
		blocks = append(blocks, "no encontrados: "+strings.Join(missing, ", "))
	}
	return strings.Join(blocks, "\n\n")
}

var kindLabels = map[string]string{
	"fact":       "dato",
	"preference": "preferencia",
	"decision":   "decisión",
	"event":      "evento",
	"note":       "nota",
}

var statusLabels = map[string]string{
	"active":      "vigente",
	"superseded":  "sustituido",
	"invalidated": "invalidado",
}

var stateLabels = map[string]string{
	"suggested": "sugerida",
	"confirmed": "confirmada",
	"rejected":  "rechazada",
}

// relationPhrases maps a relation kind to its active phrase (this record is the
// source) and its passive phrase (this record is the target).
var relationPhrases = map[string][2]string{
	"supersedes":  {"sustituye a", "sustituido por"},
	"contradicts": {"contradice a", "contradicho por"},
	"related":     {"relacionado con", "relacionado con"},
	"part_of":     {"parte de", "incluye a"},
}

func label(m map[string]string, key string) string {
	if v, ok := m[key]; ok {
		return v
	}
	return key
}

// formatFull renders one record, omitting empty lines:
//
//	#123 [decisión · vigente · 2026-09-28] Título
//	temas: a, b · clave: x
//	<contenido>
//	relaciones: sustituye a #45 «t» (confirmada); sustituido por #9 «u» (confirmada)
func formatFull(f memory.Full) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d [%s · %s · %s] %s", f.ID,
		label(kindLabels, string(f.Kind)), label(statusLabels, string(f.Status)),
		f.OccurredAt.Local().Format("2006-01-02"), oneLine(f.Title))

	var meta []string
	if len(f.Topics) > 0 {
		meta = append(meta, "temas: "+strings.Join(f.Topics, ", "))
	}
	if f.Key != "" {
		meta = append(meta, "clave: "+f.Key)
	}
	if len(meta) > 0 {
		b.WriteString("\n" + strings.Join(meta, " · "))
	}
	if c := strings.TrimSpace(f.Content); c != "" {
		b.WriteString("\n" + c)
	}
	if len(f.Relations) > 0 {
		rels := make([]string, len(f.Relations))
		for i, r := range f.Relations {
			phrase := r.Kind
			if p, ok := relationPhrases[r.Kind]; ok {
				if r.Outgoing {
					phrase = p[0]
				} else {
					phrase = p[1]
				}
			}
			rels[i] = fmt.Sprintf("%s #%d «%s» (%s)", phrase, r.OtherID, oneLine(r.OtherTitle), label(stateLabels, r.State))
		}
		b.WriteString("\nrelaciones: " + strings.Join(rels, "; "))
	}
	return b.String()
}
