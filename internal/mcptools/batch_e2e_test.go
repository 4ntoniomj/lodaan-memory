package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/database"
	"lodan/internal/embedding"
	"lodan/internal/memory"
	"lodan/internal/recall"
	"lodan/internal/session"
	"lodan/internal/topic"
)

const getHintLine = "Usa get con los ids para ver el detalle."

// countingEmbedder wraps the fake embedder and counts the calls that compute
// embeddings (single queries and document batches).
type countingEmbedder struct {
	*embedding.FakeEmbedder
	calls atomic.Int64
}

func (c *countingEmbedder) EmbedQuery(ctx context.Context, q string) ([]float32, error) {
	c.calls.Add(1)
	return c.FakeEmbedder.EmbedQuery(ctx, q)
}

func (c *countingEmbedder) EmbedDocuments(ctx context.Context, docs []embedding.Document) ([][]float32, error) {
	c.calls.Add(1)
	return c.FakeEmbedder.EmbedDocuments(ctx, docs)
}

// newStackWith is newStack with a caller-provided embedder.
func newStackWith(tc *database.TestCluster, emb embedding.Embedder) (Deps, *Server) {
	cfg := tc.Cfg
	sessions := session.NewManager(tc.Pool, time.Duration(cfg.SessionIdleMinutes)*time.Minute)
	topics := topic.NewResolver(tc.Pool, emb, cfg.TopicSimilarity)
	d := Deps{
		Pool:     tc.Pool,
		Memory:   memory.NewService(tc.Pool, emb, topics, sessions, cfg.DupSimilarity, time.Duration(cfg.EmbedTimeoutMs)*time.Millisecond),
		Recall:   recall.NewService(tc.Pool, emb, topics, recall.Options{MaxBytes: cfg.RecallMaxBytes, TopicDetectThreshold: cfg.TopicDetectSimilarity}),
		Sessions: sessions,
		Topics:   topics,
		Embedder: emb,
		Cfg:      cfg,
		Version:  "test",
	}
	return d, NewServer(d)
}

func itemAt(title, content, kind, day string, topics ...string) map[string]any {
	it := item(title, content, kind, topics...)
	it["occurred_at"] = day
	return it
}

func relateTo(pairs ...any) []any {
	var out []any
	for i := 0; i < len(pairs); i += 2 {
		r := map[string]any{"id": pairs[i]}
		if rel, _ := pairs[i+1].(string); rel != "" {
			r["relation"] = rel
		}
		out = append(out, r)
	}
	return out
}

// saveOne saves one item and returns its id as a string.
func saveOne(t *testing.T, cs *mcp.ClientSession, it map[string]any) string {
	t.Helper()
	return firstID(t, remember(t, cs, it))
}

func idInt(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	if _, err := fmt.Sscan(s, &n); err != nil {
		t.Fatalf("id %q no es un número", s)
	}
	return n
}

// recallLine returns the line of a recall answer that describes record id.
func recallLine(t *testing.T, cs *mcp.ClientSession, query string, topics []string, id string) string {
	t.Helper()
	out := mustOK(t, cs, "recall", map[string]any{"query": query, "topics": topics, "limit": 20})
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "#"+id+" [") {
			return l
		}
	}
	t.Fatalf("recall %q no muestra el registro #%s:\n%s", query, id, out)
	return ""
}

// reviewLines returns the `  revisa: ` lines of a remember answer.
func reviewLines(out string) []string {
	var res []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "  revisa: ") {
			res = append(res, l)
		}
	}
	return res
}

var reviewIDRe = regexp.MustCompile(`^  revisa: #(\d+) \[`)

func reviewIDs(t *testing.T, out string) map[string]bool {
	t.Helper()
	ids := map[string]bool{}
	for _, l := range reviewLines(out) {
		m := reviewIDRe.FindStringSubmatch(l)
		if m == nil {
			t.Errorf("línea de candidato con formato inesperado: %q", l)
			continue
		}
		ids[m[1]] = true
	}
	return ids
}

// splitBatch checks the shape of a multi-query recall answer (one trailing
// hint, blocks separated by a blank line) and returns the blocks.
func splitBatch(t *testing.T, out string) []string {
	t.Helper()
	if n := strings.Count(out, getHintLine); n != 1 {
		t.Errorf("la pista %q aparece %d veces, quería 1:\n%s", getHintLine, n, out)
	}
	if !strings.HasSuffix(out, "\n"+getHintLine) {
		t.Errorf("la pista no es la última línea:\n%s", out)
	}
	body := strings.TrimSuffix(out, "\n"+getHintLine)
	return strings.Split(body, "\n\n")
}

// singleBody is the answer of a one-query recall without its closing hint.
func singleBody(t *testing.T, cs *mcp.ClientSession, args map[string]any) string {
	t.Helper()
	out := mustOK(t, cs, "recall", args)
	if !strings.HasSuffix(out, "\n"+getHintLine) {
		t.Fatalf("la respuesta de una consulta no acaba con la pista:\n%s", out)
	}
	return strings.TrimSuffix(out, "\n"+getHintLine)
}

func batchLines(out string) []string { return strings.Split(out, "\n") }

