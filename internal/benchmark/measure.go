package benchmark

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"lodan/internal/memory"
	"lodan/internal/recall"
)

const (
	// warmup is the number of untimed iterations before measuring.
	warmup = 20
	// rerankLimit is the LIMIT of the reordering (Q2): the number of semantic results that
	// recall asks Nearest for.
	rerankLimit = 40
	// textLimit is the LIMIT of the full-text search (Q3), as in recall.
	textLimit = 40
	// profileLimit and eventsLimit are the LIMITs of the two statements of the topic card (Q4),
	// as in recall.
	profileLimit = 8
	eventsLimit  = 5
	// recallK is the k of recall@k.
	recallK = 10
)

// Latency is the distribution summary of one query.
type Latency struct {
	P50, P95, P99 time.Duration
}

// percentile returns the nearest-rank percentile p (0-100) of sorted durations.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p * float64(len(sorted)) / 100))
	rank = min(max(rank, 1), len(sorted))
	return sorted[rank-1]
}

// summarize computes p50, p95 and p99 of samples (it sorts a copy).
func summarize(samples []time.Duration) Latency {
	s := slices.Clone(samples)
	slices.Sort(s)
	return Latency{P50: percentile(s, 50), P95: percentile(s, 95), P99: percentile(s, 99)}
}

// searchSQL holds the statements measured at one scale. Every one comes from the
// production code (memory.CandidatesSQL, memory.RerankSQL, recall.TextCandidatesSQL,
// recall.ProfileSQL and recall.EventsSQL), never from a copy, so the benchmark measures
// exactly what runs in production. Only the ground truth of the recall is its own.
type searchSQL struct {
	candidates string // Q1: binary index candidates
	rerank     string // Q2: rerank candidates with the full vector
	text       string // Q3: full-text search (without history, the default)
	profile    string // Q4a: stable records of a topic
	events     string // Q4b: latest events of a topic
	exact      string // ground truth for recall (not a production query)
}

func newSearchSQL(dims int) searchSQL {
	return searchSQL{
		candidates: memory.CandidatesSQL(dims),
		rerank:     memory.RerankSQL(dims),
		text:       recall.TextCandidatesSQL(false),
		profile:    recall.ProfileSQL,
		events:     recall.EventsSQL,
		exact: fmt.Sprintf(`SELECT id FROM memories WHERE status = 'active'
			ORDER BY embedding <=> $1::halfvec(%[1]d) LIMIT %[2]d`, dims, recallK),
	}
}

// queryIDs runs sql and returns the first column of every row as int64. The result is never
// nil, so it can be passed as an array parameter.
func queryIDs(ctx context.Context, conn *pgx.Conn, sql string, args ...any) ([]int64, error) {
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int64, 0, 64)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// queryReranked runs the statement of memory.RerankSQL, which returns (id, title,
// similarity), reading every column like memory.Nearest does, and returns the ids.
func queryReranked(ctx context.Context, conn *pgx.Conn, sql string, args ...any) ([]int64, error) {
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int64, 0, rerankLimit)
	for rows.Next() {
		var (
			id    int64
			title string
			sim   float64
		)
		if err := rows.Scan(&id, &title, &sim); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// drainItems runs a statement of the topic card (recall.ProfileSQL or recall.EventsSQL) and
// reads every column of every row, like recall does, discarding the values.
func drainItems(ctx context.Context, conn *pgx.Conn, sql string, args ...any) error {
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id                          int64
			kind, status, title, detail string
			occurredAt                  time.Time
			supersededBy                int64
		)
		if err := rows.Scan(&id, &kind, &status, &title, &detail, &occurredAt, &supersededBy); err != nil {
			return err
		}
	}
	return rows.Err()
}

