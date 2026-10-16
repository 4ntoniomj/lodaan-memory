package recall

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"lodan/internal/memory"
)

const (
	// maxCandidateTerms bounds the words of a record used in the full-text
	// search of its candidates.
	maxCandidateTerms = 32
	// candidateTextLimit and candidateSemanticLimit bound each candidate source.
	candidateTextLimit     = 50
	candidateSemanticLimit = 20
	// candidateMinSimilarity is the cosine a record of another topic needs to
	// be a candidate by meaning alone. Records of the same topic and records
	// that share words are candidates regardless of their similarity.
	candidateMinSimilarity = 0.6
	// sameTopicLimit bounds how many records of the topics are ranked.
	sameTopicLimit = 200
	// sameTopicBonus puts every record of the same topics before the others.
	sameTopicBonus = 1.0
)

// CandidateRequest asks for the records a newly saved one should be checked
// against (possible contradictions or relations).
type CandidateRequest struct {
	// Text is the title and content of the new record.
	Text string
	// Vector is the embedding of the new record; nil skips the semantic source.
	Vector []float32
	// TopicIDs are the topics of the new record.
	TopicIDs []int32
	// Exclude are ids that must not be returned (the record itself, the ones
	// saved with it, the ones it replaced or already relates to).
	Exclude []int64
	// Limit is the number of candidates to return.
	Limit int
}

// Candidates returns active records worth reviewing against a new one, best
// first, using the three sources of recall: the records of the same topics
// (always, whatever their similarity), full-text matches on any word of the
// record and, for other topics, semantic neighbours above
// candidateMinSimilarity. The items are those recall shows, so Item.Line
// renders them exactly like a recall answer.
func (s *Service) Candidates(ctx context.Context, req CandidateRequest) ([]Item, error) {
	if req.Limit <= 0 {
		return nil, nil
	}
	exclude := make(map[int64]bool, len(req.Exclude))
	for _, id := range req.Exclude {
		exclude[id] = true
	}
	excludeList := append([]int64(nil), req.Exclude...)
	if excludeList == nil {
		excludeList = []int64{}
	}

	score := make(map[int64]float64)
	if len(req.TopicIDs) > 0 {
		same, err := s.sameTopic(ctx, req.TopicIDs, excludeList)
		if err != nil {
			return nil, err
		}
		for i, id := range same {
			// Most recent first among the records that match no other source.
			score[id] += sameTopicBonus + 1e-6/float64(i+1)
		}
	}

	if q := orQuery(req.Text); q != "" {
		rows, err := s.pool.Query(ctx, CandidateTextSQL, q, excludeList, candidateTextLimit)
		if err != nil {
			return nil, fmt.Errorf("falló la búsqueda de texto de candidatos: %w", err)
		}
		ids, err := scanIDs(rows)
		if err != nil {
			return nil, err
		}
		for i, id := range ids {
			score[id] += 1 / float64(rrfK+i+1)
		}
	}

	if req.Vector != nil {
		near, err := memory.Nearest(ctx, s.pool, req.Vector, s.emb.Dims(), s.opts.Candidates, candidateSemanticLimit, candidateMinSimilarity, req.Exclude)
		if err != nil {
			return nil, fmt.Errorf("falló la búsqueda semántica de candidatos: %w", err)
		}
		for i, n := range near {
			score[n.ID] += 1 / float64(rrfK+i+1)
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
	if len(ids) > req.Limit {
		ids = ids[:req.Limit]
	}
	return s.details(ctx, ids)
}

// sameTopic returns the active records of the topics, most recent first.
func (s *Service) sameTopic(ctx context.Context, topicIDs []int32, exclude []int64) ([]int64, error) {
	rows, err := s.pool.Query(ctx, SameTopicSQL, topicIDs, exclude, sameTopicLimit)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron leer los registros del tema: %w", err)
	}
	return scanIDs(rows)
}

// isShortNumber reports whether w is a number of at most four digits, such as
// the parts of a date ("2026", "10", "15"). Records store absolute dates, so
// those numbers are shared by unrelated records and are not a useful link.
func isShortNumber(w string) bool {
	if len(w) > 4 {
		return false
	}
	for _, r := range w {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// orQuery turns text into a websearch query that matches any of its words
// ("a or b or c"), so a record sharing a single meaningful word is found.
func orQuery(text string) string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := make(map[string]bool, len(words))
	var terms []string
	for _, w := range words {
		if w == "or" || seen[w] || isShortNumber(w) {
			continue
		}
		seen[w] = true
		terms = append(terms, w)
		if len(terms) == maxCandidateTerms {
			break
		}
	}
	return strings.Join(terms, " or ")
}
