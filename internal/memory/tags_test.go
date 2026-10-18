package memory

import (
	"context"
	"reflect"
	"testing"

	"lodan/internal/database"
)

func (e *env) slugsOf(t *testing.T, id int64) []string {
	t.Helper()
	rows, err := e.pool.Query(e.ctx, `SELECT t.slug FROM memory_topics mt JOIN topics t ON t.id = mt.topic_id
		WHERE mt.memory_id = $1 ORDER BY t.slug`, id)
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

func (e *env) wantSlugs(t *testing.T, id int64, want ...string) {
	t.Helper()
	if got := e.slugsOf(t, id); !reflect.DeepEqual(got, want) {
		t.Errorf("etiquetas del #%d = %v, quería %v", id, got, want)
	}
}

// TestTags covers T1 (S1-S3) of odd/tasks/hashtags-y-calidad.md at the memory.Service level.
func TestTags(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)
	run := func(name string, f func(t *testing.T, e *env)) {
		t.Run(name, func(t *testing.T) {
			tc.Reset(t)
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			f(t, newEnv(ctx, tc))
		})
	}

	run("S1 Item.Tags and Item.Topics are joined, # ignored, normalized, repeats removed", func(t *testing.T, e *env) {
		s := e.save(t, Item{
			Title: "Coche", Content: "contenido del coche", Kind: KindNote,
			Tags: []string{"#VDM", "Coche 4860", "vdm"}, Topics: []string{"ITV"},
		})
		e.wantSlugs(t, s.ID, "coche-4860", "itv", "vdm")
		if len(s.Topics) != 3 {
			t.Errorf("Saved.Topics tiene %d resoluciones, quería 3", len(s.Topics))
		}
	})

	run("S1 more than 8 distinct tags: error and nothing saved", func(t *testing.T, e *env) {
		_, err := e.svc.Remember(e.ctx, e.tr, []Item{
			{Title: "Bien", Content: "uno válido", Kind: KindNote, Tags: []string{"uno"}},
			{Title: "Mal", Content: "nueve etiquetas", Kind: KindNote,
				Tags: []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7"}, Topics: []string{"t8", "t9"}},
		})
		wantErr(t, err, "ítem 2: como máximo 8 etiquetas (llegaron 9)")
		for _, table := range []string{"memories", "topics", "memory_topics"} {
			if n := e.count(t, "SELECT count(*) FROM "+table); n != 0 {
				t.Errorf("tras el error %s tiene %d filas, quería 0", table, n)
			}
		}
	})

	run("S1 exactly 8 distinct tags is accepted", func(t *testing.T, e *env) {
		s := e.save(t, Item{Title: "Ocho", Content: "ocho etiquetas", Kind: KindNote,
			Tags: []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "#T8"}})
		e.wantSlugs(t, s.ID, "t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8")
	})

	run("S1 no usable tag gives general", func(t *testing.T, e *env) {
		s := e.save(t, Item{Title: "Nada", Content: "sin etiquetas útiles", Kind: KindNote, Tags: []string{"#", " "}})
		e.wantSlugs(t, s.ID, "general")
	})

	run("S1 Revise update accepts Tags and Topics with the same rules", func(t *testing.T, e *env) {
		s := e.save(t, Item{Title: "Base", Content: "registro base", Kind: KindNote, Topics: []string{"inicial"}})
		e.revise(t, Revision{ID: s.ID, Action: "update", Tags: []string{"#Nuevo Uno", "dos"}})
		e.wantSlugs(t, s.ID, "dos", "nuevo-uno")
		e.revise(t, Revision{ID: s.ID, Action: "update", Tags: []string{"tres"}, Topics: []string{"#cuatro"}})
		e.wantSlugs(t, s.ID, "cuatro", "tres")

		_, err := e.svc.Revise(e.ctx, e.tr, Revision{ID: s.ID, Action: "update",
			Tags: []string{"t1", "t2", "t3", "t4", "t5"}, Topics: []string{"t6", "t7", "t8", "t9"}})
		wantErr(t, err, "como máximo 8 etiquetas (llegaron 9)")
		e.wantSlugs(t, s.ID, "cuatro", "tres")
	})

	run("S2 hyphen-insensitive resolution to an existing tag (remember and revise)", func(t *testing.T, e *env) {
		e.save(t, Item{Title: "A", Content: "primero", Kind: KindNote, Tags: []string{"coche-4860ngl"}})
		s := e.save(t, Item{Title: "B", Content: "segundo", Kind: KindNote, Tags: []string{"coche-4860-ngl"}})
		e.wantSlugs(t, s.ID, "coche-4860ngl")
		c := e.save(t, Item{Title: "C", Content: "tercero", Kind: KindNote, Tags: []string{"otra"}})
		e.revise(t, Revision{ID: c.ID, Action: "update", Tags: []string{"coche-4860-ngl"}})
		e.wantSlugs(t, c.ID, "coche-4860ngl")
		if n := e.count(t, `SELECT count(*) FROM topics WHERE slug = 'coche-4860-ngl'`); n != 0 {
			t.Errorf("se creó la etiqueta con guiones (%d)", n)
		}
	})

	run("S3 identical embeddings are not merged (remember and revise)", func(t *testing.T, e *env) {
		e.emb.WithAlias("gym", "entrenamiento")
		e.save(t, Item{Title: "A", Content: "primero", Kind: KindNote, Tags: []string{"entrenamiento"}})
		s := e.save(t, Item{Title: "B", Content: "segundo", Kind: KindNote, Tags: []string{"gym"}})
		e.wantSlugs(t, s.ID, "gym")
		if len(s.Topics) != 1 || s.Topics[0].Equivalent {
			t.Errorf("Saved.Topics = %+v, quería gym sin Equivalent", s.Topics)
		}
		c := e.save(t, Item{Title: "C", Content: "tercero", Kind: KindNote, Tags: []string{"otra"}})
		e.revise(t, Revision{ID: c.ID, Action: "update", Tags: []string{"gym"}})
		e.wantSlugs(t, c.ID, "gym")
		if n := e.count(t, `SELECT count(*) FROM topics`); n != 3 {
			t.Errorf("hay %d etiquetas, quería 3 (entrenamiento, gym, otra)", n)
		}
	})

	// Previous behaviour that must still hold.
	run("previous: exact tag reuse and general default through Topics", func(t *testing.T, e *env) {
		a := e.save(t, Item{Title: "A", Content: "primero", Kind: KindNote, Topics: []string{"lodan"}})
		b := e.save(t, Item{Title: "B", Content: "segundo", Kind: KindNote, Topics: []string{"Lodan"}})
		e.wantSlugs(t, a.ID, "lodan")
		e.wantSlugs(t, b.ID, "lodan")
		if n := e.count(t, `SELECT count(*) FROM topics`); n != 1 {
			t.Errorf("hay %d etiquetas, quería 1", n)
		}
		g := e.save(t, Item{Title: "G", Content: "tercero", Kind: KindNote})
		e.wantSlugs(t, g.ID, "general")
	})
}
