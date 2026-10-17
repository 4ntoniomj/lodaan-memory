package recall

import (
	"context"
	"fmt"
	"sort"
)

// defaultTagLinkStrength is the strength two records need to be connected
// when Options.TagLinkStrength is not set (config tag_link_strength).
const defaultTagLinkStrength = 3.0

// maxConnected is the maximum number of connected records shown by remember
// and by each recall answer.
const maxConnected = 5

// connectedSources is how many of the first results of a recall answer are
// used to look for connected records.
const connectedSources = 3

// Link is an active record connected to another one by their shared tags.
type Link struct {
	Item
	// Strength is Σ ln(N/n) over the shared tags: N active records, n active
	// records carrying the tag. Rare tags weigh more than common ones.
	Strength float64
	// Tag is the rarest shared tag (fewest records; ties alphabetical).
	Tag string
}

// linkStrengthSQL returns, for every active record outside $2 that shares a tag
// with any record in $1, its strength with each of those records and the rarest
// tag they share. Only active records count for N and n.
const linkStrengthSQL = `WITH total AS (
		SELECT count(*)::float8 AS n FROM memories WHERE status = 'active'
	), tagcount AS (
		SELECT mt.topic_id, count(DISTINCT mt.memory_id)::float8 AS n
		FROM memory_topics mt JOIN memories m ON m.id = mt.memory_id AND m.status = 'active'
		GROUP BY mt.topic_id
	)
	SELECT o.memory_id, sum(ln(total.n / tc.n)) AS strength,
		(array_agg(t.slug ORDER BY tc.n, t.slug))[1] AS rarest
	FROM memory_topics s
	JOIN memory_topics o ON o.topic_id = s.topic_id AND o.memory_id <> s.memory_id
	JOIN memories m ON m.id = o.memory_id AND m.status = 'active'
	JOIN tagcount tc ON tc.topic_id = s.topic_id
	JOIN topics t ON t.id = s.topic_id
	CROSS JOIN total
	WHERE s.memory_id = ANY($1) AND NOT (o.memory_id = ANY($2))
	GROUP BY o.memory_id, s.memory_id`

// Connected returns up to limit active records connected to any record of
// sources (strength at least the configured tag_link_strength), strongest
// first (ties: smaller id first). Records in exclude are never returned; the
// sources themselves are always excluded.
func (s *Service) Connected(ctx context.Context, sources, exclude []int64, limit int) ([]Link, error) {
	if len(sources) == 0 || limit <= 0 {
		return nil, nil
	}
	skip := append(append([]int64{}, exclude...), sources...)
	rows, err := s.pool.Query(ctx, linkStrengthSQL, sources, skip)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron calcular las conexiones por etiquetas: %w", err)
	}
	best := make(map[int64]Link)
	for rows.Next() {
		var id int64
		var strength float64
		var tag string
		if err := rows.Scan(&id, &strength, &tag); err != nil {
			rows.Close()
			return nil, fmt.Errorf("no se pudo leer una conexión: %w", err)
		}
		if strength < s.opts.TagLinkStrength {
			continue
		}
		if cur, ok := best[id]; !ok || strength > cur.Strength {
			best[id] = Link{Item: Item{ID: id}, Strength: strength, Tag: tag}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("no se pudieron leer las conexiones: %w", err)
	}

	links := make([]Link, 0, len(best))
	for _, l := range best {
		links = append(links, l)
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].Strength != links[j].Strength {
			return links[i].Strength > links[j].Strength
		}
		return links[i].ID < links[j].ID
	})
	if len(links) > limit {
		links = links[:limit]
	}
	ids := make([]int64, len(links))
	for i, l := range links {
		ids[i] = l.ID
	}
	items, err := s.details(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]Item, len(items))
	for _, it := range items {
		byID[it.ID] = it
	}
	out := links[:0]
	for _, l := range links {
		if it, ok := byID[l.ID]; ok {
			l.Item = it
			out = append(out, l)
		}
	}
	return out, nil
}
