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
	Query          string   `json:"query,omitempty" jsonschema:"Pregunta o tema en lenguaje natural (puede ser vacía si hay filtros)"`
	Queries        []string `json:"queries,omitempty" jsonschema:"Varias preguntas en una sola llamada (máx. 200 en total); cada una se responde en su bloque"`
	Topics         []string `json:"topics,omitempty" jsonschema:"Temas conocidos a priorizar (opcional; si no, se detectan)"`
	Limit          int      `json:"limit,omitempty" jsonschema:"Resultados por pregunta (por defecto 8, máx. 20)"`
	IncludeHistory bool     `json:"include_history,omitempty" jsonschema:"Incluir registros sustituidos e invalidados"`
	TagsAll        []string `json:"tags_all,omitempty" jsonschema:"AND de etiquetas: el registro debe tener TODAS. Tags inexistentes se ignoran; si todas son inválidas, error."`
	TagsAny        []string `json:"tags_any,omitempty" jsonschema:"OR de etiquetas: el registro debe tener AL MENOS UNA. Tags inexistentes se ignoran."`
	DateFrom       string   `json:"date_from,omitempty" jsonschema:"Fecha mínima (inclusive) en RFC3339 o AAAA-MM-DD; default sin límite"`
	DateTo         string   `json:"date_to,omitempty" jsonschema:"Fecha máxima (inclusive) en RFC3339 o AAAA-MM-DD; default sin límite"`
}

func (s *Server) handleRecall(ctx context.Context, req *mcp.CallToolRequest, in recallInput) (*mcp.CallToolResult, any, error) {
	queries, err := recallQueries(in)
	if err != nil {
		return errorResult(err), nil, nil
	}
	if _, err := s.begin(ctx, req); err != nil {
		return errorResult(err), nil, nil
	}
	base := recall.Request{
		Topics:         in.Topics,
		Limit:          in.Limit,
		IncludeHistory: in.IncludeHistory,
		TagsAll:        in.TagsAll,
		TagsAny:        in.TagsAny,
		DateFrom:       in.DateFrom,
		DateTo:         in.DateTo,
	}
	if len(queries) > 1 {
		return textResult(s.recallBatch(ctx, queries, base)), nil, nil
	}
	if len(queries) == 1 {
		base.Query = queries[0]
	}
	// len(queries) == 0 con filtros: base.Query se queda "" y Recall devuelve
	// los registros que casen con los filtros, sin pasar por la búsqueda de texto.
	res, err := s.d.Recall.Recall(ctx, base)
	if err != nil {
		return errorResult(err), nil, nil
	}
	return textResult(res.Format(s.d.Cfg.RecallMaxBytes)), nil, nil
}

// getInput is the input of the get tool.
type getInput struct {
	IDs []int64 `json:"ids" jsonschema:"Ids de los registros (máx. 200)"`
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
//	etiquetas: #a, #b · clave: x
//	<contenido>
//	relaciones: sustituye a #45 «t» (confirmada); relacionado con #9 «u» (confirmada; veredicto compatible, confianza 0.90)
func formatFull(f memory.Full) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d [%s · %s · %s] %s", f.ID,
		label(kindLabels, string(f.Kind)), label(statusLabels, string(f.Status)),
		f.OccurredAt.Local().Format("2006-01-02"), oneLine(f.Title))

	var meta []string
	if len(f.Topics) > 0 {
		meta = append(meta, "etiquetas: #"+strings.Join(f.Topics, ", #"))
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
			state := label(stateLabels, r.State)
			if r.Verdict != "" {
				state += "; veredicto " + r.Verdict
				if r.Confidence != nil {
					state += fmt.Sprintf(", confianza %.2f", *r.Confidence)
				}
			}
			rels[i] = fmt.Sprintf("%s #%d «%s» (%s)", phrase, r.OtherID, oneLine(r.OtherTitle), state)
		}
		b.WriteString("\nrelaciones: " + strings.Join(rels, "; "))
	}
	return b.String()
}
