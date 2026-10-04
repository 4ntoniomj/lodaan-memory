package topic

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"lodan/internal/embedding"
)

const (
	// cacheTTL is how long the in-memory topic cache is trusted by Resolve.
	cacheTTL = 60 * time.Second
	// fillBatchSize is the number of pending topics embedded per transaction.
	fillBatchSize = 32
)

// Topic is a stored topic.
type Topic struct {
	ID   int32
	Slug string
}

// Resolution is the outcome of resolving one requested topic name.
type Resolution struct {
	Topic Topic
	// Requested is the normalized slug that was asked for.
	Requested string
	// Equivalent is true when the request was mapped to an existing topic
	// different from the requested one.
	Equivalent bool
}

// cached is a topic with its embedding, as kept in memory.
type cached struct {
	topic Topic
	vec   []float32
}

// Resolver resolves topic names against the database, keeping an in-memory
// cache of topics and their embeddings to detect equivalents without queries.
// It is safe for concurrent use.
type Resolver struct {
	pool      *pgxpool.Pool
	emb       embedding.Embedder
	threshold float64

	mu       sync.RWMutex
	withVec  []cached         // only topics that have an embedding
	bySlug   map[string]Topic // every known topic
	loadedAt time.Time
}

// NewResolver creates a Resolver. Two topics are considered equivalent when the
// cosine similarity of their embeddings is at least threshold. The cache starts
// empty: call Load, or let Resolve load it when it is first needed.
func NewResolver(pool *pgxpool.Pool, emb embedding.Embedder, threshold float64) *Resolver {
	return &Resolver{
		pool:      pool,
		emb:       emb,
		threshold: threshold,
		bySlug:    make(map[string]Topic),
	}
}

// Load replaces the cache with every topic stored in the database.
func (r *Resolver) Load(ctx context.Context) error {
	rows, err := r.pool.Query(ctx, `SELECT id, slug, embedding FROM topics ORDER BY id`)
	if err != nil {
		return fmt.Errorf("no se pudieron cargar los temas: %w", err)
	}
	defer rows.Close()

	var withVec []cached
	bySlug := make(map[string]Topic)
	for rows.Next() {
		var t Topic
		var vec *pgvector.HalfVector
		if err := rows.Scan(&t.ID, &t.Slug, &vec); err != nil {
			return fmt.Errorf("no se pudo leer un tema: %w", err)
		}
		bySlug[t.Slug] = t
		if vec != nil {
			withVec = append(withVec, cached{topic: t, vec: vec.Slice()})
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("no se pudieron cargar los temas: %w", err)
	}

	r.mu.Lock()
	r.withVec = withVec
	r.bySlug = bySlug
	r.loadedAt = time.Now()
	r.mu.Unlock()
	return nil
}

// loadIfStale reloads the cache if it was never loaded or is older than cacheTTL.
func (r *Resolver) loadIfStale(ctx context.Context) error {
	r.mu.RLock()
	stale := r.loadedAt.IsZero() || time.Since(r.loadedAt) > cacheTTL
	r.mu.RUnlock()
	if !stale {
		return nil
	}
	return r.Load(ctx)
}

// cacheSet adds or replaces a topic in the cache. A nil vec registers the
// topic without embedding.
func (r *Resolver) cacheSet(t Topic, vec []float32) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.bySlug[t.Slug] = t
	if vec == nil {
		return
	}
	for i := range r.withVec {
		if r.withVec[i].topic.ID == t.ID {
			r.withVec[i].vec = vec
			return
		}
	}
	r.withVec = append(r.withVec, cached{topic: t, vec: vec})
}

// nearest returns the cached topic most similar to vec. found is false if the
// cache holds no topic with an embedding.
func (r *Resolver) nearest(vec []float32) (best Topic, sim float64, found bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, c := range r.withVec {
		s := embedding.Cosine(vec, c.vec)
		if !found || s > sim {
			best, sim, found = c.topic, s, true
		}
	}
	return best, sim, found
}

// Detect returns the cached topic most similar to queryVec, its cosine
// similarity, and whether that similarity reaches the threshold. It only reads
// the in-memory cache and never touches the database.
func (r *Resolver) Detect(_ context.Context, queryVec []float32) (Topic, float64, bool) {
	best, sim, found := r.nearest(queryVec)
	return best, sim, found && sim >= r.threshold
}

