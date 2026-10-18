package mcptools

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/database"
)

// rarityFillers saves n records in one call, each with unique words, so that
// they do not look alike for the fake embedder and do not share words with the
// scenario records. They carry the given tags (none means the `general` tag).
// They only make the number of active records N what a scenario needs.
func rarityFillers(t *testing.T, cs *mcp.ClientSession, n int, tags ...string) {
	t.Helper()
	items := make([]map[string]any, n)
	for i := range items {
		title := fmt.Sprintf("zv%dq zw%dq", i, i)
		content := fmt.Sprintf("zx%dq zy%dq zz%dq", i, i, i)
		if len(tags) == 0 {
			items[i] = item(title, content, "note")
		} else {
			items[i] = tagItem(title, content, tags...)
		}
	}
	remember(t, cs, items...)
}

// strength is the expected link strength: sum of ln(N/n) over the shared tags.
func strength(total int, counts ...int) float64 {
	s := 0.0
	for _, n := range counts {
		s += math.Log(float64(total) / float64(n))
	}
	return s
}

// connectedLines returns the `  conectado por etiquetas: ` lines of a remember answer.
func connectedLines(out string) []string {
	var res []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "  conectado por etiquetas:") {
			res = append(res, l)
		}
	}
	return res
}

func lineIndex(lines []string, prefix string) int {
	for i, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return i
		}
	}
	return -1
}

