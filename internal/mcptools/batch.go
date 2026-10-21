package mcptools

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/memory"
	"lodan/internal/recall"
)

const (
	// recallWorkers bounds how many queries of a recall batch run at once.
	recallWorkers = 4
	// recallChunk is how many queries of a batch are embedded and answered
	// together; it stays well below the size of the query embedding cache.
	recallChunk = 32
	// minBlockBytes is the smallest byte cap of one block of a recall batch.
	minBlockBytes = 600
	// candidateLimit is how many review candidates remember lists per item.
	candidateLimit = 6
)

// batchSizeError reports a batch above memory.MaxBatch.
func batchSizeError(n int, noun string) error {
	return fmt.Errorf("se enviaron %d %s y el máximo es %d", n, noun, memory.MaxBatch)
}

// recallQueries returns the queries of a recall request: query first, then
// queries, trimmed, without empty ones and without exact repeats. The cap is
// checked before removing repeats. A query is optional when the request carries
// at least one filter (tags_all, tags_any, date_from or date_to): with no query
// and no filters the call fails.
func recallQueries(in recallInput) ([]string, error) {
	var all []string
	for _, q := range append([]string{in.Query}, in.Queries...) {
		if q = strings.TrimSpace(q); q != "" {
			all = append(all, q)
		}
	}
	hasFilters := len(in.TagsAll) > 0 || len(in.TagsAny) > 0 ||
		strings.TrimSpace(in.DateFrom) != "" || strings.TrimSpace(in.DateTo) != ""
	if len(all) == 0 && !hasFilters {
		return nil, fmt.Errorf("hay que enviar query o queries")
	}
	if len(all) > memory.MaxBatch {
		return nil, batchSizeError(len(all), "consultas")
	}
	seen := make(map[string]bool, len(all))
	out := all[:0:0]
	for _, q := range all {
		if !seen[q] {
			seen[q] = true
			out = append(out, q)
		}
	}
	return out, nil
}

// blockBytes is the byte cap of each block of a recall batch of n queries: the
// whole batch gets about four single answers, never less than minBlockBytes
// per query and never more than a single answer.
func blockBytes(maxBytes, n int) int {
	return min(maxBytes, max(4*maxBytes/n, minBlockBytes))
}

// recallBatch answers several queries: their embeddings are computed in
// batches, the searches run concurrently and the answer has one block per
// query, in order, with the closing hint once at the end.
func (s *Server) recallBatch(ctx context.Context, queries []string, base recall.Request) string {
	blockCap := blockBytes(s.d.Cfg.RecallMaxBytes, len(queries))
	blocks := make([]string, len(queries))
	for start := 0; start < len(queries); start += recallChunk {
		chunk := queries[start:min(start+recallChunk, len(queries))]
		s.d.Recall.Prime(ctx, chunk)

		sem := make(chan struct{}, recallWorkers)
		var wg sync.WaitGroup
		for i, q := range chunk {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, q string) {
				defer wg.Done()
				defer func() { <-sem }()
				req := base
				req.Query = q
				var body string
				if res, err := s.d.Recall.Recall(ctx, req); err != nil {
					body = "error: " + err.Error()
				} else {
					body = res.FormatBody(blockCap)
				}
				blocks[start+i] = "## " + q + "\n" + body
			}(i, q)
		}
		wg.Wait()
	}
	return strings.Join(blocks, "\n\n") + "\n" + recall.GetHint
}

// opResult is the outcome of one operation of a batch.
type opResult struct {
	text string
	err  error
}

// batchResult renders the outcomes of a batch of operations. A single
// operation keeps the output of the unbatched tool; several are numbered
// ("N. " or "N. error: ") and the result is an error only when all failed.
func batchResult(results []opResult) *mcp.CallToolResult {
	if len(results) == 1 {
		if results[0].err != nil {
			return errorResult(results[0].err)
		}
		return textResult(results[0].text)
	}
	lines := make([]string, len(results))
	failed := 0
	for i, r := range results {
		if r.err != nil {
			failed++
			lines[i] = fmt.Sprintf("%d. error: %s", i+1, r.err.Error())
			continue
		}
		lines[i] = fmt.Sprintf("%d. %s", i+1, r.text)
	}
	res := textResult(strings.Join(lines, "\n"))
	res.IsError = failed == len(results)
	return res
}
