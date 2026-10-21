package mcptools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/database"
)

const previewHeader = "VISTA PREVIA — no se ha guardado nada"

// previewCall runs remember with preview: true and returns the answer text and the error flag.
func previewCall(t *testing.T, cs *mcp.ClientSession, items ...map[string]any) (string, bool) {
	t.Helper()
	list := make([]any, len(items))
	for i, it := range items {
		list[i] = it
	}
	return call(t, cs, "remember", map[string]any{"items": list, "preview": true})
}

// preview runs remember with preview: true and fails on an error result.
func preview(t *testing.T, cs *mcp.ClientSession, items ...map[string]any) string {
	t.Helper()
	out, isErr := previewCall(t, cs, items...)
	if isErr {
		t.Fatalf("preview devolvió error: %s", out)
	}
	return out
}

// itemBlock returns the lines of the `ítem n «` block of a preview answer
// (including its header line), up to the next `ítem ` line.
func itemBlock(t *testing.T, out string, n int) []string {
	t.Helper()
	lines := strings.Split(out, "\n")
	prefix := fmt.Sprintf("ítem %d «", n)
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, prefix) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("no hay bloque %q en la vista previa:\n%s", prefix, out)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "ítem ") {
			end = i
			break
		}
	}
	return lines[start:end]
}

// blockLine returns the first line of block starting with prefix ("" if none).
func blockLine(block []string, prefix string) string {
	if i := lineIndex(block, prefix); i >= 0 {
		return block[i]
	}
	return ""
}

