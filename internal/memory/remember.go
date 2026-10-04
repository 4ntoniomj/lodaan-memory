package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"lodan/internal/database"
	"lodan/internal/embedding"
	"lodan/internal/session"
	"lodan/internal/topic"
)

// prepared is an Item after validation, with everything computed for it.
type prepared struct {
	title, content string
	kind           Kind
	key            string
	occurred       time.Time
	hash           []byte
	names          []string // topic names to resolve, never empty
	topics         []topic.Resolution
	vec            []float32 // nil when pending
	dup            bool
	saved          Saved
}

// Remember saves up to 20 items and returns one Saved per item, in order.
//
// For each item: an active record with the same normalized content makes it a
// duplicate (nothing is inserted); otherwise it is inserted with its topics.
// An item with a key supersedes the active record with the same key. All
// inserts run in one transaction. Afterwards, active records similar to each
// new one (at least the duplicate threshold) get a suggested "related" relation
// and are listed in Saved.Similar; that last step is best effort: if it fails,
// the items are already saved and the error is not reported.
//
// If the embedding backend is unavailable, or takes longer than the embedding
// timeout, the items are saved as pending (NULL embedding); FillPending computes
// the embedding and the similar records later. tr may be nil, in which case
// the records belong to no session.
func (s *Service) Remember(ctx context.Context, tr *session.Tracker, items []Item) ([]Saved, error) {
	if len(items) == 0 {
		return nil, errors.New("hay que enviar al menos un ítem")
	}
	if len(items) > maxItems {
		return nil, fmt.Errorf("se enviaron %d ítems y el máximo es %d", len(items), maxItems)
	}

	now := time.Now()
	ps := make([]prepared, len(items))
	for i, it := range items {
		p, err := prepareItem(it, now)
		if err != nil {
			return nil, fmt.Errorf("ítem %d: %w", i+1, err)
		}
		ps[i] = p
	}

	// 1. Duplicados exactos por hash del contenido normalizado.
	var todo []int
	for i := range ps {
		id, title, found, err := findActiveByHash(ctx, s.pool, ps[i].hash)
		if err != nil {
			return nil, err
		}
		if found {
			ps[i].dup = true
			ps[i].saved = Saved{ID: id, Title: title, Duplicate: true}
			continue
		}
		todo = append(todo, i)
	}
	if len(todo) == 0 {
		return collectSaved(ps), nil
	}

	// 2. Temas de todos los ítems en una sola llamada.
	if err := s.resolveTopics(ctx, ps, todo); err != nil {
		return nil, err
	}

	// 3. Embeddings en lote; si Ollama no está o tarda más que embedTimeout,
	// todos quedan pendientes.
	docs := make([]embedding.Document, len(todo))
	for j, i := range todo {
		docs[j] = embedding.Document{Title: ps[i].title, Text: ps[i].content}
	}
	vecs, err := s.embedDocsWithin(ctx, docs)
	if err != nil {
		return nil, err
	}
	for j, i := range todo {
		if vecs != nil {
			ps[i].vec = vecs[j]
		}
	}

	// 4. Sesión perezosa de la conexión.
	var sessionID string
	if tr != nil {
		if sessionID, err = tr.Current(ctx); err != nil {
			return nil, err
		}
	}

	// 5. Modelo de embeddings.
	var modelID *int16
	if vecs != nil {
		id, err := s.embeddingModelID(ctx)
		if err != nil {
			return nil, err
		}
		modelID = &id
	}

	// 6. Inserción en una sola transacción.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("no se pudo iniciar la transacción de guardado: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op tras un Commit correcto
	for _, i := range todo {
		if err := s.insertItem(ctx, tx, &ps[i], sessionID, modelID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("no se pudo confirmar el guardado: %w", err)
	}

	// 7. Parecidos: solo con embedding y sin contar lo que acaba de sustituirse.
	replaced := make(map[int64]bool)
	var replacedList []int64
	for _, i := range todo {
		for _, id := range ps[i].saved.Superseded {
			replaced[id] = true
			replacedList = append(replacedList, id)
		}
	}
	for _, i := range todo {
		p := &ps[i]
		if p.dup || p.vec == nil || replaced[p.saved.ID] {
			continue
		}
		exclude := append([]int64{p.saved.ID}, replacedList...)
		p.saved.Similar = s.suggestSimilar(ctx, p.saved.ID, p.vec, exclude)
	}
	return collectSaved(ps), nil
}

// prepareItem validates an item and computes its hash and defaults.
func prepareItem(it Item, now time.Time) (prepared, error) {
	title, err := validateText("título", it.Title, maxTitleLen)
	if err != nil {
		return prepared{}, err
	}
	content, err := validateText("contenido", it.Content, maxContentLen)
	if err != nil {
		return prepared{}, err
	}
	kind, err := ParseKind(string(it.Kind))
	if err != nil {
		return prepared{}, err
	}
	occurred := now
	if it.OccurredAt != nil {
		occurred = *it.OccurredAt
	}

	var names []string
	for _, n := range it.Topics {
		if topic.Normalize(n) != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		names = []string{defaultTopic}
	}
	return prepared{
		title:    title,
		content:  content,
		kind:     kind,
		key:      strings.TrimSpace(it.Key),
		occurred: occurred,
		hash:     ContentHash(content),
		names:    names,
	}, nil
}

// resolveTopics resolves the topics of the items in todo with one call to the
// resolver and assigns to each item its own topics, without repeats. New topics
// are embedded within the embedding timeout; past it they are created with a
// NULL embedding and topic.Resolver.FillPending completes them later.
func (s *Service) resolveTopics(ctx context.Context, ps []prepared, todo []int) error {
	var union []string
	for _, i := range todo {
		union = append(union, ps[i].names...)
	}
	res, err := s.topics.ResolveWithin(ctx, union, s.embedTimeout)
	if err != nil {
		return err
	}
	byRequested := make(map[string]topic.Resolution, len(res))
	bySlug := make(map[string]topic.Resolution, len(res))
	for _, r := range res {
		byRequested[r.Requested] = r
		bySlug[r.Topic.Slug] = r
	}

	// Resolve deduplica por tema: un nombre puede haber quedado sin resolución
	// propia si otro nombre equivalente lo precedía; en ese caso se busca por slug
	// y, como último recurso, se resuelve suelto.
	lookup := func(name string) (topic.Resolution, error) {
		slug := topic.Normalize(name)
		if r, ok := byRequested[slug]; ok {
			return r, nil
		}
		if r, ok := bySlug[slug]; ok {
			return topic.Resolution{Topic: r.Topic, Requested: slug}, nil
		}
		one, err := s.topics.ResolveWithin(ctx, []string{name}, s.embedTimeout)
		if err != nil {
			return topic.Resolution{}, err
		}
		if len(one) == 0 {
			return topic.Resolution{}, fmt.Errorf("no se pudo resolver el tema %q", name)
		}
		byRequested[slug] = one[0]
		return one[0], nil
	}

	for _, i := range todo {
		seen := make(map[int32]bool)
		for _, name := range ps[i].names {
			r, err := lookup(name)
			if err != nil {
				return err
			}
			if seen[r.Topic.ID] {
				continue
			}
			seen[r.Topic.ID] = true
			ps[i].topics = append(ps[i].topics, r)
		}
	}
	return nil
}

// insertItem inserts one item inside tx. It runs in a savepoint so that a
// duplicate found by a race (the unique index on the hash) undoes the
// supersession of the key it may have done.
func (s *Service) insertItem(ctx context.Context, tx pgx.Tx, p *prepared, sessionID string, modelID *int16) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("no se pudo iniciar el punto de guardado: %w", err)
	}
	defer func() { _ = sp.Rollback(ctx) }() // no-op tras un Commit correcto

	// Sustitución por clave.
	var superseded []int64
	if p.key != "" {
		var oldID int64
		err := sp.QueryRow(ctx, `SELECT id FROM memories WHERE key = $1 AND status = 'active' FOR UPDATE`, p.key).Scan(&oldID)
		switch {
		case err == nil:
			if _, err := sp.Exec(ctx, `UPDATE memories SET status = 'superseded', updated_at = now() WHERE id = $1`, oldID); err != nil {
				return fmt.Errorf("no se pudo sustituir el registro #%d: %w", oldID, err)
			}
			if _, err := sp.Exec(ctx, `UPDATE memory_topics SET active = false WHERE memory_id = $1`, oldID); err != nil {
				return fmt.Errorf("no se pudieron desactivar los temas del registro #%d: %w", oldID, err)
			}
			superseded = append(superseded, oldID)
		case errors.Is(err, pgx.ErrNoRows):
		default:
			return fmt.Errorf("no se pudo buscar la clave %q: %w", p.key, err)
		}
	}

	var sid, key any
	if sessionID != "" {
		sid = sessionID
	}
	if p.key != "" {
		key = p.key
	}
	var model any
	if p.vec != nil && modelID != nil {
		model = *modelID
	}

	var id int64
	err = sp.QueryRow(ctx, `INSERT INTO memories
		(kind, title, content, key, content_hash, session_id, occurred_at, embedding, embedding_model)
		VALUES ($1::text::memory_kind, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (content_hash) WHERE status = 'active' DO NOTHING
		RETURNING id`,
		string(p.kind), p.title, p.content, key, p.hash, sid, p.occurred, halfParam(p.vec), model).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// Duplicado por carrera: se deshace lo hecho en este ítem y se marca.
		if err := sp.Rollback(ctx); err != nil {
			return fmt.Errorf("no se pudo deshacer el guardado duplicado: %w", err)
		}
		existing, title, found, err := findActiveByHash(ctx, tx, p.hash)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("el registro duplicado desapareció durante el guardado; vuelve a intentarlo")
		}
		p.dup = true
		p.saved = Saved{ID: existing, Title: title, Duplicate: true}
		return nil
	}
	if err != nil {
		return fmt.Errorf("no se pudo guardar %q: %w", p.title, err)
	}

	topicIDs := make([]int32, len(p.topics))
	for i, r := range p.topics {
		topicIDs[i] = r.Topic.ID
	}
	if err := insertMemoryTopics(ctx, sp, id, topicIDs, p.kind, p.occurred, true); err != nil {
		return err
	}
	for _, oldID := range superseded {
		if _, err := sp.Exec(ctx, `INSERT INTO memory_relations (source_id, target_id, kind, state)
			VALUES ($1, $2, 'supersedes', 'confirmed')
			ON CONFLICT (source_id, target_id, kind) DO UPDATE SET state = 'confirmed'`, id, oldID); err != nil {
			return fmt.Errorf("no se pudo crear la relación de sustitución: %w", err)
		}
	}
	if sessionID != "" {
		if err := s.sessions.AddTopics(ctx, sp, sessionID, topicIDs); err != nil {
			return err
		}
	}
	if err := sp.Commit(ctx); err != nil {
		return fmt.Errorf("no se pudo confirmar el punto de guardado: %w", err)
	}

	p.saved = Saved{
		ID:         id,
		Title:      p.title,
		Topics:     p.topics,
		Pending:    p.vec == nil,
		Superseded: superseded,
	}
	return nil
}

