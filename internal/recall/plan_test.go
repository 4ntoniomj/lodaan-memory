package recall

import (
	"context"
	"strings"
	"testing"

	"lodan/internal/database"
)

// TestFichaUsaIndicesParciales comprueba que ProfileSQL y EventsSQL pueden usar los índices
// parciales de la migración 0002: sus predicados deben ser idénticos a los del índice para
// que el planificador los reconozca. Si alguien cambia una consulta o un índice y dejan de
// coincidir, este test falla.
func TestFichaUsaIndicesParciales(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)
	tc.Reset(t)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// Datos suficientes para que las estadísticas tengan sentido: 5000 registros repartidos
	// entre 20 temas y los 5 tipos.
	for _, stmt := range []string{
		`INSERT INTO topics (slug) SELECT 'tema-' || g FROM generate_series(1, 20) g`,
		`INSERT INTO memories (kind, title, content, content_hash, occurred_at)
			SELECT (ARRAY['fact','preference','decision','event','note'])[1 + g % 5]::memory_kind,
				'titulo ' || g, 'contenido ' || g, sha256(convert_to(g::text, 'UTF8')),
				now() - g * interval '1 minute'
			FROM generate_series(1, 5000) g`,
		`INSERT INTO memory_topics (topic_id, memory_id, kind, active, ts)
			SELECT (SELECT min(id) FROM topics) + (m.id % 20)::int, m.id, m.kind, true, m.occurred_at
			FROM memories m`,
		`ANALYZE memories`,
		`ANALYZE memory_topics`,
	} {
		if _, err := tc.Pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("no se pudieron preparar los datos: %v\n%s", err, stmt)
		}
	}
	var topicID int32
	if err := tc.Pool.QueryRow(ctx, `SELECT min(id) FROM topics`).Scan(&topicID); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		sql   string
		limit int
		index string
	}{
		{"ProfileSQL", ProfileSQL, profileLimit, "memory_topics_profile_idx"},
		{"EventsSQL", EventsSQL, eventsLimit, "memory_topics_events_idx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// SET LOCAL solo dura lo que la transacción: no contamina la conexión del pool.
			tx, err := tc.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
				t.Fatal(err)
			}

			rows, err := tx.Query(ctx, `EXPLAIN (FORMAT TEXT) `+tt.sql, topicID, tt.limit)
			if err != nil {
				t.Fatalf("EXPLAIN falló: %v", err)
			}
			var plan strings.Builder
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				plan.WriteString(line + "\n")
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(plan.String(), tt.index) {
				t.Errorf("el plan de %s no usa el índice %s:\n%s", tt.name, tt.index, plan.String())
			}
		})
	}
}