// storeSnapshot fingerprints the five tables a preview must not touch. It
// leaves out the embedding columns, which a background worker may fill.
func storeSnapshot(t *testing.T, d Deps) string {
	t.Helper()
	queries := []string{
		`SELECT count(*) || ':' || coalesce(md5(string_agg((id, kind, status, title, content, key, content_hash, updated_at)::text, '|' ORDER BY id)), '') FROM memories`,
		`SELECT count(*) || ':' || coalesce(md5(string_agg((id, slug, last_used_at)::text, '|' ORDER BY id)), '') FROM topics`,
		`SELECT count(*) || ':' || coalesce(md5(string_agg(t::text, '|' ORDER BY t::text)), '') FROM memory_topics t`,
		`SELECT count(*) || ':' || coalesce(md5(string_agg(t::text, '|' ORDER BY t::text)), '') FROM memory_relations t`,
		`SELECT count(*) || ':' || coalesce(md5(string_agg(t::text, '|' ORDER BY t::text)), '') FROM tag_aliases t`,
	}
	var parts []string
	for _, q := range queries {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		var s string
		err := d.Pool.QueryRow(ctx, q).Scan(&s)
		cancel()
		if err != nil {
			t.Fatalf("snapshot %q: %v", q, err)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n")
}

func wantSnapshotUnchanged(t *testing.T, d Deps, before string) {
	t.Helper()
	if after := storeSnapshot(t, d); after != before {
		t.Errorf("la vista previa cambió el almacén\nantes:\n%s\ndespués:\n%s", before, after)
	}
}

func keyedItem(title, content, key string, tags ...string) map[string]any {
	it := tagItem(title, content, tags...)
	it["key"] = key
	return it
}

// suggestedKeyLines returns the `  clave sugerida: ` lines of an answer or block.
func suggestedKeyLines(lines []string) []string {
	var res []string
	for _, l := range lines {
		if strings.HasPrefix(l, "  clave sugerida: ") {
			res = append(res, l)
		}
	}
	return res
}

// TestPreviewE2E covers T4 (S11, S12, S13) of odd/tasks/hashtags-y-calidad.md through the MCP tools.
func TestPreviewE2E(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)
	run := func(name string, f func(t *testing.T, d Deps, cs *mcp.ClientSession)) {
		t.Run(name, func(t *testing.T) {
			tc.Reset(t)
			d, srv := newLegacyStack(tc)
			f(t, d, connect(t, srv, "test-client"))
		})
	}

	// ---------------------------------------------------------------- S11

	run("S11 header, item line and tags in the S4 format; nothing is saved", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Registro de la flota", "camión de reparto de la flota", "vdm", "agridemur"))
		mustOK(t, cs, "revise", map[string]any{"action": "alias", "tag": "agm", "with_tag": "agridemur"})
		before := storeSnapshot(t, d)
		out := preview(t, cs, tagItem("Nuevo camión de reparto", "compra de un camión nuevo", "agm", "vdm", "nueva-x"))
		lines := strings.Split(out, "\n")
		if len(lines) < 3 {
			t.Fatalf("respuesta demasiado corta:\n%s", out)
		}
		if lines[0] != previewHeader {
			t.Errorf("primera línea = %q, quería %q", lines[0], previewHeader)
		}
		if lines[1] != "ítem 1 «Nuevo camión de reparto»" {
			t.Errorf("segunda línea = %q", lines[1])
		}
		if lines[2] != "  etiquetas: #agm→#agridemur, #vdm, #nueva-x (nueva)" {
			t.Errorf("tercera línea = %q", lines[2])
		}
		if savedIDRe.MatchString(out) {
			t.Errorf("la vista previa no debe decir que guardó:\n%s", out)
		}
		wantSnapshotUnchanged(t, d, before)
	})

	run("S11 several items: one block each, in input order, nothing saved", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		before := storeSnapshot(t, d)
		out := preview(t, cs,
			tagItem("Riego del olivar", "turnos de riego del olivar", "olivar"),
			tagItem("Poda del olivo", "poda anual del olivo", "poda"),
		)
		if !strings.HasPrefix(out, previewHeader+"\n") {
			t.Errorf("debe empezar por la cabecera:\n%s", out)
		}
		i1 := strings.Index(out, "ítem 1 «Riego del olivar»")
		i2 := strings.Index(out, "ítem 2 «Poda del olivo»")
		if i1 < 0 || i2 < 0 || i1 > i2 {
			t.Fatalf("faltan o están desordenados los bloques:\n%s", out)
		}
		if got := blockLine(itemBlock(t, out, 1), "  etiquetas:"); got != "  etiquetas: #olivar (nueva)" {
			t.Errorf("etiquetas del ítem 1 = %q", got)
		}
		if got := blockLine(itemBlock(t, out, 2), "  etiquetas:"); got != "  etiquetas: #poda (nueva)" {
			t.Errorf("etiquetas del ítem 2 = %q", got)
		}
		wantSnapshotUnchanged(t, d, before)
	})

	run("S11 ya existe: same content as an active record", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		id := saveOne(t, cs, tagItem("Rutina de entrenamiento", "entrenar pierna los lunes", "entrenamiento"))
		before := storeSnapshot(t, d)
		out := preview(t, cs, tagItem("Rutina de entrenamiento", "entrenar pierna los lunes", "entrenamiento"))
		block := itemBlock(t, out, 1)
		if got := blockLine(block, "  ya existe:"); got != "  ya existe: #"+id+" (mismo contenido)" {
			t.Errorf("línea ya existe = %q, quería %q\n%s", got, "  ya existe: #"+id+" (mismo contenido)", out)
		}
		wantSnapshotUnchanged(t, d, before)
	})

	run("S11 ya existe is omitted when the content is new", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Rutina de entrenamiento", "entrenar pierna los lunes", "entrenamiento"))
		out := preview(t, cs, tagItem("Rutina de entrenamiento", "entrenar espalda los jueves", "entrenamiento"))
		if strings.Contains(out, "ya existe:") {
			t.Errorf("no debería haber línea ya existe:\n%s", out)
		}
	})

	run("S11 clave K: sustituiría a the record that holds the key", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		a := saveOne(t, cs, keyedItem("Horario del taller", "el taller abre a las siete", "taller/horario", "taller"))
		before := storeSnapshot(t, d)
		out := preview(t, cs, keyedItem("Horario del taller", "el taller abre a las ocho", "taller/horario", "taller"))
		block := itemBlock(t, out, 1)
		want := "  clave taller/horario: sustituiría a #" + a
		if got := blockLine(block, "  clave taller/horario:"); got != want {
			t.Errorf("línea clave = %q, quería %q\n%s", got, want, out)
		}
		if len(suggestedKeyLines(block)) != 0 {
			t.Errorf("un ítem con key no lleva clave sugerida:\n%s", out)
		}
		wantSnapshotUnchanged(t, d, before)
	})

	run("S11 clave K is omitted when nobody holds the key", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, keyedItem("Horario del taller", "el taller abre a las siete", "taller/horario", "taller"))
		out := preview(t, cs, keyedItem("Otro horario", "la tienda abre a las nueve", "tienda/horario", "tienda"))
		if strings.Contains(out, "sustituiría a") {
			t.Errorf("no debería haber línea de clave:\n%s", out)
		}
	})

	run("S11 ¿alias?: suggested for a new tag, not stored", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Coche A", "primer coche", "coche-4860ngl"))
		before := storeSnapshot(t, d)
		out := preview(t, cs, tagItem("Coche B", "segundo coche", "coche-4860gnl"))
		block := itemBlock(t, out, 1)
		if got := blockLine(block, "  etiquetas:"); got != "  etiquetas: #coche-4860gnl (nueva)" {
			t.Errorf("etiquetas = %q", got)
		}
		if got := aliasLines(out); len(got) != 1 || got[0] != "  ¿alias?: #coche-4860gnl ≈ #coche-4860ngl (1 registros)" {
			t.Errorf("líneas ¿alias? = %q\n%s", got, out)
		}
		wantAliasRows(t, d, "suggested")
		wantSnapshotUnchanged(t, d, before)
	})

	run("S11 conectaría: counts include the item as if saved; no conectado line", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		r1 := saveOne(t, cs, tagItem("Sembrado de la parcela norte", "cebada sembrada en octubre", "alfa", "beta", "gamma"))
		r2 := saveOne(t, cs, tagItem("Riego de la parcela sur", "turnos de riego nocturnos", "alfa", "beta"))
		saveOne(t, cs, tagItem("Almacén de aperos", "herramientas guardadas en la nave", "alfa"))
		rarityFillers(t, cs, 16)
		before := storeSnapshot(t, d)
		out := preview(t, cs, tagItem("Cosecha de la parcela este", "trigo recogido en julio", "alfa", "beta", "gamma"))
		// 19 active records + the item = 20; alfa 3+1, beta 2+1, gamma 1+1.
		want := fmt.Sprintf("  conectaría: #%s (#gamma, %.1f), #%s (#beta, %.1f)",
			r1, strength(20, 4, 3, 2), r2, strength(20, 4, 3))
		block := itemBlock(t, out, 1)
		if got := blockLine(block, "  conectaría:"); got != want {
			t.Errorf("línea conectaría = %q, quería %q\n%s", got, want, out)
		}
		if strings.Contains(out, "conectado por etiquetas") {
			t.Errorf("la vista previa usa 'conectaría', no 'conectado':\n%s", out)
		}
		wantSnapshotUnchanged(t, d, before)
	})

	run("S11 conectaría is omitted when nothing is connected", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, tagItem("Otro de comun", "texto otroq", "comun-x"))
		rarityFillers(t, cs, 18)
		out := preview(t, cs, tagItem("Nuevo de comun", "texto nuevoq", "comun-x"))
		if strings.Contains(out, "conectaría") {
			t.Errorf("no debería haber línea conectaría:\n%s", out)
		}
	})

	run("S11 revisa: candidates are listed", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		same := saveOne(t, cs, tagItem("Riego del olivar", "turnos de riego del olivar", "olivar"))
		text := saveOne(t, cs, tagItem("Poda en invierno", "la poda del olivar se hace en invierno", "poda"))
		before := storeSnapshot(t, d)
		out := preview(t, cs, tagItem("Nuevo del olivar", "la poda del olivar cambia este año", "olivar"))
		ids := reviewIDs(t, out)
		if !ids[same] || !ids[text] {
			t.Errorf("revisa: debe listar #%s y #%s, hay %v:\n%s", same, text, ids, out)
		}
		wantSnapshotUnchanged(t, d, before)
	})

	run("S11 line order inside an item: etiquetas, ya existe, clave K", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, keyedItem("Horario del taller", "el taller abre a las siete", "taller/horario", "taller"))
		same := saveOne(t, cs, tagItem("Aviso", "contenido repetido del aviso", "taller"))
		out := preview(t, cs, keyedItem("Aviso", "contenido repetido del aviso", "taller/horario", "taller"))
		block := itemBlock(t, out, 1)
		e := lineIndex(block, "  etiquetas:")
		y := lineIndex(block, "  ya existe: #"+same)
		k := lineIndex(block, "  clave taller/horario:")
		if !(e == 1 && y > e && k > y) {
			t.Errorf("orden esperado etiquetas < ya existe < clave (posiciones %d, %d, %d):\n%s", e, y, k, out)
		}
	})

	run("S11 validation errors are the same as a real save and nothing changes", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		before := storeSnapshot(t, d)
		nine := []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9"}
		text, isErr := previewCall(t, cs, tagItem("Bueno", "ítem válido", "ok"), tagItem("Malo", "demasiadas etiquetas", nine...))
		if !isErr || text != "ítem 2: como máximo 8 etiquetas (llegaron 9)" {
			t.Errorf("isErr=%v texto=%q", isErr, text)
		}
		wantSnapshotUnchanged(t, d, before)
	})

	// ---------------------------------------------------------------- S12

	// keyScenario saves a record R with the given key and tags plus 16 fillers.
	keyScenario := func(t *testing.T, cs *mcp.ClientSession, key string, tags ...string) string {
		r := saveOne(t, cs, keyedItem("Registro de partida", "contenido de partida "+key, key, tags...))
		rarityFillers(t, cs, 16)
		return r
	}

	run("S12 suggested key from a tag equal to the last part of the key, in the preview", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		r := keyScenario(t, cs, "coche-4860ngl/itv", "coche-4860ngl", "itv")
		before := storeSnapshot(t, d)
		out := preview(t, cs, tagItem("ITV pasada", "la ITV caduca en 2028", "coche-4860ngl", "itv"))
		want := "  clave sugerida: coche-4860ngl/itv (sustituiría a #" + r + ")"
		if got := suggestedKeyLines(itemBlock(t, out, 1)); len(got) != 1 || got[0] != want {
			t.Errorf("clave sugerida = %q, quería %q\n%s", got, want, out)
		}
		wantSnapshotUnchanged(t, d, before)
	})

	run("S12 the tag may come through an alias", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		r := keyScenario(t, cs, "coche-4860ngl/itv", "coche-4860ngl", "itv")
		mustOK(t, cs, "revise", map[string]any{"action": "alias", "tag": "inspeccion", "with_tag": "itv"})
		out := preview(t, cs, tagItem("Inspección pasada", "la inspección caduca en 2028", "coche-4860ngl", "inspeccion"))
		block := itemBlock(t, out, 1)
		wantContains(t, strings.Join(block, "\n"), "  etiquetas: #coche-4860ngl, #inspeccion→#itv")
		want := "  clave sugerida: coche-4860ngl/itv (sustituiría a #" + r + ")"
		if got := suggestedKeyLines(block); len(got) != 1 || got[0] != want {
			t.Errorf("clave sugerida = %q, quería %q\n%s", got, want, out)
		}
	})

	run("S12 suggested key from a whole dash-delimited sequence of the normalized title", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		r := keyScenario(t, cs, "finca-norte/riego", "finca-norte", "vdm")
		out := preview(t, cs, tagItem("Riego de la finca norte", "turnos de riego nuevos", "finca-norte", "vdm"))
		want := "  clave sugerida: finca-norte/riego (sustituiría a #" + r + ")"
		if got := suggestedKeyLines(itemBlock(t, out, 1)); len(got) != 1 || got[0] != want {
			t.Errorf("clave sugerida = %q, quería %q\n%s", got, want, out)
		}
	})

	run("S12 a partial word of the title is not enough", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		keyScenario(t, cs, "finca-norte/rieg", "finca-norte", "vdm")
		out := preview(t, cs, tagItem("Riego de la finca norte", "turnos de riego nuevos", "finca-norte", "vdm"))
		if got := suggestedKeyLines(strings.Split(out, "\n")); len(got) != 0 {
			t.Errorf("no debería haber clave sugerida: %q", got)
		}
	})

	run("S12 not shown when the item has a key", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		keyScenario(t, cs, "coche-4860ngl/itv", "coche-4860ngl", "itv")
		out := preview(t, cs, keyedItem("ITV pasada", "la ITV caduca en 2028", "otra/clave", "coche-4860ngl", "itv"))
		if got := suggestedKeyLines(strings.Split(out, "\n")); len(got) != 0 {
			t.Errorf("no debería haber clave sugerida: %q", got)
		}
	})

	run("S12 not shown when the record is not connected enough", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		keyScenario(t, cs, "coche-1111abc/itv", "itv")
		out := preview(t, cs, tagItem("ITV pasada", "la ITV caduca en 2028", "itv"))
		// N = 18, n = 2: ln(9) = 2.2 < 3.0.
		if got := suggestedKeyLines(strings.Split(out, "\n")); len(got) != 0 {
			t.Errorf("no debería haber clave sugerida: %q", got)
		}
	})

	run("S12 not shown when the record with the key is no longer active", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		r := keyScenario(t, cs, "coche-4860ngl/itv", "coche-4860ngl", "itv")
		mustOK(t, cs, "revise", map[string]any{"id": idInt(t, r), "action": "invalidate"})
		out := preview(t, cs, tagItem("ITV pasada", "la ITV caduca en 2028", "coche-4860ngl", "itv"))
		if got := suggestedKeyLines(strings.Split(out, "\n")); len(got) != 0 {
			t.Errorf("no debería haber clave sugerida: %q", got)
		}
	})

	run("S12 the strongest connected record wins over a smaller id", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		saveOne(t, cs, keyedItem("Débil", "registro débil", "k-two/itv", "aa-t", "itv"))
		strong := saveOne(t, cs, keyedItem("Fuerte", "registro fuerte", "k-one/itv", "aa-t", "bb-t", "itv"))
		rarityFillers(t, cs, 16)
		out := preview(t, cs, tagItem("Nuevo", "registro nuevo", "aa-t", "bb-t", "itv"))
		want := "  clave sugerida: k-one/itv (sustituiría a #" + strong + ")"
		if got := suggestedKeyLines(itemBlock(t, out, 1)); len(got) != 1 || got[0] != want {
			t.Errorf("clave sugerida = %q, quería %q\n%s", got, want, out)
		}
	})

	run("S12 ties go to the smaller id", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		first := saveOne(t, cs, keyedItem("Primero", "registro primero", "k-one/itv", "aa-t", "bb-t", "itv"))
		saveOne(t, cs, keyedItem("Segundo", "registro segundo", "k-two/itv", "aa-t", "bb-t", "itv"))
		rarityFillers(t, cs, 16)
		out := preview(t, cs, tagItem("Nuevo", "registro nuevo", "aa-t", "bb-t", "itv"))
		want := "  clave sugerida: k-one/itv (sustituiría a #" + first + ")"
		if got := suggestedKeyLines(itemBlock(t, out, 1)); len(got) != 1 || got[0] != want {
			t.Errorf("clave sugerida = %q, quería %q\n%s", got, want, out)
		}
	})

	run("S12 after a real save: right after ¿alias?, before conectado and revisa; nothing superseded", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		r := saveOne(t, cs, keyedItem("Coche vdm", "ficha del coche con su ITV", "coche-4860ngl/itv", "coche-4860ngl", "itv", "vdm"))
		rarityFillers(t, cs, 16)
		out := remember(t, cs, tagItem("ITV pasada", "la ITV caduca en 2028", "coche-4860gnl", "itv", "vdm"))
		// N = 18 after the save; itv and vdm are carried by 2 records.
		lines := strings.Split(out, "\n")
		wantKey := "  clave sugerida: coche-4860ngl/itv (sustituiría a #" + r + ")"
		ki := lineIndex(lines, "  clave sugerida:")
		ai := lineIndex(lines, "  ¿alias?:")
		ci := lineIndex(lines, "  conectado por etiquetas:")
		if ki < 0 || lines[ki] != wantKey {
			t.Fatalf("clave sugerida = %q, quería %q\n%s", blockLine(lines, "  clave sugerida:"), wantKey, out)
		}
		if ai != 1 || ki != ai+1 {
			t.Errorf("la clave sugerida va justo después de la línea ¿alias? (alias=%d, clave=%d):\n%s", ai, ki, out)
		}
		if ci != ki+1 {
			t.Errorf("la línea conectado va justo después de la clave sugerida (clave=%d, conectado=%d):\n%s", ki, ci, out)
		}
		for i, l := range lines {
			if strings.HasPrefix(l, "  revisa:") && i < ci {
				t.Errorf("las líneas revisa: van después:\n%s", out)
			}
		}
		if strings.Contains(lines[0], "sustituye") {
			t.Errorf("la clave solo se sugiere, no se aplica:\n%s", out)
		}
		if n := tagCount(t, d, `SELECT count(*) FROM memories WHERE id = $1 AND status = 'active'`, idInt(t, r)); n != 1 {
			t.Errorf("#%s debe seguir vigente", r)
		}
	})

	run("S12 preview line order: etiquetas, ¿alias?, clave sugerida, conectaría, revisa", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		r := saveOne(t, cs, keyedItem("Coche vdm", "ficha del coche con su ITV", "coche-4860ngl/itv", "coche-4860ngl", "itv", "vdm"))
		rarityFillers(t, cs, 16)
		out := preview(t, cs, tagItem("ITV pasada", "la ITV caduca en 2028", "coche-4860gnl", "itv", "vdm"))
		block := itemBlock(t, out, 1)
		e := lineIndex(block, "  etiquetas:")
		a := lineIndex(block, "  ¿alias?:")
		k := lineIndex(block, "  clave sugerida: coche-4860ngl/itv (sustituiría a #"+r+")")
		c := lineIndex(block, fmt.Sprintf("  conectaría: #%s (#itv, %.1f)", r, strength(18, 2, 2)))
		if !(e == 1 && a > e && k == a+1 && c == k+1) {
			t.Errorf("orden esperado etiquetas < ¿alias? < clave sugerida < conectaría (posiciones %d, %d, %d, %d):\n%s", e, a, k, c, out)
		}
		for i, l := range block {
			if strings.HasPrefix(l, "  revisa:") && i < c {
				t.Errorf("las líneas revisa: van al final:\n%s", out)
			}
		}
	})

	// ---------------------------------------------------------------- S13

	run("S13 preview, then save with the suggested key leaves one active record", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		r := keyScenario(t, cs, "coche-4860ngl/itv", "coche-4860ngl", "itv")
		out := preview(t, cs, tagItem("ITV renovada", "la ITV caduca en 2029", "coche-4860ngl", "itv"))
		sug := suggestedKeyLines(itemBlock(t, out, 1))
		if len(sug) != 1 {
			t.Fatalf("falta la clave sugerida:\n%s", out)
		}
		key := strings.TrimSuffix(strings.TrimPrefix(sug[0], "  clave sugerida: "), " (sustituiría a #"+r+")")
		if key != "coche-4860ngl/itv" {
			t.Fatalf("clave sugerida = %q", key)
		}
		saved := remember(t, cs, keyedItem("ITV renovada", "la ITV caduca en 2029", key, "coche-4860ngl", "itv"))
		wantContains(t, saved, "guardado", "sustituye #"+r)
		if n := tagCount(t, d, `SELECT count(*) FROM memories WHERE key = $1 AND status = 'active'`, key); n != 1 {
			t.Errorf("hay %d registros vigentes con la clave, quería 1", n)
		}
		if n := tagCount(t, d, `SELECT count(*) FROM memories WHERE id = $1 AND status = 'active'`, idInt(t, r)); n != 0 {
			t.Errorf("#%s debería quedar sustituido", r)
		}
	})

	// ---------------------------------------------------------------- previous behaviour

	run("previous: a real save without preview saves and prints the saved line", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		out := remember(t, cs, tagItem("Rutina", "entrenar pierna", "entrenamiento"))
		wantContains(t, out, "#1 guardado · etiquetas: #entrenamiento (nueva)")
		if strings.Contains(out, "VISTA PREVIA") {
			t.Errorf("un guardado normal no es una vista previa:\n%s", out)
		}
		if n := tagCount(t, d, `SELECT count(*) FROM memories`); n != 1 {
			t.Errorf("memories tiene %d filas, quería 1", n)
		}
	})

	run("previous: preview false saves like a real save", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		out := mustOK(t, cs, "remember", map[string]any{"items": []any{tagItem("Rutina", "entrenar pierna", "entrenamiento")}, "preview": false})
		wantContains(t, out, "#1 guardado · etiquetas: #entrenamiento (nueva)")
		if n := tagCount(t, d, `SELECT count(*) FROM memories`); n != 1 {
			t.Errorf("memories tiene %d filas, quería 1", n)
		}
	})

	run("previous: a real duplicate is still reported", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		id := saveOne(t, cs, tagItem("Rutina", "entrenar pierna", "entrenamiento"))
		out := remember(t, cs, tagItem("Rutina", "entrenar pierna", "entrenamiento"))
		wantContains(t, out, "#"+id+" ya existía (duplicado): «Rutina»")
		if n := tagCount(t, d, `SELECT count(*) FROM memories`); n != 1 {
			t.Errorf("memories tiene %d filas, quería 1", n)
		}
	})

	run("previous: a real save with an active key still supersedes", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		a := saveOne(t, cs, keyedItem("Horario", "entreno a las 7", "entrenamiento/horario", "entrenamiento"))
		out := remember(t, cs, keyedItem("Horario", "entreno a las 8", "entrenamiento/horario", "entrenamiento"))
		wantContains(t, out, "sustituye #"+a)
		if n := tagCount(t, d, `SELECT count(*) FROM memories WHERE key = 'entrenamiento/horario' AND status = 'active'`); n != 1 {
			t.Errorf("hay %d registros vigentes con la clave, quería 1", n)
		}
	})
}
