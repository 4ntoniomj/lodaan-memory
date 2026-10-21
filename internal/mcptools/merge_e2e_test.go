package mcptools

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/database"
)

var (
	fingerprintRe = regexp.MustCompile(`^[0-9a-f]{8}$`)
	mergedRe      = regexp.MustCompile(`^#(\d+) junta #\d+, #\d+ \(quedan sustituidos\)$`)
)

func mergeArgs(ids []int64, title, content string, extra map[string]any) map[string]any {
	list := make([]any, len(ids))
	for i, id := range ids {
		list[i] = id
	}
	args := map[string]any{"action": "merge", "ids": list, "title": title, "content": content}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

func mergeLine(out, prefix string) string {
	return blockLine(strings.Split(out, "\n"), prefix)
}

// mergeFingerprint previews a merge and returns the preview text and its fingerprint.
func mergeFingerprint(t *testing.T, cs *mcp.ClientSession, ids []int64, title, content string, extra map[string]any) (string, string) {
	t.Helper()
	out := mustOK(t, cs, "revise", mergeArgs(ids, title, content, extra))
	line := mergeLine(out, "  huella: ")
	fp := strings.TrimPrefix(line, "  huella: ")
	if !fingerprintRe.MatchString(fp) {
		t.Fatalf("huella %q no son 8 hex en minúscula; vista previa:\n%s", line, out)
	}
	return out, fp
}

func memField(t *testing.T, d Deps, id int64, col string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var s string
	if err := d.Pool.QueryRow(ctx, fmt.Sprintf(`SELECT coalesce(%s::text, '-') FROM memories WHERE id = $1`, col), id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func relsOf(t *testing.T, d Deps, id int64) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	rows, err := d.Pool.Query(ctx, `SELECT source_id || '>' || target_id || ' ' || kind::text || ' ' || state::text
		FROM memory_relations WHERE source_id = $1 OR target_id = $1 ORDER BY source_id DESC, target_id ASC`, id)
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

func wantRelsOf(t *testing.T, d Deps, id int64, want ...string) {
	t.Helper()
	got := relsOf(t, d, id)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("relaciones de #%d:\n%s\nquería:\n%s", id, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestMergeE2E covers T6 (S15) of odd/tasks/hashtags-y-calidad.md through the MCP tools.
func TestMergeE2E(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)
	run := func(name string, f func(t *testing.T, d Deps, cs *mcp.ClientSession)) {
		t.Run(name, func(t *testing.T) {
			tc.Reset(t)
			d, srv := newLegacyStack(tc)
			f(t, d, connect(t, srv, "test-client"))
		})
	}
	save := func(t *testing.T, cs *mcp.ClientSession, title, content, kind, day, key string, tags ...string) int64 {
		it := map[string]any{"title": title, "content": content, "kind": kind, "tags": tags, "occurred_at": day}
		if key != "" {
			it["key"] = key
		}
		return idInt(t, saveOne(t, cs, it))
	}

	run("S15 preview shows every line and writes nothing", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		a := save(t, cs, "Coche de Ana", "El coche de Ana es un Seat", "fact", "2024-03-05", "", "zeta", "alpha")
		b := save(t, cs, "Coche de Ana otra vez", "Ana tiene un Seat azul", "note", "2024-04-06", "ana/coche", "beta", "alpha")
		x := save(t, cs, "Taller", "Taller de confianza", "note", "2024-01-01", "", "taller")
		y := save(t, cs, "Seguro", "Seguro del coche", "note", "2024-01-02", "", "seguro")
		z := save(t, cs, "Viejo", "Dato viejo del coche", "note", "2024-01-03", "", "viejo")
		w := save(t, cs, "Descartado", "Dato descartado", "note", "2024-01-04", "", "descartado")
		relate(t, cs, a, x, "related")
		relate(t, cs, y, b, "part_of")
		relate(t, cs, a, b, "related")
		relate(t, cs, b, w, "related")
		execSQL(t, d, `UPDATE memory_relations SET state = 'rejected' WHERE source_id = $1 AND target_id = $2`, b, w)
		mustOK(t, cs, "revise", map[string]any{"id": z, "action": "supersede", "with_id": a})
		before := storeSnapshot(t, d)

		out, fp := mergeFingerprint(t, cs, []int64{a, b}, "Coche de Ana (junto)", "Ana tiene un Seat azul", nil)
		want := strings.Join([]string{
			fmt.Sprintf("VISTA PREVIA — juntar #%d, #%d en un registro nuevo (no se ha guardado nada)", a, b),
			"  título: Coche de Ana (junto)",
			"  etiquetas: #alpha, #zeta, #beta",
			"  clave: ana/coche",
			"  relaciones que pasan al nuevo: 2",
			fmt.Sprintf("  #%d, #%d quedarán sustituidos por el nuevo", a, b),
			"  huella: " + fp,
			"  para confirmar: repite con confirm: true y fingerprint: " + fp,
		}, "\n")
		if strings.TrimSpace(out) != want {
			t.Errorf("vista previa:\n%s\nquería:\n%s", out, want)
		}
		wantSnapshotUnchanged(t, d, before)
	})

	run("S15 preview uses the given tags and omits the key line when no original has one", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		a := save(t, cs, "Uno", "contenido uno", "note", "2024-03-05", "", "alpha")
		b := save(t, cs, "Dos", "contenido dos", "note", "2024-03-06", "", "beta")
		before := storeSnapshot(t, d)
		out, _ := mergeFingerprint(t, cs, []int64{a, b}, "Junto", "contenido junto", map[string]any{"tags": []string{"gamma"}})
		if got := mergeLine(out, "  etiquetas: "); got != "  etiquetas: #gamma" {
			t.Errorf("línea de etiquetas = %q, quería %q", got, "  etiquetas: #gamma")
		}
		if got := mergeLine(out, "  clave: "); got != "" {
			t.Errorf("no debería haber línea de clave y hay %q", got)
		}
		if got := mergeLine(out, "  relaciones que pasan al nuevo: "); got != "  relaciones que pasan al nuevo: 0" {
			t.Errorf("línea de relaciones = %q", got)
		}
		wantSnapshotUnchanged(t, d, before)
	})

	run("S15 the key line is the first non-empty key in ids order", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		a := save(t, cs, "Uno", "contenido uno", "note", "2024-03-05", "k/uno", "alpha")
		b := save(t, cs, "Dos", "contenido dos", "note", "2024-03-06", "k/dos", "beta")
		out, _ := mergeFingerprint(t, cs, []int64{b, a}, "Junto", "contenido junto", nil)
		if got := mergeLine(out, "  clave: "); got != "  clave: k/dos" {
			t.Errorf("línea de clave = %q, quería %q", got, "  clave: k/dos")
		}
	})

	run("S15 the fingerprint changes when an original changes", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		a := save(t, cs, "Uno", "contenido uno", "note", "2024-03-05", "", "alpha")
		b := save(t, cs, "Dos", "contenido dos", "note", "2024-03-06", "", "beta")
		_, fp1 := mergeFingerprint(t, cs, []int64{a, b}, "Junto", "contenido junto", nil)
		_, fp1b := mergeFingerprint(t, cs, []int64{a, b}, "Junto", "contenido junto", nil)
		if fp1 != fp1b {
			t.Errorf("la huella no es estable: %s y %s", fp1, fp1b)
		}
		mustOK(t, cs, "revise", map[string]any{"id": a, "action": "update", "content": "contenido uno cambiado"})
		_, fp2 := mergeFingerprint(t, cs, []int64{a, b}, "Junto", "contenido junto", nil)
		if fp1 == fp2 {
			t.Errorf("la huella no cambió tras modificar un original: %s", fp1)
		}
	})

	run("S15 confirm creates the new record, moves the key and copies relations", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		a := save(t, cs, "Coche de Ana", "El coche de Ana es un Seat", "decision", "2024-03-05", "", "zeta", "alpha")
		b := save(t, cs, "Coche de Ana otra vez", "Ana tiene un Seat azul", "note", "2024-04-06", "ana/coche", "beta", "alpha")
		x := save(t, cs, "Taller", "Taller de confianza", "note", "2024-01-01", "", "taller")
		y := save(t, cs, "Seguro", "Seguro del coche", "note", "2024-01-02", "", "seguro")
		w := save(t, cs, "Descartado", "Dato descartado", "note", "2024-01-04", "", "descartado")
		relate(t, cs, a, x, "contradicts")
		relate(t, cs, y, b, "part_of")
		execSQL(t, d, `UPDATE memory_relations SET state = 'suggested' WHERE source_id = $1 AND target_id = $2`, y, b)
		relate(t, cs, a, b, "related")
		relate(t, cs, b, w, "related")
		execSQL(t, d, `UPDATE memory_relations SET state = 'rejected' WHERE source_id = $1 AND target_id = $2`, b, w)

		_, fp := mergeFingerprint(t, cs, []int64{a, b}, "Coche de Ana (junto)", "Ana tiene un Seat azul", nil)
		out := mustOK(t, cs, "revise", mergeArgs([]int64{a, b}, "Coche de Ana (junto)", "Ana tiene un Seat azul",
			map[string]any{"confirm": true, "fingerprint": fp}))
		m := mergedRe.FindStringSubmatch(strings.TrimSpace(out))
		if m == nil {
			t.Fatalf("respuesta = %q, quería «#N junta #%d, #%d (quedan sustituidos)»", out, a, b)
		}
		n := idInt(t, m[1])
		if want := fmt.Sprintf("#%d junta #%d, #%d (quedan sustituidos)", n, a, b); strings.TrimSpace(out) != want {
			t.Errorf("respuesta = %q, quería %q", out, want)
		}
		if got := memField(t, d, n, "title"); got != "Coche de Ana (junto)" {
			t.Errorf("título nuevo = %q", got)
		}
		if got := memField(t, d, n, "content"); got != "Ana tiene un Seat azul" {
			t.Errorf("contenido nuevo = %q", got)
		}
		if got := memField(t, d, n, "kind"); got != "decision" {
			t.Errorf("tipo nuevo = %q, quería el del primer id (decision)", got)
		}
		if got := memField(t, d, n, "occurred_at::date"); got != "2024-03-05" {
			t.Errorf("occurred_at nuevo = %q, quería 2024-03-05", got)
		}
		if got := memField(t, d, n, "key"); got != "ana/coche" {
			t.Errorf("clave nueva = %q, quería ana/coche", got)
		}
		wantStatuses(t, d, a, "superseded", b, "superseded")
		if got := memStatus(t, d, n); got != "active" {
			t.Errorf("estado del nuevo = %q, quería active", got)
		}
		wantSlugs(t, d, m[1], "alpha", "beta", "zeta")
		if c := tagCount(t, d, `SELECT count(*) FROM memories WHERE key = 'ana/coche' AND status = 'active'`); c != 1 {
			t.Errorf("registros vigentes con la clave = %d, quería 1", c)
		}
		wantRelsOf(t, d, n,
			fmt.Sprintf("%d>%d supersedes confirmed", n, a),
			fmt.Sprintf("%d>%d supersedes confirmed", n, b),
			fmt.Sprintf("%d>%d contradicts confirmed", n, x),
			fmt.Sprintf("%d>%d part_of suggested", y, n),
		)
		wantRels(t, d, a, b, fmt.Sprintf("%d>%d related confirmed - -", a, b))
	})

	run("S15 confirm with given tags stores only those", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		a := save(t, cs, "Uno", "contenido uno", "note", "2024-03-05", "", "alpha")
		b := save(t, cs, "Dos", "contenido dos", "note", "2024-03-06", "", "beta")
		_, fp := mergeFingerprint(t, cs, []int64{a, b}, "Junto", "contenido junto", map[string]any{"tags": []string{"gamma"}})
		out := mustOK(t, cs, "revise", mergeArgs([]int64{a, b}, "Junto", "contenido junto",
			map[string]any{"tags": []string{"gamma"}, "confirm": true, "fingerprint": fp}))
		m := mergedRe.FindStringSubmatch(strings.TrimSpace(out))
		if m == nil {
			t.Fatalf("respuesta inesperada %q", out)
		}
		wantSlugs(t, d, m[1], "gamma")
	})

	run("S15 merge works inside ops", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		a := save(t, cs, "Uno", "contenido uno", "note", "2024-03-05", "", "alpha")
		b := save(t, cs, "Dos", "contenido dos", "note", "2024-03-06", "", "beta")
		out := mustOK(t, cs, "revise", map[string]any{"ops": []any{mergeArgs([]int64{a, b}, "Junto", "contenido junto", nil)}})
		wantContains(t, out, "VISTA PREVIA — juntar", "  huella: ")
		fp := strings.TrimPrefix(mergeLine(out, "  huella: "), "  huella: ")
		if !fingerprintRe.MatchString(fp) {
			t.Fatalf("huella %q no válida en ops", fp)
		}
		out = mustOK(t, cs, "revise", map[string]any{"ops": []any{mergeArgs([]int64{a, b}, "Junto", "contenido junto",
			map[string]any{"confirm": true, "fingerprint": fp})}})
		wantContains(t, out, " junta #", fmt.Sprintf("#%d, #%d (quedan sustituidos)", a, b))
		wantStatuses(t, d, a, "superseded", b, "superseded")
	})

	errCase := func(name string, setup func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) (map[string]any, string)) {
		run(name, func(t *testing.T, d Deps, cs *mcp.ClientSession) {
			a := save(t, cs, "Uno", "contenido uno", "note", "2024-03-05", "", "alpha")
			b := save(t, cs, "Dos", "contenido dos", "note", "2024-03-06", "", "beta")
			args, want := setup(t, d, cs, a, b)
			before := storeSnapshot(t, d)
			out, isErr := call(t, cs, "revise", args)
			if !isErr || !strings.Contains(out, want) {
				t.Errorf("respuesta (error=%v) = %q, quería un error con %q", isErr, out, want)
			}
			wantSnapshotUnchanged(t, d, before)
		})
	}
	confirmArgs := func(ids []int64, extra map[string]any) map[string]any {
		e := map[string]any{"confirm": true}
		for k, v := range extra {
			e[k] = v
		}
		return mergeArgs(ids, "Junto", "contenido junto", e)
	}

	errCase("S15 error with fewer than 2 ids", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) (map[string]any, string) {
		return mergeArgs([]int64{a}, "Junto", "contenido junto", nil), "hay que indicar al menos 2 registros en ids"
	})
	errCase("S15 error with no ids", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) (map[string]any, string) {
		return mergeArgs(nil, "Junto", "contenido junto", nil), "hay que indicar al menos 2 registros en ids"
	})
	errCase("S15 error when an id is not active", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) (map[string]any, string) {
		c := save(t, cs, "Tres", "contenido tres", "note", "2024-03-07", "", "gamma")
		mustOK(t, cs, "revise", map[string]any{"id": b, "action": "supersede", "with_id": c})
		return mergeArgs([]int64{a, b}, "Junto", "contenido junto", nil), fmt.Sprintf("#%d no está vigente", b)
	})
	errCase("S15 error when an id does not exist", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) (map[string]any, string) {
		return mergeArgs([]int64{a, 999999}, "Junto", "contenido junto", nil), "el registro #999999 no existe"
	})
	errCase("S15 confirm without fingerprint", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) (map[string]any, string) {
		return confirmArgs([]int64{a, b}, nil), "falta la huella de la vista previa (fingerprint)"
	})
	errCase("S15 confirm with a wrong fingerprint", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) (map[string]any, string) {
		_, fp := mergeFingerprint(t, cs, []int64{a, b}, "Junto", "contenido junto", nil)
		bad := "00000000"
		if fp == bad {
			bad = "ffffffff"
		}
		return confirmArgs([]int64{a, b}, map[string]any{"fingerprint": bad}), "los registros cambiaron desde la vista previa: vuelve a pedirla"
	})
	errCase("S15 confirm with a stale fingerprint after an original changed", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) (map[string]any, string) {
		_, fp := mergeFingerprint(t, cs, []int64{a, b}, "Junto", "contenido junto", nil)
		mustOK(t, cs, "revise", map[string]any{"id": b, "action": "update", "content": "contenido dos cambiado"})
		return confirmArgs([]int64{a, b}, map[string]any{"fingerprint": fp}), "los registros cambiaron desde la vista previa: vuelve a pedirla"
	})
	errCase("S15 empty title is rejected like in remember", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) (map[string]any, string) {
		return mergeArgs([]int64{a, b}, "  ", "contenido junto", nil), "el título no puede estar vacío"
	})
	errCase("S15 empty content is rejected like in remember", func(t *testing.T, d Deps, cs *mcp.ClientSession, a, b int64) (map[string]any, string) {
		return mergeArgs([]int64{a, b}, "Junto", "", nil), "el contenido no puede estar vacío"
	})

	run("S15 existing supersede still works", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		a := save(t, cs, "Uno", "contenido uno", "note", "2024-03-05", "", "alpha")
		b := save(t, cs, "Dos", "contenido dos", "note", "2024-03-06", "", "beta")
		mustOK(t, cs, "revise", map[string]any{"id": a, "action": "supersede", "with_id": b})
		wantStatuses(t, d, a, "superseded", b, "active")
		wantRels(t, d, a, b, fmt.Sprintf("%d>%d supersedes confirmed - -", b, a))
	})
}
