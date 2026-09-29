// Package recall retrieves memory records for the AI in a single call.
//
// It implements the retrieval algorithm of spec 001: a hybrid search (semantic
// candidates from the binary-quantized HNSW index plus Spanish full-text
// candidates) merged with Reciprocal Rank Fusion, the topic sheet (stable facts
// and latest events of the detected or requested topic) and a compact
// plain-text format with a byte cap. Writing records lives in package memory.
package recall

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lodan/internal/embedding"
	"lodan/internal/memory"
	"lodan/internal/topic"
)

const (
	// maxQueryRunes is the maximum length of a query, in runes.
	maxQueryRunes = 1000
	// textCandidates and semanticCandidates are the size of each candidate list
	// that goes into the fusion.
	textCandidates     = 40
	semanticCandidates = 40
	// profileLimit and eventsLimit are the sizes of the topic sheet sections.
	profileLimit = 8
	eventsLimit  = 5
	// rrfK is the constant of Reciprocal Rank Fusion.
	rrfK = 60
	// topicBonus is added to the score of the records that belong to the topic.
	topicBonus = 0.01
	// snippetRunes is the approximate length of the fragment shown per record.
	snippetRunes = 160
	// topicsTTL is how long the topic cache of the resolver is trusted.
	topicsTTL = time.Minute

	defaultMaxBytes     = 6000
	defaultCandidates   = 200
	defaultDefaultLimit = 8
	defaultMaxLimit     = 20
	defaultCacheSize    = 256
)

// Options configures a Service. Zero values take the defaults noted per field.
type Options struct {
	// MaxBytes is the byte cap of the formatted answer (default 6000).
	MaxBytes int
	// Candidates is how many records the binary index looks up before
	// reordering them with the full vectors (default 200).
	Candidates int
	// DefaultLimit is the number of results when the request has none (default 8).
	DefaultLimit int
	// MaxLimit is the highest number of results a request may ask for (default 20).
	MaxLimit int
	// TopicThreshold is the minimum cosine similarity between the query and a
	// topic to detect it. It is required (it comes from the configuration) and
	// is applied on top of the threshold of the topic.Resolver, which should
	// have been built with the same value.
	TopicThreshold float64
	// CacheSize is the number of query embeddings kept in memory (default 256).
	CacheSize int
}

// withDefaults returns o with every zero field replaced by its default.
func (o Options) withDefaults() Options {
	if o.MaxBytes <= 0 {
		o.MaxBytes = defaultMaxBytes
	}
	if o.Candidates <= 0 {
		o.Candidates = defaultCandidates
	}
	if o.MaxLimit <= 0 {
		o.MaxLimit = defaultMaxLimit
	}
	if o.DefaultLimit <= 0 {
		o.DefaultLimit = defaultDefaultLimit
	}
	if o.DefaultLimit > o.MaxLimit {
		o.DefaultLimit = o.MaxLimit
	}
	if o.CacheSize <= 0 {
		o.CacheSize = defaultCacheSize
	}
	return o
}

// Request is a recall query.
type Request struct {
	// Query is the free-text question; 1 to 1000 runes once trimmed.
	Query string
	// Topics are the topics the caller wants; the first one that exists is used.
	Topics []string
	// Limit is the number of results: 0 means the default, and it is capped at MaxLimit.
	Limit int
	// IncludeHistory also returns superseded and invalidated records.
	IncludeHistory bool
}

// Item is one record in a recall answer.
type Item struct {
	ID int64
	// Kind is the record type: fact, preference, decision, event or note.
	Kind string
	// Status is active, superseded or invalidated.
	Status  string
	Title   string
	Snippet string
	// OccurredAt is when the recorded fact happened.
	OccurredAt time.Time
	// SupersededBy is the id of the record that replaced this one (0 if none or not superseded).
	SupersededBy int64
}

// Result is the answer to a Request.
type Result struct {
	// Query is the trimmed query.
	Query string
	// Topic is the requested or detected topic; nil if there is none.
	Topic *topic.Topic
	// TopicDetected is true when Topic was inferred from the query.
	TopicDetected bool
	// Profile holds the stable records of the topic (facts, preferences, decisions).
	Profile []Item
	// Events holds the latest events of the topic, most recent first.
	Events []Item
	// Results are the other matches, best first, without the records in Profile or Events.
	Results []Item
	// TextOnly is true when the embedding service was unavailable and only full-text search was used.
	TextOnly bool
}

// Service answers recall requests. It is safe for concurrent use.
type Service struct {
	pool   *pgxpool.Pool
	emb    embedding.Embedder
	topics *topic.Resolver
	opts   Options
	cache  *lru

	topicsMu     sync.Mutex
	topicsLoaded time.Time
}

