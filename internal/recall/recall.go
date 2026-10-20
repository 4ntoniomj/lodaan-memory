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
	"slices"
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
	// voteSize is how many of the best fused results take part in the topic vote.
	voteSize = 5
	// topicBonus is added to the score of the records that belong to the topic.
	topicBonus = 0.01
	// snippetRunes is the approximate length of the fragment shown per record.
	snippetRunes = 160
	// topicsTTL is how long the topic cache of the resolver is trusted.
	topicsTTL = time.Minute

	defaultMaxBytes     = 6000
	defaultCandidates   = 400 // benchmark 1M: recall@10 0,89, p95 total 88 ms
	defaultDefaultLimit = 8
	defaultMaxLimit     = 20
	defaultCacheSize    = 256
)

// Values of Result.TopicVia: how the topic of an answer was chosen.
const (
	// TopicViaRequested: the caller asked for the topic.
	TopicViaRequested = "pedido"
	// TopicViaSimilarity: the query embedding is close enough to the topic.
	TopicViaSimilarity = "similitud"
	// TopicViaVote: the best results of the query share the topic.
	TopicViaVote = "votación"
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
	// TopicDetectThreshold is the minimum cosine similarity between the query and
	// a topic to detect that topic by similarity. It is required (it comes from
	// the configuration, topic_detect_similarity). It is independent of the
	// equivalence threshold of the topic.Resolver, which is not used here.
	TopicDetectThreshold float64
	// TagLinkStrength is the strength (Σ ln(N/n) over shared tags) two records
	// need to be connected (config tag_link_strength; default 3.0).
	TagLinkStrength float64
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
	if o.TagLinkStrength <= 0 {
		o.TagLinkStrength = defaultTagLinkStrength
	}
	return o
}

// Request is a recall query.
type Request struct {
	// Query is the free-text question; 1 to 1000 runes once trimmed. May be
	// empty when TagsAll, TagsAny, DateFrom or DateTo are set.
	Query string
	// Topics are the topics the caller wants; the first one that exists is used.
	Topics []string
	// Limit is the number of results: 0 means the default, and it is capped at MaxLimit.
	Limit int
	// IncludeHistory also returns superseded and invalidated records.
	IncludeHistory bool
	// TagsAll (AND) are the topic names the record must carry ALL of. Names
	// that do not exist in the topics table are silently ignored. If the list
	// becomes empty after filtering, Recall fails.
	TagsAll []string
	// TagsAny (OR) are the topic names the record must carry at least ONE of.
	// Names that do not exist in the topics table are silently ignored.
	TagsAny []string
	// DateFrom is the inclusive lower bound on occurred_at. RFC3339 or
	// AAAA-MM-DD (the latter interpreted as local midnight).
	DateFrom string
	// DateTo is the inclusive upper bound on occurred_at. RFC3339 or
	// AAAA-MM-DD (the latter interpreted as the end of that local day).
	DateTo string
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
	// Tags are the slugs of the record's tags, sorted.
	Tags []string
}

