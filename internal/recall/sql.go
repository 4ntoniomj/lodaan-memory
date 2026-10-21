package recall

import (
	"strconv"
	"time"
)

// The statements in this file are the single source of truth of the recall queries:
// `lodan bench` measures exactly these texts. Any change to them must be validated
// with `lodan bench`.

// TextRankCap is the maximum number of full-text matches that are ranked. The
// ranking (ts_rank_cd) needs to read every matching tsvector, so it is computed
// over at most this many matches, taken in no particular order, instead of over
// all of them. For a query that matches more rows than this, the best ranked
// ones may be left out.
const TextRankCap = 2000

// textQuery returns the tsquery of the text in the parameter param. The tsv
// column holds the lexemes of both 'spanish' and 'lodan_es' (unaccented before
// stemming), because the stemmer cuts a word differently with and without its
// accent ("albarán" → albaran, "albaran" → albar). The query is parsed with
// both configurations and either one may match.
func textQuery(param string) string {
	return "(websearch_to_tsquery('spanish', " + param + "::text) || websearch_to_tsquery('lodan_es', " + param + "::text))"
}

// TextFilter is the set of filters applied to the full-text candidate search of
// Recall (S12). An empty Query skips the text predicate; the topic/date filters
// are AND-combined.
type TextFilter struct {
	Query          string
	Mode           string
	IncludeHistory bool
	// TagsAll are the topic ids a record must carry ALL of. Empty skips the filter.
	TagsAll []int32
	// TagsAny are the topic ids a record must carry at least ONE of. Empty skips the filter.
	TagsAny []int32
	// DateFrom is the inclusive lower bound on occurred_at (nil skips the filter).
	DateFrom *time.Time
	// DateTo is the inclusive upper bound on occurred_at (nil skips the filter).
	DateTo *time.Time
}

// TextCandidatesSQL returns the full-text candidates statement: the ids of the
// records that match the query and the filters of f, best ranked first (or most
// recent first in list mode). Non-active records are included only with IncludeHistory.
//
// Parameters: $1 is the query text, $2 is the include_history flag, $3 is the
// TagsAll topic ids, $4 is the TagsAny topic ids, $5 is the DateFrom, $6 is the
// DateTo, and $7 is the number of ids to return. The ranking is computed over at
// most TextRankCap matches.
func TextCandidatesSQL(f TextFilter) string {
	if f.Mode == "list" {
		// Superseded and invalidated records keep their tags with active = false,
		// so with include_history ($2) their inactive links count for the tag filters.
		return `SELECT id FROM (
			SELECT m.id, m.tsv, m.occurred_at FROM memories m
			WHERE ($1::text = '' OR m.tsv @@ ` + textQuery("$1") + `)
			  AND ($2::bool OR m.status = 'active')
			  AND (cardinality($3::int[]) = 0 OR
			    (SELECT count(DISTINCT mt.topic_id) FROM memory_topics mt
			     WHERE mt.memory_id = m.id AND mt.topic_id = ANY($3::int[]) AND (mt.active OR m.status <> 'active')) = cardinality($3::int[]))
			  AND (cardinality($4::int[]) = 0 OR EXISTS (
			    SELECT 1 FROM memory_topics mt
			    WHERE mt.memory_id = m.id AND mt.topic_id = ANY($4::int[]) AND (mt.active OR m.status <> 'active')))
			  AND ($5::timestamptz IS NULL OR m.occurred_at >= $5::timestamptz)
			  AND ($6::timestamptz IS NULL OR m.occurred_at <= $6::timestamptz)
			ORDER BY m.occurred_at DESC, m.id DESC
			LIMIT ` + strconv.Itoa(TextRankCap) + `
		) m
		ORDER BY m.occurred_at DESC, m.id DESC
		LIMIT $7`
	}

	return `SELECT id FROM (
		SELECT m.id, m.tsv, m.occurred_at FROM memories m
		WHERE ($1::text = '' OR m.tsv @@ ` + textQuery("$1") + `)
		  AND ($2::bool OR m.status = 'active')
		  AND (cardinality($3::int[]) = 0 OR
		    (SELECT count(DISTINCT mt.topic_id) FROM memory_topics mt
		     WHERE mt.memory_id = m.id AND mt.topic_id = ANY($3::int[]) AND mt.active) = cardinality($3::int[]))
		  AND (cardinality($4::int[]) = 0 OR EXISTS (
		    SELECT 1 FROM memory_topics mt
		    WHERE mt.memory_id = m.id AND mt.topic_id = ANY($4::int[]) AND mt.active))
		  AND ($5::timestamptz IS NULL OR m.occurred_at >= $5::timestamptz)
		  AND ($6::timestamptz IS NULL OR m.occurred_at <= $6::timestamptz)
		LIMIT ` + strconv.Itoa(TextRankCap) + `
	) m
	ORDER BY (CASE WHEN $1::text = '' THEN 0 ELSE ts_rank_cd(m.tsv, ` + textQuery("$1") + `) END) DESC, m.id DESC
	LIMIT $7`
}