func TestBatches(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)

	run := func(name string, f func(t *testing.T, cs *mcp.ClientSession)) {
		t.Run(name, func(t *testing.T) {
			tc.Reset(t)
			_, srv := newStack(tc)
			f(t, connect(t, srv, "test-client"))
		})
	}

	seedHome := func(t *testing.T, cs *mcp.ClientSession) {
		remember(t, cs,
			item("Matrícula del coche", "La matrícula del coche es 1234 ABC", "fact", "coche"),
			item("Seguro del coche", "El seguro del coche vence en marzo", "fact", "coche"),
			item("Alquiler de la casa", "El alquiler de la casa son 800 euros al mes", "fact", "casa"),
			item("Wifi de la casa", "La clave del wifi de la casa es tortuga", "fact", "casa"),
		)
	}

	// ---------------------------------------------------------------- S3

	run("S3 anotaciones de solo lectura y esquemas", func(t *testing.T, cs *mcp.ClientSession) {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		res, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
		if err != nil {
			t.Fatalf("ListTools: %v", err)
		}
		readOnly := map[string]bool{"recall": true, "get": true, "status": true, "tags_index": true}
		raw := map[string]string{}
		for _, tool := range res.Tools {
			got := tool.Annotations != nil && tool.Annotations.ReadOnlyHint
			if got != readOnly[tool.Name] {
				t.Errorf("%s: readOnlyHint = %v, quería %v", tool.Name, got, readOnly[tool.Name])
			}
			b, _ := json.Marshal(tool)
			raw[tool.Name] = string(b)
		}
		// S1: the new inputs exist and the descriptions announce the cap of 200.
		for tool, prop := range map[string]string{"recall": `"queries"`, "revise": `"ops"`, "session": `"ops"`, "remember": `"relate"`} {
			if !strings.Contains(raw[tool], prop) {
				t.Errorf("el esquema de %s no tiene %s: %s", tool, prop, raw[tool])
			}
		}
		for _, tool := range []string{"recall", "get", "remember", "revise", "session"} {
			if !strings.Contains(raw[tool], "200") {
				t.Errorf("el esquema o la descripción de %s no menciona el máximo 200: %s", tool, raw[tool])
			}
		}
		for _, tool := range []string{"get", "remember"} {
			for _, old := range []string{"(máx. 20)", "(1-20)"} {
				if strings.Contains(raw[tool], old) {
					t.Errorf("%s sigue anunciando el máximo antiguo %s: %s", tool, old, raw[tool])
				}
			}
		}
	})

	t.Run("S3 los embeddings de un lote de recall van en llamadas por lotes", func(t *testing.T) {
		tc.Reset(t)
		emb := &countingEmbedder{FakeEmbedder: (&embedding.FakeEmbedder{Dimensions: testDims}).WithAlias("gym", "entrenamiento")}
		_, srv := newStackWith(tc, emb)
		cs := connect(t, srv, "test-client")
		seedHome(t, cs)

		const n = 10
		queries := make([]any, n)
		for i := range queries {
			queries[i] = fmt.Sprintf("consulta distinta número %d sobre el coche y la casa", i)
		}
		before := emb.calls.Load()
		out := mustOK(t, cs, "recall", map[string]any{"queries": queries})
		calls := emb.calls.Load() - before
		if calls >= n {
			t.Errorf("%d consultas sin caché usaron %d llamadas al embedder; debían ir por lotes (menos que consultas)", n, calls)
		}
		if got := len(splitBatch(t, out)); got != n {
			t.Errorf("%d bloques, quería %d", got, n)
		}
	})

	// ---------------------------------------------------------------- S1 / S2 recall

	run("S1 recall: query y queries, orden, recorte y duplicados", func(t *testing.T, cs *mcp.ClientSession) {
		seedHome(t, cs)
		out := mustOK(t, cs, "recall", map[string]any{
			"query":   "  matrícula del coche ",
			"queries": []any{"alquiler de la casa", "", "   ", "matrícula del coche", "clave del wifi", "alquiler de la casa"},
		})
		blocks := splitBatch(t, out)
		wantHeaders := []string{"## matrícula del coche", "## alquiler de la casa", "## clave del wifi"}
		if len(blocks) != len(wantHeaders) {
			t.Fatalf("%d bloques, quería %d:\n%s", len(blocks), len(wantHeaders), out)
		}
		for i, b := range blocks {
			if first := strings.SplitN(b, "\n", 2)[0]; first != wantHeaders[i] {
				t.Errorf("bloque %d empieza por %q, quería %q", i+1, first, wantHeaders[i])
			}
		}
	})

	run("S2 recall: cada bloque es la respuesta de esa consulta sola, sin pista repetida", func(t *testing.T, cs *mcp.ClientSession) {
		seedHome(t, cs)
		qs := []string{"matrícula del coche", "alquiler de la casa"}
		out := mustOK(t, cs, "recall", map[string]any{"queries": []any{qs[0], qs[1]}})
		blocks := splitBatch(t, out)
		if len(blocks) != 2 {
			t.Fatalf("%d bloques, quería 2:\n%s", len(blocks), out)
		}
		for i, q := range qs {
			want := "## " + q + "\n" + singleBody(t, cs, map[string]any{"query": q})
			if blocks[i] != want {
				t.Errorf("bloque %d =\n%s\nquería\n%s", i+1, blocks[i], want)
			}
		}
	})

	run("S2 recall: con una sola consulta la salida es idéntica a la actual", func(t *testing.T, cs *mcp.ClientSession) {
		seedHome(t, cs)
		q := "matrícula del coche"
		base := mustOK(t, cs, "recall", map[string]any{"query": q})
		if strings.Contains(base, "## ") {
			t.Fatalf("la salida de una consulta no lleva cabecera ##:\n%s", base)
		}
		for name, args := range map[string]map[string]any{
			"queries con una":        {"queries": []any{q}},
			"queries duplicada":      {"queries": []any{q, " " + q + " "}},
			"query y queries igual":  {"query": q, "queries": []any{q}},
			"query y queries vacías": {"query": q, "queries": []any{"", "  "}},
		} {
			if got := mustOK(t, cs, "recall", args); got != base {
				t.Errorf("%s:\n%s\nquería la salida de query:\n%s", name, got, base)
			}
		}
	})

	run("S1 recall: topics, limit e include_history valen para cada consulta", func(t *testing.T, cs *mcp.ClientSession) {
		seedHome(t, cs)
		oldID := saveOne(t, cs, item("Matrícula antigua del coche", "La matrícula antigua del coche era 9876 XYZ", "fact", "coche"))
		mustOK(t, cs, "revise", map[string]any{"id": idInt(t, oldID), "action": "invalidate"})
		qs := []string{"matrícula del coche", "seguro del coche"}

		cases := map[string]map[string]any{
			"topics":          {"topics": []any{"casa"}},
			"limit":           {"limit": 1},
			"include_history": {"include_history": true},
		}
		for name, extra := range cases {
			args := map[string]any{"queries": []any{qs[0], qs[1]}}
			for k, v := range extra {
				args[k] = v
			}
			blocks := splitBatch(t, mustOK(t, cs, "recall", args))
			if len(blocks) != 2 {
				t.Fatalf("%s: %d bloques, quería 2", name, len(blocks))
			}
			for i, q := range qs {
				one := map[string]any{"query": q}
				for k, v := range extra {
					one[k] = v
				}
				want := "## " + q + "\n" + singleBody(t, cs, one)
				if blocks[i] != want {
					t.Errorf("%s, consulta %q:\n%s\nquería\n%s", name, q, blocks[i], want)
				}
			}
		}
		// Guards: the options must actually change the answer, or the test proves nothing.
		plain := singleBody(t, cs, map[string]any{"query": qs[0]})
		if plain == singleBody(t, cs, map[string]any{"query": qs[0], "topics": []any{"casa"}}) {
			t.Error("topics no cambia la respuesta: el caso de prueba no discrimina")
		}
		if plain == singleBody(t, cs, map[string]any{"query": qs[0], "include_history": true}) {
			t.Error("include_history no cambia la respuesta: el caso de prueba no discrimina")
		}
	})

	run("S2 recall: una consulta inválida muestra su error y las demás se responden", func(t *testing.T, cs *mcp.ClientSession) {
		seedHome(t, cs)
		long := strings.Repeat("a", 1001)
		out := mustOK(t, cs, "recall", map[string]any{"queries": []any{"matrícula del coche", long, "alquiler de la casa"}})
		blocks := splitBatch(t, out)
		if len(blocks) != 3 {
			t.Fatalf("%d bloques, quería 3", len(blocks))
		}
		if !strings.HasPrefix(blocks[0], "## matrícula del coche\n") || !strings.Contains(blocks[0], "Matrícula del coche") {
			t.Errorf("el primer bloque no responde a su consulta:\n%s", blocks[0])
		}
		if !strings.HasPrefix(blocks[1], "## "+long) || !strings.Contains(blocks[1], "error: la consulta tiene 1001 caracteres y el máximo es 1000") {
			t.Errorf("el bloque inválido no muestra el error:\n%s", blocks[1])
		}
		if !strings.HasPrefix(blocks[2], "## alquiler de la casa\n") || !strings.Contains(blocks[2], "Alquiler de la casa") {
			t.Errorf("el tercer bloque no responde a su consulta:\n%s", blocks[2])
		}
	})

	run("S1 recall: sin consulta y por encima del máximo", func(t *testing.T, cs *mcp.ClientSession) {
		for name, args := range map[string]map[string]any{
			"nada":              {},
			"query en blanco":   {"query": "   "},
			"queries vacías":    {"queries": []any{"", "  "}},
			"lista queries nil": {"queries": []any{}},
		} {
			text, isErr := call(t, cs, "recall", args)
			if !isErr || text != "hay que enviar query o queries" {
				t.Errorf("%s: IsError=%v texto %q, quería error «hay que enviar query o queries»", name, isErr, text)
			}
		}

		many := func(n int) []any {
			qs := make([]any, n)
			for i := range qs {
				qs[i] = fmt.Sprintf("consulta %d", i)
			}
			return qs
		}
		text, isErr := call(t, cs, "recall", map[string]any{"queries": many(201)})
		if !isErr || text != "se enviaron 201 consultas y el máximo es 200" {
			t.Errorf("201 consultas: IsError=%v texto %q", isErr, text)
		}
		text, isErr = call(t, cs, "recall", map[string]any{"query": "extra", "queries": many(200)})
		if !isErr || text != "se enviaron 201 consultas y el máximo es 200" {
			t.Errorf("query + 200 queries: IsError=%v texto %q", isErr, text)
		}
		text, isErr = call(t, cs, "recall", map[string]any{"queries": many(200)})
		if isErr || len(splitBatch(t, text)) != 200 {
			t.Errorf("200 consultas deberían responderse (IsError=%v)", isErr)
		}
	})

	for _, n := range []int{10, 50} {
		n := n
		run(fmt.Sprintf("S2 recall: tope por bloque con %d consultas", n), func(t *testing.T, cs *mcp.ClientSession) {
			var items []map[string]any
			for i := 0; i < 20; i++ {
				items = append(items, item(
					fmt.Sprintf("Registro alfa número %d con un título bastante largo para ocupar espacio", i),
					fmt.Sprintf("alfa registro %d: ", i)+strings.Repeat("contenido extenso de relleno alfa ", 8),
					"event", "alfa"))
			}
			remember(t, cs, items...)

			maxBytes := tc.Cfg.RecallMaxBytes
			capPerBlock := min(maxBytes, max(4*maxBytes/n, 600))
			args := func(q string) map[string]any {
				return map[string]any{"query": q, "topics": []any{"alfa"}, "limit": 20}
			}
			if full := singleBody(t, cs, args("alfa registro")); len(full) <= capPerBlock {
				t.Fatalf("datos de prueba insuficientes: la respuesta completa (%d bytes) cabe en el tope %d", len(full), capPerBlock)
			}

			qs := make([]any, n)
			for i := range qs {
				qs[i] = fmt.Sprintf("alfa registro %d", i)
			}
			out := mustOK(t, cs, "recall", map[string]any{"queries": qs, "topics": []any{"alfa"}, "limit": 20})
			blocks := splitBatch(t, out)
			if len(blocks) != n {
				t.Fatalf("%d bloques, quería %d", len(blocks), n)
			}
			notice := fmt.Sprintf("… (respuesta recortada al tope de %d bytes)", capPerBlock)
			for i, b := range blocks {
				header := fmt.Sprintf("## alfa registro %d", i)
				if !strings.HasPrefix(b, header+"\n") {
					t.Errorf("bloque %d no empieza por %q", i+1, header)
				}
				if len(b) > capPerBlock+len(header)+1 {
					t.Errorf("bloque %d tiene %d bytes y el tope es %d (+ cabecera)", i+1, len(b), capPerBlock)
				}
				if !strings.Contains(b, notice) {
					t.Errorf("bloque %d no lleva el aviso %q", i+1, notice)
				}
			}
		})
	}

	// ---------------------------------------------------------------- S1 get / remember caps

	run("S1 get: el máximo de ids es 200", func(t *testing.T, cs *mcp.ClientSession) {
		ids := func(n int) []int64 {
			v := make([]int64, n)
			for i := range v {
				v[i] = int64(i + 1)
			}
			return v
		}
		text, isErr := call(t, cs, "get", map[string]any{"ids": ids(201)})
		if !isErr || text != "se pidieron 201 ids y el máximo es 200" {
			t.Errorf("201 ids: IsError=%v texto %q", isErr, text)
		}
		text = mustOK(t, cs, "get", map[string]any{"ids": ids(200)})
		wantContains(t, text, "no encontrados: #1, #2")
	})

	run("S1 remember: el máximo de ítems es 200", func(t *testing.T, cs *mcp.ClientSession) {
		mk := func(n int) []any {
			items := make([]any, n)
			for i := range items {
				items[i] = item(fmt.Sprintf("Nota %d", i), fmt.Sprintf("contenido único de la nota w%dx", i), "note", "lote")
			}
			return items
		}
		text, isErr := call(t, cs, "remember", map[string]any{"items": mk(201)})
		if !isErr || text != "se enviaron 201 ítems y el máximo es 200" {
			t.Errorf("201 ítems: IsError=%v texto %q", isErr, text)
		}
		text = mustOK(t, cs, "remember", map[string]any{"items": mk(200)})
		if got := strings.Count(text, " guardado"); got != 200 {
			t.Errorf("se guardaron %d ítems, quería 200", got)
		}
	})

	// ---------------------------------------------------------------- S1 / S2 revise

	run("S1 revise: top-level y ops, orden, independencia y prefijos", func(t *testing.T, cs *mcp.ClientSession) {
		a := saveOne(t, cs, itemAt("Uno", "contenido del uno", "note", "2026-01-01", "t1"))
		b := saveOne(t, cs, itemAt("Dos", "contenido del dos", "note", "2026-01-02", "t2"))
		c := saveOne(t, cs, itemAt("Tres", "contenido del tres", "note", "2026-01-03", "t3"))

		out := mustOK(t, cs, "revise", map[string]any{
			"id": idInt(t, a), "action": "invalidate",
			"ops": []any{
				map[string]any{"id": idInt(t, b), "action": "invalidate"},
				map[string]any{"id": 9999, "action": "invalidate"},
				map[string]any{"id": idInt(t, c), "action": "update", "title": "Título nuevo", "content": "contenido nuevo del tres"},
			},
		})
		lines := batchLines(out)
		if len(lines) != 4 {
			t.Fatalf("%d líneas, quería 4:\n%s", len(lines), out)
		}
		if !strings.HasPrefix(lines[0], "1. ") || !strings.Contains(lines[0], "#"+a+" invalidado") {
			t.Errorf("línea 1 = %q", lines[0])
		}
		if !strings.HasPrefix(lines[1], "2. ") || !strings.Contains(lines[1], "#"+b+" invalidado") {
			t.Errorf("línea 2 = %q", lines[1])
		}
		if !strings.HasPrefix(lines[2], "3. error: ") || !strings.Contains(lines[2], "#9999 no existe") {
			t.Errorf("línea 3 = %q", lines[2])
		}
		if !strings.HasPrefix(lines[3], "4. ") || strings.HasPrefix(lines[3], "4. error") || !strings.Contains(lines[3], "#"+c) {
			t.Errorf("línea 4 = %q (una operación que falla no debe impedir las siguientes)", lines[3])
		}
		wantContains(t, mustOK(t, cs, "get", map[string]any{"ids": []int64{idInt(t, c)}}), "Título nuevo")
		wantContains(t, mustOK(t, cs, "get", map[string]any{"ids": []int64{idInt(t, b)}}), "invalidado")
	})

	run("S2 revise: IsError solo si todas las operaciones fallan", func(t *testing.T, cs *mcp.ClientSession) {
		text, isErr := call(t, cs, "revise", map[string]any{"ops": []any{
			map[string]any{"id": 9999, "action": "invalidate"},
			map[string]any{"id": 9998, "action": "invalidate"},
		}})
		lines := batchLines(text)
		if !isErr || len(lines) != 2 || !strings.HasPrefix(lines[0], "1. error: ") || !strings.HasPrefix(lines[1], "2. error: ") {
			t.Errorf("todas fallan: IsError=%v texto %q", isErr, text)
		}
	})

	run("S2 revise: una sola operación en ops da la misma salida que el top-level", func(t *testing.T, cs *mcp.ClientSession) {
		a := saveOne(t, cs, item("Uno", "contenido del uno", "note", "t1"))
		b := saveOne(t, cs, item("Dos", "contenido del dos", "note", "t2"))
		top := mustOK(t, cs, "revise", map[string]any{"id": idInt(t, a), "action": "invalidate"})
		viaOps := mustOK(t, cs, "revise", map[string]any{"ops": []any{map[string]any{"id": idInt(t, b), "action": "invalidate"}}})
		if want := strings.ReplaceAll(top, "#"+a, "#"+b); viaOps != want {
			t.Errorf("con ops: %q, quería %q", viaOps, want)
		}

		topErr, topIsErr := call(t, cs, "revise", map[string]any{"id": 9999, "action": "invalidate"})
		opsErr, opsIsErr := call(t, cs, "revise", map[string]any{"ops": []any{map[string]any{"id": 9999, "action": "invalidate"}}})
		if !topIsErr || !opsIsErr || topErr != opsErr {
			t.Errorf("error de una sola operación: top-level %v %q, ops %v %q", topIsErr, topErr, opsIsErr, opsErr)
		}
	})

	run("S1 revise: sin operación y por encima del máximo", func(t *testing.T, cs *mcp.ClientSession) {
		for name, args := range map[string]map[string]any{
			"nada":      {},
			"ops vacío": {"ops": []any{}},
		} {
			text, isErr := call(t, cs, "revise", args)
			if !isErr || text != "hay que enviar una operación (id y action) u ops" {
				t.Errorf("%s: IsError=%v texto %q", name, isErr, text)
			}
		}
		ops := func(n int) []any {
			v := make([]any, n)
			for i := range v {
				v[i] = map[string]any{"id": 999999, "action": "invalidate"}
			}
			return v
		}
		text, isErr := call(t, cs, "revise", map[string]any{"ops": ops(201)})
		if !isErr || text != "se enviaron 201 operaciones y el máximo es 200" {
			t.Errorf("201 operaciones: IsError=%v texto %q", isErr, text)
		}
		text, _ = call(t, cs, "revise", map[string]any{"ops": ops(200)})
		if strings.Contains(text, "el máximo es") || len(batchLines(text)) != 200 {
			t.Errorf("200 operaciones deberían ejecutarse: %d líneas", len(batchLines(text)))
		}
	})

	// ---------------------------------------------------------------- S1 / S2 session

	run("S1 session: top-level y ops, orden, independencia y prefijos", func(t *testing.T, cs *mcp.ClientSession) {
		out := mustOK(t, cs, "session", map[string]any{
			"action": "last",
			"ops": []any{
				map[string]any{"action": "end"},
				map[string]any{"action": "last", "date": "ayer"},
				map[string]any{"action": "end", "summary": "Resumen del lote"},
			},
		})
		lines := batchLines(out)
		if len(lines) != 4 {
			t.Fatalf("%d líneas, quería 4:\n%s", len(lines), out)
		}
		if lines[0] != "1. No hay conversaciones anteriores que coincidan." {
			t.Errorf("línea 1 = %q", lines[0])
		}
		if !strings.HasPrefix(lines[1], "2. error: ") || !strings.Contains(lines[1], "end necesita un resumen") {
			t.Errorf("línea 2 = %q", lines[1])
		}
		if !strings.HasPrefix(lines[2], "3. error: ") || !strings.Contains(lines[2], "ayer") {
			t.Errorf("línea 3 = %q", lines[2])
		}
		if !strings.HasPrefix(lines[3], "4. Sesión cerrada con resumen (id ") {
			t.Errorf("línea 4 = %q", lines[3])
		}
	})

	run("S2 session: IsError solo si todas las operaciones fallan", func(t *testing.T, cs *mcp.ClientSession) {
		text, isErr := call(t, cs, "session", map[string]any{"ops": []any{
			map[string]any{"action": "end"},
			map[string]any{"action": "end", "summary": "  "},
		}})
		lines := batchLines(text)
		if !isErr || len(lines) != 2 || !strings.HasPrefix(lines[0], "1. error: ") || !strings.HasPrefix(lines[1], "2. error: ") {
			t.Errorf("todas fallan: IsError=%v texto %q", isErr, text)
		}
	})

	run("S2 session: una sola operación en ops da la misma salida que el top-level", func(t *testing.T, cs *mcp.ClientSession) {
		top := mustOK(t, cs, "session", map[string]any{"action": "last"})
		viaOps := mustOK(t, cs, "session", map[string]any{"ops": []any{map[string]any{"action": "last"}}})
		if top != "No hay conversaciones anteriores que coincidan." || viaOps != top {
			t.Errorf("top-level %q, ops %q", top, viaOps)
		}
		topErr, topIsErr := call(t, cs, "session", map[string]any{"action": "end"})
		opsErr, opsIsErr := call(t, cs, "session", map[string]any{"ops": []any{map[string]any{"action": "end"}}})
		if !topIsErr || !opsIsErr || topErr != opsErr {
			t.Errorf("error de una sola operación: top-level %v %q, ops %v %q", topIsErr, topErr, opsIsErr, opsErr)
		}
	})

	run("S1 session: sin operación y por encima del máximo", func(t *testing.T, cs *mcp.ClientSession) {
		for name, args := range map[string]map[string]any{
			"nada":      {},
			"ops vacío": {"ops": []any{}},
		} {
			text, isErr := call(t, cs, "session", args)
			if !isErr || text != "hay que enviar una operación (action) u ops" {
				t.Errorf("%s: IsError=%v texto %q", name, isErr, text)
			}
		}
		ops := func(n int) []any {
			v := make([]any, n)
			for i := range v {
				v[i] = map[string]any{"action": "last"}
			}
			return v
		}
		text, isErr := call(t, cs, "session", map[string]any{"ops": ops(201)})
		if !isErr || text != "se enviaron 201 operaciones y el máximo es 200" {
			t.Errorf("201 operaciones: IsError=%v texto %q", isErr, text)
		}
		text = mustOK(t, cs, "session", map[string]any{"ops": ops(200)})
		if got := len(batchLines(text)); got != 200 {
			t.Errorf("200 operaciones dieron %d líneas", got)
		}
	})

	// ---------------------------------------------------------------- S4

	run("S4 revisa: matrícula y seguro del coche (poca similitud, mismo tema)", func(t *testing.T, cs *mcp.ClientSession) {
		first := remember(t, cs, item("Matrícula del coche", "La matrícula del coche es 1234 ABC", "fact", "coche"))
		if len(batchLines(first)) != 1 || strings.Contains(first, "revisa:") {
			t.Errorf("el primer registro no debería tener candidatos:\n%s", first)
		}
		a := firstID(t, first)
		out := remember(t, cs, item("Seguro del coche", "El seguro del coche vence en marzo", "fact", "coche"))
		lines := batchLines(out)
		if len(lines) != 2 || !strings.Contains(lines[0], " guardado") {
			t.Fatalf("salida inesperada:\n%s", out)
		}
		if want := "  revisa: " + recallLine(t, cs, "matrícula del coche", []string{"coche"}, a); lines[1] != want {
			t.Errorf("candidato = %q, quería %q", lines[1], want)
		}
	})

	run("S4 revisa: desayuno (no es solo el coche)", func(t *testing.T, cs *mcp.ClientSession) {
		a := saveOne(t, cs, itemAt("Desayuno del 15", "2026-10-15 desayuné tostadas", "event", "2026-10-15", "desayuno"))
		out := remember(t, cs, itemAt("Desayuno del 15 (churros)", "2026-10-15 desayuné churros", "event", "2026-10-15", "desayuno"))
		lines := batchLines(out)
		if len(lines) != 2 {
			t.Fatalf("salida inesperada:\n%s", out)
		}
		if want := "  revisa: " + recallLine(t, cs, "desayuné tostadas", []string{"desayuno"}, a); lines[1] != want {
			t.Errorf("candidato = %q, quería %q", lines[1], want)
		}
	})

	run("S4 revisa: con 3 o menos registros del tema aparecen todos", func(t *testing.T, cs *mcp.ClientSession) {
		ids := map[string]bool{}
		for _, it := range []map[string]any{
			item("Caldera", "La caldera es de gas natural", "fact", "hogar"),
			item("Luz", "El contrato de luz vence en enero", "fact", "hogar"),
			item("Portero", "El portero se llama Manuel", "fact", "hogar"),
		} {
			ids[saveOne(t, cs, it)] = true
		}
		out := remember(t, cs, item("Lavadora", "La lavadora hace ruido al centrifugar", "note", "hogar"))
		got := reviewIDs(t, out)
		if len(got) != 3 {
			t.Fatalf("candidatos = %v, quería los 3 registros del tema:\n%s", got, out)
		}
		for id := range ids {
			if !got[id] {
				t.Errorf("falta el candidato #%s", id)
			}
		}
	})

	run("S4 revisa: como mucho 6 candidatos", func(t *testing.T, cs *mcp.ClientSession) {
		sentences := []string{
			"El primer libro trata de piratas", "Prestar el segundo libro a Marta", "El tercero está en la estantería alta",
			"Comprar la saga completa en octubre", "El quinto tiene una dedicatoria", "Devolver el sexto a la biblioteca",
			"El séptimo es una novela negra", "Regalé el octavo por navidad", "El noveno lo leo en el tren", "El décimo sigue sin abrir",
		}
		var items []map[string]any
		for i, s := range sentences {
			items = append(items, item(fmt.Sprintf("Libro %d", i+1), s, "note", "libros"))
		}
		remember(t, cs, items...)
		out := remember(t, cs, item("Estantería nueva", "Hay que montar la estantería del salón", "note", "libros"))
		if n := len(reviewLines(out)); n != 6 {
			t.Errorf("%d candidatos, quería 6:\n%s", n, out)
		}
	})

	run("S4 revisa: no son candidatos el propio registro ni los guardados en la misma llamada", func(t *testing.T, cs *mcp.ClientSession) {
		out := remember(t, cs,
			item("Uno", "Primer apunte del cuaderno rojo", "note", "cuaderno"),
			item("Dos", "Segundo apunte del cuaderno rojo", "note", "cuaderno"),
		)
		if strings.Contains(out, "revisa:") {
			t.Errorf("registros guardados en la misma llamada no son candidatos entre sí:\n%s", out)
		}
	})

	run("S4 revisa: no son candidatos los sustituidos ni los invalidados", func(t *testing.T, cs *mcp.ClientSession) {
		old := map[string]any{"title": "Horario", "content": "Entreno a las 7", "kind": "fact", "topics": []string{"deporte"}, "key": "deporte/horario"}
		oldID := saveOne(t, cs, old)
		cur := map[string]any{"title": "Horario", "content": "Ahora entreno por la tarde", "kind": "fact", "topics": []string{"deporte"}, "key": "deporte/horario"}
		out := remember(t, cs, cur)
		wantContains(t, out, "sustituye #"+oldID)
		if strings.Contains(out, "revisa:") {
			t.Errorf("el registro sustituido no es candidato:\n%s", out)
		}

		inv := saveOne(t, cs, item("Cinta de correr", "Hay una cinta de correr en el garaje", "fact", "deporte"))
		mustOK(t, cs, "revise", map[string]any{"id": idInt(t, inv), "action": "invalidate"})
		out = remember(t, cs, item("Pesas", "Las pesas están en el trastero", "fact", "deporte"))
		if got := reviewIDs(t, out); got[inv] || got[oldID] {
			t.Errorf("hay candidatos sustituidos o invalidados: %v\n%s", got, out)
		}
	})

	run("S4 revisa: no son candidatos los registros con relate en la misma llamada", func(t *testing.T, cs *mcp.ClientSession) {
		e := saveOne(t, cs, item("Bici", "La bici es de montaña", "fact", "bici"))
		g := saveOne(t, cs, item("Casco", "El casco de la bici es azul", "fact", "bici"))
		out := remember(t, cs, map[string]any{
			"title": "Candado", "content": "El candado de la bici es de cadena", "kind": "fact",
			"topics": []string{"bici"}, "relate": relateTo(idInt(t, e), "related"),
		})
		got := reviewIDs(t, out)
		if got[e] {
			t.Errorf("el registro relacionado con relate no debe ser candidato:\n%s", out)
		}
		if !got[g] {
			t.Errorf("el otro registro del tema debe ser candidato:\n%s", out)
		}
	})

	run("S4 revisa: los duplicados no tienen candidatos", func(t *testing.T, cs *mcp.ClientSession) {
		h := saveOne(t, cs, item("Plantas", "Las plantas se riegan los lunes", "note", "jardin"))
		saveOne(t, cs, item("Terraza", "La terraza tiene mesa y cuatro sillas", "note", "jardin"))
		out := remember(t, cs, item("Plantas", "Las plantas se riegan los lunes", "note", "jardin"))
		wantContains(t, out, "#"+h+" ya existía (duplicado)")
		if len(batchLines(out)) != 1 || strings.Contains(out, "revisa:") {
			t.Errorf("un duplicado no lleva candidatos:\n%s", out)
		}
	})

	run("S4 revisa: los candidatos van justo después de la línea de su ítem", func(t *testing.T, cs *mcp.ClientSession) {
		x := saveOne(t, cs, item("Receta", "La receta de la tortilla lleva cebolla", "note", "cocina"))
		y := saveOne(t, cs, item("Viaje", "El viaje a Lisboa es en mayo", "note", "viajes"))
		out := remember(t, cs,
			item("Postre", "El postre del domingo es flan", "note", "cocina"),
			item("Hotel", "El hotel de Lisboa está en el centro", "note", "viajes"),
		)
		lines := batchLines(out)
		if len(lines) != 4 {
			t.Fatalf("%d líneas, quería 4:\n%s", len(lines), out)
		}
		if !strings.Contains(lines[0], " guardado") || !strings.HasPrefix(lines[1], "  revisa: #"+x+" [") ||
			!strings.Contains(lines[2], " guardado") || !strings.HasPrefix(lines[3], "  revisa: #"+y+" [") {
			t.Errorf("orden inesperado:\n%s", out)
		}
	})

	// ---------------------------------------------------------------- S5

	run("S5 remember: sin parecidos ni relaciones por similitud", func(t *testing.T, cs *mcp.ClientSession) {
		words := func(n int, extra string) string {
			w := make([]string, n)
			for i := range w {
				w[i] = fmt.Sprintf("termino%d", i)
			}
			return strings.Join(w, " ") + " " + extra
		}
		a := saveOne(t, cs, item("Lista de términos", words(40, "uno"), "note", "glosario"))
		out := remember(t, cs, item("Lista de términos", words(40, "dos"), "note", "glosario"))
		if strings.Contains(out, "parecidos:") {
			t.Errorf("la salida ya no incluye parecidos:\n%s", out)
		}
		b := firstID(t, out)
		got := mustOK(t, cs, "get", map[string]any{"ids": []int64{idInt(t, a), idInt(t, b)}})
		if strings.Contains(got, "relaciones:") {
			t.Errorf("la similitud no debe crear relaciones:\n%s", got)
		}
	})

	run("S5 remember: relate crea relaciones confirmadas", func(t *testing.T, cs *mcp.ClientSession) {
		a := saveOne(t, cs, item("Alfa", "contenido de alfa", "note", "x1"))
		b := saveOne(t, cs, item("Beta", "contenido de beta", "note", "x2"))
		c := saveOne(t, cs, item("Gamma", "contenido de gamma", "note", "x3"))
		n := saveOne(t, cs, map[string]any{
			"title": "Nuevo", "content": "contenido del nuevo", "kind": "note", "topics": []string{"x4"},
			"relate": relateTo(idInt(t, a), "", idInt(t, b), "contradicts", idInt(t, c), "part_of"),
		})
		got := mustOK(t, cs, "get", map[string]any{"ids": []int64{idInt(t, n)}})
		wantContains(t, got,
			"relacionado con #"+a+" «Alfa» (confirmada)",
			"contradice a #"+b+" «Beta» (confirmada)",
			"parte de #"+c+" «Gamma» (confirmada)")
		wantContains(t, mustOK(t, cs, "get", map[string]any{"ids": []int64{idInt(t, a)}}), "relacionado con #"+n+" «Nuevo» (confirmada)")
	})

	run("S5 remember: relation no válida", func(t *testing.T, cs *mcp.ClientSession) {
		a := saveOne(t, cs, item("Alfa", "contenido de alfa", "note", "x1"))
		for _, bad := range []string{"supersedes", "raro"} {
			text, isErr := call(t, cs, "remember", map[string]any{"items": []any{map[string]any{
				"title": "Nuevo", "content": "contenido del nuevo", "kind": "note", "topics": []string{"x2"},
				"relate": relateTo(idInt(t, a), bad),
			}}})
			if !isErr {
				t.Errorf("relation %q debería ser un error: %s", bad, text)
			}
			wantContains(t, text, "related", "contradicts", "part_of")
		}
	})

	run("S5 remember: un destino inexistente no impide guardar", func(t *testing.T, cs *mcp.ClientSession) {
		a := saveOne(t, cs, item("Alfa", "contenido de alfa", "note", "x1"))
		out := remember(t, cs, map[string]any{
			"title": "Nuevo", "content": "contenido del nuevo", "kind": "note", "topics": []string{"x2"},
			"relate": relateTo(idInt(t, a), "related", 9999, "related"),
		})
		first := batchLines(out)[0]
		if !strings.Contains(first, " guardado") || !strings.HasSuffix(first, " · relación con #9999 no creada: no existe") {
			t.Errorf("primera línea = %q", first)
		}
		n := firstID(t, out)
		got := mustOK(t, cs, "get", map[string]any{"ids": []int64{idInt(t, n)}})
		wantContains(t, got, "relacionado con #"+a)
		if strings.Contains(got, "#9999") {
			t.Errorf("no debe existir relación con #9999:\n%s", got)
		}
	})
}
