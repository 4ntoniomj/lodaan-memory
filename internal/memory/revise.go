package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"lodan/internal/embedding"
	"lodan/internal/session"
	"lodan/internal/topic"
)

// lockedMemory is the part of a record read while holding its row lock.
type lockedMemory struct {
	status     Status
	kind       Kind
	occurredAt time.Time
}

// Revise applies r.Action to the record r.ID and returns a one-line description
// of the result. Actions:
//   - update: changes title and/or content (when not empty) and replaces the
//     topics (when given) of an active record; a new content or title gets a
//     new embedding (NULL if the embedding backend is unavailable);
//   - supersede: marks r.ID as superseded by the active record r.WithID;
//   - invalidate: marks the record as invalidated;
//   - delete: physically deletes the record; needs r.Confirm;
//   - relate: creates a confirmed relation r.ID -> r.WithID of kind
//     related (default), contradicts or part_of;
//   - confirm_relation / reject_relation: sets the state of the relation of
//     kind r.Relation (default related) between r.ID and r.WithID, in either direction.
//
// tr may be nil; otherwise activity is recorded on its session.
func (s *Service) Revise(ctx context.Context, tr *session.Tracker, r Revision) (string, error) {
	if r.ID <= 0 {
		return "", errors.New("hay que indicar el id del registro")
	}
	action := strings.ToLower(strings.TrimSpace(r.Action))
	if tr != nil {
		if err := tr.Touch(ctx); err != nil {
			return "", err
		}
	}

	switch action {
	case "update":
		return s.update(ctx, r)
	case "supersede":
		return s.supersede(ctx, r)
	case "invalidate":
		return s.invalidate(ctx, r)
	case "delete":
		return s.delete(ctx, r)
	case "relate":
		return s.relate(ctx, r)
	case "confirm_relation":
		return s.setRelationState(ctx, r, "confirmed", "confirmada")
	case "reject_relation":
		return s.setRelationState(ctx, r, "rejected", "rechazada")
	default:
		return "", fmt.Errorf("acción %q no válida (válidas: update, supersede, invalidate, delete, relate, confirm_relation, reject_relation)", r.Action)
	}
}

// lockMemories locks the given records (in id order, to avoid deadlocks) and
// returns their state. It fails if any of them does not exist.
func lockMemories(ctx context.Context, tx pgx.Tx, ids ...int64) (map[int64]lockedMemory, error) {
	rows, err := tx.Query(ctx, `SELECT id, status::text, kind::text, occurred_at
		FROM memories WHERE id = ANY($1) ORDER BY id FOR UPDATE`, ids)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron bloquear los registros: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]lockedMemory, len(ids))
	for rows.Next() {
		var id int64
		var status, kind string
		var m lockedMemory
		if err := rows.Scan(&id, &status, &kind, &m.occurredAt); err != nil {
			return nil, fmt.Errorf("no se pudo leer un registro: %w", err)
		}
		m.status, m.kind = Status(status), Kind(kind)
		out[id] = m
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("no se pudieron bloquear los registros: %w", err)
	}
	for _, id := range ids {
		if _, ok := out[id]; !ok {
			return nil, notFound(id)
		}
	}
	return out, nil
}

// requireActive fails unless the record is active.
func requireActive(id int64, m lockedMemory, what string) error {
	if m.status != StatusActive {
		return fmt.Errorf("el registro #%d no está vigente (estado: %s); %s", id, m.status, what)
	}
	return nil
}

