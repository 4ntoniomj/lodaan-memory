package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"lodan/internal/database"
	"lodan/internal/topic"
)

// TagSuggestion is an existing tag that may be the same as a tag created by Remember.
type TagSuggestion struct {
	// New is the tag just created.
	New string
	// Existing is the tag that may be the same.
	Existing string
	// Records is the number of active records that carry Existing.
	Records int
}

// aliasPair normalizes the two tags of an alias action.
func aliasPair(r Revision) (tag, with string, err error) {
	tag, with = topic.Normalize(r.Tag), topic.Normalize(r.WithTag)
	if tag == "" || with == "" {
		return "", "", errors.New("hay que indicar tag y with_tag")
	}
	if tag == with {
		return "", "", errors.New("una etiqueta no puede ser alias de sí misma")
	}
	return tag, with, nil
}

// alias implements the "alias" action: confirms tag → with_tag (or the final
// canonical of with_tag, so there are no chains), re-points the aliases of tag
// and moves every record tagged tag to the canonical tag.
func (s *Service) alias(ctx context.Context, r Revision) (string, error) {
	tag, with, err := aliasPair(r)
	if err != nil {
		return "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("no se pudo iniciar la transacción: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	canonical := with
	var final string
	switch err := tx.QueryRow(ctx, `SELECT canonical FROM tag_aliases WHERE alias = $1 AND state = 'confirmed'`, with).Scan(&final); {
	case err == nil:
		canonical = final
	case !errors.Is(err, pgx.ErrNoRows):
		return "", fmt.Errorf("no se pudo consultar el diccionario de etiquetas: %w", err)
	}
	if canonical == tag {
		return "", errors.New("una etiqueta no puede ser alias de sí misma")
	}

	// tag pasa a ser alias confirmado de canonical; sus otras propuestas sobran.
	if _, err := tx.Exec(ctx, `DELETE FROM tag_aliases WHERE alias = $1 AND canonical <> $2 AND state <> 'rejected'`, tag, canonical); err != nil {
		return "", fmt.Errorf("no se pudo actualizar el diccionario de etiquetas: %w", err)
	}
	if err := upsertAlias(ctx, tx, tag, canonical, "confirmed"); err != nil {
		return "", err
	}
	// Los alias que apuntaban a tag apuntan ahora a canonical.
	rows, err := tx.Query(ctx, `DELETE FROM tag_aliases WHERE canonical = $1 AND state = 'confirmed' RETURNING alias`, tag)
	if err != nil {
		return "", fmt.Errorf("no se pudo actualizar el diccionario de etiquetas: %w", err)
	}
	repoint, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", fmt.Errorf("no se pudo actualizar el diccionario de etiquetas: %w", err)
	}
	for _, a := range repoint {
		if a != canonical {
			if err := upsertAlias(ctx, tx, a, canonical, "confirmed"); err != nil {
				return "", err
			}
		}
	}

	moved, err := retag(ctx, tx, tag, canonical)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("no se pudo confirmar el alias: %w", err)
	}
	return fmt.Sprintf("alias #%s → #%s (%d registros reetiquetados)", tag, canonical, moved), nil
}

// rejectAlias implements the "reject_alias" action.
func (s *Service) rejectAlias(ctx context.Context, r Revision) (string, error) {
	tag, with, err := aliasPair(r)
	if err != nil {
		return "", err
	}
	if err := upsertAlias(ctx, s.pool, tag, with, "rejected"); err != nil {
		return "", err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM tag_aliases WHERE alias = $1 AND canonical = $2 AND state = 'suggested'`, with, tag); err != nil {
		return "", fmt.Errorf("no se pudo actualizar el diccionario de etiquetas: %w", err)
	}
	return fmt.Sprintf("#%s ≠ #%s: no se volverá a sugerir", tag, with), nil
}

// upsertAlias stores alias → canonical with the given state.
func upsertAlias(ctx context.Context, q database.Querier, alias, canonical, state string) error {
	if _, err := q.Exec(ctx, `INSERT INTO tag_aliases (alias, canonical, state) VALUES ($1, $2, $3)
		ON CONFLICT (alias, canonical) DO UPDATE SET state = EXCLUDED.state`, alias, canonical, state); err != nil {
		return fmt.Errorf("no se pudo guardar el alias: %w", err)
	}
	return nil
}

// retag moves every record (and session) tagged with tag, or with a tag equal
// to it without hyphens, to canonical, deletes those tags and returns how many
// records carried them.
func retag(ctx context.Context, tx pgx.Tx, tag, canonical string) (int, error) {
	var canonID int32
	if err := tx.QueryRow(ctx, `INSERT INTO topics (slug) VALUES ($1)
		ON CONFLICT (slug) DO UPDATE SET last_used_at = now() RETURNING id`, canonical).Scan(&canonID); err != nil {
		return 0, fmt.Errorf("no se pudo crear la etiqueta %q: %w", canonical, err)
	}
	var moved int
	err := tx.QueryRow(ctx, `WITH old AS (
			SELECT id FROM topics WHERE replace(slug, '-', '') = replace($1, '-', '') AND id <> $2
		), copied AS (
			INSERT INTO memory_topics (topic_id, memory_id, kind, active, ts)
			SELECT $2, mt.memory_id, mt.kind, mt.active, mt.ts FROM memory_topics mt JOIN old ON old.id = mt.topic_id
			ON CONFLICT (topic_id, memory_id) DO NOTHING
		), sessions AS (
			INSERT INTO session_topics (session_id, topic_id)
			SELECT st.session_id, $2 FROM session_topics st JOIN old ON old.id = st.topic_id
			ON CONFLICT DO NOTHING
		)
		SELECT count(DISTINCT mt.memory_id)::int FROM memory_topics mt JOIN old ON old.id = mt.topic_id`,
		tag, canonID).Scan(&moved)
	if err != nil {
		return 0, fmt.Errorf("no se pudieron reetiquetar los registros: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM topics WHERE replace(slug, '-', '') = replace($1, '-', '') AND id <> $2`, tag, canonID); err != nil {
		return 0, fmt.Errorf("no se pudo borrar la etiqueta %q: %w", tag, err)
	}
	return moved, nil
}
