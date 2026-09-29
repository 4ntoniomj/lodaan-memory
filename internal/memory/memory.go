// Package memory stores, revises and retrieves memory records by id.
//
// It implements the save algorithm of spec 001 (exact deduplication by content
// hash, upsert by key, topics, lazy embeddings, suggested relations to similar
// records), the revision actions (update, supersede, invalidate, delete,
// relate, confirm/reject relations) and the worker that fills pending
// embeddings. Search lives in package recall; Nearest is exported for it.
//
// memories and memory_topics are always updated in the same transaction, from
// code (no triggers).
package memory

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"lodan/internal/embedding"
	"lodan/internal/session"
	"lodan/internal/topic"
)

const (
	// maxItems is the maximum number of items per Remember call.
	maxItems = 20
	// maxGetIDs is the maximum number of ids per Get call.
	maxGetIDs = 20
	// maxTitleLen and maxContentLen are measured in runes, after trimming.
	maxTitleLen   = 200
	maxContentLen = 8000
	// defaultTopic is used when an item has no usable topic.
	defaultTopic = "general"
	// fillBatchSize is the number of pending records embedded per transaction.
	fillBatchSize = 32
	// neighborCandidates and maxNeighbors bound the similar-record search done by Remember.
	neighborCandidates = 50
	maxNeighbors       = 3
)

// Kind is the type of a memory record.
type Kind string

// Valid kinds.
const (
	KindFact       Kind = "fact"
	KindPreference Kind = "preference"
	KindDecision   Kind = "decision"
	KindEvent      Kind = "event"
	KindNote       Kind = "note"
)

// ParseKind validates a kind name (case-insensitive, surrounding spaces ignored).
func ParseKind(s string) (Kind, error) {
	switch k := Kind(strings.ToLower(strings.TrimSpace(s))); k {
	case KindFact, KindPreference, KindDecision, KindEvent, KindNote:
		return k, nil
	}
	return "", fmt.Errorf("tipo de registro no válido %q (válidos: fact, preference, decision, event, note)", s)
}

// Status is the lifecycle state of a memory record.
type Status string

// Valid statuses.
const (
	StatusActive      Status = "active"
	StatusSuperseded  Status = "superseded"
	StatusInvalidated Status = "invalidated"
)

// Item is a record to save with Remember.
type Item struct {
	Title, Content string
	Kind           Kind
	// Topics are free-form topic names; empty means "general".
	Topics []string
	// Key, if not empty, makes the item replace the active record with the same key.
	Key string
	// OccurredAt is when the fact happened; nil means now.
	OccurredAt *time.Time
}

// Neighbor is an active record semantically similar to another one.
type Neighbor struct {
	ID         int64
	Title      string
	Similarity float64
}

// Saved is the outcome of saving one Item.
type Saved struct {
	ID    int64
	Title string
	// Topics are the resolved topics (empty for duplicates).
	Topics []topic.Resolution
	// Duplicate is true when an active record with the same normalized content
	// already existed; ID and Title are then those of the existing record.
	Duplicate bool
	// Pending is true when the embedding could not be computed and is left NULL.
	Pending bool
	// Superseded lists the records replaced because they had the same key.
	Superseded []int64
	// Similar lists active records similar enough to be duplicates or contradictions.
	Similar []Neighbor
}

// Relation links a record with another one.
type Relation struct {
	Kind, State string
	OtherID     int64
	OtherTitle  string
	// Outgoing is true when the record is the source of the relation.
	Outgoing bool
}

// Full is a complete record with its topics and relations.
type Full struct {
	ID         int64
	Kind       Kind
	Status     Status
	Title      string
	Content    string
	Key        string
	SessionID  string
	Topics     []string // slugs, sorted
	OccurredAt time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Relations  []Relation
}

// Revision describes a change to an existing record; see Service.Revise.
type Revision struct {
	ID     int64
	Action string // update, supersede, invalidate, delete, relate, confirm_relation, reject_relation
	WithID int64
	// Relation is the relation kind for relate, confirm_relation and reject_relation.
	Relation string
	Title    string
	Content  string
	Topics   []string
	// Confirm must be true for delete.
	Confirm bool
}

// Service saves, revises and reads memory records. It is safe for concurrent use.
//
// The id of the embedding model row is cached on first use: if the
// embedding_models table is emptied (as tests do with Reset), create a new Service.
type Service struct {
	pool         *pgxpool.Pool
	emb          embedding.Embedder
	topics       *topic.Resolver
	sessions     *session.Manager
	dupThreshold float64

	mu      sync.Mutex
	modelID int16
	modelOK bool
}

// NewService creates a Service. Records whose cosine similarity to a new one
// reaches dupThreshold are reported as possible duplicates.
func NewService(pool *pgxpool.Pool, emb embedding.Embedder, topics *topic.Resolver, sessions *session.Manager, dupThreshold float64) *Service {
	return &Service{pool: pool, emb: emb, topics: topics, sessions: sessions, dupThreshold: dupThreshold}
}

// NormalizeContent lowercases s, collapses every run of whitespace into one
// space and trims it. Two contents that normalize equally are duplicates.
func NormalizeContent(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// ContentHash returns the sha256 of the normalized content of s.
func ContentHash(s string) []byte {
	sum := sha256.Sum256([]byte(NormalizeContent(s)))
	return sum[:]
}

// embeddingModelID returns the id of the embedding model row, creating it on first use.
func (s *Service) embeddingModelID(ctx context.Context) (int16, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.modelOK {
		return s.modelID, nil
	}
	var id int16
	err := s.pool.QueryRow(ctx, `INSERT INTO embedding_models (name, dims) VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET dims = EXCLUDED.dims
		RETURNING id`, s.emb.Model(), s.emb.Dims()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("no se pudo registrar el modelo de embeddings %q: %w", s.emb.Model(), err)
	}
	s.modelID, s.modelOK = id, true
	return id, nil
}

// embedDocs embeds docs (which must not be empty). It returns nil vectors and
// no error if the embedding backend is unavailable, so the records stay pending.
func (s *Service) embedDocs(ctx context.Context, docs []embedding.Document) ([][]float32, error) {
	vecs, err := s.emb.EmbedDocuments(ctx, docs)
	switch {
	case err == nil:
		if len(vecs) != len(docs) {
			return nil, fmt.Errorf("el embedder devolvió %d vectores para %d documentos", len(vecs), len(docs))
		}
		return vecs, nil
	case errors.Is(err, embedding.ErrUnavailable):
		return nil, nil
	default:
		return nil, fmt.Errorf("no se pudo calcular el embedding: %w", err)
	}
}

// halfParam converts a vector into a query parameter; a nil vector becomes NULL.
func halfParam(v []float32) any {
	if v == nil {
		return nil
	}
	return pgvector.NewHalfVector(v)
}

// validateText trims s and checks that it has between 1 and limit runes.
func validateText(field, s string, limit int) (string, error) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	if n == 0 {
		return "", fmt.Errorf("el %s no puede estar vacío", field)
	}
	if n > limit {
		return "", fmt.Errorf("el %s tiene %d caracteres y el máximo es %d", field, n, limit)
	}
	return s, nil
}

// notFound is the error for an id that does not exist.
func notFound(id int64) error {
	return fmt.Errorf("el registro #%d no existe", id)
}
