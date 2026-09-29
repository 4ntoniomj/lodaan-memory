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
)

const (
	// warmup is the number of untimed iterations before measuring.
	warmup = 20
	// efSearch is hnsw.ef_search during the measurements (the value recall uses in production).
	efSearch = 100
	// candidates and reranked are the LIMITs of the semantic search (plan: 100 -> 40).
	candidates = 100
	reranked   = 40
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

// searchSQL holds the statements measured at one scale, with the dimension already applied.
type searchSQL struct {
	candidates  string // Q1: binary index candidates
	rerank      string // Q2: rerank candidates with the full vector
	text        string // Q3: full-text search
	topicStable string // Q4a: stable facts of a topic
	topicEvents string // Q4b: latest events of a topic
	exact       string // ground truth for recall
}

func newSearchSQL(dims int) searchSQL {
	return searchSQL{
		candidates: fmt.Sprintf(`SELECT id FROM memories WHERE status = 'active'
			ORDER BY binary_quantize(embedding)::bit(%[1]d) <~> binary_quantize($1::halfvec(%[1]d))::bit(%[1]d)
			LIMIT %[2]d`, dims, candidates),
		rerank: fmt.Sprintf(`SELECT id FROM memories WHERE id = ANY($1)
			ORDER BY embedding <=> $2::halfvec(%[1]d) LIMIT %[2]d`, dims, reranked),
		text: `SELECT id FROM memories WHERE tsv @@ websearch_to_tsquery('spanish', $1)
			ORDER BY ts_rank_cd(tsv, websearch_to_tsquery('spanish', $1)) DESC LIMIT 40`,
		topicStable: `SELECT memory_id FROM memory_topics
			WHERE topic_id = $1 AND active AND kind IN ('fact','preference','decision')
			ORDER BY ts DESC LIMIT 8`,
		topicEvents: `SELECT memory_id FROM memory_topics
			WHERE topic_id = $1 AND active AND kind = 'event'
			ORDER BY ts DESC LIMIT 5`,
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

// scaleLatencies are the latencies of every query at one scale.
type scaleLatencies struct {
	Q1, Q2, Q3, Q4, Total Latency
}

// measureLatencies runs the four queries in sequence for each of the queries in qs (after
// warmup untimed iterations) and summarizes each one and the whole sequence. qs must hold
// warmup+n items: the first warmup are used to warm up, the rest are measured.
func measureLatencies(ctx context.Context, conn *pgx.Conn, sqls searchSQL, qs querySet, n int) (scaleLatencies, error) {
	var q1, q2, q3, q4, total []time.Duration

	iteration := func(i int, record bool) error {
		start := time.Now()

		t := time.Now()
		ids, err := queryIDs(ctx, conn, sqls.candidates, qs.vecs[i])
		if err != nil {
			return fmt.Errorf("Q1 (candidatos): %w", err)
		}
		d1 := time.Since(t)

		t = time.Now()
		if _, err := queryIDs(ctx, conn, sqls.rerank, ids, qs.vecs[i]); err != nil {
			return fmt.Errorf("Q2 (reordenación): %w", err)
		}
		d2 := time.Since(t)

		t = time.Now()
		if _, err := queryIDs(ctx, conn, sqls.text, qs.texts[i]); err != nil {
			return fmt.Errorf("Q3 (texto): %w", err)
		}
		d3 := time.Since(t)

		t = time.Now()
		if _, err := queryIDs(ctx, conn, sqls.topicStable, qs.topics[i]); err != nil {
			return fmt.Errorf("Q4 (ficha de tema, estables): %w", err)
		}
		if _, err := queryIDs(ctx, conn, sqls.topicEvents, qs.topics[i]); err != nil {
			return fmt.Errorf("Q4 (ficha de tema, eventos): %w", err)
		}
		d4 := time.Since(t)

		if record {
			q1 = append(q1, d1)
			q2 = append(q2, d2)
			q3 = append(q3, d3)
			q4 = append(q4, d4)
			total = append(total, time.Since(start))
		}
		return nil
	}

	for i := 0; i < warmup+n; i++ {
		if err := ctx.Err(); err != nil {
			return scaleLatencies{}, err
		}
		if err := iteration(i, i >= warmup); err != nil {
			return scaleLatencies{}, err
		}
	}
	return scaleLatencies{
		Q1: summarize(q1), Q2: summarize(q2), Q3: summarize(q3), Q4: summarize(q4), Total: summarize(total),
	}, nil
}

// measureRecall computes the mean recall@10 of the approximate search (Q1 + Q2) against the
// exact search over the first n queries of qs (skipping the warm-up ones).
func measureRecall(ctx context.Context, conn *pgx.Conn, sqls searchSQL, qs querySet, n int) (float64, error) {
	approx := make([][]int64, n)
	for i := 0; i < n; i++ {
		q := qs.vecs[warmup+i]
		ids, err := queryIDs(ctx, conn, sqls.candidates, q)
		if err != nil {
			return 0, fmt.Errorf("recall, candidatos: %w", err)
		}
		top, err := queryIDs(ctx, conn, sqls.rerank, ids, q)
		if err != nil {
			return 0, fmt.Errorf("recall, reordenación: %w", err)
		}
		approx[i] = top[:min(len(top), recallK)]
	}

	// Exact search: without index scans the planner can only scan the table and sort.
	if _, err := conn.Exec(ctx, "SET enable_indexscan = off"); err != nil {
		return 0, fmt.Errorf("no se pudo desactivar enable_indexscan: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "RESET enable_indexscan") }()

	var sum float64
	counted := 0
	for i := 0; i < n; i++ {
		truth, err := queryIDs(ctx, conn, sqls.exact, qs.vecs[warmup+i])
		if err != nil {
			return 0, fmt.Errorf("recall, búsqueda exacta: %w", err)
		}
		if len(truth) == 0 {
			continue
		}
		hits := 0
		for _, id := range approx[i] {
			if slices.Contains(truth, id) {
				hits++
			}
		}
		sum += float64(hits) / float64(len(truth))
		counted++
	}
	if counted == 0 {
		return 0, errors.New("la búsqueda exacta no devolvió resultados: no se puede calcular el recall")
	}
	return sum / float64(counted), nil
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
