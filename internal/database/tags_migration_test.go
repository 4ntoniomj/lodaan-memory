package database

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestMigration0004Tags covers S17 (schema only) of odd/tasks/hashtags-y-calidad.md.
func TestMigration0004Tags(t *testing.T) {
	tc := NewTestCluster(t, 8)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	exec := func(sql string, args ...any) error {
		_, err := tc.Pool.Exec(ctx, sql, args...)
		return err
	}
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if err := exec(sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	wantReject := func(what, sql string, args ...any) {
		t.Helper()
		if err := exec(sql, args...); err == nil {
			t.Errorf("debería rechazarse: %s", what)
		}
	}

	t.Run("tag_aliases table, columns, key and constraints", func(t *testing.T) {
		cols := map[string]string{}
		rows, err := tc.Pool.Query(ctx, `SELECT column_name, udt_name FROM information_schema.columns WHERE table_name = 'tag_aliases'`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var n, ty string
			if err := rows.Scan(&n, &ty); err != nil {
				t.Fatal(err)
			}
			cols[n] = ty
		}
		rows.Close()
		for _, c := range []string{"alias", "canonical", "state", "created_at", "updated_at", "sync_xid"} {
			if _, ok := cols[c]; !ok {
				t.Errorf("tag_aliases no tiene la columna %s (tiene %v)", c, cols)
			}
		}
		if cols["sync_xid"] != "xid8" {
			t.Errorf("tag_aliases.sync_xid es %q, quería xid8", cols["sync_xid"])
		}

		var pk string
		if err := tc.Pool.QueryRow(ctx, `SELECT COALESCE(string_agg(a.attname, ',' ORDER BY k.ord), '')
			FROM pg_index i
			CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
			JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
			WHERE i.indrelid = to_regclass('tag_aliases') AND i.indisprimary`).Scan(&pk); err != nil {
			t.Fatal(err)
		}
		if pk != "alias,canonical" {
			t.Errorf("clave primaria de tag_aliases = %q, quería alias,canonical", pk)
		}

		for i, st := range []string{"confirmed", "suggested", "rejected"} {
			if err := exec(`INSERT INTO tag_aliases (alias, canonical, state) VALUES ($1, 'c', $2)`, fmt.Sprintf("a%d", i), st); err != nil {
				t.Errorf("el estado %s debería aceptarse: %v", st, err)
			}
		}
		wantReject("state fuera de confirmed|suggested|rejected", `INSERT INTO tag_aliases (alias, canonical, state) VALUES ('x', 'c', 'otro')`)
		wantReject("par (alias, canonical) repetido", `INSERT INTO tag_aliases (alias, canonical, state) VALUES ('a0', 'c', 'suggested')`)
		wantReject("segundo confirmed del mismo alias", `INSERT INTO tag_aliases (alias, canonical, state) VALUES ('a0', 'd', 'confirmed')`)
		if err := exec(`INSERT INTO tag_aliases (alias, canonical, state) VALUES ('a0', 'd', 'suggested')`); err != nil {
			t.Errorf("una sugerencia más para un alias ya confirmado no debe rechazarse: %v", err)
		}
	})

	t.Run("memory_relations verdict and confidence", func(t *testing.T) {
		mustExec(`INSERT INTO memories (kind, title, content, content_hash) VALUES
			('note','a','ca','\x01'), ('note','b','cb','\x02'), ('note','c','cc','\x03'), ('note','d','cd','\x04'),
			('note','e','ce','\x05'), ('note','f','cf','\x06')`)
		for _, col := range []string{"verdict", "confidence"} {
			var nullable string
			if err := tc.Pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns
				WHERE table_name = 'memory_relations' AND column_name = $1`, col).Scan(&nullable); err != nil {
				t.Fatalf("falta memory_relations.%s: %v", col, err)
			}
			if nullable != "YES" {
				t.Errorf("memory_relations.%s debería admitir NULL", col)
			}
		}
		if err := exec(`INSERT INTO memory_relations (source_id, target_id, kind, state) VALUES (1, 2, 'related', 'confirmed')`); err != nil {
			t.Errorf("una relación sin veredicto ni confianza debe seguir siendo válida: %v", err)
		}
		target := int64(2)
		for _, v := range []string{"contradice", "sustituye", "compatible", "no_relacionado"} {
			target++
			if err := exec(`INSERT INTO memory_relations (source_id, target_id, kind, state, verdict, confidence)
				VALUES (1, $1, 'related', 'confirmed', $2, 0.9)`, target, v); err != nil {
				t.Errorf("el veredicto %s debería aceptarse: %v", v, err)
			}
		}
		wantReject("veredicto no válido", `INSERT INTO memory_relations (source_id, target_id, kind, state, verdict) VALUES (2, 3, 'related', 'confirmed', 'quizas')`)
		for _, c := range []string{"-0.1", "1.1"} {
			wantReject("confianza fuera de 0..1: "+c, `INSERT INTO memory_relations (source_id, target_id, kind, state, confidence) VALUES (2, 3, 'part_of', 'confirmed', `+c+`)`)
		}
		for i, c := range []string{"0", "1"} {
			if err := exec(`INSERT INTO memory_relations (source_id, target_id, kind, state, confidence) VALUES (3, 4, $1::relation_kind, 'confirmed', `+c+`)`,
				[]string{"part_of", "contradicts"}[i]); err != nil {
				t.Errorf("la confianza %s debería aceptarse: %v", c, err)
			}
		}
	})

	t.Run("migrating an existing database changes no existing data", func(t *testing.T) {
		var exists bool
		if err := tc.Pool.QueryRow(ctx, `SELECT to_regclass('tag_aliases') IS NOT NULL`).Scan(&exists); err != nil || !exists {
			t.Fatalf("la migración 0004 no está aplicada (tag_aliases existe: %v, %v)", exists, err)
		}
		mustExec(`TRUNCATE memories, topics, memory_topics, memory_relations, tag_aliases RESTART IDENTITY CASCADE`)

		// Rewind to the schema before 0004 and fill it with data.
		mustExec(`DROP TABLE tag_aliases`)
		mustExec(`ALTER TABLE memory_relations DROP COLUMN verdict, DROP COLUMN confidence`)
		mustExec(`DELETE FROM schema_migrations WHERE version = 4`)
		mustExec(`INSERT INTO memories (kind, title, content, content_hash) VALUES
			('fact','uno','contenido uno','\x11'), ('decision','dos','contenido dos','\x12')`)
		mustExec(`INSERT INTO topics (slug) VALUES ('lodan'), ('agm')`)
		mustExec(`INSERT INTO memory_topics (topic_id, memory_id, kind, active, ts) VALUES
			(1, 1, 'fact', true, now()), (2, 1, 'fact', true, now()), (1, 2, 'decision', true, now())`)
		mustExec(`INSERT INTO memory_relations (source_id, target_id, kind, state) VALUES (2, 1, 'supersedes', 'confirmed')`)

		snapshot := func() string {
			var parts []string
			for _, q := range []string{
				`SELECT id, kind, status, title, content, key, content_hash, occurred_at, created_at, updated_at, sync_id, sync_xid FROM memories ORDER BY id`,
				`SELECT id, slug, created_at, last_used_at FROM topics ORDER BY id`,
				`SELECT topic_id, memory_id, kind, active, ts FROM memory_topics ORDER BY topic_id, memory_id`,
				`SELECT source_id, target_id, kind, state, created_at, sync_xid FROM memory_relations ORDER BY source_id, target_id, kind`,
			} {
				var s string
				if err := tc.Pool.QueryRow(ctx, `SELECT COALESCE(string_agg(t::text, '|'), '') FROM (`+q+`) t`).Scan(&s); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
				parts = append(parts, s)
			}
			return strings.Join(parts, "\n")
		}
		before := snapshot()

		if err := Migrate(ctx, tc.Pool, Settings{Dims: 8, Model: "test-model"}); err != nil {
			t.Fatalf("Migrate sobre la base antigua falló: %v", err)
		}
		if after := snapshot(); after != before {
			t.Errorf("los datos existentes cambiaron:\nantes:\n%s\ndespués:\n%s", before, after)
		}
		var n int
		if err := tc.Pool.QueryRow(ctx, `SELECT count(*) FROM memory_relations WHERE verdict IS NULL AND confidence IS NULL`).Scan(&n); err != nil || n != 1 {
			t.Errorf("la relación existente debería tener verdict y confidence NULL (n=%d, err=%v)", n, err)
		}
		if err := tc.Pool.QueryRow(ctx, `SELECT count(*) FROM tag_aliases`).Scan(&n); err != nil || n != 0 {
			t.Errorf("tag_aliases debería existir vacía (n=%d, err=%v)", n, err)
		}
	})
}
