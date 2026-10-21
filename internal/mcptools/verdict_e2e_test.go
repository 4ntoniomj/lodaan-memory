package mcptools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/database"
)

// relRows returns every row of memory_relations between a and b (either
// direction) as "src>dst kind state verdict conf" sorted by source, target, kind.
// verdict is "-" when NULL; conf is "-" when NULL, else printed with two decimals.
func relRows(t *testing.T, d Deps, a, b int64) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	rows, err := d.Pool.Query(ctx, `SELECT source_id, target_id, kind::text, state::text,
			coalesce(verdict, '-'), coalesce(to_char(round(confidence::numeric, 2), 'FM0.00'), '-')
		FROM memory_relations
		WHERE (source_id = $1 AND target_id = $2) OR (source_id = $2 AND target_id = $1)
		ORDER BY source_id, target_id, kind::text`, a, b)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s, tg int64
		var kind, state, verdict, conf string
		if err := rows.Scan(&s, &tg, &kind, &state, &verdict, &conf); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d>%d %s %s %s %s", s, tg, kind, state, verdict, conf))
	}
	return out
}

func wantRels(t *testing.T, d Deps, a, b int64, want ...string) {
	t.Helper()
	got := relRows(t, d, a, b)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("memory_relations entre #%d y #%d:\n%s\nquería:\n%s", a, b, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func memStatus(t *testing.T, d Deps, id int64) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var s string
	if err := d.Pool.QueryRow(ctx, `SELECT status::text FROM memories WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func wantStatuses(t *testing.T, d Deps, a int64, sa string, b int64, sb string) {
	t.Helper()
	if got := memStatus(t, d, a); got != sa {
		t.Errorf("estado de #%d = %q, quería %q", a, got, sa)
	}
	if got := memStatus(t, d, b); got != sb {
		t.Errorf("estado de #%d = %q, quería %q", b, got, sb)
	}
}

func verdictCall(t *testing.T, cs *mcp.ClientSession, a, b int64, verdict string, conf float64) (string, bool) {
	t.Helper()
	return call(t, cs, "revise", map[string]any{"id": a, "action": "verdict", "with_id": b, "verdict": verdict, "confidence": conf})
}

func verdictOK(t *testing.T, cs *mcp.ClientSession, a, b int64, verdict string, conf float64) string {
	t.Helper()
	text, isErr := verdictCall(t, cs, a, b, verdict, conf)
	if isErr {
		t.Fatalf("verdict %s devolvió un error: %s", verdict, text)
	}
	return text
}

func relate(t *testing.T, cs *mcp.ClientSession, a, b int64, kind string) {
	t.Helper()
	mustOK(t, cs, "revise", map[string]any{"id": a, "action": "relate", "with_id": b, "relation": kind})
}

// TestVerdictE2E covers T5 (S14) of odd/tasks/hashtags-y-calidad.md through the MCP tools.
func TestVerdictE2E(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)
	run := func(name string, f func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64)) {
		t.Run(name, func(t *testing.T) {
			tc.Reset(t)
			d, srv := newLegacyStack(tc)
			cs := connect(t, srv, "test-client")
			a := idInt(t, saveOne(t, cs, tagItem("Registro A", "contenido del registro a", "uno")))
			b := idInt(t, saveOne(t, cs, tagItem("Registro B", "contenido del registro b", "dos")))
			f(t, d, cs, a, b)
		})
	}

	run("S14 contradice with confidence >= 0.7 confirms contradicts and keeps both active", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) {
		out := verdictOK(t, cs, a, b, "contradice", 0.9)
		if want := fmt.Sprintf("#%d contradice #%d: pregunta al usuario cuál es el bueno", a, b); out != want {
			t.Errorf("respuesta = %q, quería %q", out, want)
		}
		wantRels(t, d, a, b, fmt.Sprintf("%d>%d contradicts confirmed contradice 0.90", a, b))
		wantStatuses(t, d, a, "active", b, "active")
	})

	run("S14 sustituye with confidence >= 0.7 supersedes B by A like supersede", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) {
		out := verdictOK(t, cs, a, b, "sustituye", 0.85)
		if want := fmt.Sprintf("#%d sustituye a #%d", a, b); out != want {
			t.Errorf("respuesta = %q, quería %q", out, want)
		}
		wantRels(t, d, a, b, fmt.Sprintf("%d>%d supersedes confirmed sustituye 0.85", a, b))
		wantStatuses(t, d, a, "active", b, "superseded")
		if n := tagCount(t, d, `SELECT count(*) FROM memory_topics WHERE memory_id = $1 AND active`, b); n != 0 {
			t.Errorf("#%d sustituido conserva %d etiquetas activas, quería 0", b, n)
		}
		if n := tagCount(t, d, `SELECT count(*) FROM memory_topics WHERE memory_id = $1 AND active`, a); n == 0 {
			t.Errorf("#%d debería conservar sus etiquetas activas", a)
		}
	})

	run("S14 compatible confirms related and rejects contradicts in either direction", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) {
		relate(t, cs, b, a, "contradicts")
		execSQL(t, d, `INSERT INTO memory_relations (source_id, target_id, kind, state)
			VALUES ($1, $2, 'contradicts', 'suggested')`, a, b)
		out := verdictOK(t, cs, a, b, "compatible", 0.9)
		if want := fmt.Sprintf("#%d y #%d son compatibles", a, b); out != want {
			t.Errorf("respuesta = %q, quería %q", out, want)
		}
		rows := relRows(t, d, a, b)
		want := map[string]bool{
			fmt.Sprintf("%d>%d related confirmed compatible 0.90", a, b): false,
		}
		for _, r := range rows {
			switch {
			case strings.HasPrefix(r, fmt.Sprintf("%d>%d related ", a, b)):
				if _, ok := want[r]; !ok {
					t.Errorf("fila related = %q, quería %q", r, fmt.Sprintf("%d>%d related confirmed compatible 0.90", a, b))
				}
				want[r] = true
			case strings.Contains(r, " contradicts rejected "):
			default:
				t.Errorf("fila inesperada %q", r)
			}
		}
		for r, seen := range want {
			if !seen {
				t.Errorf("falta la fila %q en %v", r, rows)
			}
		}
		if n := len(rows); n != 3 {
			t.Errorf("quería 3 filas (related + 2 contradicts rechazadas), hay %d: %v", n, rows)
		}
		wantStatuses(t, d, a, "active", b, "active")
	})

	run("S14 no_relacionado rejects every related/contradicts in either direction", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) {
		relate(t, cs, a, b, "related")
		relate(t, cs, b, a, "contradicts")
		out := verdictOK(t, cs, a, b, "no_relacionado", 0.95)
		if want := fmt.Sprintf("#%d y #%d no tienen que ver", a, b); out != want {
			t.Errorf("respuesta = %q, quería %q", out, want)
		}
		wantRels(t, d, a, b,
			fmt.Sprintf("%d>%d related rejected - -", a, b),
			fmt.Sprintf("%d>%d contradicts rejected - -", b, a))
		wantStatuses(t, d, a, "active", b, "active")
	})

	run("S14 no_relacionado with no previous relation stores a rejected related A->B", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) {
		out := verdictOK(t, cs, a, b, "no_relacionado", 0.95)
		if want := fmt.Sprintf("#%d y #%d no tienen que ver", a, b); out != want {
			t.Errorf("respuesta = %q, quería %q", out, want)
		}
		wantRels(t, d, a, b, fmt.Sprintf("%d>%d related rejected no_relacionado 0.95", a, b))
	})

	run("S14 confidence below 0.7 applies nothing and stores a suggested relation", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) {
		cases := []struct{ verdict, kind string }{
			{"contradice", "contradicts"}, {"sustituye", "supersedes"}, {"compatible", "related"},
		}
		for _, c := range cases {
			execSQL(t, d, `DELETE FROM memory_relations`)
			out := verdictOK(t, cs, a, b, c.verdict, 0.55)
			if want := fmt.Sprintf("veredicto «%s» con confianza 0.55: queda pendiente, pregunta al usuario", c.verdict); out != want {
				t.Errorf("%s: respuesta = %q, quería %q", c.verdict, out, want)
			}
			wantRels(t, d, a, b, fmt.Sprintf("%d>%d %s suggested %s 0.55", a, b, c.kind, c.verdict))
			wantStatuses(t, d, a, "active", b, "active")
		}
	})

	run("S14 no_relacionado below 0.7 stores nothing", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) {
		out := verdictOK(t, cs, a, b, "no_relacionado", 0.55)
		if want := "veredicto «no_relacionado» con confianza 0.55: queda pendiente, pregunta al usuario"; out != want {
			t.Errorf("respuesta = %q, quería %q", out, want)
		}
		wantRels(t, d, a, b)
	})

	run("S14 low confidence does not touch existing relations", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) {
		relate(t, cs, a, b, "contradicts")
		verdictOK(t, cs, a, b, "compatible", 0.4)
		wantRels(t, d, a, b,
			fmt.Sprintf("%d>%d contradicts confirmed - -", a, b),
			fmt.Sprintf("%d>%d related suggested compatible 0.40", a, b))
		wantStatuses(t, d, a, "active", b, "active")
	})

	run("S14 threshold: 0.7 applies, 0.69 stays pending", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) {
		out := verdictOK(t, cs, a, b, "compatible", 0.69)
		if want := "veredicto «compatible» con confianza 0.69: queda pendiente, pregunta al usuario"; out != want {
			t.Errorf("0.69: respuesta = %q, quería %q", out, want)
		}
		wantRels(t, d, a, b, fmt.Sprintf("%d>%d related suggested compatible 0.69", a, b))
		out = verdictOK(t, cs, a, b, "compatible", 0.7)
		if want := fmt.Sprintf("#%d y #%d son compatibles", a, b); out != want {
			t.Errorf("0.7: respuesta = %q, quería %q", out, want)
		}
		wantRels(t, d, a, b, fmt.Sprintf("%d>%d related confirmed compatible 0.70", a, b))
	})

	run("S14 verdict works inside ops", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) {
		out := mustOK(t, cs, "revise", map[string]any{"ops": []any{
			map[string]any{"id": a, "action": "verdict", "with_id": b, "verdict": "contradice", "confidence": 0.8},
			map[string]any{"id": b, "action": "verdict", "with_id": a, "verdict": "compatible", "confidence": 0.3},
		}})
		wantContains(t, out,
			fmt.Sprintf("1. #%d contradice #%d: pregunta al usuario cuál es el bueno", a, b),
			"2. veredicto «compatible» con confianza 0.30: queda pendiente, pregunta al usuario")
		wantRels(t, d, a, b,
			fmt.Sprintf("%d>%d contradicts confirmed contradice 0.80", a, b),
			fmt.Sprintf("%d>%d related suggested compatible 0.30", b, a))
	})

	run("S14 invalid verdict is an error and stores nothing", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) {
		text, isErr := verdictCall(t, cs, a, b, "x", 0.9)
		if want := `veredicto "x" no válido (válidos: contradice, sustituye, compatible, no_relacionado)`; !isErr || text != want {
			t.Errorf("isErr=%v texto=%q, quería %q", isErr, text, want)
		}
		wantRels(t, d, a, b)
		wantStatuses(t, d, a, "active", b, "active")
	})

	run("S14 confidence outside 0..1 or missing is an error and stores nothing", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) {
		const want = "la confianza debe estar entre 0 y 1"
		for _, c := range []float64{-0.1, 1.5} {
			text, isErr := verdictCall(t, cs, a, b, "compatible", c)
			if !isErr || text != want {
				t.Errorf("confianza %v: isErr=%v texto=%q, quería %q", c, isErr, text, want)
			}
		}
		text, isErr := call(t, cs, "revise", map[string]any{"id": a, "action": "verdict", "with_id": b, "verdict": "compatible"})
		if !isErr || text != want {
			t.Errorf("sin confianza: isErr=%v texto=%q, quería %q", isErr, text, want)
		}
		wantRels(t, d, a, b)
		wantStatuses(t, d, a, "active", b, "active")
	})

	run("S14 confidence 0 and 1 are valid", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) {
		out := verdictOK(t, cs, a, b, "contradice", 0)
		if want := "veredicto «contradice» con confianza 0.00: queda pendiente, pregunta al usuario"; out != want {
			t.Errorf("0: respuesta = %q, quería %q", out, want)
		}
		out = verdictOK(t, cs, a, b, "compatible", 1)
		if want := fmt.Sprintf("#%d y #%d son compatibles", a, b); out != want {
			t.Errorf("1: respuesta = %q, quería %q", out, want)
		}
	})

	run("S14 missing id and nonexistent with_id are errors", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) {
		text, isErr := call(t, cs, "revise", map[string]any{"action": "verdict", "with_id": b, "verdict": "compatible", "confidence": 0.9})
		if !isErr || text != "hay que indicar el id del registro" {
			t.Errorf("sin id: isErr=%v texto=%q", isErr, text)
		}
		text, isErr = verdictCall(t, cs, a, 99999, "compatible", 0.9)
		if !isErr || text != "el registro #99999 no existe" {
			t.Errorf("with_id inexistente: isErr=%v texto=%q", isErr, text)
		}
		text, isErr = call(t, cs, "revise", map[string]any{"id": a, "action": "verdict", "verdict": "compatible", "confidence": 0.9})
		if !isErr {
			t.Errorf("sin with_id debería ser un error, texto=%q", text)
		}
		if n := tagCount(t, d, `SELECT count(*) FROM memory_relations`); n != 0 {
			t.Errorf("memory_relations tiene %d filas, quería 0", n)
		}
	})

	run("S14 get shows verdict and confidence on the relation line", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) {
		verdictOK(t, cs, a, b, "compatible", 0.9)
		for _, id := range []int64{a, b} {
			out := mustOK(t, cs, "get", map[string]any{"ids": []int64{id}})
			wantContains(t, out, "(confirmada; veredicto compatible, confianza 0.90)")
		}
	})

	run("S14 status counts low-confidence verdicts, contradice and resolves with sustituye", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) {
		c := idInt(t, saveOne(t, cs, tagItem("Registro C", "contenido del registro c", "tres")))
		e := idInt(t, saveOne(t, cs, forced(tagItem("Registro D", "contenido del registro d", "cuatro"))))

		verdictOK(t, cs, a, b, "compatible", 0.5)
		wantContains(t, mustOK(t, cs, "status", map[string]any{}),
			" · veredictos pendientes: 1 · contradicciones abiertas: 0 · ")

		verdictOK(t, cs, c, e, "contradice", 0.9)
		wantContains(t, mustOK(t, cs, "status", map[string]any{}),
			" · veredictos pendientes: 1 · contradicciones abiertas: 1 · ")

		verdictOK(t, cs, c, e, "sustituye", 0.9)
		wantContains(t, mustOK(t, cs, "status", map[string]any{}),
			" · veredictos pendientes: 1 · contradicciones abiertas: 0 · ")
	})

	// ------------------------------------------------ existing behaviour

	run("S14 existing supersede and relate actions keep working", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) {
		out := mustOK(t, cs, "revise", map[string]any{"id": a, "action": "relate", "with_id": b})
		if want := fmt.Sprintf("#%d related #%d (confirmada)", a, b); out != want {
			t.Errorf("relate = %q, quería %q", out, want)
		}
		wantRels(t, d, a, b, fmt.Sprintf("%d>%d related confirmed - -", a, b))
		out = mustOK(t, cs, "revise", map[string]any{"id": b, "action": "supersede", "with_id": a})
		if want := fmt.Sprintf("#%d sustituido por #%d", b, a); out != want {
			t.Errorf("supersede = %q, quería %q", out, want)
		}
		wantRels(t, d, a, b,
			fmt.Sprintf("%d>%d related confirmed - -", a, b),
			fmt.Sprintf("%d>%d supersedes confirmed - -", a, b))
		wantStatuses(t, d, a, "active", b, "superseded")
	})

	run("S14 get relation line without verdict is unchanged", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) {
		relate(t, cs, a, b, "related")
		out := mustOK(t, cs, "get", map[string]any{"ids": []int64{a}})
		wantContains(t, out, fmt.Sprintf("relaciones: relacionado con #%d «Registro B» (confirmada)", b))
		if strings.Contains(out, "veredicto") {
			t.Errorf("get no debe mostrar veredicto sin veredicto:\n%s", out)
		}
	})
}
