package gitsync_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"lodan/internal/memory"
)

// TestSyncTags covers T7 (S18) of odd/tasks/hashtags-y-calidad.md through git sync:
// aliases travel with the sync, relation lines carry verdict and confidence, the
// batch file format bumps to lodan-sync/2 only when there are aliases, the reader
// accepts both versions, suggested aliases stay local, confirmed aliases retag the
// local records on import, last-write-wins by updated_at resolves concurrent edits.
func TestSyncTags(t *testing.T) {
	run := func(name string, f func(t *testing.T, p *pair)) {
		t.Run(name, func(t *testing.T) {
			p := newPair(t)
			f(t, p)
		})
	}

	firstHeader := func(t *testing.T, plain []byte) map[string]any {
		t.Helper()
		end := bytes.IndexByte(plain, '\n')
		if end < 0 {
			t.Fatalf("el fichero descifrado no tiene saltos de línea: %q", plain)
		}
		var hdr map[string]any
		if err := json.Unmarshal(plain[:end], &hdr); err != nil {
			t.Fatalf("la cabecera no es JSON: %v", err)
		}
		return hdr
	}

	// S18 format: /2 with aliases, /1 without.
	run("S18 export emits lodan-sync/2 when there is at least one alias", func(t *testing.T, p *pair) {
		p.a.rem(t, "Coche de Ana", "El coche de Ana es un Seat", memory.KindFact, "", "alpha")
		p.a.revise(t, memory.Revision{Action: "alias", Tag: "alpha", WithTag: "zeta"})
		res := p.export(t, p.a)
		if res.File == "" {
			t.Fatal("se esperaba un fichero de export con el alias")
		}
		plain := decryptFile(t, p, res.File)
		if got := firstHeader(t, plain)["format"]; got != "lodan-sync/2" {
			t.Errorf("cabecera format = %v, quería lodan-sync/2", got)
		}
	})

	run("S18 export emits lodan-sync/1 when there are no aliases", func(t *testing.T, p *pair) {
		p.a.rem(t, "Una nota", "contenido de la nota", memory.KindNote, "", "general")
		res := p.export(t, p.a)
		if res.File == "" {
			t.Fatal("se esperaba un fichero de export con la nota")
		}
		plain := decryptFile(t, p, res.File)
		if got := firstHeader(t, plain)["format"]; got != "lodan-sync/1" {
			t.Errorf("cabecera format = %v, quería lodan-sync/1", got)
		}
	})

	// S18 suggested aliases stay local.
	run("S18 suggested aliases are not exported", func(t *testing.T, p *pair) {
		if _, err := p.a.pool.Exec(context.Background(),
			`INSERT INTO tag_aliases (alias, canonical, state) VALUES ('foo', 'bar', 'suggested')`); err != nil {
			t.Fatalf("no se pudo crear la sugerencia: %v", err)
		}
		res := p.export(t, p.a)
		if res.File != "" {
			plain := string(decryptFile(t, p, res.File))
			if strings.Contains(plain, `"alias":"foo"`) {
				t.Errorf("la sugerencia foo no debería exportarse:\n%s", plain)
			}
		}
	})

	// S18 relation lines carry verdict and confidence.
	run("S18 export carries verdict and confidence on relation lines", func(t *testing.T, p *pair) {
		idA := p.a.rem(t, "A", "contenido a", memory.KindNote, "", "general")
		idB := p.a.rem(t, "B", "contenido b", memory.KindNote, "", "general")
		p.a.revise(t, memory.Revision{Action: "relate", ID: idA, WithID: idB, Relation: "related"})
		p.a.revise(t, memory.Revision{Action: "verdict", ID: idA, WithID: idB, Verdict: "compatible", Confidence: ptr(0.9)})
		res := p.export(t, p.a)
		if res.File == "" {
			t.Fatal("se esperaba un fichero de export con la relación")
		}
		plain := decryptFile(t, p, res.File)
		var found bool
		for _, line := range bytes.Split(bytes.TrimSpace(plain), []byte("\n")) {
			var r map[string]any
			if err := json.Unmarshal(line, &r); err != nil {
				continue
			}
			if r["type"] != "relation" {
				continue
			}
			if r["verdict"] == "compatible" && r["confidence"] != nil {
				found = true
			}
		}
		if !found {
			t.Errorf("no se encontró una relation con verdict=compatible y confidence: %s", plain)
		}
	})

	// S18 import re-tags records with the canonical of an imported confirmed alias.
	run("S18 import of a confirmed alias retags local records to the canonical", func(t *testing.T, p *pair) {
		idA := p.a.rem(t, "Coche de Ana", "El coche de Ana es un Seat", memory.KindFact, "", "alpha")
		p.a.revise(t, memory.Revision{Action: "alias", Tag: "alpha", WithTag: "zeta"})
		p.export(t, p.a)
		if _, err := p.b.pool.Exec(context.Background(),
			`INSERT INTO memories (sync_id, sync_rev, sync_dirty, kind, title, content, occurred_at)
			 VALUES (gen_random_uuid(), gen_random_uuid(), false, 'fact', 'Copia del coche', 'otra versión', now())`); err != nil {
			t.Fatalf("no se pudo crear el record en B: %v", err)
		}
		if _, err := p.b.pool.Exec(context.Background(),
			`INSERT INTO memory_topics (memory_id, topic_id, kind, ts)
			 SELECT m.id, t.id, 'fact'::memory_kind, now()
			 FROM memories m, topics t WHERE m.title = 'Copia del coche' AND t.slug = 'alpha'`); err != nil {
			t.Fatalf("no se pudo etiquetar la copia con alpha: %v", err)
		}
		_ = idA
		p.import_(t, p.b)
		var withAlpha, withZeta int
		if err := p.b.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM memory_topics mt JOIN topics t ON t.id = mt.topic_id WHERE t.slug = 'alpha'`).Scan(&withAlpha); err != nil {
			t.Fatal(err)
		}
		if err := p.b.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM memory_topics mt JOIN topics t ON t.id = mt.topic_id WHERE t.slug = 'zeta'`).Scan(&withZeta); err != nil {
			t.Fatal(err)
		}
		if withAlpha != 0 || withZeta == 0 {
			t.Errorf("B debería tener zeta y no alpha: alpha=%d zeta=%d", withAlpha, withZeta)
		}
	})

	// S18 last-write-wins by updated_at on alias edits.
	run("S18 incoming alias wins when its updated_at is newer", func(t *testing.T, p *pair) {
		p.a.revise(t, memory.Revision{Action: "alias", Tag: "agm", WithTag: "agridemur"})
		p.export(t, p.a)
		// B already has agm → agridemur viejo.
		if _, err := p.b.pool.Exec(context.Background(),
			`INSERT INTO tag_aliases (alias, canonical, state, created_at, updated_at)
			 VALUES ('agm', 'otro', 'confirmed', now() - interval '1 day', now() - interval '1 day')`); err != nil {
			t.Fatalf("no se pudo crear el alias antiguo en B: %v", err)
		}
		p.import_(t, p.b)
		var canonical string
		if err := p.b.pool.QueryRow(context.Background(),
			`SELECT canonical FROM tag_aliases WHERE alias = 'agm' AND state = 'confirmed'`).Scan(&canonical); err != nil {
			t.Fatal(err)
		}
		if canonical != "agridemur" {
			t.Errorf("el canónico de agm = %q, quería agridemur", canonical)
		}
	})

	run("S18 incoming alias loses when the local updated_at is newer", func(t *testing.T, p *pair) {
		if _, err := p.a.pool.Exec(context.Background(),
			`INSERT INTO tag_aliases (alias, canonical, state, created_at, updated_at)
			 VALUES ('agm', 'antiguo-a', 'confirmed', now() - interval '1 day', now() - interval '1 day')`); err != nil {
			t.Fatal(err)
		}
		p.export(t, p.a)
		if _, err := p.b.pool.Exec(context.Background(),
			`INSERT INTO tag_aliases (alias, canonical, state, created_at, updated_at)
			 VALUES ('agm', 'agridemur', 'confirmed', now(), now())`); err != nil {
			t.Fatalf("no se pudo crear el alias reciente en B: %v", err)
		}
		p.import_(t, p.b)
		var canonical string
		if err := p.b.pool.QueryRow(context.Background(),
			`SELECT canonical FROM tag_aliases WHERE alias = 'agm' AND state = 'confirmed'`).Scan(&canonical); err != nil {
			t.Fatal(err)
		}
		if canonical != "agridemur" {
			t.Errorf("el canónico local debería ganar: agm = %q, quería agridemur", canonical)
		}
	})

	// S18 verdict/confidence on import.
	run("S18 import stores verdict and confidence on the relation", func(t *testing.T, p *pair) {
		idA := p.a.rem(t, "A", "contenido a", memory.KindNote, "", "general")
		idB := p.a.rem(t, "B", "contenido b", memory.KindNote, "", "general")
		p.a.revise(t, memory.Revision{Action: "relate", ID: idA, WithID: idB, Relation: "related"})
		p.a.revise(t, memory.Revision{Action: "verdict", ID: idA, WithID: idB, Verdict: "compatible", Confidence: ptr(0.85)})
		p.export(t, p.a)
		p.import_(t, p.b)
		var verdict string
		var confidence float64
		err := p.b.pool.QueryRow(context.Background(),
			`SELECT COALESCE(verdict, ''), COALESCE(confidence, 0)::float8
			 FROM memory_relations
			 WHERE source_id = $1 AND target_id = $2 AND kind = 'related'`,
			p.b.idBySync(t, p.a.syncID(t, idA)), p.b.idBySync(t, p.a.syncID(t, idB)),
		).Scan(&verdict, &confidence)
		if err != nil {
			t.Fatalf("no se pudo leer la relación en B: %v", err)
		}
		if verdict != "compatible" || confidence != 0.85 {
			t.Errorf("veredicto/confianza = (%q, %v), quería (compatible, 0.85)", verdict, confidence)
		}
	})
}

func ptr(f float64) *float64 { return &f }