// NewService creates a Service. topics is the resolver whose in-memory cache is
// used to detect the topic of a query; the Service reloads that cache from the
// database when it is older than a minute, so topics created by other processes
// are found.
func NewService(pool *pgxpool.Pool, emb embedding.Embedder, topics *topic.Resolver, opts Options) *Service {
	opts = opts.withDefaults()
	return &Service{
		pool:   pool,
		emb:    emb,
		topics: topics,
		opts:   opts,
		cache:  newLRU(opts.CacheSize),
	}
}

// MaxBytes returns the configured byte cap, to pass to Result.Format.
func (s *Service) MaxBytes() int { return s.opts.MaxBytes }

// clampLimit applies the default and the maximum to a requested limit.
func (s *Service) clampLimit(limit int) int {
	if limit == 0 {
		limit = s.opts.DefaultLimit
	}
	return min(max(limit, 1), s.opts.MaxLimit)
}

// Recall runs the hybrid search.
//
// The embedding of the query (slow on CPU) runs in parallel with the full-text
// search and, when the topic is already known, with its sheet. If the
// embedding service is unavailable the search continues with text only and
// Result.TextOnly is set; any other embedding error is returned.
func (s *Service) Recall(ctx context.Context, req Request) (Result, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return Result{}, errors.New("la consulta no puede estar vacía")
	}
	if n := utf8.RuneCountInString(query); n > maxQueryRunes {
		return Result{}, fmt.Errorf("la consulta tiene %d caracteres y el máximo es %d", n, maxQueryRunes)
	}
	limit := s.clampLimit(req.Limit)

	tp, err := s.requestedTopic(ctx, req.Topics)
	if err != nil {
		return Result{}, err
	}

	var (
		vec      []float32
		textOnly bool
		detected bool
		semantic []memory.Neighbor
		textIDs  []int64
		profile  []Item
		events   []Item
	)
	g, gctx := newGroup(ctx)

	// Recarga de la caché de temas: solo hace falta si hay que detectar el tema.
	topicsReady := make(chan struct{})
	if tp == nil && s.topics != nil {
		g.Go(func() error {
			defer close(topicsReady)
			return s.refreshTopics(gctx)
		})
	} else {
		close(topicsReady)
	}

	// (c) Ficha del tema pedido.
	if tp != nil {
		id := tp.ID
		g.Go(func() error {
			p, e, err := s.topicCard(gctx, id)
			if err != nil {
				return err
			}
			profile, events = p, e
			return nil
		})
	}

	// (b) Candidatos de texto.
	g.Go(func() error {
		ids, err := s.textSearch(gctx, query, req.IncludeHistory)
		if err != nil {
			return err
		}
		textIDs = ids
		return nil
	})

	// (a) Embedding, detección del tema y candidatos semánticos.
	g.Go(func() error {
		v, err := s.embedQuery(gctx, query)
		switch {
		case err == nil:
			vec = v
		case errors.Is(err, embedding.ErrUnavailable):
			textOnly = true
			return nil
		default:
			return err
		}

		if tp == nil && s.topics != nil {
			<-topicsReady
			if err := gctx.Err(); err != nil {
				return err
			}
			if t, sim, ok := s.topics.Detect(gctx, vec); ok && sim >= s.opts.TopicThreshold {
				tp, detected = &t, true
				g.Go(func() error {
					p, e, err := s.topicCard(gctx, t.ID)
					if err != nil {
						return err
					}
					profile, events = p, e
					return nil
				})
			}
		}

		near, err := memory.Nearest(gctx, s.pool, vec, s.emb.Dims(), s.opts.Candidates, semanticCandidates, -1, nil)
		if err != nil {
			return fmt.Errorf("falló la búsqueda semántica: %w", err)
		}
		semantic = near
		return nil
	})

	if err := g.Wait(); err != nil {
		return Result{}, err
	}

	res := Result{
		Query:         query,
		Topic:         tp,
		TopicDetected: detected,
		Profile:       profile,
		Events:        events,
		TextOnly:      textOnly,
	}

	// Fusión: los registros de la ficha no se repiten en los resultados.
	shown := make(map[int64]bool, len(profile)+len(events))
	for _, it := range profile {
		shown[it.ID] = true
	}
	for _, it := range events {
		shown[it.ID] = true
	}
	ids, err := s.fuse(ctx, tp, semantic, textIDs, shown, limit)
	if err != nil {
		return Result{}, err
	}
	if req.IncludeHistory {
		ids, err = s.addHistory(ctx, ids, shown, limit*2)
		if err != nil {
			return Result{}, err
		}
	}
	res.Results, err = s.details(ctx, ids)
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