// measureShared measures the queries that do not depend on the number of candidates: the
// full-text search (Q3) and the topic card (Q4, two statements). It runs the queries of qs
// in sequence; qs must hold warmup+n items: the first warmup are used to warm up and the
// rest are measured. It returns the n samples of each query.
func measureShared(ctx context.Context, conn *pgx.Conn, sqls searchSQL, qs querySet, n int) (q3, q4 []time.Duration, err error) {
	for i := 0; i < warmup+n; i++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}

		t := time.Now()
		if _, err := queryIDs(ctx, conn, sqls.text, qs.texts[i], textLimit); err != nil {
			return nil, nil, fmt.Errorf("Q3 (texto): %w", err)
		}
		d3 := time.Since(t)

		t = time.Now()
		if err := drainItems(ctx, conn, sqls.profile, qs.topics[i], profileLimit); err != nil {
			return nil, nil, fmt.Errorf("Q4 (ficha de tema, estables): %w", err)
		}
		if err := drainItems(ctx, conn, sqls.events, qs.topics[i], eventsLimit); err != nil {
			return nil, nil, fmt.Errorf("Q4 (ficha de tema, eventos): %w", err)
		}
		d4 := time.Since(t)

		if i >= warmup {
			q3 = append(q3, d3)
			q4 = append(q4, d4)
		}
	}
	return q3, q4, nil
}

// measureSemantic measures the semantic search for a number of candidates: the lookup in
// the binary index (Q1, LIMIT candidates) and the reordering of what it found (Q2). The
// caller sets hnsw.ef_search on conn. qs must hold warmup+n items, as in measureShared.
// It returns the n samples of each query.
func measureSemantic(ctx context.Context, conn *pgx.Conn, sqls searchSQL, qs querySet, n, candidates int) (q1, q2 []time.Duration, err error) {
	for i := 0; i < warmup+n; i++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}

		t := time.Now()
		ids, err := queryIDs(ctx, conn, sqls.candidates, qs.vecs[i], candidates)
		if err != nil {
			return nil, nil, fmt.Errorf("Q1 (candidatos): %w", err)
		}
		d1 := time.Since(t)

		t = time.Now()
		if _, err := queryReranked(ctx, conn, sqls.rerank, ids, qs.vecs[i], []int64{}, rerankLimit); err != nil {
			return nil, nil, fmt.Errorf("Q2 (reordenación): %w", err)
		}
		d2 := time.Since(t)

		if i >= warmup {
			q1 = append(q1, d1)
			q2 = append(q2, d2)
		}
	}
	return q1, q2, nil
}

// sumSamples adds the samples of the parts iteration by iteration: the result has the length
// of the shortest part.
func sumSamples(parts ...[]time.Duration) []time.Duration {
	if len(parts) == 0 {
		return nil
	}
	n := len(parts[0])
	for _, p := range parts {
		n = min(n, len(p))
	}
	out := make([]time.Duration, n)
	for _, p := range parts {
		for i := 0; i < n; i++ {
			out[i] += p[i]
		}
	}
	return out
}