// TestRarityE2E covers T3 (S8, S9, S10) of odd/tasks/hashtags-y-calidad.md through the MCP tools.
// The default tag_link_strength (3.0) is used.
func TestRarityE2E(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)
	run := func(name string, f func(t *testing.T, d Deps, cs *mcp.ClientSession)) {
		t.Run(name, func(t *testing.T) {
			tc.Reset(t)
			d, srv := newStack(tc)
			f(t, d, connect(t, srv, "test-client"))
		})
	}

	// ---------------------------------------------------------------- S8 + S9 remember

	run("S9 remember: connected line, strongest first, rarest tag named, one decimal, between the saved/alias lines and revisa", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		r1 := saveOne(t, cs, tagItem("Sembrado de la parcela norte", "cebada sembrada en octubre", "alfa", "beta", "gamma"))
		r2 := saveOne(t, cs, tagItem("Riego de la parcela sur", "turnos de riego nocturnos", "alfa", "beta"))
		saveOne(t, cs, tagItem("Almacén de aperos", "herramientas guardadas en la nave", "alfa"))
		rarityFillers(t, cs, 16)
		out := remember(t, cs, tagItem("Cosecha de la parcela este", "trigo recogido en julio", "alfa", "beta", "gamma"))
		// N = 20 active records after the save; alfa n=4, beta n=3, gamma n=2.
		want := fmt.Sprintf("  conectado por etiquetas: #%s (#gamma, %.1f), #%s (#beta, %.1f)",
			r1, strength(20, 4, 3, 2), r2, strength(20, 4, 3))
		lines := strings.Split(out, "\n")
		got := connectedLines(out)
		if len(got) != 1 || got[0] != want {
			t.Fatalf("línea conectado = %q, quería exactamente %q\n%s", got, want, out)
		}
		ci := lineIndex(lines, "  conectado por etiquetas:")
		if ci < 1 || !strings.Contains(lines[0], " guardado · etiquetas: ") {
			t.Errorf("la línea conectado debe ir tras la línea guardada:\n%s", out)
		}
		for i, l := range lines {
			if strings.HasPrefix(l, "  ¿alias?:") && i > ci {
				t.Errorf("las líneas ¿alias?: van antes de la línea conectado:\n%s", out)
			}
			if strings.HasPrefix(l, "  revisa:") && i < ci {
				t.Errorf("las líneas revisa: van después de la línea conectado:\n%s", out)
			}
		}
		if len(reviewLines(out)) == 0 {
			t.Errorf("las líneas revisa: deben seguir apareciendo:\n%s", out)
		}
	})

	run("S9 remember: at most 5 connected, ties broken by smaller id, rarest tag tie is alphabetical", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		var ids []string
		for i := 0; i < 7; i++ {
			ids = append(ids, saveOne(t, cs, tagItem(fmt.Sprintf("Parcela hermana %d", i), fmt.Sprintf("hermano%dq distinto%dq", i, i), "cc-t", "aa-t", "bb-t")))
		}
		rarityFillers(t, cs, 16)
		out := remember(t, cs, tagItem("Parcela nueva", "nuevoq sinparecido", "bb-t", "cc-t", "aa-t"))
		// N = 24; every tag has n = 8; strength 3*ln(3) = 3.3 for each of the 7.
		var parts []string
		for _, id := range ids[:5] {
			parts = append(parts, fmt.Sprintf("#%s (#aa-t, %.1f)", id, strength(24, 8, 8, 8)))
		}
		want := "  conectado por etiquetas: " + strings.Join(parts, ", ")
		got := connectedLines(out)
		if len(got) != 1 || got[0] != want {
			t.Errorf("línea conectado = %q, quería exactamente %q", got, want)
		}
	})

	run("S9 remember: a link under tag_link_strength is not shown and the line is omitted", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Otro de comun", "texto otroq", "comun-x"))
		rarityFillers(t, cs, 18)
		out := remember(t, cs, tagItem("Nuevo de comun", "texto nuevoq", "comun-x"))
		// N = 20, n = 2: ln(10) = 2.30 < 3.0.
		if strings.Contains(out, "conectado por etiquetas") {
			t.Errorf("no debería haber línea conectado:\n%s", out)
		}
		if len(reviewLines(out)) == 0 {
			t.Errorf("las líneas revisa: deben seguir apareciendo:\n%s", out)
		}
	})

	run("S8 remember: only active records count for N, n and the list", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		z := saveOne(t, cs, tagItem("Registro viejo", "contenido viejoq", "pp-t", "qq-t"))
		x := saveOne(t, cs, tagItem("Registro vigente", "contenido vigenteq", "pp-t", "qq-t"))
		mustOK(t, cs, "revise", map[string]any{"id": idInt(t, z), "action": "invalidate"})
		rarityFillers(t, cs, 14)
		out := remember(t, cs, tagItem("Registro nuevo", "contenido nuevoq", "pp-t", "qq-t"))
		// Active after the save: 14 + x + new = 16; pp-t and qq-t have n = 2 (z is invalidated).
		want := fmt.Sprintf("  conectado por etiquetas: #%s (#pp-t, %.1f)", x, strength(16, 2, 2))
		got := connectedLines(out)
		if len(got) != 1 || got[0] != want {
			t.Errorf("línea conectado = %q, quería exactamente %q", got, want)
		}
	})

	// ---------------------------------------------------------------- S9 recall

	// recallScenario saves R (quasar), C1 (pulsar) and C2 plus 17 fillers: N = 20.
	// rr-a: R, C1, C2 (n=3); rr-b: R, C1, C2 (n=3); rr-c: R, C1 (n=2).
	recallScenario := func(t *testing.T, cs *mcp.ClientSession) (r, c1, c2 string) {
		r = saveOne(t, cs, tagItem("Informe del sondeo", "quasar7 observado desde la torre", "rr-a", "rr-b", "rr-c"))
		c1 = saveOne(t, cs, tagItem("Informe del sondeo gemelo", "pulsar7 observado desde el valle", "rr-a", "rr-b", "rr-c"))
		c2 = saveOne(t, cs, tagItem("Informe del sondeo lejano", "cometa7 observado desde la costa", "rr-a", "rr-b"))
		rarityFillers(t, cs, 17)
		return
	}

	run("S9 recall: Conectado por etiquetas lists connected records not shown, strongest first, each line followed by (por #tag)", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		r, c1, c2 := recallScenario(t, cs)
		out := mustOK(t, cs, "recall", map[string]any{"query": "quasar7", "limit": 1})
		lines := strings.Split(out, "\n")
		ri := lineIndex(lines, "Resultados:")
		si := lineIndex(lines, "Conectado por etiquetas:")
		if ri < 0 || si < ri {
			t.Fatalf("falta la sección Conectado por etiquetas: tras Resultados:\n%s", out)
		}
		if i := lineIndex(lines[ri:], "#"+r+" ["); i < 0 || ri+i > si {
			t.Errorf("el primer resultado #%s debe estar en Resultados:\n%s", r, out)
		}
		sec := lines[si+1:]
		i1 := lineIndex(sec, "#"+c1+" [")
		i2 := lineIndex(sec, "#"+c2+" [")
		if i1 != 0 || i2 != 1 {
			t.Fatalf("la sección debe listar #%s (6.1) y luego #%s (3.8), posiciones %d y %d:\n%s", c1, c2, i1, i2, out)
		}
		wantContains(t, sec[0], "] Informe del sondeo gemelo · #rr-a #rr-b #rr-c — ")
		if !strings.HasSuffix(sec[0], " (por #rr-c)") {
			t.Errorf("la línea de #%s debe acabar en ' (por #rr-c)': %q", c1, sec[0])
		}
		wantContains(t, sec[1], "] Informe del sondeo lejano · #rr-a #rr-b — ")
		if !strings.HasSuffix(sec[1], " (por #rr-a)") {
			t.Errorf("la línea de #%s debe acabar en ' (por #rr-a)': %q", c2, sec[1])
		}
		for _, id := range []string{c1, c2} {
			if n := strings.Count(out, "#"+id+" ["); n != 1 {
				t.Errorf("#%s aparece %d veces, quería 1", id, n)
			}
		}
	})

	run("S9 recall: records already shown in the answer are not repeated in the connected section", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		_, c1, c2 := recallScenario(t, cs)
		out := mustOK(t, cs, "recall", map[string]any{"query": "quasar7 pulsar7", "limit": 2})
		lines := strings.Split(out, "\n")
		si := lineIndex(lines, "Conectado por etiquetas:")
		if si < 0 {
			t.Fatalf("falta la sección Conectado por etiquetas:\n%s", out)
		}
		if n := strings.Count(out, "#"+c1+" ["); n != 1 {
			t.Errorf("#%s (ya mostrado en Resultados) aparece %d veces, quería 1:\n%s", c1, n, out)
		}
		if lineIndex(lines[si+1:], "#"+c2+" [") != 0 {
			t.Errorf("la sección debe empezar por #%s:\n%s", c2, out)
		}
	})

	run("previous: recall without connections keeps Resultados and adds no connected section", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		recallScenario(t, cs)
		out := mustOK(t, cs, "recall", map[string]any{"query": "zv3q zw3q", "limit": 1})
		wantContains(t, out, "Resultados:", "zv3q zw3q")
		if strings.Contains(out, "Conectado por etiquetas") {
			t.Errorf("no debería haber sección conectado:\n%s", out)
		}
	})

	run("S9 recall: the connected section respects recall_max_bytes", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		long := strings.Repeat("texto largo de relleno ", 12)
		first := saveOne(t, cs, tagItem("Informe base del sondeo", "quasar8 "+long, "ss-a", "ss-b", "ss-c"))
		for i := 0; i < 5; i++ {
			saveOne(t, cs, tagItem(fmt.Sprintf("Informe conectado número %d del sondeo", i), fmt.Sprintf("hermano%dq %s", i, long), "ss-a", "ss-b", "ss-c"))
		}
		rarityFillers(t, cs, 14)

		full := mustOK(t, cs, "recall", map[string]any{"query": "quasar8", "limit": 1})
		if !strings.Contains(full, "Conectado por etiquetas:") || len(full) <= 500 {
			t.Fatalf("datos de prueba: la respuesta completa (%d bytes) debe llevar la sección y pasar de 500 bytes:\n%s", len(full), full)
		}

		small := d
		small.Cfg.RecallMaxBytes = 500
		cs2 := connect(t, NewServer(small), "test-client-2")
		out := mustOK(t, cs2, "recall", map[string]any{"query": "quasar8", "limit": 1})
		if len(out) > 500 {
			t.Errorf("la respuesta tiene %d bytes y el tope es 500:\n%s", len(out), out)
		}
		wantContains(t, out, "… (respuesta recortada al tope de 500 bytes)", "Usa get con los ids para ver el detalle.", "#"+first+" [")
	})

	// ---------------------------------------------------------------- S10

	run("S10 revisa: a record sharing a rare tag ranks before common-tag and text-only matches", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		a := saveOne(t, cs, tagItem("Contrato de arrendamiento", "alquiler anual del almacén", "rara-x"))
		saveOne(t, cs, tagItem("Presupuesto trimestral cooperativa", "presupuesto trimestral cooperativa revisado", "comun"))
		saveOne(t, cs, tagItem("Presupuesto trimestral cooperativa agrícola", "presupuesto trimestral cooperativa agrícola ajustado", "otra-y"))
		rarityFillers(t, cs, 10, "comun")
		out := remember(t, cs, tagItem("Presupuesto trimestral cooperativa agrícola nuevo", "presupuesto trimestral cooperativa agrícola nuevo", "rara-x", "comun"))
		// N = 14: comun is carried by 12 records (more than half), rara-x by 2.
		lines := reviewLines(out)
		if len(lines) == 0 || !strings.HasPrefix(lines[0], "  revisa: #"+a+" [") {
			t.Errorf("la primera línea revisa: debe ser #%s (comparte una etiqueta rara):\n%s", a, out)
		}
	})

	run("previous: revisa lists a same-tag record and a text-only match", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		same := saveOne(t, cs, tagItem("Riego del olivar", "turnos de riego del olivar", "olivar"))
		text := saveOne(t, cs, tagItem("Poda en invierno", "la poda del olivar se hace en invierno", "poda"))
		out := remember(t, cs, tagItem("Nuevo del olivar", "la poda del olivar cambia este año", "olivar"))
		ids := reviewIDs(t, out)
		if !ids[same] || !ids[text] {
			t.Errorf("revisa: debe listar #%s (misma etiqueta) y #%s (texto), hay %v:\n%s", same, text, ids, out)
		}
	})
}
