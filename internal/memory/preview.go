package memory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"lodan/internal/embedding"
	"lodan/internal/topic"
)

// PreviewItem is what saving one Item would do, computed without writing.
type PreviewItem struct {
	Title string
	// Tags are the resolved tags; a tag that does not exist yet has Created set
	// and ID 0.
	Tags []topic.Resolution
	// DuplicateOf is the active record with the same content (0 if none).
	DuplicateOf int64
	// Key is the item's key and KeyHolder the active record that has it (0 if none).
	Key       string
	KeyHolder int64
	// Suggestions are the existing tags that may be the same as a new tag.
	Suggestions []TagSuggestion
	// Vector is the embedding of the item (nil if unavailable).
	Vector []float32
}

// Preview runs the checks of Remember on items and saves nothing: no record,
// tag, relation or alias suggestion is written. Validation errors are the same
// as Remember's.
func (s *Service) Preview(ctx context.Context, items []Item) ([]PreviewItem, error) {
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

	out := make([]PreviewItem, len(ps))
	var created []string
	for i, p := range ps {
		pv := PreviewItem{Title: p.title, Key: p.key}
		id, _, found, err := findActiveByHash(ctx, s.pool, p.hash)
		if err != nil {
			return nil, err
		}
		if found {
			pv.DuplicateOf = id
		}
		if p.key != "" {
			err := s.pool.QueryRow(ctx, `SELECT id FROM memories WHERE key = $1 AND status = 'active'`, p.key).Scan(&pv.KeyHolder)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("no se pudo buscar la clave %q: %w", p.key, err)
			}
		}
		if pv.Tags, err = s.topics.Peek(ctx, p.names); err != nil {
			return nil, err
		}
		for _, r := range pv.Tags {
			if r.Created {
				created = append(created, r.Topic.Slug)
			}
		}
		out[i] = pv
	}

	// Sugerencias de alias, solo frente a etiquetas que ya existen (si falla, no hay).
	for i := range out {
		for _, r := range out[i].Tags {
			if !r.Created {
				continue
			}
			sugs, err := s.topics.PreviewSuggest(ctx, r.Topic.Slug, created)
			if err != nil {
				continue
			}
			for _, sg := range sugs {
				out[i].Suggestions = append(out[i].Suggestions,
					TagSuggestion{New: r.Topic.Slug, Existing: sg.Tag, Records: sg.Records})
			}
		}
	}

	// Embeddings para los candidatos por significado; sin Ollama, no hay vector.
	docs := make([]embedding.Document, len(ps))
	for i, p := range ps {
		docs[i] = embedding.Document{Title: p.title, Text: p.content}
	}
	vecs, err := s.embedDocsWithin(ctx, docs)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if vecs != nil {
			out[i].Vector = vecs[i]
		}
	}
	return out, nil
}