// Result is the answer to a Request.
type Result struct {
	// Query is the trimmed query.
	Query string
	// Topic is the requested or detected topic; nil if there is none.
	Topic *topic.Topic
	// TopicDetected is true when Topic was inferred from the query (by similarity or by vote).
	TopicDetected bool
	// TopicVia is how Topic was chosen: TopicViaRequested, TopicViaSimilarity or
	// TopicViaVote. It is empty when there is no topic.
	TopicVia string
	// Profile holds the stable records of the topic (facts, preferences, decisions).
	Profile []Item
	// Events holds the latest events of the topic, most recent first.
	Events []Item
	// Results are the other matches, best first, without the records in Profile or Events.
	Results []Item
	// Connected are the active records connected by rare tags to the first
	// results and not shown elsewhere in the answer.
	Connected []Link
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
//
// When the caller does not ask for a topic, it is detected in two ways. First
// by similarity: the topic closest to the query embedding, if its cosine
// reaches Options.TopicDetectThreshold. If that fails (or there is no
// embedding), by vote once the results are fused: if the best result and at
// least another of the best voteSize share a topic, that topic is used and its
// sheet is read after the fusion.
func (s *Service) Recall(ctx context.Context, req Request) (Result, error) {
	query := strings.TrimSpace(req.Query)
	hasFilters := len(req.TagsAll) > 0 || len(req.TagsAny) > 0 || strings.TrimSpace(req.DateFrom) != "" || strings.TrimSpace(req.DateTo) != ""
	if query == "" && !hasFilters {
		return Result{}, errors.New("la consulta no puede estar vacía")
	}
	if n := utf8.RuneCountInString(query); n > maxQueryRunes {
		return Result{}, fmt.Errorf("la consulta tiene %d caracteres y el máximo es %d", n, maxQueryRunes)
	}
	limit := s.clampLimit(req.Limit)

	dateFrom, dateTo, err := parseRangeBoundaries(req.DateFrom, req.DateTo)
	if err != nil {
		return Result{}, err
	}
	tagsAllIDs, tagsAnyIDs, err := s.resolveFilters(ctx, req.TagsAll, req.TagsAny)
	if err != nil {
		return Result{}, err
	}

	tp, err := s.requestedTopic(ctx, req.Topics)
	if err != nil {
		return Result{}, err
	}

	via := ""
	if tp != nil {
		via = TopicViaRequested
	}

	var (
		vec      []float32
		textOnly bool
		semantic []memory.Neighbor
		textIDs  []int64
		profile  []Item
		events   []Item
	)
	// Con filtros: la ficha del tema (pedida o detectada) se omite. Los filtros
	// ya acotan la búsqueda, y la ficha ignoraría los filtros, mostrando más de
	// lo que el usuario pidió. Si el usuario quiere la ficha, quita los filtros.
	skipTopic := hasFilters
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

	// (c) Ficha del tema pedido. Se omite si hay filtros (skipTopic): el
	// usuario ya delimitó con tags_all/tags_any/date_from/date_to.
	if tp != nil && !skipTopic {
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
		ids, err := s.textSearch(gctx, TextFilter{
			Query:          query,
			IncludeHistory: req.IncludeHistory,
			TagsAll:        tagsAllIDs,
			TagsAny:        tagsAnyIDs,
			DateFrom:       dateFrom,
			DateTo:         dateTo,
		})
		if err != nil {
			return err
		}
		textIDs = ids
		return nil
	})

	// (a) Embedding, detección del tema y candidatos semánticos.
	if skipTopic {
		// La salida vive solo del full-text + los filtros (S12). No hace falta
		// ni embedding ni vecinos semánticos ni detección de tema.
		textOnly = true
	} else {
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
				// The ok of Detect applies the equivalence threshold of the resolver, so
				// only the returned cosine is used. A zero ID means the cache holds no
				// topic with an embedding.
				if t, sim, _ := s.topics.Detect(gctx, vec); t.ID != 0 && sim >= s.opts.TopicDetectThreshold {
					tp, via = &t, TopicViaSimilarity
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
	}

	if err := g.Wait(); err != nil {
		return Result{}, err
	}

	res := Result{
		Query:    query,
		Topic:    tp,
		TopicVia: via,
		Profile:  profile,
		Events:   events,
		TextOnly: textOnly,
	}

	// Fusión: los registros de la ficha no se repiten en los resultados.
	shown := make(map[int64]bool, len(profile)+len(events))
	for _, it := range profile {
		shown[it.ID] = true
	}
	for _, it := range events {
		shown[it.ID] = true
	}
	ids, err := s.fuse(ctx, tp, semantic, textIDs, shown)
	if err != nil {
		return Result{}, err
	}

	// Sin tema todavía: votación entre los mejores resultados. La ficha se lee ahora,
	// tras la fusión, así que se quitan de los resultados los registros que ya muestra.
	if tp == nil && !skipTopic {
		voted, ok, err := s.voteTopic(ctx, ids)
		if err != nil {
			return Result{}, err
		}
		if ok {
			p, e, err := s.topicCard(ctx, voted.ID)
			if err != nil {
				return Result{}, err
			}
			res.Topic, res.TopicVia, res.Profile, res.Events = &voted, TopicViaVote, p, e
			for _, it := range p {
				shown[it.ID] = true
			}
			for _, it := range e {
				shown[it.ID] = true
			}
			ids = slices.DeleteFunc(ids, func(id int64) bool { return shown[id] })
		}
	}
	res.TopicDetected = res.Topic != nil && res.TopicVia != TopicViaRequested
	if len(ids) > limit {
		ids = ids[:limit]
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

	// Conectados por etiquetas raras con los primeros resultados, sin repetir lo mostrado.
	var sources, seen []int64
	for i, it := range res.Results {
		if i < connectedSources {
			sources = append(sources, it.ID)
		}
		seen = append(seen, it.ID)
	}
	for id := range shown {
		seen = append(seen, id)
	}
	if res.Connected, err = s.Connected(ctx, sources, seen, maxConnected); err != nil {
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

	// Un alias confirmado se busca por su etiqueta canónica, y los guiones no cuentan.
	compact := make([]string, len(slugs))
	for i, slug := range slugs {
		if s.topics != nil {
			canonical, ok, err := s.topics.Canonical(ctx, slug)
			if err != nil {
				return nil, err
			}
			if ok {
				slug = canonical
			}
		}
		compact[i] = strings.ReplaceAll(slug, "-", "")
	}
	rows, err := s.pool.Query(ctx, `SELECT id, slug FROM topics WHERE replace(slug, '-', '') = ANY($1) ORDER BY id`, compact)
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
		if _, dup := found[strings.ReplaceAll(t.Slug, "-", "")]; !dup {
			found[strings.ReplaceAll(t.Slug, "-", "")] = t
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("no se pudieron buscar los temas pedidos: %w", err)
	}
	for _, c := range compact { // en el orden pedido
		if t, ok := found[c]; ok {
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

// textSearch returns the ids of the records matching the filter, best first.
// Non-active records are included only when IncludeHistory is set.
func (s *Service) textSearch(ctx context.Context, f TextFilter) ([]int64, error) {
	tagsAll := f.TagsAll
	if tagsAll == nil {
		tagsAll = []int32{}
	}
	tagsAny := f.TagsAny
	if tagsAny == nil {
		tagsAny = []int32{}
	}
	rows, err := s.pool.Query(ctx, TextCandidatesSQL(f),
		f.Query, f.IncludeHistory, tagsAll, tagsAny, f.DateFrom, f.DateTo, textCandidates)
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

// parseRangeBoundaries parses DateFrom and DateTo from a Request. An AAAA-MM-DD
// date_from is local midnight (start of the day); an AAAA-MM-DD date_to is the
// end of that local day so the whole day is included. RFC3339 dates are used
// as-is. Empty strings return nil.
func parseRangeBoundaries(dateFrom, dateTo string) (*time.Time, *time.Time, error) {
	from, err := parseRangeBoundary(dateFrom, false)
	if err != nil {
		return nil, nil, fmt.Errorf("date_from: %w", err)
	}
	to, err := parseRangeBoundary(dateTo, true)
	if err != nil {
		return nil, nil, fmt.Errorf("date_to: %w", err)
	}
	return from, to, nil
}

// parseRangeBoundary parses a date_from or date_to string. isTo=true interprets
// AAAA-MM-DD as the end of that local day (inclusive).
func parseRangeBoundary(raw string, isTo bool) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return &t, nil
	}
	t, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return nil, fmt.Errorf("%q no es válida: usa RFC3339 (2026-09-28T10:30:00+02:00) o AAAA-MM-DD", raw)
	}
	if isTo {
		t = t.Add(24*time.Hour - time.Nanosecond)
	}
	return &t, nil
}

// resolveFilters normalizes each requested tag name, resolves confirmed aliases
// and returns the topic ids that exist in the topics table. Names that do not
// exist are silently dropped from the corresponding list. tagsAll that becomes
// empty after filtering is an error ("tags_all: ninguna tag existe"); tagsAny
// that becomes empty is allowed (the OR filter is simply skipped).
func (s *Service) resolveFilters(ctx context.Context, tagsAll, tagsAny []string) ([]int32, []int32, error) {
	all, err := s.resolveTagIDs(ctx, tagsAll)
	if err != nil {
		return nil, nil, err
	}
	if len(tagsAll) > 0 && len(all) == 0 {
		return nil, nil, errors.New("tags_all: ninguna tag existe")
	}
	any, err := s.resolveTagIDs(ctx, tagsAny)
	if err != nil {
		return nil, nil, err
	}
	return all, any, nil
}

// resolveTagIDs normalizes the names, resolves aliases and returns the topic
// ids that exist in the topics table (silently dropping the rest).
func (s *Service) resolveTagIDs(ctx context.Context, names []string) ([]int32, error) {
	if len(names) == 0 {
		return nil, nil
	}
	var slugs []string
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		slug := topic.Normalize(n)
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		if s.topics != nil {
			canonical, ok, err := s.topics.Canonical(ctx, slug)
			if err != nil {
				return nil, err
			}
			if ok {
				slug = canonical
			}
		}
		slugs = append(slugs, slug)
	}
	if len(slugs) == 0 {
		return nil, nil
	}
	compact := make([]string, len(slugs))
	for i, slug := range slugs {
		compact[i] = strings.ReplaceAll(slug, "-", "")
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id FROM topics WHERE replace(slug, '-', '') = ANY($1) ORDER BY id`, compact)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron buscar las etiquetas: %w", err)
	}
	defer rows.Close()
	var ids []int32
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("no se pudo leer el id de una etiqueta: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("no se pudieron buscar las etiquetas: %w", err)
	}
	return ids, nil
}

// topicCard returns the profile (stable records) and the latest events of a topic.
func (s *Service) topicCard(ctx context.Context, topicID int32) (profile, events []Item, err error) {
	profile, err = s.topicItems(ctx, topicID, ProfileSQL, profileLimit)
	if err != nil {
		return nil, nil, err
	}
	events, err = s.topicItems(ctx, topicID, EventsSQL, eventsLimit)
	if err != nil {
		return nil, nil, err
	}
	return profile, events, nil
}

// topicItems runs sql (ProfileSQL or EventsSQL) for a topic and returns its records.
func (s *Service) topicItems(ctx context.Context, topicID int32, sql string, limit int) ([]Item, error) {
	rows, err := s.pool.Query(ctx, sql, topicID, limit)
	if err != nil {
		return nil, fmt.Errorf("no se pudo leer la ficha del tema: %w", err)
	}
	items, err := scanItems(rows)
	if err != nil {
		return nil, err
	}
	return items, s.attachTags(ctx, items)
}

// attachTags fills the Tags of items with one query.
func (s *Service) attachTags(ctx context.Context, items []Item) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	rows, err := s.pool.Query(ctx, `SELECT mt.memory_id, t.slug FROM memory_topics mt
		JOIN topics t ON t.id = mt.topic_id WHERE mt.memory_id = ANY($1) ORDER BY mt.memory_id, t.slug`, ids)
	if err != nil {
		return fmt.Errorf("no se pudieron leer las etiquetas: %w", err)
	}
	defer rows.Close()
	tags := make(map[int64][]string, len(items))
	for rows.Next() {
		var id int64
		var slug string
		if err := rows.Scan(&id, &slug); err != nil {
			return fmt.Errorf("no se pudo leer una etiqueta: %w", err)
		}
		tags[id] = append(tags[id], slug)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("no se pudieron leer las etiquetas: %w", err)
	}
	for i := range items {
		items[i].Tags = tags[items[i].ID]
	}
	return nil
}

// voteTopic picks a topic from the best results. It looks at the first voteSize
// ids of ranked (best first) and their topics, in a single query. If the first
// one and at least another share a topic, that topic wins; among several, the
// one present in most of those results, and on a tie the lowest id. ok is
// false when there is no such topic.
func (s *Service) voteTopic(ctx context.Context, ranked []int64) (topic.Topic, bool, error) {
	if len(ranked) > voteSize {
		ranked = ranked[:voteSize]
	}
	if len(ranked) < 2 {
		return topic.Topic{}, false, nil
	}
	first := ranked[0]

	rows, err := s.pool.Query(ctx, `SELECT mt.memory_id, t.id, t.slug
		FROM memory_topics mt JOIN topics t ON t.id = mt.topic_id
		WHERE mt.memory_id = ANY($1)`, ranked)
	if err != nil {
		return topic.Topic{}, false, fmt.Errorf("no se pudieron leer los temas de los mejores resultados: %w", err)
	}
	defer rows.Close()

	// count: how many of the top results belong to each topic; slug: the slug of
	// each topic seen; ofFirst: the topics of the best result.
	count := make(map[int32]int)
	slug := make(map[int32]string)
	ofFirst := make(map[int32]bool)
	for rows.Next() {
		var memoryID int64
		var topicID int32
		var name string
		if err := rows.Scan(&memoryID, &topicID, &name); err != nil {
			return topic.Topic{}, false, fmt.Errorf("no se pudo leer el tema de un resultado: %w", err)
		}
		count[topicID]++
		slug[topicID] = name
		if memoryID == first {
			ofFirst[topicID] = true
		}
	}
	if err := rows.Err(); err != nil {
		return topic.Topic{}, false, fmt.Errorf("no se pudieron leer los temas de los mejores resultados: %w", err)
	}

	var bestID int32
	bestCount := 0
	for id := range ofFirst {
		n := count[id]
		if n < 2 { // only the first result has it: nothing is shared
			continue
		}
		if n > bestCount || (n == bestCount && id < bestID) {
			bestID, bestCount = id, n
		}
	}
	if bestCount == 0 {
		return topic.Topic{}, false, nil
	}
	return topic.Topic{ID: bestID, Slug: slug[bestID]}, true, nil
}

// fuse merges the semantic and text candidates with Reciprocal Rank Fusion
// (score = sum of 1/(k+rank), rank starting at 1), adds the topic bonus if tp is
// set, drops the ids in exclude and returns every remaining id, best first. The
// caller cuts the list to the limit, because it may still remove more ids.
func (s *Service) fuse(ctx context.Context, tp *topic.Topic, semantic []memory.Neighbor, textIDs []int64, exclude map[int64]bool) ([]int64, error) {
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
	if err := s.attachTags(ctx, items); err != nil {
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