// requestedTopic returns the first requested topic that exists, or nil.
func (s *Service) requestedTopic(ctx context.Context, names []string) (*topic.Topic, error) {
	var slugs []string
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		slug := topic.Normalize(n)
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		slugs = append(slugs, slug)
	}
	if len(slugs) == 0 {
		return nil, nil
	}

	rows, err := s.pool.Query(ctx, `SELECT id, slug FROM topics WHERE slug = ANY($1)`, slugs)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron buscar los temas pedidos: %w", err)
	}
	defer rows.Close()
	found := make(map[string]topic.Topic, len(slugs))
	for rows.Next() {
		var t topic.Topic
		if err := rows.Scan(&t.ID, &t.Slug); err != nil {
			return nil, fmt.Errorf("no se pudo leer un tema: %w", err)
		}
		found[t.Slug] = t
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("no se pudieron buscar los temas pedidos: %w", err)
	}
	for _, slug := range slugs { // en el orden pedido
		if t, ok := found[slug]; ok {
			return &t, nil
		}
	}
	return nil, nil
}

// refreshTopics reloads the topic cache of the resolver if it is older than topicsTTL.
func (s *Service) refreshTopics(ctx context.Context) error {
	s.topicsMu.Lock()
	defer s.topicsMu.Unlock()
	if !s.topicsLoaded.IsZero() && time.Since(s.topicsLoaded) < topicsTTL {
		return nil
	}
	if err := s.topics.Load(ctx); err != nil {
		return err
	}
	s.topicsLoaded = time.Now()
	return nil
}

// embedQuery returns the embedding of the query, from the cache when possible.
// The returned slice is shared with the cache and must not be modified.
func (s *Service) embedQuery(ctx context.Context, query string) ([]float32, error) {
	key := cacheKey(query)
	if v, ok := s.cache.get(key); ok {
		return v, nil
	}
	v, err := s.emb.EmbedQuery(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("no se pudo calcular el embedding de la consulta: %w", err)
	}
	if len(v) != s.emb.Dims() {
		return nil, fmt.Errorf("el embedder devolvió %d dimensiones y se esperaban %d", len(v), s.emb.Dims())
	}
	s.cache.put(key, v)
	return v, nil
}

