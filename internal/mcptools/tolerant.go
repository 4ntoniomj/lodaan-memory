package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tolerant argument decoding.
//
// Why: the model that calls a lodan tool never sees the Go structs, it only sees
// the tool description, so it guesses the shape. Measured in 189 pi sessions
// (2026-11-06 to 2026-11-14): 162 tool calls were rejected by the strict schema
// before reaching the handler — arrays sent as {"item": [...]} or as a plain
// string, tags as a map, Spanish kinds ("decisión", "evento"), revise ops with
// target/source instead of with_id, ids written "#3024", or extra fields. Every
// rejection was a lost memory or a lost recall, never a real domain error.
//
// The fix keeps the published schema (it still guides the model) but decodes the
// arguments tolerantly: the same normalization is applied to each tool input, the
// unknown properties are dropped instead of failing, and the handler always runs.

// fieldAliases maps a canonical JSON field of a tool input to the names models
// use for it. An alias is applied only when the input struct has the canonical
// field and the alias is not itself a field of that struct.
var fieldAliases = map[string][]string{
	"title":       {"titulo", "título", "name", "subject", "encabezado"},
	"content":     {"contenido", "text", "texto", "body", "description", "descripcion", "descripción"},
	"kind":        {"type", "tipo", "category", "categoria", "categoría"},
	"tags":        {"etiquetas", "labels"},
	"topics":      {"temas"},
	"key":         {"clave"},
	"occurred_at": {"occurredat", "occurred", "when", "fecha", "date", "cuando", "cuándo"},
	"items":       {"registros", "records", "entries", "memorias"},
	"queries":     {"preguntas", "consultas", "search_queries"},
	"ids":         {"record_ids", "lista_ids", "id_list"},
	"with_id":     {"target", "target_id", "to", "from", "source", "source_id", "other", "other_id", "with", "withid", "destino", "otro", "related_id", "b_id"},
	"with_tag":    {"canonical", "canonical_tag", "canonicaltag", "target_tag", "tag_canonica"},
	"action":      {"accion", "acción", "op", "operation", "operacion", "operación"},
	"relation":    {"rel", "relation_type", "tipo_relacion", "tipo_relación"},
	"limit":       {"max", "maximo", "máximo"},
	"preview":     {"dry_run", "dryrun", "solo_ver"},
	"confirm":     {"commit", "confirmar"},
	"force":       {"forzar"},
	"mode":        {"modo"},
}

// structField is one JSON field of a tool input struct.
type structField struct {
	name    string
	typ     reflect.Type
	aliases []string
}

var fieldCache sync.Map // reflect.Type -> []structField

// structFields returns the JSON fields of a tool input struct, with their
// aliases. The result is cached: the tools run on every call of every client.
func structFields(t reflect.Type) []structField {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if v, ok := fieldCache.Load(t); ok {
		return v.([]structField)
	}
	fields := make([]structField, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Tag.Get("json")
		if i := strings.IndexByte(name, ','); i >= 0 {
			name = name[:i]
		}
		if name == "" || name == "-" {
			name = f.Name
		}
		fields = append(fields, structField{name: name, typ: f.Type, aliases: fieldAliases[name]})
	}
	fieldCache.Store(t, fields)
	return fields
}

// addTool registers tool on the MCP server with a tolerant decoder. The
// arguments are cleaned and coerced before the typed handler runs, so a model
// that guesses the shape still gets an answer instead of a schema error.
func addTool[T any](srv *mcp.Server, tool *mcp.Tool, h mcp.ToolHandlerFor[T, any]) {
	target := reflect.TypeFor[T]()
	srv.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := CoerceArguments(tool.Name, target, req.Params.Arguments)
		var in T
		if len(args) > 0 {
			if err := json.Unmarshal(args, &in); err != nil {
				return errorResult(fmt.Errorf("%s: argumentos no válidos: %v", tool.Name, err)), nil
			}
		}
		res, _, err := h(ctx, req, in)
		if err != nil {
			return errorResult(err), nil
		}
		return res, nil
	})
}

// CoerceArguments returns the arguments of a tool call ready to be decoded into
// target: wrapped arrays unwrapped, scalars widened, aliases renamed, known
// values normalized and unknown properties dropped. When the arguments cannot be
// read at all they are returned untouched, so the handler reports the real error.
func CoerceArguments(tool string, target reflect.Type, raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	v = unwrapStringJSON(v)
	if m, ok := v.(map[string]any); ok {
		v = normalizeRoot(tool, m)
	}
	out := coerceTo(target, v)
	b, err := json.Marshal(out)
	if err != nil {
		return raw
	}
	return b
}