// FuzzyCandidatesSQL returns the fuzzy trigram match statement used when full-text
// search finds no candidates. Parameters are the same as TextCandidatesSQL.
func FuzzyCandidatesSQL(f TextFilter) string {
	return `SELECT id FROM (
		SELECT m.id, m.occurred_at,
		       greatest(
		           word_similarity(unaccent(lower($1::text)), unaccent(lower(m.title || ' ' || m.content))),
		           similarity(unaccent(lower($1::text)), unaccent(lower(m.title)))
		       ) AS sim
		FROM memories m
		WHERE ($2::bool OR m.status = 'active')
		  AND (cardinality($3::int[]) = 0 OR
		    (SELECT count(DISTINCT mt.topic_id) FROM memory_topics mt
		     WHERE mt.memory_id = m.id AND mt.topic_id = ANY($3::int[]) AND mt.active) = cardinality($3::int[]))
		  AND (cardinality($4::int[]) = 0 OR EXISTS (
		    SELECT 1 FROM memory_topics mt
		    WHERE mt.memory_id = m.id AND mt.topic_id = ANY($4::int[]) AND mt.active))
		  AND ($5::timestamptz IS NULL OR m.occurred_at >= $5::timestamptz)
		  AND ($6::timestamptz IS NULL OR m.occurred_at <= $6::timestamptz)
		  AND (
		      unaccent(lower(m.title || ' ' || m.content)) % unaccent(lower($1::text))
		      OR word_similarity(unaccent(lower($1::text)), unaccent(lower(m.title || ' ' || m.content))) >= 0.3
		      OR similarity(unaccent(lower($1::text)), unaccent(lower(m.title))) >= 0.3
		  )
		LIMIT ` + strconv.Itoa(TextRankCap) + `
	) m
	ORDER BY m.sim DESC, m.id DESC
	LIMIT $7`
}

// CandidateTextSQL is the full-text statement of the review candidates of a new
// record: active records matching any of its words (the query is built as
// "a or b or c"), best ranked first. Parameters: $1 the query, $2 the ids to
// leave out and $3 the number of ids. The ranking is computed over at most
// TextRankCap matches, as in TextCandidatesSQL.
var CandidateTextSQL = `SELECT id FROM (
		SELECT id, tsv FROM memories
		WHERE tsv @@ ` + textQuery("$1") + ` AND status = 'active' AND NOT (id = ANY($2))
		LIMIT ` + strconv.Itoa(TextRankCap) + `
	) m
	ORDER BY ts_rank_cd(m.tsv, ` + textQuery("$1") + `) DESC, id DESC
	LIMIT $3`

// SharedTagsSQL is the statement of the active records that share tags with a
// new record, used for its review candidates: for each one, the strength of the
// link (Σ ln(N/n) over the shared tags, N active records, n active records with
// the tag) and n of its rarest shared tag, strongest first. Parameters: $1 the
// tag (topic) ids, $2 the ids to leave out and $3 the maximum number of rows.
const SharedTagsSQL = `WITH total AS (
		SELECT count(*)::float8 AS n FROM memories WHERE status = 'active'
	), tagcount AS (
		SELECT mt.topic_id, count(DISTINCT mt.memory_id)::float8 AS n
		FROM memory_topics mt JOIN memories m ON m.id = mt.memory_id AND m.status = 'active'
		WHERE mt.topic_id = ANY($1)
		GROUP BY mt.topic_id
	)
	SELECT m.id, sum(ln(total.n / tc.n)), min(tc.n)::int
	FROM memories m
	JOIN memory_topics mt ON mt.memory_id = m.id AND mt.topic_id = ANY($1)
	JOIN tagcount tc ON tc.topic_id = mt.topic_id
	CROSS JOIN total
	WHERE m.status = 'active' AND NOT (m.id = ANY($2))
	GROUP BY m.id
	ORDER BY 2 DESC, m.id DESC
	LIMIT $3`

// ProfileSQL is the statement of the stable records (facts, preferences and
// decisions) of a topic, most recent first. Parameters: $1 the topic id and $2 the
// maximum number of rows. It returns the columns of scanItems: id, kind, status,
// title, content, occurred_at and a constant superseded_by of 0.
//
// The predicates on mt are written exactly like the predicate of the partial index
// memory_topics_profile_idx (topic_id, ts DESC) so that the planner can use it, and
// ORDER BY mt.ts DESC follows the order of the index, which avoids a sort. There is
// no tie-break by id on purpose: it would defeat that. The order among records with
// the same ts is therefore unspecified.
const ProfileSQL = `SELECT m.id, m.kind::text, m.status::text, m.title, m.content, m.occurred_at, 0::bigint
	FROM memory_topics mt JOIN memories m ON m.id = mt.memory_id
	WHERE mt.topic_id = $1 AND mt.active AND mt.kind IN ('fact','preference','decision')
	ORDER BY mt.ts DESC
	LIMIT $2`

// EventsSQL is the statement of the latest events of a topic, most recent first.
// Parameters and columns are those of ProfileSQL. Its predicates match the partial
// index memory_topics_events_idx, with the same considerations as ProfileSQL.
const EventsSQL = `SELECT m.id, m.kind::text, m.status::text, m.title, m.content, m.occurred_at, 0::bigint
	FROM memory_topics mt JOIN memories m ON m.id = mt.memory_id
	WHERE mt.topic_id = $1 AND mt.active AND mt.kind = 'event'
	ORDER BY mt.ts DESC
	LIMIT $2`