// Resolve turns raw topic names into stored topics, in order. Names are
// normalized and deduplicated (empty ones are ignored); a topic reached by
// several names appears once. A name that does not exist is mapped to an
// existing topic whose embedding is similar enough, or created otherwise.
//
// It uses the pool rather than a transaction: computing embeddings is slow and
// must not hold a transaction open.
func (r *Resolver) Resolve(ctx context.Context, raw []string) ([]Resolution, error) {
	return r.ResolveWithin(ctx, raw, 0)
}

// ResolveWithin is Resolve with a limit on the time spent computing embeddings.
// embedTimeout is a single budget shared by all the new topics of the call
// (zero or negative means no limit). When it runs out, the topics still to be
// embedded are created with a NULL embedding, exactly as if the embedding
// backend were unavailable, and FillPending completes them later. Database
// queries are not subject to the limit.
func (r *Resolver) ResolveWithin(ctx context.Context, raw []string, embedTimeout time.Duration) ([]Resolution, error) {
	var deadline time.Time
	if embedTimeout > 0 {
		deadline = time.Now().Add(embedTimeout)
	}
	var out []Resolution
	seenSlug := make(map[string]bool, len(raw))
	seenID := make(map[int32]bool, len(raw))

	for _, name := range raw {
		slug := Normalize(name)
		if slug == "" || seenSlug[slug] {
			continue
		}
		seenSlug[slug] = true

		res, err := r.resolveOne(ctx, slug, deadline)
		if err != nil {
			return nil, err
		}
		if seenID[res.Topic.ID] {
			continue
		}
		seenID[res.Topic.ID] = true
		out = append(out, res)
	}
	return out, nil
}

// resolveOne resolves a single normalized, non-empty slug. A non-zero
// embedDeadline limits the time spent embedding it (see embedSlug).
func (r *Resolver) resolveOne(ctx context.Context, slug string, embedDeadline time.Time) (Resolution, error) {
	// 1. Existe: se usa tal cual y se marca como usado.
	var id int32
	err := r.pool.QueryRow(ctx,
		`UPDATE topics SET last_used_at = now() WHERE slug = $1 RETURNING id`, slug).Scan(&id)
	if err == nil {
		return Resolution{Topic: Topic{ID: id, Slug: slug}, Requested: slug}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Resolution{}, fmt.Errorf("no se pudo buscar el tema %q: %w", slug, err)
	}

	// 2. No existe: se calcula su embedding.
	vec, err := r.embedSlug(ctx, slug, embedDeadline)
	if err != nil {
		return Resolution{}, err
	}

	// 2a. Con embedding, se busca un tema equivalente en la caché.
	if vec != nil {
		if err := r.loadIfStale(ctx); err != nil {
			return Resolution{}, err
		}
		if best, sim, found := r.nearest(vec); found && sim >= r.threshold {
			var bestID int32
			err := r.pool.QueryRow(ctx,
				`UPDATE topics SET last_used_at = now() WHERE id = $1 RETURNING id`, best.ID).Scan(&bestID)
			switch {
			case err == nil:
				return Resolution{Topic: best, Requested: slug, Equivalent: best.Slug != slug}, nil
			case errors.Is(err, pgx.ErrNoRows):
				// El tema de la caché ya no existe: se recarga y se crea el pedido.
				if err := r.Load(ctx); err != nil {
					return Resolution{}, err
				}
			default:
				return Resolution{}, fmt.Errorf("no se pudo actualizar el tema %q: %w", best.Slug, err)
			}
		}
	}

	// 2b. Sin equivalente: se crea (o se reutiliza si otro proceso lo creó a la vez).
	var param any // nil se guarda como NULL: embedding pendiente
	if vec != nil {
		param = pgvector.NewHalfVector(vec)
	}
	err = r.pool.QueryRow(ctx,
		`INSERT INTO topics (slug, embedding) VALUES ($1, $2)
		 ON CONFLICT (slug) DO UPDATE
		   SET last_used_at = now(), embedding = COALESCE(topics.embedding, EXCLUDED.embedding)
		 RETURNING id`, slug, param).Scan(&id)
	if err != nil {
		return Resolution{}, fmt.Errorf("no se pudo crear el tema %q: %w", slug, err)
	}
	t := Topic{ID: id, Slug: slug}
	r.cacheSet(t, vec)
	return Resolution{Topic: t, Requested: slug}, nil
}