// unwrapStringJSON decodes values sent as a JSON string, the shape some clients
// produce when they nest the arguments twice or wrap an array in a string.
func unwrapStringJSON(v any) any {
	switch t := v.(type) {
	case string:
		trimmed := strings.TrimSpace(t)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var inner any
			if err := json.Unmarshal([]byte(trimmed), &inner); err == nil {
				return unwrapStringJSON(inner)
			}
		}
		return t
	case map[string]any:
		for k, e := range t {
			t[k] = unwrapStringJSON(e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = unwrapStringJSON(e)
		}
		return t
	}
	return v
}

// normalizeRoot fixes the shape of the whole call: remember accepts a single
// record written flat at the root instead of inside items.
func normalizeRoot(tool string, m map[string]any) any {
	if tool != "remember" {
		return m
	}
	if _, ok := m["items"]; ok {
		return m
	}
	if _, ok := m["registros"]; ok {
		return m
	}
	if !hasItemShape(m) {
		return m
	}
	item := make(map[string]any, len(m))
	for k, v := range m {
		if k == "preview" {
			continue
		}
		item[k] = v
	}
	out := map[string]any{"items": []any{item}}
	if p, ok := m["preview"]; ok {
		out["preview"] = p
	}
	return out
}

// hasItemShape reports whether m carries the fields of one record.
func hasItemShape(m map[string]any) bool {
	for _, k := range []string{"content", "contenido", "text", "texto", "title", "titulo", "título"} {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

// coerceTo converts v to the shape of t.
func coerceTo(t reflect.Type, v any) any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		m, ok := asMap(v)
		if !ok {
			return v
		}
		return coerceStruct(t, m)
	case reflect.Slice:
		return coerceSlice(t.Elem(), v)
	case reflect.String:
		return asString(v)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return asInt(v)
	case reflect.Float32, reflect.Float64:
		return asFloat(v)
	case reflect.Bool:
		return asBool(v)
	}
	return v
}

// arrayWrappers are the keys models use to wrap a list they send as an object.
var arrayWrappers = map[string]bool{"item": true, "items": true, "values": true, "value": true, "list": true, "lista": true, "elements": true, "datos": true}

// coerceSlice converts v to a slice of elem.
func coerceSlice(elem reflect.Type, v any) any {
	var arr []any
	switch t := v.(type) {
	case nil:
		return []any{}
	case []any:
		arr = t
	case map[string]any:
		if len(t) == 1 {
			for k, inner := range t {
				if arrayWrappers[strings.ToLower(k)] {
					return coerceSlice(elem, inner)
				}
			}
		}
		arr = []any{t}
	case string:
		trimmed := strings.TrimSpace(t)
		if trimmed == "" {
			return []any{}
		}
		if strings.HasPrefix(trimmed, "[") {
			var inner []any
			if err := json.Unmarshal([]byte(trimmed), &inner); err == nil {
				arr = inner
				break
			}
		}
		parts := strings.Split(trimmed, ",")
		arr = make([]any, len(parts))
		for i, p := range parts {
			arr[i] = strings.TrimSpace(p)
		}
	default:
		arr = []any{v}
	}
	out := make([]any, 0, len(arr))
	for _, e := range arr {
		if e == nil {
			continue
		}
		out = append(out, coerceTo(elem, e))
	}
	return out
}

// asMap reads v as an object, decoding the objects models send as a JSON
// string. The single-key wrappers are unwrapped by coerceSlice and asString,
// never here: {"items": {...}} is a one-field object, not a wrapper.
func asMap(v any) (map[string]any, bool) {
	switch t := v.(type) {
	case map[string]any:
		return t, true
	case string:
		trimmed := strings.TrimSpace(t)
		if strings.HasPrefix(trimmed, "{") {
			var inner map[string]any
			if err := json.Unmarshal([]byte(trimmed), &inner); err == nil {
				return inner, true
			}
		}
	}
	return nil, false
}

// coerceStruct keeps the known JSON fields of t, applies the aliases of the
// missing ones and drops the rest. Values are normalized by field name.
func coerceStruct(t reflect.Type, m map[string]any) map[string]any {
	fields := structFields(t)
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		if val, ok := m[f.name]; ok && val != nil {
			out[f.name] = normalizeField(f.name, coerceTo(f.typ, val))
			continue
		}
		for _, a := range f.aliases {
			if val, ok := m[a]; ok && val != nil {
				out[f.name] = normalizeField(f.name, coerceTo(f.typ, val))
				break
			}
		}
	}
	return out
}

// normalizeField fixes the value of a field whose vocabulary is closed: the
// record kind, the revise actions and the relation kinds.
func normalizeField(name string, v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	switch name {
	case "kind":
		return normKind(s)
	case "action":
		return normAction(s)
	case "relation":
		return normRelation(s)
	}
	return v
}

