package mcptools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Límites de la tool tags_index.
const (
	// tagsIndexDefaultLimit es el límite por defecto cuando el caller no lo indica.
	tagsIndexDefaultLimit = 1000
	// tagsIndexMaxLimit es el tope duro para evitar respuestas enormes.
	tagsIndexMaxLimit = 5000
	// tagsIndexDBTimeout acota la consulta SQL para que no supere la paciencia
	// del modelo en una llamada de descubrimiento.
	tagIndexDBTimeout = 5 * time.Second
)

// tagsIndexInput es la entrada de la herramienta tags_index. Scope filtra por la
// etiqueta general ("trabajo" o "personal"); vacío = todas. Limit acota el
// número de filas devueltas (por defecto 1000, máximo 5000).
type tagsIndexInput struct {
	Scope string `json:"scope,omitempty" jsonschema:"Filtro por cobertura general (vacío = todas); sólo cuentan los registros vigentes que lleven esa etiqueta"`
	Limit int    `json:"limit,omitempty" jsonschema:"Máximo de filas devueltas (por defecto 1000, máximo 5000)"`
}

// handleTagsIndex implementa la herramienta tags_index (S15). Devuelve el árbol
// de tags concretas (las 12 familias de D2) con jerarquía y conteos de registros
// vigentes, opcionalmente filtrado por la etiqueta general "trabajo" o
// "personal".
//
// Sólo lectura: marca ReadOnlyHint en el registro.
func (s *Server) handleTagsIndex(ctx context.Context, _ *mcp.CallToolRequest, in tagsIndexInput) (*mcp.CallToolResult, any, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = tagsIndexDefaultLimit
	}
	if limit > tagsIndexMaxLimit {
		limit = tagsIndexMaxLimit
	}
	scope := strings.TrimSpace(in.Scope)
	if scope != "" && scope != "trabajo" && scope != "personal" {
		return errorResult(fmt.Errorf("scope debe ser \"trabajo\", \"personal\" o vacío")), nil, nil
	}
	text, err := s.formatTagsIndex(ctx, scope, limit)
	if err != nil {
		return errorResult(err), nil, nil
	}
	return textResult(text), nil, nil
}

// formatTagsIndex ejecuta la consulta y devuelve el árbol en texto plano.
// Exportado en minúscula (paquete) porque sólo lo usa handleTagsIndex.
func (s *Server) formatTagsIndex(ctx context.Context, scope string, limit int) (string, error) {
	if s.d.Pool == nil {
		return "no hay conexión a la base de datos", nil
	}
	ctx, cancel := context.WithTimeout(ctx, tagIndexDBTimeout)
	defer cancel()

	rows, err := s.d.Pool.Query(ctx, tagsIndexSQL, scope, limit)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var entries []tagIndexEntry
	for rows.Next() {
		var e tagIndexEntry
		if err := rows.Scan(&e.Slug, &e.Count); err != nil {
			return "", err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return renderTagsTree(entries), nil
}

// tagIndexEntry es un par (slug, count) tal y como sale de la consulta.
type tagIndexEntry struct {
	Slug  string
	Count int
}

// tagsIndexSQL cuenta, por cada etiqueta concreta de las 12 familias, los
// registros vigentes (memories.status='active') que la llevan. Si $1 (scope) no
// está vacío, sólo cuentan los registros que también lleven esa etiqueta
// general como tag propia.
//
// La expresión regular ^(proyecto|persona|...)-[a-z0-9] acepta las 12 familias
// definidas en D2 §3, dejando fuera las generales (general, trabajo, personal,
// arquitectura, lodan, odd, runbook, recurso) y los slug sueltos.
const tagsIndexSQL = `
SELECT t.slug,
       count(DISTINCT mt.memory_id) AS n
FROM memory_topics mt
JOIN topics t ON t.id = mt.topic_id
JOIN memories m ON m.id = mt.memory_id AND m.status = 'active'
WHERE mt.active = true
  AND t.slug ~ '^(proyecto|persona|contacto|proveedor|cliente|finca|vehiculo|coche|skill|herramienta|recurso|evento)-[a-z0-9]'
  AND ($1 = '' OR EXISTS (
        SELECT 1 FROM memory_topics mt2
        JOIN topics t2 ON t2.id = mt2.topic_id
        WHERE mt2.memory_id = mt.memory_id
          AND mt2.active = true
          AND t2.slug = $1
      ))
GROUP BY t.slug
ORDER BY t.slug
LIMIT $2
`

// renderTagsTree pinta el árbol en texto plano. Cada slug va en su propia
// línea con la sangría que corresponde a su profundidad dentro del árbol de
// jerarquías (D2 §5). Si la lista está vacía devuelve un mensaje claro.
func renderTagsTree(entries []tagIndexEntry) string {
	if len(entries) == 0 {
		return "no hay tags concretas registradas"
	}
	known := make(map[string]bool, len(entries))
	for _, e := range entries {
		known[e.Slug] = true
	}
	var b strings.Builder
	for _, e := range entries {
		depth := depthOf(e.Slug, known)
		b.WriteString(strings.Repeat("  ", depth))
		fmt.Fprintf(&b, "%s (%d)\n", e.Slug, e.Count)
	}
	return strings.TrimRight(b.String(), "\n")
}

// depthOf devuelve la profundidad visual del slug dentro del árbol: 0 si es
// raíz (sin `/` o cuyo padre no está en el índice), 1 si su padre directo
// está en el índice, hasta el máximo de 3 atributos de D2 §5.
//
//	coche-volvo-s60                  -> 0
//	coche-volvo-s60/matricula        -> 1
//	coche-volvo-s60/a/b              -> 2 (si los tres están en el índice)
//	coche-volvo-s60/x (huérfano)     -> 0 (porque coche-volvo-s60 no está)
func depthOf(slug string, known map[string]bool) int {
	if !strings.Contains(slug, "/") {
		return 0
	}
	parent := slug
	depth := 0
	for {
		idx := strings.LastIndex(parent, "/")
		if idx < 0 {
			return depth
		}
		parent = parent[:idx]
		depth++
		if known[parent] || depth >= 3 {
			return depth
		}
	}
}
