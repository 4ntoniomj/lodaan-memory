package mcptools

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/database"
)

// aliasRows returns the sorted "alias>canonical" pairs stored in tag_aliases with the given state.
func aliasRows(t *testing.T, d Deps, state string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	rows, err := d.Pool.Query(ctx, `SELECT alias || '>' || canonical FROM tag_aliases WHERE state = $1 ORDER BY 1`, state)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func wantAliasRows(t *testing.T, d Deps, state string, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if got := aliasRows(t, d, state); !reflect.DeepEqual(got, want) {
		t.Errorf("tag_aliases (%s) = %v, quería %v", state, got, want)
	}
}

func execSQL(t *testing.T, d Deps, sql string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	if _, err := d.Pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// aliasLines returns the `  ¿alias?: ` lines of an answer.
func aliasLines(out string) []string {
	var res []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "  ¿alias?: ") {
			res = append(res, l)
		}
	}
	return res
}

func reviseAlias(t *testing.T, cs *mcp.ClientSession, action, tag, with string) (string, bool) {
	t.Helper()
	return call(t, cs, "revise", map[string]any{"action": action, "tag": tag, "with_tag": with})
}

// TestAliasesE2E covers T2 (S5, S6, S7, S16) of odd/tasks/hashtags-y-calidad.md through the MCP tools.
func TestAliasesE2E(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)
	run := func(name string, f func(t *testing.T, d Deps, cs *mcp.ClientSession)) {
		t.Run(name, func(t *testing.T) {
			tc.Reset(t)
			d, srv := newLegacyStack(tc)
			f(t, d, connect(t, srv, "test-client"))
		})
	}

	// ---------------------------------------------------------------- S6

	run("S6 alias confirms, retags every record and answers with the count", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		a := saveOne(t, cs, tagItem("Uno", "primer registro agm", "agm"))
		b := saveOne(t, cs, tagItem("Dos", "segundo registro agm", "agm", "vdm"))
		c := saveOne(t, cs, tagItem("Tres", "registro agridemur", "agridemur"))
		text, isErr := reviseAlias(t, cs, "alias", "#AGM", "Agridemur")
		if isErr {
			t.Fatalf("alias devolvió error: %s", text)
		}
		if text != "alias #agm → #agridemur (2 registros reetiquetados)" {
			t.Errorf("respuesta = %q", text)
		}
		wantAliasRows(t, d, "confirmed", "agm>agridemur")
		wantSlugs(t, d, a, "agridemur")
		wantSlugs(t, d, b, "agridemur", "vdm")
		wantSlugs(t, d, c, "agridemur")
	})

	run("S6 alias normalizes accents and spaces of both inputs", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		a := saveOne(t, cs, tagItem("Uno", "registro de año nuevo", "ano-nuevo", "vdm"))
		text, isErr := reviseAlias(t, cs, "alias", "#Año Nuevo", " Cosecha ")
		if isErr {
			t.Fatalf("alias devolvió error: %s", text)
		}
		if text != "alias #ano-nuevo → #cosecha (1 registros reetiquetados)" {
			t.Errorf("respuesta = %q", text)
		}
		wantAliasRows(t, d, "confirmed", "ano-nuevo>cosecha")
		wantSlugs(t, d, a, "cosecha", "vdm")
	})

	run("S6 alias: a record already tagged with both keeps one", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		both := saveOne(t, cs, tagItem("Ambos", "lleva las dos etiquetas", "agm", "agridemur"))
		mustOK(t, cs, "revise", map[string]any{"action": "alias", "tag": "agm", "with_tag": "agridemur"})
		wantSlugs(t, d, both, "agridemur")
		wantAliasRows(t, d, "confirmed", "agm>agridemur")
	})

	run("S6 alias works inside ops and needs no id", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		a := saveOne(t, cs, tagItem("Uno", "registro agm", "agm"))
		out := mustOK(t, cs, "revise", map[string]any{"ops": []any{
			map[string]any{"action": "alias", "tag": "agm", "with_tag": "agridemur"},
			map[string]any{"action": "reject_alias", "tag": "foo", "with_tag": "bar"},
		}})
		wantContains(t, out, "1. alias #agm → #agridemur (1 registros reetiquetados)", "2. #foo ≠ #bar: no se volverá a sugerir")
		wantSlugs(t, d, a, "agridemur")
		wantAliasRows(t, d, "confirmed", "agm>agridemur")
		wantAliasRows(t, d, "rejected", "foo>bar")
	})

	run("S6 alias with the same tag on both sides is an error and stores nothing", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Uno", "registro agm", "agm"))
		text, isErr := reviseAlias(t, cs, "alias", "agm", "#AGM")
		if !isErr || text != "una etiqueta no puede ser alias de sí misma" {
			t.Errorf("isErr=%v texto=%q", isErr, text)
		}
		if n := tagCount(t, d, `SELECT count(*) FROM tag_aliases`); n != 0 {
			t.Errorf("tag_aliases tiene %d filas, quería 0", n)
		}
	})

	run("S6 alias and reject_alias need tag and with_tag", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		for _, action := range []string{"alias", "reject_alias"} {
			for _, in := range [][2]string{{"", ""}, {"agm", ""}, {"", "agridemur"}, {"#", "agridemur"}} {
				text, isErr := reviseAlias(t, cs, action, in[0], in[1])
				if !isErr || text != "hay que indicar tag y with_tag" {
					t.Errorf("%s tag=%q with_tag=%q: isErr=%v texto=%q", action, in[0], in[1], isErr, text)
				}
			}
		}
		if n := tagCount(t, d, `SELECT count(*) FROM tag_aliases`); n != 0 {
			t.Errorf("tag_aliases tiene %d filas, quería 0", n)
		}
	})

	run("S6 reject_alias stores the rejection and answers", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		text, isErr := reviseAlias(t, cs, "reject_alias", "#AGM", "Agm Repo")
		if isErr {
			t.Fatalf("reject_alias devolvió error: %s", text)
		}
		if text != "#agm ≠ #agm-repo: no se volverá a sugerir" {
			t.Errorf("respuesta = %q", text)
		}
		wantAliasRows(t, d, "rejected", "agm>agm-repo")
		wantAliasRows(t, d, "confirmed")
	})

	run("S6 reject_alias with the same tag on both sides is an error", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		text, isErr := reviseAlias(t, cs, "reject_alias", "agm", "#AGM")
		if !isErr || text != "una etiqueta no puede ser alias de sí misma" {
			t.Errorf("isErr=%v texto=%q", isErr, text)
		}
	})

	run("S6 revise actions that need an id still require it", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		text, isErr := call(t, cs, "revise", map[string]any{"action": "invalidate"})
		if !isErr || text != "hay que indicar el id del registro" {
			t.Errorf("isErr=%v texto=%q", isErr, text)
		}
	})

	// ---------------------------------------------------------------- S5

	run("S5 no chains: confirming A to C when C is an alias of D stores A to D", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		x := saveOne(t, cs, tagItem("X", "registro x", "xx"))
		saveOne(t, cs, tagItem("A", "registro agm", "agm"))
		saveOne(t, cs, tagItem("D", "registro agridemur", "agridemur"))
		mustOK(t, cs, "revise", map[string]any{"action": "alias", "tag": "agm", "with_tag": "agridemur"})
		text, isErr := reviseAlias(t, cs, "alias", "xx", "agm")
		if isErr {
			t.Fatalf("alias devolvió error: %s", text)
		}
		if text != "alias #xx → #agridemur (1 registros reetiquetados)" {
			t.Errorf("respuesta = %q", text)
		}
		wantAliasRows(t, d, "confirmed", "agm>agridemur", "xx>agridemur")
		wantSlugs(t, d, x, "agridemur")
	})

	run("S5 no chains: confirming A to C re-points the aliases that pointed to A", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("B", "registro b", "bb"))
		saveOne(t, cs, tagItem("A", "registro a", "aa"))
		saveOne(t, cs, tagItem("C", "registro c", "cc"))
		mustOK(t, cs, "revise", map[string]any{"action": "alias", "tag": "bb", "with_tag": "aa"})
		mustOK(t, cs, "revise", map[string]any{"action": "alias", "tag": "aa", "with_tag": "cc"})
		wantAliasRows(t, d, "confirmed", "aa>cc", "bb>cc")
	})

	run("S5 remember rewrites a confirmed alias and shows #agm→#agridemur", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Base", "registro agridemur", "agridemur"))
		mustOK(t, cs, "revise", map[string]any{"action": "alias", "tag": "agm", "with_tag": "agridemur"})
		out := remember(t, cs, tagItem("Nuevo", "cosecha de agridemur", "agm", "itv"))
		id := firstID(t, out)
		want := "#" + id + " guardado · etiquetas: #agm→#agridemur, #itv (nueva)"
		if got := firstLine(out); !strings.HasPrefix(got, want) {
			t.Errorf("línea guardada = %q, quería que empezara por %q", got, want)
		}
		wantSlugs(t, d, id, "agridemur", "itv")
	})

	run("S5 alias lookup ignores hyphens", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Viejo", "coche con la matrícula bien", "coche-4860ngl"))
		saveOne(t, cs, tagItem("Erróneo", "coche con la matrícula mal", "coche-4860gnl"))
		mustOK(t, cs, "revise", map[string]any{"action": "alias", "tag": "coche-4860gnl", "with_tag": "coche-4860ngl"})
		out := remember(t, cs, tagItem("Itv", "pasa la itv en marzo", "coche-4860-gnl"))
		id := firstID(t, out)
		wantContains(t, firstLine(out), "#coche-4860-gnl→#coche-4860ngl")
		wantSlugs(t, d, id, "coche-4860ngl")
	})

	run("S5 revise update rewrites a confirmed alias", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Base", "registro agridemur", "agridemur"))
		mustOK(t, cs, "revise", map[string]any{"action": "alias", "tag": "agm", "with_tag": "agridemur"})
		id := saveOne(t, cs, tagItem("Otro", "otro registro", "otra"))
		mustOK(t, cs, "revise", map[string]any{"id": idInt(t, id), "action": "update", "tags": []string{"#AGM"}})
		wantSlugs(t, d, id, "agridemur")
	})

	run("S5 recall topics priority applies a confirmed alias", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		in := saveOne(t, cs, tagItem("Cosecha uno", "la cosecha de maíz", "agridemur"))
		saveOne(t, cs, tagItem("Cosecha dos", "la cosecha de trigo", "otra"))
		mustOK(t, cs, "revise", map[string]any{"action": "alias", "tag": "agm", "with_tag": "agridemur"})
		res := mustOK(t, cs, "recall", map[string]any{"query": "cosecha", "topics": []string{"agm"}, "limit": 20})
		if !strings.Contains(res, "Tema: agridemur") {
			t.Errorf("recall con topics=[agm] debería nombrar la etiqueta canónica (Tema: agridemur):\n%s", res)
		}
		if strings.Contains(res, "Tema: agm") {
			t.Errorf("recall no debería nombrar el alias como tema:\n%s", res)
		}
		if !strings.Contains(res, "#"+in+" [") {
			t.Errorf("recall con topics=[agm] debería traer #%s:\n%s", in, res)
		}
	})

	// ---------------------------------------------------------------- S7

	run("S7 plural: alias line right after the saved line, before revisa, stored as suggested", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Cosecha maíz", "cosecha de maíz en septiembre", "cosecha"))
		saveOne(t, cs, tagItem("Cosecha trigo", "cosecha de trigo en junio", "cosecha"))
		out := remember(t, cs, tagItem("Cosecha uva", "cosecha de uva en octubre", "cosecha", "cosechas"))
		lines := strings.Split(out, "\n")
		if len(lines) < 2 || lines[1] != "  ¿alias?: #cosechas ≈ #cosecha (2 registros)" {
			t.Fatalf("la segunda línea debería ser el alias:\n%s", out)
		}
		if got := len(aliasLines(out)); got != 1 {
			t.Errorf("hay %d líneas ¿alias?, quería 1", got)
		}
		rev := reviewLines(out)
		if len(rev) == 0 {
			t.Fatalf("se esperaba alguna línea revisa: para comprobar el orden:\n%s", out)
		}
		for i, l := range lines {
			if strings.HasPrefix(l, "  revisa: ") && i < 2 {
				t.Errorf("revisa: antes de la línea de alias:\n%s", out)
			}
		}
		wantAliasRows(t, d, "suggested", "cosechas>cosecha")
	})

	run("S7 plural es and the reverse direction", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Tractor", "el tractor nuevo", "tractor"))
		out := remember(t, cs, tagItem("Tractores", "los tractores de la finca", "tractores"))
		wantContains(t, out, "  ¿alias?: #tractores ≈ #tractor (1 registros)")

		saveOne(t, cs, tagItem("Cosechas", "las cosechas del año", "cosechas"))
		out = remember(t, cs, tagItem("Cosecha", "la cosecha de hoy", "cosecha"))
		wantContains(t, out, "  ¿alias?: #cosecha ≈ #cosechas (1 registros)")
		wantAliasRows(t, d, "suggested", "cosecha>cosechas", "tractores>tractor")
	})

	run("S7 distance 1 (transposition) when both tags have at least 5 characters", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Coche A", "primer coche", "coche-4860ngl"))
		out := remember(t, cs, tagItem("Coche B", "segundo coche", "coche-4860gnl"))
		wantContains(t, out, "  ¿alias?: #coche-4860gnl ≈ #coche-4860ngl (1 registros)")
		wantAliasRows(t, d, "suggested", "coche-4860gnl>coche-4860ngl")
	})

	run("S7 distance 1 is not suggested when a tag has fewer than 5 characters", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Corto A", "primer registro corto", "abcd"))
		out := remember(t, cs, tagItem("Corto B", "segundo registro corto", "abdc"))
		if l := aliasLines(out); len(l) != 0 {
			t.Errorf("no debería haber sugerencias: %v", l)
		}
		wantAliasRows(t, d, "suggested")
	})

	run("S7 embedding similarity at or above topic_similarity", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Rutina", "rutina de pierna", "entrenamiento"))
		out := remember(t, cs, tagItem("Horario", "horario del gimnasio", "gym"))
		wantContains(t, out, "  ¿alias?: #gym ≈ #entrenamiento (1 registros)")
		wantAliasRows(t, d, "suggested", "gym>entrenamiento")
	})

	run("S7 unrelated new tag gets no suggestion", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Uno", "registro uno", "alfombra"))
		out := remember(t, cs, tagItem("Dos", "registro dos", "bicicleta"))
		if l := aliasLines(out); len(l) != 0 {
			t.Errorf("no debería haber sugerencias: %v", l)
		}
		wantAliasRows(t, d, "suggested")
	})

	run("S7 at most 3 per new tag, more active records first", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		counts := map[string]int{"abcdf": 1, "abcdg": 2, "abcdh": 3, "abcdi": 5}
		var lastI string
		for tag, n := range counts {
			for i := 0; i < n; i++ {
				lastI = saveOne(t, cs, tagItem(fmt.Sprintf("Registro %s %d", tag, i), fmt.Sprintf("contenido distinto de %s número %d", tag, i), tag))
			}
		}
		// abcdi ends with 4 active records: invalidate one of its records.
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		var inv int64
		if err := d.Pool.QueryRow(ctx, `SELECT m.id FROM memories m JOIN memory_topics mt ON mt.memory_id = m.id
			JOIN topics t ON t.id = mt.topic_id WHERE t.slug = 'abcdi' ORDER BY m.id LIMIT 1`).Scan(&inv); err != nil {
			t.Fatal(err)
		}
		_ = lastI
		mustOK(t, cs, "revise", map[string]any{"id": inv, "action": "invalidate"})

		out := remember(t, cs, tagItem("Nuevo", "registro con la etiqueta nueva", "abcde"))
		got := aliasLines(out)
		want := []string{
			"  ¿alias?: #abcde ≈ #abcdi (4 registros)",
			"  ¿alias?: #abcde ≈ #abcdh (3 registros)",
			"  ¿alias?: #abcde ≈ #abcdg (2 registros)",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("líneas ¿alias? = %q, quería %q", got, want)
		}
		if n := tagCount(t, d, `SELECT count(*) FROM tag_aliases WHERE alias = 'abcde' AND state = 'suggested'`); n != 3 {
			t.Errorf("hay %d sugerencias guardadas para abcde, quería 3", n)
		}
	})

	run("S7 a rejected pair is not suggested, in either direction", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Coche A", "primer coche", "coche-4860ngl"))
		mustOK(t, cs, "revise", map[string]any{"action": "reject_alias", "tag": "coche-4860gnl", "with_tag": "coche-4860ngl"})
		out := remember(t, cs, tagItem("Coche B", "segundo coche", "coche-4860gnl"))
		if l := aliasLines(out); len(l) != 0 {
			t.Errorf("mismo sentido: no debería haber sugerencias: %v", l)
		}

		saveOne(t, cs, tagItem("Coche C", "tercer coche", "coche-1111abc"))
		mustOK(t, cs, "revise", map[string]any{"action": "reject_alias", "tag": "coche-1111abc", "with_tag": "coche-1111acb"})
		out = remember(t, cs, tagItem("Coche D", "cuarto coche", "coche-1111acb"))
		if l := aliasLines(out); len(l) != 0 {
			t.Errorf("sentido contrario: no debería haber sugerencias: %v", l)
		}
		wantAliasRows(t, d, "suggested")
	})

	run("S7 no suggestions when the call creates no new tag", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Coche A", "primer coche", "coche-4860ngl"))
		saveOne(t, cs, tagItem("Coche B", "segundo coche", "coche-4860gnl"))
		before := tagCount(t, d, `SELECT count(*) FROM tag_aliases`)
		out := remember(t, cs, tagItem("Coche C", "tercer coche", "coche-4860gnl"))
		if l := aliasLines(out); len(l) != 0 {
			t.Errorf("no debería haber sugerencias con una etiqueta ya existente: %v", l)
		}
		if after := tagCount(t, d, `SELECT count(*) FROM tag_aliases`); after != before {
			t.Errorf("tag_aliases pasó de %d a %d filas", before, after)
		}
	})

	// ---------------------------------------------------------------- S16

	run("S16 status line: etiquetas instead of temas and the new counters at the end", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		out := mustOK(t, cs, "status", map[string]any{})
		want := ProductName + ": BD: ok · registros vigentes: 0 · pendientes de embedding: 0 · etiquetas: 0 · sesiones: 0 · alias: 0 · sugerencias de alias: 0 · veredictos pendientes: 0 · contradicciones abiertas: 0 · Ollama: no aplica"
		if out != want {
			t.Errorf("status = %q\nquería     %q", out, want)
		}
	})

	run("S16 status counts aliases, suggestions, pending verdicts and open contradictions", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		a := idInt(t, saveOne(t, cs, tagItem("A", "registro a", "cosecha")))
		b := idInt(t, saveOne(t, cs, tagItem("B", "registro b", "uno")))
		c := idInt(t, saveOne(t, cs, tagItem("C", "registro c", "dos")))
		dd := idInt(t, saveOne(t, cs, tagItem("D", "registro d", "tres")))
		// One suggestion (cosechas ≈ cosecha), one confirmed alias, one rejected pair.
		saveOne(t, cs, tagItem("E", "registro e", "cosechas"))
		mustOK(t, cs, "revise", map[string]any{"action": "alias", "tag": "uno", "with_tag": "dos"})
		mustOK(t, cs, "revise", map[string]any{"action": "reject_alias", "tag": "foo", "with_tag": "bar"})
		mustOK(t, cs, "revise", map[string]any{"id": dd, "action": "invalidate"})

		rel := func(src, dst int64, kind, state string) {
			execSQL(t, d, `INSERT INTO memory_relations (source_id, target_id, kind, state)
				VALUES ($1, $2, $3::text::relation_kind, $4::text::relation_state)`, src, dst, kind, state)
		}
		rel(a, b, "contradicts", "confirmed")  // open
		rel(a, c, "contradicts", "suggested")  // open and pending verdict
		rel(b, c, "contradicts", "rejected")   // not open
		rel(c, dd, "contradicts", "confirmed") // D is not active: not open
		rel(a, b, "related", "suggested")      // pending verdict

		topics := tagCount(t, d, `SELECT count(*) FROM topics`)
		out := mustOK(t, cs, "status", map[string]any{})
		wantContains(t, out,
			fmt.Sprintf(" · etiquetas: %d · sesiones: 1", topics),
			" · alias: 1 · sugerencias de alias: 1 · veredictos pendientes: 2 · contradicciones abiertas: 2 · Ollama: no aplica")
		if strings.Contains(out, "temas:") {
			t.Errorf("status no debe decir temas:\n%s", out)
		}
		lines := strings.Split(out, "\n")
		if len(lines) != 2 || lines[1] != "  ¿alias?: #cosechas ≈ #cosecha (1 registros)" {
			t.Errorf("tras la línea de BD debería haber una sola línea de alias:\n%s", out)
		}
	})

	run("S16 status lists at most 10 pending suggestions", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		stems := []string{"alfombra", "bicicleta", "calendario", "dormitorio", "escalera", "fotografia",
			"guitarra", "helicoptero", "impresora", "kilometro", "lavadora", "nevera"}
		for _, s := range stems {
			saveOne(t, cs, tagItem("Base "+s, "registro de "+s, s))
			saveOne(t, cs, tagItem("Plural "+s, "registros de "+s+"s", s+"s"))
		}
		out := mustOK(t, cs, "status", map[string]any{})
		wantContains(t, out, " · sugerencias de alias: 12")
		if n := len(aliasLines(out)); n != 10 {
			t.Errorf("status lista %d líneas ¿alias?, quería 10:\n%s", n, out)
		}
		for _, l := range aliasLines(out) {
			if !strings.HasSuffix(l, " (1 registros)") {
				t.Errorf("línea de alias con formato inesperado: %q", l)
			}
		}
	})
}