// textSearch returns the ids of the records matching the query in full text,
// best first. Non-active records are included only with history.
func (s *Service) textSearch(ctx context.Context, query string, history bool) ([]int64, error) {
	sql := `SELECT id FROM memories WHERE tsv @@ websearch_to_tsquery('spanish', $1::text)`
	if !history {
		sql += ` AND status = 'active'`
	}
	sql += ` ORDER BY ts_rank_cd(tsv, websearch_to_tsquery('spanish', $1::text)) DESC, id DESC LIMIT $2`

	rows, err := s.pool.Query(ctx, sql, query, textCandidates)
	if err != nil {
		return nil, fmt.Errorf("falló la búsqueda por texto: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("no se pudo leer un candidato de texto: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("falló la búsqueda por texto: %w", err)
	}
	return ids, nil
}

// topicCard returns the profile (stable records) and the latest events of a topic.
func (s *Service) topicCard(ctx context.Context, topicID int32) (profile, events []Item, err error) {
	profile, err = s.topicItems(ctx, topicID, `mt.kind IN ('fact','preference','decision')`, profileLimit)
	if err != nil {
		return nil, nil, err
	}
	events, err = s.topicItems(ctx, topicID, `mt.kind = 'event'`, eventsLimit)
	if err != nil {
		return nil, nil, err
	}
	return profile, events, nil
}

// topicItems returns the active records of a topic that satisfy cond (a fixed
// SQL condition over mt), most recent first.
func (s *Service) topicItems(ctx context.Context, topicID int32, cond string, limit int) ([]Item, error) {
	rows, err := s.pool.Query(ctx, `SELECT m.id, m.kind::text, m.status::text, m.title, m.content, m.occurred_at, 0::bigint
		FROM memory_topics mt JOIN memories m ON m.id = mt.memory_id
		WHERE mt.topic_id = $1 AND mt.active AND `+cond+`
		ORDER BY mt.ts DESC, m.id DESC LIMIT $2`, topicID, limit)
	if err != nil {
		return nil, fmt.Errorf("no se pudo leer la ficha del tema: %w", err)
	}
	return scanItems(rows)
}

// fuse merges the semantic and text candidates with Reciprocal Rank Fusion
// (score = sum of 1/(k+rank), rank starting at 1), adds the topic bonus, drops
// the ids in exclude and returns at most limit ids, best first.
func (s *Service) fuse(ctx context.Context, tp *topic.Topic, semantic []memory.Neighbor, textIDs []int64, exclude map[int64]bool, limit int) ([]int64, error) {
	score := make(map[int64]float64, len(semantic)+len(textIDs))
	for i, n := range semantic {
		score[n.ID] += 1 / float64(rrfK+i+1)
	}
	for i, id := range textIDs {
		score[id] += 1 / float64(rrfK+i+1)
	}
	if len(score) == 0 {
		return nil, nil
	}

	if tp != nil {
		all := make([]int64, 0, len(score))
		for id := range score {
			all = append(all, id)
		}
		rows, err := s.pool.Query(ctx, `SELECT memory_id FROM memory_topics WHERE topic_id = $1 AND memory_id = ANY($2)`, tp.ID, all)
		if err != nil {
			return nil, fmt.Errorf("no se pudo comprobar la pertenencia al tema: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return nil, fmt.Errorf("no se pudo leer una pertenencia al tema: %w", err)
			}
			score[id] += topicBonus
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("no se pudo comprobar la pertenencia al tema: %w", err)
		}
	}

	ids := make([]int64, 0, len(score))
	for id := range score {
		if !exclude[id] {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		if score[ids[i]] != score[ids[j]] {
			return score[ids[i]] > score[ids[j]]
		}
		return ids[i] > ids[j]
	})
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

// addHistory appends to ids the records that they replaced (relation
// "supersedes" whose source is in ids), skipping those already shown, until
// the list reaches maxTotal.
func (s *Service) addHistory(ctx context.Context, ids []int64, shown map[int64]bool, maxTotal int) ([]int64, error) {
	if len(ids) == 0 || len(ids) >= maxTotal {
		return ids, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT source_id, target_id FROM memory_relations
		WHERE kind = 'supersedes' AND source_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("no se pudo leer el historial: %w", err)
	}
	defer rows.Close()

	rank := make(map[int64]int, len(ids))
	inList := make(map[int64]bool, len(ids))
	for i, id := range ids {
		rank[id] = i
		inList[id] = true
	}
	type pair struct{ source, target int64 }
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.source, &p.target); err != nil {
			return nil, fmt.Errorf("no se pudo leer una relación de sustitución: %w", err)
		}
		if !inList[p.target] && !shown[p.target] {
			pairs = append(pairs, p)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("no se pudo leer el historial: %w", err)
	}

	// Primero los sustituidos por el mejor resultado; entre los de uno, el más reciente antes.
	sort.Slice(pairs, func(i, j int) bool {
		if rank[pairs[i].source] != rank[pairs[j].source] {
			return rank[pairs[i].source] < rank[pairs[j].source]
		}
		return pairs[i].target > pairs[j].target
	})
	for _, p := range pairs {
		if len(ids) >= maxTotal {
			break
		}
		if inList[p.target] {
			continue
		}
		inList[p.target] = true
		ids = append(ids, p.target)
	}
	return ids, nil
}

// details loads the items for ids, in that order, in a single query. Ids that
// no longer exist are skipped.
func (s *Service) details(ctx context.Context, ids []int64) ([]Item, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT m.id, m.kind::text, m.status::text, m.title, m.content, m.occurred_at,
			CASE WHEN m.status = 'superseded'
				THEN COALESCE((SELECT r.source_id FROM memory_relations r
					WHERE r.target_id = m.id AND r.kind = 'supersedes'
					ORDER BY r.created_at DESC, r.source_id DESC LIMIT 1), 0)
				ELSE 0 END::bigint
		FROM memories m WHERE m.id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron leer los detalles de los resultados: %w", err)
	}
	items, err := scanItems(rows)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]Item, len(items))
	for _, it := range items {
		byID[it.ID] = it
	}
	out := make([]Item, 0, len(ids))
	for _, id := range ids {
		if it, ok := byID[id]; ok {
			out = append(out, it)
		}
	}
	return out, nil
}

// scanItems reads rows of (id, kind, status, title, content, occurred_at,
// superseded_by) into items and closes rows.
func scanItems(rows pgx.Rows) ([]Item, error) {
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		var content string
		if err := rows.Scan(&it.ID, &it.Kind, &it.Status, &it.Title, &content, &it.OccurredAt, &it.SupersededBy); err != nil {
			return nil, fmt.Errorf("no se pudo leer un registro: %w", err)
		}
		it.Snippet = snippet(content)
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("no se pudieron leer los registros: %w", err)
	}
	return out, nil
}

// snippet returns about snippetRunes runes of content with line breaks and
// runs of spaces collapsed, ending in "…" if it was cut.
func snippet(content string) string {
	s := strings.Join(strings.Fields(content), " ")
	if utf8.RuneCountInString(s) <= snippetRunes {
		return s
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:snippetRunes]), " ") + "…"
}