// measureTruth computes the exact top recallK of the first n queries of qs (skipping the
// warm-up ones), by scanning the table. Without index scans the planner can only scan the
// table and sort. The result is the ground truth of measureRecall.
func measureTruth(ctx context.Context, conn *pgx.Conn, sqls searchSQL, qs querySet, n int) ([][]int64, error) {
	if _, err := conn.Exec(ctx, "SET enable_indexscan = off"); err != nil {
		return nil, fmt.Errorf("no se pudo desactivar enable_indexscan: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "RESET enable_indexscan") }()

	truth := make([][]int64, n)
	nonEmpty := 0
	for i := 0; i < n; i++ {
		ids, err := queryIDs(ctx, conn, sqls.exact, qs.vecs[warmup+i])
		if err != nil {
			return nil, fmt.Errorf("recall, búsqueda exacta: %w", err)
		}
		truth[i] = ids
		if len(ids) > 0 {
			nonEmpty++
		}
	}
	if nonEmpty == 0 {
		return nil, errors.New("la búsqueda exacta no devolvió resultados: no se puede calcular el recall")
	}
	return truth, nil
}

// measureRecall computes the mean recall@10 of the approximate search (Q1 with the given
// number of candidates + Q2) against truth, over the first len(truth) queries of qs (skipping
// the warm-up ones). The caller sets hnsw.ef_search on conn.
func measureRecall(ctx context.Context, conn *pgx.Conn, sqls searchSQL, qs querySet, truth [][]int64, candidates int) (float64, error) {
	var sum float64
	counted := 0
	for i, want := range truth {
		if len(want) == 0 {
			continue
		}
		q := qs.vecs[warmup+i]
		ids, err := queryIDs(ctx, conn, sqls.candidates, q, candidates)
		if err != nil {
			return 0, fmt.Errorf("recall, candidatos: %w", err)
		}
		top, err := queryReranked(ctx, conn, sqls.rerank, ids, q, []int64{}, rerankLimit)
		if err != nil {
			return 0, fmt.Errorf("recall, reordenación: %w", err)
		}
		top = top[:min(len(top), recallK)]

		hits := 0
		for _, id := range top {
			if slices.Contains(want, id) {
				hits++
			}
		}
		sum += float64(hits) / float64(len(want))
		counted++
	}
	if counted == 0 {
		return 0, errors.New("no hay consultas con resultado exacto: no se puede calcular el recall")
	}
	return sum / float64(counted), nil
}

// setEfSearch sets hnsw.ef_search on conn for a number of candidates, with the same rule as
// memory.Nearest.
func setEfSearch(ctx context.Context, conn *pgx.Conn, candidates int) (int, error) {
	ef := memory.EfSearch(candidates)
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET hnsw.ef_search = %d", ef)); err != nil {
		return 0, fmt.Errorf("no se pudo fijar hnsw.ef_search: %w", err)
	}
	return ef, nil
}

// sizes are the on-disk sizes, in bytes, of the table and its indexes.
type sizes struct {
	Table, HNSW, GIN, Total int64
}

// measureSizes reads pg_relation_size of memories and the two indexes, and the total size.
func measureSizes(ctx context.Context, conn *pgx.Conn, defs indexDefs) (sizes, error) {
	var s sizes
	err := conn.QueryRow(ctx, `SELECT pg_relation_size('memories'),
			coalesce(pg_relation_size(to_regclass($1)), 0),
			coalesce(pg_relation_size(to_regclass($2)), 0),
			pg_total_relation_size('memories')`,
		defs.hnswName, defs.ginName).Scan(&s.Table, &s.HNSW, &s.GIN, &s.Total)
	if err != nil {
		return sizes{}, fmt.Errorf("no se pudieron medir los tamaños: %w", err)
	}
	return s, nil
}

// postgresRSS returns the sum of VmRSS, in bytes, of the postmaster in pgDataDir and all its
// descendants. It only works on Linux (it reads /proc). Note that the sum counts shared
// memory (shared_buffers) once per process that touched it, so it overestimates real usage.
func postgresRSS(pgDataDir string) (int64, error) {
	if runtime.GOOS != "linux" {
		return 0, fmt.Errorf("no disponible en %s", runtime.GOOS)
	}
	pidFile := filepath.Join(pgDataDir, "postmaster.pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, fmt.Errorf("no se pudo leer %s: %w", pidFile, err)
	}
	first, _, _ := strings.Cut(string(data), "\n")
	root, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil {
		return 0, fmt.Errorf("PID inválido en la primera línea de %s: %w", pidFile, err)
	}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, fmt.Errorf("no se pudo leer /proc: %w", err)
	}
	children := map[int][]int{}
	rss := map[int]int64{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		ppid, kb, ok := readProcStatus(pid)
		if !ok {
			continue // the process may have exited meanwhile
		}
		children[ppid] = append(children[ppid], pid)
		rss[pid] = kb * 1024
	}
	if _, ok := rss[root]; !ok {
		return 0, fmt.Errorf("el proceso postmaster %d no existe", root)
	}

	var total int64
	queue := []int{root}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		total += rss[pid]
		queue = append(queue, children[pid]...)
	}
	return total, nil
}

// readProcStatus returns the parent PID and the VmRSS (in kB) of a process.
func readProcStatus(pid int) (ppid int, rssKB int64, ok bool) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()

	found := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, _ := strings.Cut(sc.Text(), ":")
		switch key {
		case "PPid":
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil {
				return 0, 0, false
			}
			ppid, found = n, true
		case "VmRSS":
			// Format: "  12345 kB". Kernel threads have no VmRSS line.
			fields := strings.Fields(val)
			if len(fields) > 0 {
				rssKB, _ = strconv.ParseInt(fields[0], 10, 64)
			}
		}
	}
	return ppid, rssKB, found
}