// embedSlug computes the embedding of a slug. It returns a nil vector, and no
// error, if the embedding backend is unavailable or if a non-zero deadline
// expires. The latter only counts when it is the embedding deadline that
// expired: if the caller's own context is done, that is an error.
func (r *Resolver) embedSlug(ctx context.Context, slug string, deadline time.Time) ([]float32, error) {
	ectx := ctx
	if !deadline.IsZero() {
		var cancel context.CancelFunc
		ectx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	vecs, err := r.emb.EmbedDocuments(ectx, []embedding.Document{{Text: slugText(slug)}})
	switch {
	case err == nil:
		if len(vecs) != 1 {
			return nil, fmt.Errorf("el embedder devolvió %d vectores para el tema %q, se esperaba 1", len(vecs), slug)
		}
		return vecs[0], nil
	case errors.Is(err, embedding.ErrUnavailable):
		return nil, nil
	case !deadline.IsZero() && errors.Is(err, context.DeadlineExceeded) && ectx.Err() != nil && ctx.Err() == nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("no se pudo calcular el embedding del tema %q: %w", slug, err)
	}
}

// slugText turns a slug into the text that is embedded: "mi-coche" -> "mi coche".
func slugText(slug string) string {
	return strings.ReplaceAll(slug, "-", " ")
}

// FillPending computes and stores the embeddings of up to limit topics whose
// embedding is NULL, in batches of 32 (one short transaction per batch, with
// FOR UPDATE SKIP LOCKED so several processes can run it at once). It returns
// how many topics were filled. If the embedder fails, it returns the count so
// far together with the error.
func (r *Resolver) FillPending(ctx context.Context, limit int) (int, error) {
	filled := 0
	for filled < limit {
		n, err := r.fillBatch(ctx, min(fillBatchSize, limit-filled))
		filled += n
		if err != nil {
			return filled, err
		}
		if n == 0 {
			break
		}
	}
	return filled, nil
}

// fillBatch fills up to batch pending topics in one transaction and returns
// how many were filled.
func (r *Resolver) fillBatch(ctx context.Context, batch int) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("no se pudo iniciar la transacción de temas pendientes: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op tras un Commit correcto

	rows, err := tx.Query(ctx,
		`SELECT id, slug FROM topics WHERE embedding IS NULL
		 ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, batch)
	if err != nil {
		return 0, fmt.Errorf("no se pudieron leer los temas pendientes: %w", err)
	}
	var pending []Topic
	for rows.Next() {
		var t Topic
		if err := rows.Scan(&t.ID, &t.Slug); err != nil {
			rows.Close()
			return 0, fmt.Errorf("no se pudo leer un tema pendiente: %w", err)
		}
		pending = append(pending, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("no se pudieron leer los temas pendientes: %w", err)
	}
	if len(pending) == 0 {
		return 0, nil
	}

	docs := make([]embedding.Document, len(pending))
	for i, t := range pending {
		docs[i] = embedding.Document{Text: slugText(t.Slug)}
	}
	vecs, err := r.emb.EmbedDocuments(ctx, docs)
	if err != nil {
		return 0, fmt.Errorf("no se pudieron calcular los embeddings de temas pendientes: %w", err)
	}
	if len(vecs) != len(pending) {
		return 0, fmt.Errorf("el embedder devolvió %d vectores para %d temas", len(vecs), len(pending))
	}

	for i, t := range pending {
		if _, err := tx.Exec(ctx, `UPDATE topics SET embedding = $1 WHERE id = $2`,
			pgvector.NewHalfVector(vecs[i]), t.ID); err != nil {
			return 0, fmt.Errorf("no se pudo guardar el embedding del tema %q: %w", t.Slug, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("no se pudo confirmar los embeddings de temas: %w", err)
	}

	for i, t := range pending {
		r.cacheSet(t, vecs[i])
	}
	return len(pending), nil
}
