package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"lodan/internal/embedding"
	"lodan/internal/topic"
)

// mergeRel is one relation that crosses the originals and is copied to the new
// record. Either Source or Target (but not both) is one of the originals.
type mergeRel struct {
	Source, Target       int64
	Kind, State, Verdict string
	Confidence           float64
}

// merge implements the "merge" action: it joins ≥2 active records into one new
// record with the AI's chosen title, content and tags. Without Confirm it
// returns a preview that does not write anything; with Confirm and a matching
// Fingerprint it creates the new record, moves the first non-empty key, copies
// every relation of the originals except those among them, and marks every
// original as superseded by the new one (history kept).
func (s *Service) merge(ctx context.Context, r Revision) (string, error) {
	if len(r.IDs) < 2 {
		return "", errors.New("hay que indicar al menos 2 registros en ids")
	}
	title, err := validateText("título", r.Title, maxTitleLen)
	if err != nil {
		return "", err
	}
	content, err := validateText("contenido", r.Content, maxContentLen)
	if err != nil {
		return "", err
	}
	if r.Confirm && strings.TrimSpace(r.Fingerprint) == "" {
		return "", errors.New("falta la huella de la vista previa (fingerprint)")
	}

	// inputIDs keeps the user's order (for the preview header, the commit
	// answer, the kind/occurred_at and the first non-empty key).
	inputIDs := make([]int64, len(r.IDs))
	copy(inputIDs, r.IDs)
	// sortedIDs is the ascending copy used to lock the originals in a stable
	// order (avoiding deadlocks).
	sortedIDs := make([]int64, len(inputIDs))
	copy(sortedIDs, inputIDs)
	sort.Slice(sortedIDs, func(i, j int) bool { return sortedIDs[i] < sortedIDs[j] })

	// Tag names the user gave (joined, normalized, deduplicated, max 8).
	// Empty when the user did not give any; the union of the originals is then
	// used at preview and commit time.
	var names []string
	if len(r.Tags) > 0 || len(r.Topics) > 0 {
		names, err = tagNames(r.Tags, r.Topics)
		if err != nil {
			return "", err
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("no se pudo iniciar la transacción: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	locked, err := lockMemories(ctx, tx, sortedIDs...)
	if err != nil {
		return "", err
	}
	for _, id := range sortedIDs {
		if err := requireActive(id, locked[id], "solo se pueden juntar los vigentes"); err != nil {
			return "", err
		}
	}

	fp, kind, occurredAt, firstKey, crossRels, err := s.mergeGather(ctx, tx, sortedIDs, inputIDs, title, content, names)
	if err != nil {
		return "", err
	}

	if r.Confirm && r.Fingerprint != fp {
		return "", errors.New("los registros cambiaron desde la vista previa: vuelve a pedirla")
	}

	previewSlugs, err := s.mergePreviewSlugs(ctx, tx, sortedIDs, names)
	if err != nil {
		return "", err
	}

	if !r.Confirm {
		return s.mergePreviewLine(inputIDs, title, previewSlugs, firstKey, len(crossRels), fp), nil
	}

	newID, err := s.mergeCommit(ctx, tx, kind, occurredAt, title, content, firstKey, previewSlugs, inputIDs, sortedIDs, crossRels)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("no se pudo confirmar la fusión: %w", err)
	}
	return fmt.Sprintf("#%d junta #%d, #%d (quedan sustituidos)", newID, inputIDs[0], inputIDs[1]), nil
}

// mergeGather computes the fingerprint and gathers the data needed both for
// the preview and for the commit. sortedIDs must already be locked. kind,
// occurredAt and firstKey follow the order of inputIDs (the user's order).
func (s *Service) mergeGather(ctx context.Context, tx pgx.Tx, sortedIDs, inputIDs []int64, title, content string, names []string) (fp string, kind Kind, occurredAt time.Time, firstKey string, crossRels []mergeRel, err error) {
	rows, err := tx.Query(ctx, `SELECT id, kind::text, occurred_at, COALESCE(key, '')
		FROM memories WHERE id = ANY($1)`, inputIDs)
	if err != nil {
		return "", "", time.Time{}, "", nil, fmt.Errorf("no se pudieron leer los originales: %w", err)
	}
	defer rows.Close()
	byID := make(map[int64]struct {
		k   string
		occ time.Time
		ky  string
	}, len(inputIDs))
	for rows.Next() {
		var id int64
		var k, ky string
		var occ time.Time
		if err := rows.Scan(&id, &k, &occ, &ky); err != nil {
			return "", "", time.Time{}, "", nil, fmt.Errorf("no se pudo leer un original: %w", err)
		}
		byID[id] = struct {
			k   string
			occ time.Time
			ky  string
		}{k, occ, ky}
	}
	rows.Close()
	if len(byID) != len(inputIDs) {
		return "", "", time.Time{}, "", nil, errors.New("no se pudieron leer todos los originales")
	}
	if first, ok := byID[inputIDs[0]]; ok {
		kind = Kind(first.k)
		occurredAt = first.occ
	}
	for _, id := range inputIDs {
		if firstKey == "" && byID[id].ky != "" {
			firstKey = byID[id].ky
			break
		}
	}

	rows, err = tx.Query(ctx, `SELECT id, content_hash FROM memories WHERE id = ANY($1) ORDER BY id`, sortedIDs)
	if err != nil {
		return "", "", time.Time{}, firstKey, nil, fmt.Errorf("no se pudieron leer las huellas: %w", err)
	}
	seen2 := make(map[int64]bool, len(sortedIDs))
	var origHashes [][]byte
	for rows.Next() {
		var id int64
		var h []byte
		if err := rows.Scan(&id, &h); err != nil {
			rows.Close()
			return "", "", time.Time{}, firstKey, nil, err
		}
		if seen2[id] {
			continue
		}
		seen2[id] = true
		origHashes = append(origHashes, h)
	}
	rows.Close()

	h := sha256.New()
	h.Write([]byte(title))
	h.Write([]byte{0})
	h.Write([]byte(content))
	h.Write([]byte{0})
	for _, n := range names {
		h.Write([]byte(topic.Normalize(n)))
		h.Write([]byte{0})
	}
	for _, oh := range origHashes {
		h.Write(oh)
		h.Write([]byte{0})
	}
	fp = hex.EncodeToString(h.Sum(nil)[:4])

	rows, err = tx.Query(ctx, `SELECT source_id, target_id, kind::text, state::text,
			COALESCE(verdict, ''), COALESCE(confidence::float8, 0)
		FROM memory_relations
		WHERE source_id = ANY($1) OR target_id = ANY($1)`, sortedIDs)
	if err != nil {
		return "", "", time.Time{}, firstKey, nil, fmt.Errorf("no se pudieron leer las relaciones: %w", err)
	}
	defer rows.Close()
	idSet := make(map[int64]bool, len(sortedIDs))
	for _, id := range sortedIDs {
		idSet[id] = true
	}
	for rows.Next() {
		var r mergeRel
		var kindS, stateS string
		if err := rows.Scan(&r.Source, &r.Target, &kindS, &stateS, &r.Verdict, &r.Confidence); err != nil {
			return "", "", time.Time{}, firstKey, nil, err
		}
		r.Kind, r.State = kindS, stateS
		if idSet[r.Source] && idSet[r.Target] {
			continue
		}
		if r.State == "rejected" || r.Kind == "supersedes" {
			continue
		}
		crossRels = append(crossRels, r)
	}

	return fp, kind, occurredAt, firstKey, crossRels, nil
}

// mergePreviewSlugs returns the slugs to show in the preview line: the user's
// tags (already normalized) when they gave some, otherwise the union of the
// originals' tags, ordered by how many originals share the tag (descending)
// and then by topic id (ascending) as a tiebreaker.
func (s *Service) mergePreviewSlugs(ctx context.Context, tx pgx.Tx, sortedIDs []int64, names []string) ([]string, error) {
	if len(names) > 0 {
		out := make([]string, 0, len(names))
		seen := make(map[string]bool, len(names))
		for _, n := range names {
			slug := topic.Normalize(n)
			if slug != "" && !seen[slug] {
				seen[slug] = true
				out = append(out, slug)
			}
		}
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT t.slug
		FROM memory_topics mt JOIN topics t ON t.id = mt.topic_id
		WHERE mt.memory_id = ANY($1) AND mt.active = true
		GROUP BY t.id, t.slug
		ORDER BY COUNT(*) DESC, t.id ASC`, sortedIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// mergePreviewLine builds the multi-line preview answer.
func (s *Service) mergePreviewLine(sortedIDs []int64, title string, slugs []string, key string, cross int, fp string) string {
	tagParts := make([]string, len(slugs))
	for i, s := range slugs {
		tagParts[i] = "#" + s
	}
	parts := []string{
		fmt.Sprintf("VISTA PREVIA — juntar #%d, #%d en un registro nuevo (no se ha guardado nada)", sortedIDs[0], sortedIDs[1]),
		"  título: " + title,
		"  etiquetas: " + strings.Join(tagParts, ", "),
	}
	if key != "" {
		parts = append(parts, "  clave: "+key)
	}
	parts = append(parts,
		fmt.Sprintf("  relaciones que pasan al nuevo: %d", cross),
		fmt.Sprintf("  #%d, #%d quedarán sustituidos por el nuevo", sortedIDs[0], sortedIDs[1]),
		"  huella: "+fp,
		"  para confirmar: repite con confirm: true y fingerprint: "+fp,
	)
	return strings.Join(parts, "\n")
}

// mergeCommit creates the new record, applies its tags and key, copies the
// cross-original relations (each side pointing to an original is repointed to
// the new record) and marks every original as superseded by it. The originals
// are marked superseded before the new row is inserted so the partial unique
// indexes on content_hash and key (which only cover active rows) do not fire.
func (s *Service) mergeCommit(ctx context.Context, tx pgx.Tx, kind Kind, occurredAt time.Time, title, content, key string, slugs []string, inputIDs, sortedIDs []int64, crossRels []mergeRel) (int64, error) {
	// Free the key and supersede the originals before inserting, so the new
	// row's content_hash and key are free of conflicts with active rows.
	if key != "" {
		if _, err := tx.Exec(ctx, `UPDATE memories SET key = NULL
			WHERE id = ANY($1) AND status = 'active'`, sortedIDs); err != nil {
			return 0, fmt.Errorf("no se pudo liberar la clave de los originales: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE memories SET status = 'superseded', updated_at = now()
		WHERE id = ANY($1) AND status = 'active'`, sortedIDs); err != nil {
		return 0, fmt.Errorf("no se pudieron sustituir los originales: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE memory_topics SET active = false WHERE memory_id = ANY($1)`, sortedIDs); err != nil {
		return 0, fmt.Errorf("no se pudieron desactivar los temas de los originales: %w", err)
	}

	effectiveSlugs := slugs
	if len(effectiveSlugs) == 0 {
		effectiveSlugs = []string{defaultTopic}
	}
	res, err := s.topics.Resolve(ctx, effectiveSlugs)
	if err != nil {
		return 0, err
	}

	var vec []float32
	var modelID *int16
	vecs, err := s.embedDocs(ctx, []embedding.Document{{Title: title, Text: content}})
	if err != nil {
		return 0, err
	}
	if vecs != nil {
		vec = vecs[0]
		id, err := s.embeddingModelID(ctx)
		if err != nil {
			return 0, err
		}
		modelID = &id
	}

	hash := ContentHash(content)
	var vecArg, modelArg any
	if vec != nil {
		vecArg = halfParam(vec)
		modelArg = *modelID
	}
	var newID int64
	err = tx.QueryRow(ctx, `INSERT INTO memories
		(kind, title, content, content_hash, key, embedding, embedding_model, occurred_at)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8)
		RETURNING id`,
		kind, title, content, hash, key, vecArg, modelArg, occurredAt).Scan(&newID)
	if err != nil {
		return 0, fmt.Errorf("no se pudo crear el registro nuevo: %w", err)
	}

	topicIDs := make([]int32, len(res))
	for i, t := range res {
		topicIDs[i] = t.Topic.ID
	}
	if err := insertMemoryTopics(ctx, tx, newID, topicIDs, kind, occurredAt, true); err != nil {
		return 0, err
	}

	idSet := make(map[int64]bool, len(sortedIDs))
	for _, id := range sortedIDs {
		idSet[id] = true
	}
	for _, r := range crossRels {
		newSource := r.Source
		newTarget := r.Target
		if idSet[r.Source] {
			newSource = newID
		}
		if idSet[r.Target] {
			newTarget = newID
		}
		var verdict, confidence any
		if r.Verdict != "" {
			verdict = r.Verdict
			confidence = r.Confidence
		}
		if _, err := tx.Exec(ctx, `INSERT INTO memory_relations (source_id, target_id, kind, state, verdict, confidence)
			VALUES ($1, $2, $3::text::relation_kind, $4::text::relation_state, $5, $6)
			ON CONFLICT (source_id, target_id, kind) DO UPDATE
			SET state = EXCLUDED.state, verdict = EXCLUDED.verdict, confidence = EXCLUDED.confidence`,
			newSource, newTarget, r.Kind, r.State, verdict, confidence); err != nil {
			return 0, fmt.Errorf("no se pudo copiar la relación #%d -> #%d: %w", newSource, newTarget, err)
		}
	}

	// new -> original (supersedes, confirmed), in the order the user gave.
	used := make(map[int64]bool, len(sortedIDs))
	for _, id := range inputIDs {
		if used[id] {
			continue
		}
		used[id] = true
		if err := upsertRelation(ctx, tx, newID, id, "supersedes", "confirmed"); err != nil {
			return 0, err
		}
	}

	return newID, nil
}