package recall

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"lodan/internal/topic"
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
	return s.links(ctx, limit, linkStrengthSQL, sources, skip)
}

// ConnectedByTags is Connected for a record that is not saved yet and would
// carry the tags tagIDs: N and every n are counted as if it were saved (+1).
func (s *Service) ConnectedByTags(ctx context.Context, tagIDs []int32, limit int) ([]Link, error) {
	if len(tagIDs) == 0 || limit <= 0 {
		return nil, nil
	}
	return s.links(ctx, limit, virtualLinkStrengthSQL, tagIDs)
}

// virtualLinkStrengthSQL is linkStrengthSQL for a record not saved yet with the
// tags $1: it counts that record in N and in the n of each of its tags.
const virtualLinkStrengthSQL = `WITH total AS (
		SELECT count(*)::float8 + 1 AS n FROM memories WHERE status = 'active'
	), tagcount AS (
		SELECT mt.topic_id, count(DISTINCT mt.memory_id)::float8 + 1 AS n
		FROM memory_topics mt JOIN memories m ON m.id = mt.memory_id AND m.status = 'active'
		WHERE mt.topic_id = ANY($1)
		GROUP BY mt.topic_id
	)
	SELECT o.memory_id, sum(ln(total.n / tc.n)) AS strength,
		(array_agg(t.slug ORDER BY tc.n, t.slug))[1] AS rarest
	FROM memory_topics o
	JOIN memories m ON m.id = o.memory_id AND m.status = 'active'
	JOIN tagcount tc ON tc.topic_id = o.topic_id
	JOIN topics t ON t.id = o.topic_id
	CROSS JOIN total
	GROUP BY o.memory_id`

// links runs a strength query (rows of id, strength, rarest tag; one id may
// come several times) and returns the strongest links above the threshold.
func (s *Service) links(ctx context.Context, limit int, sql string, args ...any) ([]Link, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
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

// SuggestKey looks, among links (strongest first), for a record whose key ends
// in a segment (after its last "/", normalized) that is one of tags or a whole
// "-"-delimited sequence of the normalized title. It returns that key and its
// record, or ok false.
func (s *Service) SuggestKey(ctx context.Context, links []Link, tags []string, title string) (key string, id int64, ok bool, err error) {
	if len(links) == 0 {
		return "", 0, false, nil
	}
	ids := make([]int64, len(links))
	for i, l := range links {
		ids[i] = l.ID
	}
	rows, err := s.pool.Query(ctx, `SELECT id, key FROM memories WHERE id = ANY($1) AND key IS NOT NULL AND status = 'active'`, ids)
	if err != nil {
		return "", 0, false, fmt.Errorf("no se pudieron leer las claves: %w", err)
	}
	keys := make(map[int64]string)
	for rows.Next() {
		var rid int64
		var k string
		if err := rows.Scan(&rid, &k); err != nil {
			rows.Close()
			return "", 0, false, fmt.Errorf("no se pudo leer una clave: %w", err)
		}
		keys[rid] = k
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", 0, false, fmt.Errorf("no se pudieron leer las claves: %w", err)
	}
	tagSet := make(map[string]bool, len(tags))
	for _, t := range tags {
		tagSet[t] = true
	}
	normTitle := "-" + topic.Normalize(title) + "-"
	for _, l := range links { // ya vienen del más fuerte al más débil
		k, has := keys[l.ID]
		if !has {
			continue
		}
		last := k
		if i := strings.LastIndex(k, "/"); i >= 0 {
			last = k[i+1:]
		}
		seg := topic.Normalize(last)
		if seg != "" && (tagSet[seg] || strings.Contains(normTitle, "-"+seg+"-")) {
			return k, l.ID, true, nil
		}
	}
	return "", 0, false, nil
}
