package mcptools

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/database"
)

// tagItem builds a remember item that uses the new `tags` field.
func tagItem(title, content string, tags ...string) map[string]any {
	return map[string]any{"title": title, "content": content, "kind": "note", "tags": tags}
}

func tagCount(t *testing.T, d Deps, sql string, args ...any) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var n int
	if err := d.Pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("consulta %q: %v", sql, err)
	}
	return n
}

// tagSlugs returns the sorted slugs stored for record id.
func tagSlugs(t *testing.T, d Deps, id string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	rows, err := d.Pool.Query(ctx, `SELECT t.slug FROM memory_topics mt JOIN topics t ON t.id = mt.topic_id
		WHERE mt.memory_id = $1 ORDER BY t.slug`, idInt(t, id))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func wantSlugs(t *testing.T, d Deps, id string, want ...string) {
	t.Helper()
	if got := tagSlugs(t, d, id); !reflect.DeepEqual(got, want) {
		t.Errorf("etiquetas guardadas del #%s = %v, quería %v", id, got, want)
	}
}

func firstLine(s string) string { return strings.SplitN(s, "\n", 2)[0] }

// TestTagsE2E covers T1 (S1-S4) of odd/tasks/hashtags-y-calidad.md through the MCP tools.
func TestTagsE2E(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)
	run := func(name string, f func(t *testing.T, d Deps, cs *mcp.ClientSession)) {
		t.Run(name, func(t *testing.T) {
			tc.Reset(t)
			d, srv := newLegacyStack(tc)
			f(t, d, connect(t, srv, "test-client"))
		})
	}

	// ---------------------------------------------------------------- S1

	run("S1 tags and topics are joined, leading # ignored, normalized", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		it := tagItem("Coche de VDM", "El coche de VDM pasa la ITV", "#VDM", "Coche 4860")
		it["topics"] = []string{"ITV"}
		id := saveOne(t, cs, it)
		wantSlugs(t, d, id, "coche-4860", "itv", "vdm")
	})

	run("S1 repeated tags after normalization count once (8 distinct from 11 written, 3 repeats)", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		id := saveOne(t, cs, tagItem("Ocho", "ocho etiquetas distintas",
			"a1", "#A1", "A1", "b2", "c3", "d4", "e5", "f6", "g7", "#h8", "H8"))
		wantSlugs(t, d, id, "a1", "b2", "c3", "d4", "e5", "f6", "g7", "h8")
	})

	run("S1 more than 8 distinct tags is an error and nothing of the call is saved", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		text, isErr := call(t, cs, "remember", map[string]any{"items": []any{
			tagItem("Valido", "este ítem es válido", "uno"),
			tagItem("Demasiadas", "este ítem tiene nueve etiquetas", "t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9"),
		}})
		if !isErr {
			t.Fatalf("nueve etiquetas deberían ser IsError: %s", text)
		}
		wantContains(t, text, "ítem 2: como máximo 8 etiquetas (llegaron 9)")
		for _, table := range []string{"memories", "topics", "memory_topics"} {
			if n := tagCount(t, d, "SELECT count(*) FROM "+table); n != 0 {
				t.Errorf("tras el error la tabla %s tiene %d filas, quería 0", table, n)
			}
		}
	})

	run("S1 no usable tag falls back to general", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		id := saveOne(t, cs, tagItem("Sin etiqueta útil", "solo símbolos como etiqueta", "#", "  "))
		wantSlugs(t, d, id, "general")
		id2 := saveOne(t, cs, item("Sin etiquetas", "ni topics ni tags", "note"))
		wantSlugs(t, d, id2, "general")
	})

	run("S1 revise update replaces the tags with tags or topics, same rules", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		id := saveOne(t, cs, tagItem("Revisable", "registro para cambiar etiquetas", "inicial"))
		n := idInt(t, id)

		mustOK(t, cs, "revise", map[string]any{"id": n, "action": "update", "tags": []string{"#Nuevo Uno", "dos"}})
		wantSlugs(t, d, id, "dos", "nuevo-uno")

		mustOK(t, cs, "revise", map[string]any{"id": n, "action": "update", "topics": []string{"Tres"}})
		wantSlugs(t, d, id, "tres")

		mustOK(t, cs, "revise", map[string]any{"ops": []any{
			map[string]any{"id": n, "action": "update", "tags": []string{"cuatro"}, "topics": []string{"#cinco"}},
		}})
		wantSlugs(t, d, id, "cinco", "cuatro")

		text, _ := call(t, cs, "revise", map[string]any{"id": n, "action": "update",
			"tags": []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9"}})
		wantContains(t, text, "como máximo 8 etiquetas (llegaron 9)")
		wantSlugs(t, d, id, "cinco", "cuatro")
	})

	// ---------------------------------------------------------------- S2

	run("S2 remember: a tag equal to an existing one without hyphens resolves to it", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Coche A", "primer coche", "coche-4860ngl"))
		out := remember(t, cs, tagItem("Coche B", "segundo coche", "coche-4860-ngl"))
		id := firstID(t, out)
		wantSlugs(t, d, id, "coche-4860ngl")
		wantContains(t, firstLine(out), "#coche-4860-ngl→#coche-4860ngl")
		if n := tagCount(t, d, `SELECT count(*) FROM topics WHERE slug = 'coche-4860-ngl'`); n != 0 {
			t.Errorf("se creó la etiqueta con guiones (%d filas)", n)
		}
		if n := tagCount(t, d, `SELECT count(*) FROM topics`); n != 1 {
			t.Errorf("hay %d etiquetas, quería 1", n)
		}
	})

	run("S2 revise update: same resolution", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Coche A", "primer coche", "coche-4860ngl"))
		id := saveOne(t, cs, tagItem("Otro", "otro registro", "otra"))
		mustOK(t, cs, "revise", map[string]any{"id": idInt(t, id), "action": "update", "tags": []string{"coche-4860-ngl"}})
		wantSlugs(t, d, id, "coche-4860ngl")
		if n := tagCount(t, d, `SELECT count(*) FROM topics WHERE slug = 'coche-4860-ngl'`); n != 0 {
			t.Errorf("se creó la etiqueta con guiones (%d filas)", n)
		}
	})

	// ---------------------------------------------------------------- S3

	run("S3 remember: a new tag with a near-identical embedding is created, not merged", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		// The test stack makes "gym" embed exactly like "entrenamiento".
		saveOne(t, cs, tagItem("Rutina", "rutina de pierna", "entrenamiento"))
		out := remember(t, cs, tagItem("Horario", "horario del gimnasio", "gym"))
		id := firstID(t, out)
		wantSlugs(t, d, id, "gym")
		if strings.Contains(out, "→") {
			t.Errorf("no debería haber reescritura de etiquetas:\n%s", out)
		}
		if n := tagCount(t, d, `SELECT count(*) FROM topics`); n != 2 {
			t.Errorf("hay %d etiquetas, quería 2", n)
		}
	})

	run("S3 revise update: a new tag with a near-identical embedding is created, not merged", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Rutina", "rutina de pierna", "entrenamiento"))
		id := saveOne(t, cs, tagItem("Horario", "horario del gimnasio", "horarios"))
		mustOK(t, cs, "revise", map[string]any{"id": idInt(t, id), "action": "update", "tags": []string{"gym"}})
		wantSlugs(t, d, id, "gym")
		if n := tagCount(t, d, `SELECT count(*) FROM topics WHERE slug = 'gym'`); n != 1 {
			t.Errorf("la etiqueta gym debería existir (%d filas)", n)
		}
	})

	// ---------------------------------------------------------------- S4

	run("S4 saved line: #slug items, #requested→#canonical rewrites, (nueva) on new tags", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Base", "registro base", "vdm", "coche-4860ngl"))
		out := remember(t, cs, tagItem("Itv del coche", "pasa la itv en marzo", "vdm", "coche-4860-ngl", "itv"))
		id := firstID(t, out)
		want := "#" + id + " guardado · etiquetas: #vdm, #coche-4860-ngl→#coche-4860ngl, #itv (nueva)"
		if got := firstLine(out); !strings.HasPrefix(got, want) {
			t.Errorf("línea guardada = %q, quería que empezara por %q", got, want)
		}
		if strings.Contains(out, "temas:") {
			t.Errorf("la salida no debe decir temas:\n%s", out)
		}
	})

	run("S4 get shows etiquetas: #a, #b instead of temas", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		id := saveOne(t, cs, tagItem("Con dos", "registro con dos etiquetas", "beta", "alfa"))
		got := mustOK(t, cs, "get", map[string]any{"ids": []int64{idInt(t, id)}})
		wantContains(t, got, "etiquetas: #alfa, #beta")
		if strings.Contains(got, "temas:") {
			t.Errorf("get no debe decir temas:\n%s", got)
		}
	})

	run("S4 recall lines show the tags after the title", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		id := saveOne(t, cs, tagItem("Matrícula del coche", "La matrícula del coche es 1234 ABC", "alfa", "beta"))
		line := recallLine(t, cs, "matrícula del coche", []string{"alfa"}, id)
		wantContains(t, line, "] Matrícula del coche · #alfa #beta — ")
	})

	run("S4 revisa lines show the tags after the title", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Matrícula del coche", "La matrícula del coche es 1234 ABC", "coche", "matricula"))
		out := remember(t, cs, tagItem("Seguro del coche", "El seguro del coche vence en marzo", "coche"))
		lines := reviewLines(out)
		if len(lines) != 1 {
			t.Fatalf("quería 1 línea revisa:, hay %d:\n%s", len(lines), out)
		}
		wantContains(t, lines[0], "] Matrícula del coche · #coche #matricula — ")
	})

	// Previous behaviour that must still hold.
	run("previous: exact reuse of an existing tag keeps one row", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, item("Uno", "primer contenido", "note", "entrenamiento"))
		id := saveOne(t, cs, item("Dos", "segundo contenido", "note", "entrenamiento"))
		wantSlugs(t, d, id, "entrenamiento")
		if n := tagCount(t, d, `SELECT count(*) FROM topics WHERE slug = 'entrenamiento'`); n != 1 {
			t.Errorf("hay %d filas de la etiqueta entrenamiento, quería 1", n)
		}
	})

	run("previous: an item without tags is saved with general", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		id := saveOne(t, cs, item("Sin tema", "contenido sin tema", "note"))
		wantSlugs(t, d, id, "general")
	})
}
