package memory

import (
	"context"
	"errors"
	"fmt"
)

// Get returns the complete records with the given ids (at most 20), in the
// order requested and without repeats. Ids that do not exist are omitted.
// Each record includes its topic slugs (sorted) and its relations in both
// directions.
func (s *Service) Get(ctx context.Context, ids []int64) ([]Full, error) {
	if len(ids) == 0 {
		return nil, errors.New("hay que indicar al menos un id")
	}
	if len(ids) > maxGetIDs {
		return nil, fmt.Errorf("se pidieron %d ids y el máximo es %d", len(ids), maxGetIDs)
	}

	rows, err := s.pool.Query(ctx, `SELECT m.id, m.kind::text, m.status::text, m.title, m.content,
			COALESCE(m.key, ''), COALESCE(m.session_id, ''), m.occurred_at, m.created_at, m.updated_at,
			COALESCE((SELECT array_agg(t.slug ORDER BY t.slug)
				FROM memory_topics mt JOIN topics t ON t.id = mt.topic_id
				WHERE mt.memory_id = m.id), ARRAY[]::text[])
		FROM memories m WHERE m.id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron leer los registros: %w", err)
	}
	defer rows.Close()

	byID := make(map[int64]*Full, len(ids))
	for rows.Next() {
		var f Full
		var kind, status string
		if err := rows.Scan(&f.ID, &kind, &status, &f.Title, &f.Content, &f.Key, &f.SessionID,
			&f.OccurredAt, &f.CreatedAt, &f.UpdatedAt, &f.Topics); err != nil {
			return nil, fmt.Errorf("no se pudo leer un registro: %w", err)
		}
		f.Kind, f.Status = Kind(kind), Status(status)
		byID[f.ID] = &f
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("no se pudieron leer los registros: %w", err)
	}
	rows.Close()
	if len(byID) == 0 {
		return nil, nil
	}

	rels, err := s.pool.Query(ctx, `
		SELECT r.source_id AS this_id, r.target_id AS other_id, r.kind::text AS kind, r.state::text AS state,
			true AS outgoing, o.title
		FROM memory_relations r JOIN memories o ON o.id = r.target_id
		WHERE r.source_id = ANY($1)
		UNION ALL
		SELECT r.target_id, r.source_id, r.kind::text, r.state::text, false, o.title
		FROM memory_relations r JOIN memories o ON o.id = r.source_id
		WHERE r.target_id = ANY($1)
		ORDER BY this_id, outgoing DESC, kind, other_id`, ids)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron leer las relaciones: %w", err)
	}
	defer rels.Close()
	for rels.Next() {
		var thisID int64
		var r Relation
		if err := rels.Scan(&thisID, &r.OtherID, &r.Kind, &r.State, &r.Outgoing, &r.OtherTitle); err != nil {
			return nil, fmt.Errorf("no se pudo leer una relación: %w", err)
		}
		if f, ok := byID[thisID]; ok {
			f.Relations = append(f.Relations, r)
		}
	}
	if err := rels.Err(); err != nil {
		return nil, fmt.Errorf("no se pudieron leer las relaciones: %w", err)
	}

	out := make([]Full, 0, len(byID))
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if f, ok := byID[id]; ok && !seen[id] {
			seen[id] = true
			out = append(out, *f)
		}
	}
	return out, nil
}
