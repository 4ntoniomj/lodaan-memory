package recall

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"lodan/internal/embedding"
)

// primeChunk is the number of queries embedded per call to the embedder.
const primeChunk = 64

// GetHint is the closing line of a recall answer.
const GetHint = getHint

// Prime computes in batched calls the embeddings of the queries that are not
// cached yet and stores them in the cache, so the Recall calls that follow do
// not embed one query at a time. It only works when the embedder implements
// embedding.QueryBatcher; otherwise, or if a batch fails, it does nothing and
// Recall embeds each query as usual. Invalid queries are skipped.
func (s *Service) Prime(ctx context.Context, queries []string) {
	b, ok := s.emb.(embedding.QueryBatcher)
	if !ok {
		return
	}
	var missing []string
	seen := make(map[string]bool, len(queries))
	for _, q := range queries {
		q = strings.TrimSpace(q)
		if q == "" || utf8.RuneCountInString(q) > maxQueryRunes {
			continue
		}
		key := cacheKey(q)
		if seen[key] {
			continue
		}
		seen[key] = true
		if _, ok := s.cache.get(key); !ok {
			missing = append(missing, q)
		}
	}
	for start := 0; start < len(missing); start += primeChunk {
		chunk := missing[start:min(start+primeChunk, len(missing))]
		vecs, err := b.EmbedQueries(ctx, chunk)
		if err != nil || len(vecs) != len(chunk) {
			return
		}
		for i, v := range vecs {
			if len(v) == s.emb.Dims() {
				s.cache.put(cacheKey(chunk[i]), v)
			}
		}
	}
}

// FormatBody is Format without the closing hint, for answers that group
// several results and show the hint once at the end.
func (r Result) FormatBody(maxBytes int) string {
	return strings.TrimSuffix(r.Format(maxBytes), "\n"+getHint)
}

// Line renders the item exactly as it appears in a recall answer.
func (it Item) Line() string { return it.line() }

// scanIDs reads a single id column and closes rows.
func scanIDs(rows pgx.Rows) ([]int64, error) {
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("no se pudo leer un id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("no se pudieron leer los ids: %w", err)
	}
	return ids, nil
}
