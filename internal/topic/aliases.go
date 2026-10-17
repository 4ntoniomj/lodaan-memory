package topic

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"lodan/internal/embedding"
)

// maxSuggestions is the maximum number of alias suggestions for one new tag.
const maxSuggestions = 3

// minEditLen is the minimum length both tags need for the one-edit rule, so
// short tags ("gym", "pc") do not match half the dictionary.
const minEditLen = 5

// Suggestion is an existing tag that may be the same as a new one.
type Suggestion struct {
	// Tag is the existing tag.
	Tag string
	// Records is the number of active records that carry it.
	Records int
}

// Canonical returns the canonical tag of a confirmed alias, comparing without
// hyphens, and whether slug is an alias at all.
func (r *Resolver) Canonical(ctx context.Context, slug string) (string, bool, error) {
	var canonical string
	err := r.pool.QueryRow(ctx, `SELECT canonical FROM tag_aliases
		WHERE state = 'confirmed' AND replace(alias, '-', '') = replace($1, '-', '')
		ORDER BY alias = $1 DESC, alias LIMIT 1`, slug).Scan(&canonical)
	var pgErr *pgconn.PgError
	switch {
	case err == nil:
		return canonical, true, nil
	case errors.Is(err, pgx.ErrNoRows):
		return "", false, nil
	case errors.As(err, &pgErr) && pgErr.Code == "42P01":
		// Sin la migración 0004 todavía (un hook que escribe con el binario nuevo
		// antes de que el servicio migre): no hay alias.
		return "", false, nil
	default:
		return "", false, fmt.Errorf("no se pudo consultar el diccionario de etiquetas: %w", err)
	}
}

// Suggest returns the existing tags that may be the same as the new tag slug:
// singular/plural ("s" or "es"), one edit away (Damerau-Levenshtein, both at
// least minEditLen long) or similar by embedding (at least the resolver
// threshold). Pairs already confirmed or rejected, in either direction, are
// skipped. At most maxSuggestions, the tags with more active records first.
// With store, the suggestions are saved as pending ("suggested"). The record
// exceptID (the one being saved, 0 if none) is not counted in Records, and the
// tags in skip (created in the same call, so not existing before) are ignored.
func (r *Resolver) Suggest(ctx context.Context, slug string, exceptID int64, skip []string, store bool) ([]Suggestion, error) {
	rows, err := r.pool.Query(ctx, `SELECT t.slug, count(DISTINCT m.id)::int
		FROM topics t
		LEFT JOIN memory_topics mt ON mt.topic_id = t.id
		LEFT JOIN memories m ON m.id = mt.memory_id AND m.status = 'active' AND m.id <> $2
		WHERE t.slug <> $1 AND NOT (t.slug = ANY($3::text[]))
		  AND NOT EXISTS (SELECT 1 FROM tag_aliases a WHERE a.state IN ('confirmed', 'rejected')
		    AND ((a.alias = $1 AND a.canonical = t.slug) OR (a.alias = t.slug AND a.canonical = $1)))
		GROUP BY t.slug`, slug, exceptID, skip)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron buscar etiquetas parecidas: %w", err)
	}
	counts := make(map[string]int)
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return nil, fmt.Errorf("no se pudo leer una etiqueta: %w", err)
		}
		counts[s] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("no se pudieron leer las etiquetas: %w", err)
	}

	similar := r.similarByEmbedding(slug)
	var out []Suggestion
	for s, n := range counts {
		if pluralPair(slug, s) || (len(slug) >= minEditLen && len(s) >= minEditLen && oneEdit(slug, s)) || similar[s] {
			out = append(out, Suggestion{Tag: s, Records: n})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Records != out[j].Records {
			return out[i].Records > out[j].Records
		}
		return out[i].Tag < out[j].Tag
	})
	if len(out) > maxSuggestions {
		out = out[:maxSuggestions]
	}
	if store {
		for _, sg := range out {
			if _, err := r.pool.Exec(ctx, `INSERT INTO tag_aliases (alias, canonical, state)
				VALUES ($1, $2, 'suggested') ON CONFLICT (alias, canonical) DO NOTHING`, slug, sg.Tag); err != nil {
				return nil, fmt.Errorf("no se pudo guardar la sugerencia de alias: %w", err)
			}
		}
	}
	return out, nil
}

// similarByEmbedding returns the cached tags whose embedding is at least the
// threshold similar to the cached embedding of slug (none if slug has none).
func (r *Resolver) similarByEmbedding(slug string) map[string]bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var vec []float32
	for _, c := range r.withVec {
		if c.topic.Slug == slug {
			vec = c.vec
			break
		}
	}
	out := make(map[string]bool)
	if vec == nil {
		return out
	}
	for _, c := range r.withVec {
		if c.topic.Slug != slug && embedding.Cosine(vec, c.vec) >= r.threshold {
			out[c.topic.Slug] = true
		}
	}
	return out
}

// pluralPair reports whether one tag is the other plus "s" or "es".
func pluralPair(a, b string) bool {
	return a == b+"s" || a == b+"es" || b == a+"s" || b == a+"es"
}

// oneEdit reports whether a and b are exactly one edit apart: one insertion,
// deletion, substitution or transposition of two adjacent characters.
func oneEdit(a, b string) bool {
	if a == b {
		return false
	}
	la, lb := len(a), len(b)
	switch {
	case la == lb:
		var diff []int
		for i := 0; i < la; i++ {
			if a[i] != b[i] {
				diff = append(diff, i)
				if len(diff) > 2 {
					return false
				}
			}
		}
		if len(diff) == 1 {
			return true
		}
		i, j := diff[0], diff[1]
		return j == i+1 && a[i] == b[j] && a[j] == b[i]
	case la == lb+1:
		return deletionOf(a, b)
	case lb == la+1:
		return deletionOf(b, a)
	default:
		return false
	}
}

// deletionOf reports whether short is long without exactly one character.
func deletionOf(long, short string) bool {
	for i := 0; i < len(long); i++ {
		if long[:i]+long[i+1:] == short {
			return true
		}
	}
	return false
}