// fold lowercases s and removes the accents, so "decisión" and "decision" match.
func fold(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch r {
		case 'á', 'à', 'ä', 'â':
			b.WriteRune('a')
		case 'é', 'è', 'ë', 'ê':
			b.WriteRune('e')
		case 'í', 'ì', 'ï', 'î':
			b.WriteRune('i')
		case 'ó', 'ò', 'ö', 'ô':
			b.WriteRune('o')
		case 'ú', 'ù', 'ü', 'û':
			b.WriteRune('u')
		case 'ñ':
			b.WriteRune('n')
		default:
			if unicode.IsSpace(r) {
				b.WriteRune('_')
				continue
			}
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "_")
}

// kindSynonyms are the words models use for the five record kinds.
var kindSynonyms = map[string]string{
	"fact": "fact", "dato": "fact", "hecho": "fact", "facts": "fact", "info": "fact",
	"preference": "preference", "preferencia": "preference", "gusto": "preference",
	"decision": "decision", "regla": "decision", "convencion": "decision",
	"event": "event", "evento": "event", "suceso": "event", "hito": "event",
	"note": "note", "nota": "note", "notes": "note", "apunte": "note",
}

// normKind maps the kind of a record to the five values lodan stores. A kind
// nobody knows is left as written: the handler rejects it and names the valid
// ones, so the model corrects it instead of losing the record silently.
func normKind(s string) string {
	if k, ok := kindSynonyms[fold(s)]; ok {
		return k
	}
	return strings.TrimSpace(s)
}

// actionSynonyms are the words models use for the revise and session actions.
var actionSynonyms = map[string]string{
	"update": "update", "actualizar": "update", "modificar": "update", "editar": "update", "update_": "update",
	"supersede": "supersede", "sustituir": "supersede", "reemplazar": "supersede", "substituir": "supersede",
	"invalidate": "invalidate", "invalidar": "invalidate", "invalidar_": "invalidate",
	"delete": "delete", "borrar": "delete", "eliminar": "delete", "remove": "delete",
	"relate": "relate", "relacionar": "relate", "link": "relate", "conectar": "relate",
	"confirm_relation": "confirm_relation", "confirmar_relacion": "confirm_relation",
	"reject_relation": "reject_relation", "rechazar_relacion": "reject_relation",
	"alias": "alias", "add_alias": "alias", "aniadir_alias": "alias",
	"reject_alias": "reject_alias", "rechazar_alias": "reject_alias",
	"verdict": "verdict", "veredicto": "verdict", "juzgar": "verdict",
	"merge": "merge", "juntar": "merge", "fusionar": "merge", "unir": "merge",
	"last": "last", "ultima": "last", "reciente": "last", "latest": "last",
	"list": "list", "lista": "list", "listar": "list", "todas": "list",
	"end": "end", "cerrar": "end", "fin": "end", "terminar": "end", "close": "end",
}

// normAction maps a revise or session action to the value lodan understands.
func normAction(s string) string {
	if a, ok := actionSynonyms[fold(s)]; ok {
		return a
	}
	return strings.TrimSpace(s)
}

// relationSynonyms are the words models use for the four relation kinds.
var relationSynonyms = map[string]string{
	"related": "related", "relacionado": "related", "related_": "related",
	"contradicts": "contradicts", "contradice": "contradicts", "contradiccion": "contradicts",
	"part_of": "part_of", "parte_de": "part_of", "pertenece_a": "part_of",
	"supersedes": "supersedes", "sustituye": "supersedes", "reemplaza": "supersedes",
}

// normRelation maps a relation kind to the four values lodan understands.
func normRelation(s string) string {
	if r, ok := relationSynonyms[fold(s)]; ok {
		return r
	}
	return strings.TrimSpace(s)
}

// asString reads v as text: objects are unwrapped, numbers and booleans printed.
func asString(v any) any {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case map[string]any:
		if m, ok := asMap(t); ok && len(m) == 1 {
			for _, inner := range m {
				return asString(inner)
			}
		}
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	case []any:
		if len(t) == 0 {
			return ""
		}
		return asString(t[0])
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// asInt reads v as an integer, accepting strings like "12", "#12" or "12 ".
func asInt(v any) any {
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		return int64(t)
	case string:
		s := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(t), "#"))
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(f)
		}
	}
	return v
}

// asFloat reads v as a float, accepting numeric strings.
func asFloat(v any) any {
	switch t := v.(type) {
	case nil:
		return 0.0
	case float64:
		return t
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return f
		}
	}
	return v
}

// asBool reads v as a boolean, accepting "true", "sí", "yes" and 1.
func asBool(v any) any {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		switch fold(t) {
		case "true", "si", "yes", "y", "1", "verdadero":
			return true
		case "false", "no", "0", "falso", "":
			return false
		}
	}
	return v
}