// update implements the "update" action.
func (s *Service) update(ctx context.Context, r Revision) (string, error) {
	title, content := strings.TrimSpace(r.Title), strings.TrimSpace(r.Content)
	if title == "" && content == "" && len(r.Topics) == 0 {
		return "", errors.New("update necesita al menos un título, un contenido o temas nuevos")
	}
	var err error
	if title != "" {
		if title, err = validateText("título", title, maxTitleLen); err != nil {
			return "", err
		}
	}
	if content != "" {
		if content, err = validateText("contenido", content, maxContentLen); err != nil {
			return "", err
		}
	}

	// Lectura previa sin bloqueo: el embedding es lento y no debe hacerse con una transacción abierta.
	var curTitle, curContent, status string
	err = s.pool.QueryRow(ctx, `SELECT title, content, status::text FROM memories WHERE id = $1`, r.ID).
		Scan(&curTitle, &curContent, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", notFound(r.ID)
	}
	if err != nil {
		return "", fmt.Errorf("no se pudo leer el registro #%d: %w", r.ID, err)
	}
	if Status(status) != StatusActive {
		return "", fmt.Errorf("el registro #%d no está vigente (estado: %s); solo se pueden modificar los vigentes", r.ID, status)
	}

	newTitle, newContent := curTitle, curContent
	if title != "" {
		newTitle = title
	}
	if content != "" {
		newContent = content
	}
	contentChanged := newContent != curContent
	titleChanged := newTitle != curTitle

	// Nuevo embedding si cambia el título o el contenido (los dos entran en el documento).
	var vec []float32
	var modelID *int16
	reembed := contentChanged || titleChanged
	if reembed {
		vecs, err := s.embedDocs(ctx, []embedding.Document{{Title: newTitle, Text: newContent}})
		if err != nil {
			return "", err
		}
		if vecs != nil {
			vec = vecs[0]
			id, err := s.embeddingModelID(ctx)
			if err != nil {
				return "", err
			}
			modelID = &id
		}
	}

	var topics []topic.Resolution
	if len(r.Topics) > 0 {
		if topics, err = s.topics.Resolve(ctx, r.Topics); err != nil {
			return "", err
		}
		if len(topics) == 0 {
			return "", errors.New("ninguno de los temas indicados es válido")
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("no se pudo iniciar la transacción: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	locked, err := lockMemories(ctx, tx, r.ID)
	if err != nil {
		return "", err
	}
	m := locked[r.ID]
	if err := requireActive(r.ID, m, "solo se pueden modificar los vigentes"); err != nil {
		return "", err
	}

	hash := ContentHash(newContent)
	if contentChanged {
		var otherID int64
		err := tx.QueryRow(ctx, `SELECT id FROM memories
			WHERE content_hash = $1 AND status = 'active' AND id <> $2`, hash, r.ID).Scan(&otherID)
		switch {
		case err == nil:
			return "", fmt.Errorf("ya existe un registro igual #%d", otherID)
		case errors.Is(err, pgx.ErrNoRows):
		default:
			return "", fmt.Errorf("no se pudo comprobar si el contenido está duplicado: %w", err)
		}
	}

	sql := `UPDATE memories SET title = $2, content = $3, content_hash = $4, updated_at = now()`
	args := []any{r.ID, newTitle, newContent, hash}
	if reembed {
		var model any
		if modelID != nil {
			model = *modelID
		}
		sql += `, embedding = $5, embedding_model = $6`
		args = append(args, halfParam(vec), model)
	}
	if _, err := tx.Exec(ctx, sql+` WHERE id = $1`, args...); err != nil {
		return "", fmt.Errorf("no se pudo actualizar el registro #%d: %w", r.ID, err)
	}

	if len(topics) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM memory_topics WHERE memory_id = $1`, r.ID); err != nil {
			return "", fmt.Errorf("no se pudieron reemplazar los temas del registro #%d: %w", r.ID, err)
		}
		ids := make([]int32, len(topics))
		for i, t := range topics {
			ids[i] = t.Topic.ID
		}
		if err := insertMemoryTopics(ctx, tx, r.ID, ids, m.kind, m.occurredAt, true); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("no se pudo confirmar la actualización: %w", err)
	}

	var changed []string
	if titleChanged {
		changed = append(changed, "título")
	}
	if contentChanged {
		changed = append(changed, "contenido")
	}
	if len(topics) > 0 {
		slugs := make([]string, len(topics))
		for i, t := range topics {
			slugs[i] = t.Topic.Slug
		}
		changed = append(changed, "temas: "+strings.Join(slugs, ", "))
	}
	msg := fmt.Sprintf("#%d actualizado", r.ID)
	if len(changed) > 0 {
		msg += " (" + strings.Join(changed, "; ") + ")"
	}
	if reembed && vec == nil {
		msg += "; embedding pendiente"
	}
	return msg, nil
}

// supersede implements the "supersede" action: r.WithID replaces r.ID.
func (s *Service) supersede(ctx context.Context, r Revision) (string, error) {
	if r.WithID <= 0 {
		return "", errors.New("supersede necesita with_id: el registro que sustituye al antiguo")
	}
	if r.WithID == r.ID {
		return "", errors.New("un registro no puede sustituirse a sí mismo")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("no se pudo iniciar la transacción: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	locked, err := lockMemories(ctx, tx, r.ID, r.WithID)
	if err != nil {
		return "", err
	}
	if err := requireActive(r.ID, locked[r.ID], "solo se pueden sustituir los vigentes"); err != nil {
		return "", err
	}
	if err := requireActive(r.WithID, locked[r.WithID], "el que sustituye debe estar vigente"); err != nil {
		return "", err
	}

	if _, err := tx.Exec(ctx, `UPDATE memories SET status = 'superseded', updated_at = now() WHERE id = $1`, r.ID); err != nil {
		return "", fmt.Errorf("no se pudo sustituir el registro #%d: %w", r.ID, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE memory_topics SET active = false WHERE memory_id = $1`, r.ID); err != nil {
		return "", fmt.Errorf("no se pudieron desactivar los temas del registro #%d: %w", r.ID, err)
	}
	if err := upsertRelation(ctx, tx, r.WithID, r.ID, "supersedes", "confirmed"); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("no se pudo confirmar la sustitución: %w", err)
	}
	return fmt.Sprintf("#%d sustituido por #%d", r.ID, r.WithID), nil
}

// invalidate implements the "invalidate" action.
func (s *Service) invalidate(ctx context.Context, r Revision) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("no se pudo iniciar la transacción: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	locked, err := lockMemories(ctx, tx, r.ID)
	if err != nil {
		return "", err
	}
	if locked[r.ID].status == StatusInvalidated {
		return fmt.Sprintf("#%d ya estaba invalidado", r.ID), nil
	}
	if _, err := tx.Exec(ctx, `UPDATE memories SET status = 'invalidated', updated_at = now() WHERE id = $1`, r.ID); err != nil {
		return "", fmt.Errorf("no se pudo invalidar el registro #%d: %w", r.ID, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE memory_topics SET active = false WHERE memory_id = $1`, r.ID); err != nil {
		return "", fmt.Errorf("no se pudieron desactivar los temas del registro #%d: %w", r.ID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("no se pudo confirmar la invalidación: %w", err)
	}
	return fmt.Sprintf("#%d invalidado", r.ID), nil
}

// delete implements the "delete" action. Foreign keys with ON DELETE CASCADE
// remove the record's relations and topic links.
func (s *Service) delete(ctx context.Context, r Revision) (string, error) {
	if !r.Confirm {
		return "", errors.New("para borrar definitivamente hay que confirmar (confirm: true)")
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM memories WHERE id = $1`, r.ID)
	if err != nil {
		return "", fmt.Errorf("no se pudo borrar el registro #%d: %w", r.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return "", notFound(r.ID)
	}
	return fmt.Sprintf("#%d borrado definitivamente", r.ID), nil
}

// relate implements the "relate" action.
func (s *Service) relate(ctx context.Context, r Revision) (string, error) {
	if r.WithID <= 0 {
		return "", errors.New("relate necesita with_id: el registro con el que se relaciona")
	}
	if r.WithID == r.ID {
		return "", errors.New("un registro no puede relacionarse consigo mismo")
	}
	kind := strings.ToLower(strings.TrimSpace(r.Relation))
	if kind == "" {
		kind = "related"
	}
	switch kind {
	case "related", "contradicts", "part_of":
	default:
		return "", fmt.Errorf("relación %q no válida para relate (válidas: related, contradicts, part_of)", r.Relation)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("no se pudo iniciar la transacción: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := lockMemories(ctx, tx, r.ID, r.WithID); err != nil {
		return "", err
	}
	if err := upsertRelation(ctx, tx, r.ID, r.WithID, kind, "confirmed"); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("no se pudo confirmar la relación: %w", err)
	}
	return fmt.Sprintf("#%d %s #%d (confirmada)", r.ID, kind, r.WithID), nil
}

// setRelationState implements confirm_relation and reject_relation.
func (s *Service) setRelationState(ctx context.Context, r Revision, state, done string) (string, error) {
	if r.WithID <= 0 {
		return "", errors.New("hay que indicar with_id: el otro registro de la relación")
	}
	kind := strings.ToLower(strings.TrimSpace(r.Relation))
	if kind == "" {
		kind = "related"
	}
	switch kind {
	case "related", "contradicts", "part_of", "supersedes":
	default:
		return "", fmt.Errorf("relación %q no válida (válidas: related, contradicts, part_of, supersedes)", r.Relation)
	}

	tag, err := s.pool.Exec(ctx, `UPDATE memory_relations SET state = $3::text::relation_state
		WHERE kind = $4::text::relation_kind
		  AND ((source_id = $1 AND target_id = $2) OR (source_id = $2 AND target_id = $1))`,
		r.ID, r.WithID, state, kind)
	if err != nil {
		return "", fmt.Errorf("no se pudo cambiar el estado de la relación: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return "", fmt.Errorf("no existe una relación %s entre #%d y #%d", kind, r.ID, r.WithID)
	}
	return fmt.Sprintf("relación %s entre #%d y #%d %s", kind, r.ID, r.WithID, done), nil
}

// upsertRelation creates the relation or sets its state if it already exists.
func upsertRelation(ctx context.Context, tx pgx.Tx, source, target int64, kind, state string) error {
	_, err := tx.Exec(ctx, `INSERT INTO memory_relations (source_id, target_id, kind, state)
		VALUES ($1, $2, $3::text::relation_kind, $4::text::relation_state)
		ON CONFLICT (source_id, target_id, kind) DO UPDATE SET state = EXCLUDED.state`,
		source, target, kind, state)
	if err != nil {
		return fmt.Errorf("no se pudo guardar la relación #%d -> #%d: %w", source, target, err)
	}
	return nil
}
