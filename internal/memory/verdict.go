package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// verdictThreshold is the confidence from which a verdict is applied; below
// it the verdict is only stored as a suggested relation.
const verdictThreshold = 0.7

// verdictKinds maps each verdict to the relation kind it stores; no_relacionado
// stores none when it is not applied.
var verdictKinds = map[string]string{
	"contradice":     "contradicts",
	"sustituye":      "supersedes",
	"compatible":     "related",
	"no_relacionado": "",
}

// verdict implements the "verdict" action, read as "r.ID <verdict> r.WithID".
func (s *Service) verdict(ctx context.Context, r Revision) (string, error) {
	if r.WithID <= 0 {
		return "", errors.New("verdict necesita with_id: el registro con el que se compara")
	}
	verdict := strings.ToLower(strings.TrimSpace(r.Verdict))
	kind, ok := verdictKinds[verdict]
	if !ok {
		return "", fmt.Errorf("veredicto %q no válido (válidos: contradice, sustituye, compatible, no_relacionado)", r.Verdict)
	}
	if r.Confidence == nil || *r.Confidence < 0 || *r.Confidence > 1 {
		return "", errors.New("la confianza debe estar entre 0 y 1")
	}
	conf := *r.Confidence
	if r.WithID == r.ID {
		return "", errors.New("un registro no puede compararse consigo mismo")
	}
	a, b := r.ID, r.WithID

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("no se pudo iniciar la transacción: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var msg string
	switch {
	case conf < verdictThreshold:
		if _, err := lockMemories(ctx, tx, a, b); err != nil {
			return "", err
		}
		if kind != "" {
			if err := suggestVerdict(ctx, tx, a, b, kind, verdict, conf); err != nil {
				return "", err
			}
		}
		msg = fmt.Sprintf("veredicto «%s» con confianza %.2f: queda pendiente, pregunta al usuario", verdict, conf)
	case verdict == "sustituye":
		if err := supersedeIn(ctx, tx, b, a); err != nil {
			return "", err
		}
		if err := upsertVerdictRelation(ctx, tx, a, b, kind, "confirmed", verdict, conf); err != nil {
			return "", err
		}
		msg = fmt.Sprintf("#%d sustituye a #%d", a, b)
	default:
		if _, err := lockMemories(ctx, tx, a, b); err != nil {
			return "", err
		}
		switch verdict {
		case "contradice":
			if err := upsertVerdictRelation(ctx, tx, a, b, kind, "confirmed", verdict, conf); err != nil {
				return "", err
			}
			msg = fmt.Sprintf("#%d contradice #%d: pregunta al usuario cuál es el bueno", a, b)
		case "compatible":
			if err := upsertVerdictRelation(ctx, tx, a, b, kind, "confirmed", verdict, conf); err != nil {
				return "", err
			}
			if _, err := rejectRelations(ctx, tx, a, b, "contradicts"); err != nil {
				return "", err
			}
			msg = fmt.Sprintf("#%d y #%d son compatibles", a, b)
		case "no_relacionado":
			n, err := rejectRelations(ctx, tx, a, b, "related", "contradicts")
			if err != nil {
				return "", err
			}
			if n == 0 {
				if err := upsertVerdictRelation(ctx, tx, a, b, "related", "rejected", verdict, conf); err != nil {
					return "", err
				}
			}
			msg = fmt.Sprintf("#%d y #%d no tienen que ver", a, b)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("no se pudo confirmar el veredicto: %w", err)
	}
	return msg, nil
}

// upsertVerdictRelation creates the relation source -> target with the given
// state, verdict and confidence, or sets them if it already exists. state is
// always in the SET list so the sync trigger marks the row as changed.
func upsertVerdictRelation(ctx context.Context, tx pgx.Tx, source, target int64, kind, state, verdict string, conf float64) error {
	_, err := tx.Exec(ctx, `INSERT INTO memory_relations (source_id, target_id, kind, state, verdict, confidence)
		VALUES ($1, $2, $3::text::relation_kind, $4::text::relation_state, $5, $6)
		ON CONFLICT (source_id, target_id, kind) DO UPDATE
		SET state = EXCLUDED.state, verdict = EXCLUDED.verdict, confidence = EXCLUDED.confidence`,
		source, target, kind, state, verdict, conf)
	if err != nil {
		return fmt.Errorf("no se pudo guardar el veredicto de #%d -> #%d: %w", source, target, err)
	}
	return nil
}

// suggestVerdict stores a low-confidence verdict as a suggested relation
// source -> target. A relation of that kind already confirmed or rejected is
// left untouched: nothing is applied below the threshold.
func suggestVerdict(ctx context.Context, tx pgx.Tx, source, target int64, kind, verdict string, conf float64) error {
	_, err := tx.Exec(ctx, `INSERT INTO memory_relations (source_id, target_id, kind, state, verdict, confidence)
		VALUES ($1, $2, $3::text::relation_kind, 'suggested', $4, $5)
		ON CONFLICT (source_id, target_id, kind) DO UPDATE
		SET state = EXCLUDED.state, verdict = EXCLUDED.verdict, confidence = EXCLUDED.confidence
		WHERE memory_relations.state = 'suggested'`,
		source, target, kind, verdict, conf)
	if err != nil {
		return fmt.Errorf("no se pudo guardar el veredicto pendiente de #%d -> #%d: %w", source, target, err)
	}
	return nil
}

// rejectRelations rejects every relation of the given kinds between a and b,
// in either direction, and returns how many relations of those kinds exist
// between them (rejected before or now).
func rejectRelations(ctx context.Context, tx pgx.Tx, a, b int64, kinds ...string) (int64, error) {
	if _, err := tx.Exec(ctx, `UPDATE memory_relations SET state = 'rejected'
		WHERE kind::text = ANY($3) AND state <> 'rejected'
		  AND ((source_id = $1 AND target_id = $2) OR (source_id = $2 AND target_id = $1))`,
		a, b, kinds); err != nil {
		return 0, fmt.Errorf("no se pudieron rechazar las relaciones entre #%d y #%d: %w", a, b, err)
	}
	var n int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM memory_relations
		WHERE kind::text = ANY($3)
		  AND ((source_id = $1 AND target_id = $2) OR (source_id = $2 AND target_id = $1))`,
		a, b, kinds).Scan(&n); err != nil {
		return 0, fmt.Errorf("no se pudieron contar las relaciones entre #%d y #%d: %w", a, b, err)
	}
	return n, nil
}
