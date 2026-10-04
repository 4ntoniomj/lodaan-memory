package recall

import "strconv"

// The statements in this file are the single source of truth of the recall queries:
// `lodan bench` measures exactly these texts. Any change to them must be validated
// with `lodan bench`.

// TextRankCap is the maximum number of full-text matches that are ranked. The
// ranking (ts_rank_cd) needs to read every matching tsvector, so it is computed
// over at most this many matches, taken in no particular order, instead of over
// all of them. For a query that matches more rows than this, the best ranked
// ones may be left out.
const TextRankCap = 2000

// TextCandidatesSQL returns the full-text candidates statement: the ids of the
// records that match the query, best ranked first. Non-active records are included
// only with includeHistory.
//
// Parameters: $1 is the query text and $2 the number of ids to return. The ranking
// is computed over at most TextRankCap matches.
func TextCandidatesSQL(includeHistory bool) string {
	sql := `SELECT id FROM (
		SELECT id, tsv FROM memories
		WHERE tsv @@ websearch_to_tsquery('spanish', $1::text)`
	if !includeHistory {
		sql += ` AND status = 'active'`
	}
	sql += `
		LIMIT ` + strconv.Itoa(TextRankCap) + `
	) m
	ORDER BY ts_rank_cd(m.tsv, websearch_to_tsquery('spanish', $1::text)) DESC, id DESC
	LIMIT $2`
	return sql
}

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