// suggestSimilar looks for active records similar to the record id (whose
// embedding is vec), leaving out excludeIDs, and stores a suggested "related"
// relation from id to each. It returns the similar records found. It is shared
// by Remember and FillPending. Errors are ignored on purpose: the record is
// already saved and this only adds hints.
func (s *Service) suggestSimilar(ctx context.Context, id int64, vec []float32, excludeIDs []int64) []Neighbor {
	near, err := Nearest(ctx, s.pool, vec, s.emb.Dims(), neighborCandidates, maxNeighbors, s.dupThreshold, excludeIDs)
	if err != nil {
		return nil
	}
	var similar []Neighbor
	for _, n := range near {
		_, err := s.pool.Exec(ctx, `INSERT INTO memory_relations (source_id, target_id, kind, state)
			VALUES ($1, $2, 'related', 'suggested')
			ON CONFLICT DO NOTHING`, id, n.ID)
		if err != nil {
			continue
		}
		similar = append(similar, n)
	}
	return similar
}

// insertMemoryTopics links a record to topics in memory_topics.
func insertMemoryTopics(ctx context.Context, q database.Querier, memoryID int64, topicIDs []int32, kind Kind, ts time.Time, active bool) error {
	if len(topicIDs) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `INSERT INTO memory_topics (topic_id, memory_id, kind, active, ts)
		SELECT unnest($1::int[]), $2::bigint, $3::text::memory_kind, $4::boolean, $5::timestamptz
		ON CONFLICT (topic_id, memory_id) DO UPDATE SET kind = EXCLUDED.kind, active = EXCLUDED.active, ts = EXCLUDED.ts`,
		topicIDs, memoryID, string(kind), active, ts)
	if err != nil {
		return fmt.Errorf("no se pudieron guardar los temas del registro #%d: %w", memoryID, err)
	}
	return nil
}

// findActiveByHash returns the active record with the given content hash, if any.
func findActiveByHash(ctx context.Context, q database.Querier, hash []byte) (id int64, title string, found bool, err error) {
	err = q.QueryRow(ctx, `SELECT id, title FROM memories WHERE content_hash = $1 AND status = 'active'`, hash).Scan(&id, &title)
	switch {
	case err == nil:
		return id, title, true, nil
	case errors.Is(err, pgx.ErrNoRows):
		return 0, "", false, nil
	default:
		return 0, "", false, fmt.Errorf("no se pudo buscar un registro duplicado: %w", err)
	}
}

// collectSaved gathers the results of every prepared item, in order.
func collectSaved(ps []prepared) []Saved {
	out := make([]Saved, len(ps))
	for i := range ps {
		out[i] = ps[i].saved
	}
	return out
}
