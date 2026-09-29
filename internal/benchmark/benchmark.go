// Package benchmark implements `lodan bench`: it generates synthetic memories, loads them with
// COPY into the separate database lodan_bench, builds the indexes and measures latency,
// recall@10, sizes and RAM at increasing scales, and renders the result as markdown.
//
// memory_topics is filled after each load from a temporary mapping table (bench_topic_map)
// that holds the topic of every new memory; kind, active and ts come from memories itself.
package benchmark

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lodan/internal/config"
	"lodan/internal/database"
	"lodan/internal/embedding"
)

const (
	// benchDatabase is the database the benchmark runs in. The real one, "lodan", is never touched.
	benchDatabase = "lodan_bench"
	// benchModel is the embedding model name recorded in the benchmark database.
	benchModel = "bench-synthetic"
	// DefaultOut is where the report is written when Options.Out is empty.
	DefaultOut = "specs/001-nucleo-memoria/benchmark.md"
	// embedWarmup is the number of untimed real embeddings before measuring them.
	embedWarmup = 2
	// progressEvery is how many rows are generated between progress lines while loading.
	progressEvery = 100_000
)

// Options configures a benchmark run. Zero values take the documented defaults.
type Options struct {
	// Rows are the table sizes to measure (default 10000, 100000, 1000000). They are sorted and
	// loaded incrementally.
	Rows []int
	// Queries is the number of measured repetitions per scale (default 500).
	Queries int
	// TruthQueries is the number of queries used for recall@10 (default 100).
	TruthQueries int
	// EmbedQueries is the number of real embeddings measured against Ollama (default 30 when
	// negative; 0 skips the measurement).
	EmbedQueries int
	// Clusters is the number of vector clusters (default 2000).
	Clusters int
	// Topics is the number of topics (default 1000).
	Topics int
	// Seed makes the generated data deterministic.
	Seed int64
	// Out is the markdown file Run writes (default DefaultOut).
	Out string
	// Log receives the progress (nil discards it).
	Log io.Writer
}

// withDefaults returns a validated copy of o with the defaults applied.
func (o Options) withDefaults() (Options, error) {
	if len(o.Rows) == 0 {
		o.Rows = []int{10_000, 100_000, 1_000_000}
	}
	o.Rows = slices.Clone(o.Rows)
	slices.Sort(o.Rows)
	o.Rows = slices.Compact(o.Rows)
	for _, n := range o.Rows {
		if n <= 0 {
			return o, fmt.Errorf("el número de filas de una escala debe ser mayor que 0, y es %d", n)
		}
	}
	if o.Queries <= 0 {
		o.Queries = 500
	}
	if o.TruthQueries <= 0 {
		o.TruthQueries = 100
	}
	o.TruthQueries = min(o.TruthQueries, o.Queries)
	if o.EmbedQueries < 0 {
		o.EmbedQueries = 30
	}
	if o.Clusters <= 0 {
		o.Clusters = 2000
	}
	if o.Topics <= 0 {
		o.Topics = 1000
	}
	if o.Out == "" {
		o.Out = DefaultOut
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	return o, nil
}

// logger is a goroutine-safe progress writer.
type logger struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *logger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, format+"\n", args...)
}

// Run prepares the local cluster and the lodan_bench database, runs the benchmark and writes
// the markdown report to opts.Out. If the run fails half-way, the scales already measured are
// still written (marked as incomplete) and returned together with the error.
func Run(ctx context.Context, cfg config.Config, opts Options) (Report, error) {
	opts, err := opts.withDefaults()
	if err != nil {
		return Report{}, err
	}
	lg := &logger{w: opts.Log}

	cluster, err := database.NewCluster(cfg)
	if err != nil {
		return Report{}, err
	}
	lg.Printf("Arrancando el clúster local de PostgreSQL...")
	if err := cluster.EnsureRunning(ctx); err != nil {
		return Report{}, err
	}
	if err := cluster.EnsureDatabase(ctx, benchDatabase); err != nil {
		return Report{}, err
	}
	dsn, err := cluster.DSN(benchDatabase)
	if err != nil {
		return Report{}, err
	}
	pool, err := database.Open(ctx, dsn)
	if err != nil {
		return Report{}, err
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool, database.Settings{Dims: cfg.EmbedDims, Model: benchModel}); err != nil {
		return Report{}, fmt.Errorf("no se pudo migrar la base %s: %w", benchDatabase, err)
	}

	rep, runErr := run(ctx, pool, cfg.PGDataDir(), cfg, opts)
	if runErr != nil {
		rep.Incomplete = runErr.Error()
	}
	if len(rep.Scales) > 0 || runErr == nil {
		if err := writeReport(rep, opts.Out); err != nil {
			// Do not lose what took so long to measure.
			lg.Printf("No se pudo escribir el informe. Resultados:\n\n%s", rep.Markdown())
			return rep, errors.Join(runErr, err)
		}
		lg.Printf("Informe escrito en %s", opts.Out)
	}
	return rep, runErr
}

// writeReport writes the markdown of rep to path, creating its directory.
func writeReport(rep Report, path string) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("no se pudo crear el directorio %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, []byte(rep.Markdown()), 0o644); err != nil {
		return fmt.Errorf("no se pudo escribir el informe %s: %w", path, err)
	}
	return nil
}

// run executes the benchmark on an already migrated database behind pool. pgDataDir is the
// data directory of the cluster, used to find its processes and measure their RAM.
// It empties memories, topics and memory_topics first, so pool must not point at real data.
// On error it returns the report built so far.
func run(ctx context.Context, pool *pgxpool.Pool, pgDataDir string, cfg config.Config, opts Options) (Report, error) {
	opts, err := opts.withDefaults()
	if err != nil {
		return Report{}, err
	}
	lg := &logger{w: opts.Log}
	dims := cfg.EmbedDims

	rep := Report{Date: time.Now(), Machine: readMachine(), Dims: dims, Opts: opts}

	// One dedicated connection keeps the session settings and the temporary table.
	pc, err := pool.Acquire(ctx)
	if err != nil {
		return rep, fmt.Errorf("no se pudo obtener una conexión: %w", err)
	}
	defer pc.Release()
	conn := pc.Conn()

	if err := conn.QueryRow(ctx, "SHOW server_version").Scan(&rep.PGVersion); err != nil {
		return rep, fmt.Errorf("no se pudo leer la versión de PostgreSQL: %w", err)
	}
	if err := conn.QueryRow(ctx, "SELECT extversion FROM pg_extension WHERE extname = 'vector'").Scan(&rep.VectorVersion); err != nil {
		return rep, fmt.Errorf("no se pudo leer la versión de pgvector: %w", err)
	}
	lg.Printf("PostgreSQL %s, pgvector %s, %d dimensiones", rep.PGVersion, rep.VectorVersion, dims)

	// Index definitions must be read before any DROP, and the data emptied afterwards.
	defs, err := loadIndexDefs(ctx, conn, dims)
	if err != nil {
		return rep, err
	}

	var existing int64
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM memories").Scan(&existing); err != nil {
		return rep, fmt.Errorf("no se pudo contar memories: %w", err)
	}
	if existing > 0 {
		lg.Printf("La base ya tenía %d filas: se vacía.", existing)
	}
	if _, err := conn.Exec(ctx, "TRUNCATE memories, topics, memory_topics RESTART IDENTITY CASCADE"); err != nil {
		return rep, fmt.Errorf("no se pudieron vaciar las tablas: %w", err)
	}
	if _, err := conn.Exec(ctx, "CREATE TEMP TABLE IF NOT EXISTS bench_topic_map (memory_id bigint, topic_id int)"); err != nil {
		return rep, fmt.Errorf("no se pudo crear la tabla temporal de temas: %w", err)
	}

	gen := newGenerator(opts.Seed, dims, opts.Clusters, opts.Topics, rep.Date)
	topicIDs, err := loadTopics(ctx, conn, gen, opts.Topics)
	if err != nil {
		return rep, err
	}
	lg.Printf("%d temas cargados", len(topicIDs))

	sqls := newSearchSQL(dims)
	var (
		qs     querySet
		loaded int
		lastID int64
	)
	for _, target := range opts.Rows {
		lg.Printf("== Escala %d filas ==", target)
		sc := Scale{Rows: target}

		if err := dropIndexes(ctx, conn, defs); err != nil {
			return rep, err
		}
		if _, err := conn.Exec(ctx, "SET maintenance_work_mem = '"+buildMaintenanceWorkMem+"'"); err != nil {
			return rep, fmt.Errorf("no se pudo fijar maintenance_work_mem: %w", err)
		}

		// Load.
		toLoad := target - loaded
		lg.Printf("Cargando %d filas con COPY...", toLoad)
		topicsOfRows := make([]int32, 0, toLoad)
		start := time.Now()
		copied, err := loadMemories(ctx, conn, gen, toLoad, &topicsOfRows, lg)
		if err != nil {
			return rep, err
		}
		sc.LoadTime = time.Since(start)
		if copied != int64(toLoad) {
			return rep, fmt.Errorf("el COPY insertó %d filas y se esperaban %d", copied, toLoad)
		}
		lg.Printf("Carga: %.2f s", sc.LoadTime.Seconds())

		start = time.Now()
		lastID, err = fillMemoryTopics(ctx, conn, topicsOfRows, topicIDs, lastID, toLoad)
		if err != nil {
			return rep, err
		}
		lg.Printf("memory_topics rellenada en %.2f s", time.Since(start).Seconds())
		loaded = target

		// Indexes.
		lg.Printf("Construyendo el índice HNSW...")
		start = time.Now()
		if _, err := conn.Exec(ctx, defs.hnswDef); err != nil {
			return rep, fmt.Errorf("no se pudo crear el índice HNSW: %w", err)
		}
		sc.HNSWBuild = time.Since(start)
		lg.Printf("Índice HNSW: %.2f s", sc.HNSWBuild.Seconds())

		lg.Printf("Construyendo el índice GIN...")
		start = time.Now()
		if _, err := conn.Exec(ctx, defs.ginDef); err != nil {
			return rep, fmt.Errorf("no se pudo crear el índice GIN: %w", err)
		}
		sc.GINBuild = time.Since(start)
		lg.Printf("Índice GIN: %.2f s", sc.GINBuild.Seconds())

		start = time.Now()
		if _, err := conn.Exec(ctx, "ANALYZE memories"); err != nil {
			return rep, fmt.Errorf("falló ANALYZE memories: %w", err)
		}
		if _, err := conn.Exec(ctx, "ANALYZE memory_topics"); err != nil {
			return rep, fmt.Errorf("falló ANALYZE memory_topics: %w", err)
		}
		lg.Printf("ANALYZE: %.2f s", time.Since(start).Seconds())

		// Queries: the same for every scale. Exact tokens come from the first scale's rows.
		if qs.vecs == nil {
			exact := slices.Clone(gen.exactTokens)
			qs = newQuerySet(gen, warmup+opts.Queries, topicIDs, exact)
		}

		if _, err := conn.Exec(ctx, fmt.Sprintf("SET hnsw.ef_search = %d", efSearch)); err != nil {
			return rep, fmt.Errorf("no se pudo fijar hnsw.ef_search: %w", err)
		}
		lg.Printf("Midiendo latencias (%d consultas, %d de calentamiento)...", opts.Queries, warmup)
		lat, err := measureLatencies(ctx, conn, sqls, qs, opts.Queries)
		if err != nil {
			return rep, err
		}
		sc.Q1, sc.Q2, sc.Q3, sc.Q4, sc.Total = lat.Q1, lat.Q2, lat.Q3, lat.Q4, lat.Total
		lg.Printf("Total p50 / p95: %s / %s ms", ms(sc.Total.P50), ms(sc.Total.P95))

		lg.Printf("Midiendo recall@10 (%d consultas)...", opts.TruthQueries)
		sc.Recall, err = measureRecall(ctx, conn, sqls, qs, opts.TruthQueries)
		if err != nil {
			return rep, err
		}
		lg.Printf("recall@10 = %.3f", sc.Recall)

		sz, err := measureSizes(ctx, conn, defs)
		if err != nil {
			return rep, err
		}
		sc.TableBytes, sc.HNSWBytes, sc.GINBytes, sc.TotalBytes = sz.Table, sz.HNSW, sz.GIN, sz.Total

		if rss, err := postgresRSS(pgDataDir); err != nil {
			sc.RAMReason = err.Error()
			lg.Printf("RAM no disponible: %v", err)
		} else {
			sc.RAMBytes, sc.RAMAvailable = rss, true
		}

		rep.Scales = append(rep.Scales, sc)
	}

	rep.Embed = measureEmbedding(ctx, cfg, opts.EmbedQueries, lg)
	return rep, nil
}

// indexDefs are the definitions used to recreate the two indexes dropped around each load.
type indexDefs struct {
	hnswName, hnswDef string
	ginName, ginDef   string
}

// loadIndexDefs reads the current definitions of the HNSW and GIN(tsv) indexes of memories from
// pg_indexes, so they are recreated exactly as the migration made them. If an index is missing
// (a previous run was interrupted), it falls back to the definition in the migration.
func loadIndexDefs(ctx context.Context, conn *pgx.Conn, dims int) (indexDefs, error) {
	defs := indexDefs{
		hnswName: "memories_bq_hnsw",
		hnswDef: fmt.Sprintf("CREATE INDEX memories_bq_hnsw ON memories USING hnsw "+
			"((binary_quantize(embedding)::bit(%d)) bit_hamming_ops) WHERE status = 'active'", dims),
		ginName: "memories_tsv_idx",
		ginDef:  "CREATE INDEX memories_tsv_idx ON memories USING gin (tsv)",
	}

	err := conn.QueryRow(ctx, `SELECT indexdef FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = 'memories' AND indexname = $1`,
		defs.hnswName).Scan(&defs.hnswDef)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return defs, fmt.Errorf("no se pudo leer la definición del índice HNSW: %w", err)
	}

	err = conn.QueryRow(ctx, `SELECT indexname, indexdef FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = 'memories' AND indexdef ILIKE '%USING gin (tsv)%'
		ORDER BY indexname LIMIT 1`).Scan(&defs.ginName, &defs.ginDef)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return defs, fmt.Errorf("no se pudo leer la definición del índice GIN: %w", err)
	}
	return defs, nil
}

// dropIndexes drops the HNSW and GIN indexes if they exist.
func dropIndexes(ctx context.Context, conn *pgx.Conn, defs indexDefs) error {
	for _, name := range []string{defs.hnswName, defs.ginName} {
		if _, err := conn.Exec(ctx, "DROP INDEX IF EXISTS "+pgx.Identifier{name}.Sanitize()); err != nil {
			return fmt.Errorf("no se pudo eliminar el índice %s: %w", name, err)
		}
	}
	return nil
}

// loadTopics inserts the synthetic topics with COPY and returns their database ids, in
// generation order.
func loadTopics(ctx context.Context, conn *pgx.Conn, gen *generator, n int) ([]int64, error) {
	slugs, vecs := gen.topicData(n)
	_, err := copyIn(ctx, conn, "COPY topics (slug, embedding) FROM STDIN", func(w *bufio.Writer) error {
		var buf []byte
		for i := range slugs {
			buf = buf[:0]
			buf = appendCopyText(buf, slugs[i])
			buf = append(buf, '\t')
			buf = appendVector(buf, vecs[i])
			buf = append(buf, '\n')
			if _, err := w.Write(buf); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("no se pudieron cargar los temas: %w", err)
	}

	rows, err := conn.Query(ctx, "SELECT id FROM topics ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("no se pudieron leer los temas: %w", err)
	}
	defer rows.Close()
	ids := make([]int64, 0, n)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) != n {
		return nil, fmt.Errorf("se esperaban %d temas y hay %d", n, len(ids))
	}
	return ids, nil
}

// loadMemories generates count rows and loads them with COPY (text format). The topic index of
// every row is appended to *topics, in load order.
func loadMemories(ctx context.Context, conn *pgx.Conn, gen *generator, count int, topics *[]int32, lg *logger) (int64, error) {
	const copySQL = "COPY memories (kind, title, content, content_hash, occurred_at, embedding) FROM STDIN"
	return copyIn(ctx, conn, copySQL, func(w *bufio.Writer) error {
		var buf []byte
		for i := 0; i < count; i++ {
			if i%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			r := gen.nextRow()
			*topics = append(*topics, int32(r.topic))

			buf = buf[:0]
			buf = append(buf, r.kind...)
			buf = append(buf, '\t')
			buf = appendCopyText(buf, r.title)
			buf = append(buf, '\t')
			buf = appendCopyText(buf, r.content)
			buf = append(buf, '\t')
			buf = appendBytea(buf, r.hash[:])
			buf = append(buf, '\t')
			buf = appendTimestamp(buf, r.occurredAt)
			buf = append(buf, '\t')
			buf = appendVector(buf, r.vec)
			buf = append(buf, '\n')
			if _, err := w.Write(buf); err != nil {
				return err
			}
			if (i+1)%progressEvery == 0 {
				lg.Printf("  %d / %d filas generadas", i+1, count)
			}
		}
		return nil
	})
}

// fillMemoryTopics links the count memories loaded last (ids above prevMaxID) to their topics.
// It writes the mapping (memory id, topic id) into the temporary table bench_topic_map and
// inserts into memory_topics, taking kind, active and ts from memories. It relies on the new
// rows having consecutive ids in load order, which it checks. It returns the new highest id.
func fillMemoryTopics(ctx context.Context, conn *pgx.Conn, topicsOfRows []int32, topicIDs []int64, prevMaxID int64, count int) (int64, error) {
	var minID, maxID, n int64
	err := conn.QueryRow(ctx,
		"SELECT coalesce(min(id), 0), coalesce(max(id), 0), count(*) FROM memories WHERE id > $1",
		prevMaxID).Scan(&minID, &maxID, &n)
	if err != nil {
		return 0, fmt.Errorf("no se pudo leer el rango de ids cargados: %w", err)
	}
	if n != int64(count) || maxID-minID+1 != n {
		return 0, fmt.Errorf("los ids cargados no son consecutivos (mín %d, máx %d, %d filas, se esperaban %d)",
			minID, maxID, n, count)
	}

	if _, err := conn.Exec(ctx, "TRUNCATE bench_topic_map"); err != nil {
		return 0, fmt.Errorf("no se pudo vaciar bench_topic_map: %w", err)
	}
	_, err = copyIn(ctx, conn, "COPY bench_topic_map (memory_id, topic_id) FROM STDIN", func(w *bufio.Writer) error {
		var buf []byte
		for i, t := range topicsOfRows {
			buf = buf[:0]
			buf = strconv.AppendInt(buf, minID+int64(i), 10)
			buf = append(buf, '\t')
			buf = strconv.AppendInt(buf, topicIDs[t], 10)
			buf = append(buf, '\n')
			if _, err := w.Write(buf); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("no se pudo cargar bench_topic_map: %w", err)
	}

	_, err = conn.Exec(ctx, `INSERT INTO memory_topics (topic_id, memory_id, kind, active, ts)
		SELECT b.topic_id, m.id, m.kind, m.status = 'active', m.occurred_at
		FROM bench_topic_map b JOIN memories m ON m.id = b.memory_id`)
	if err != nil {
		return 0, fmt.Errorf("no se pudo rellenar memory_topics: %w", err)
	}
	return maxID, nil
}

// measureEmbedding measures the latency of real query embeddings against Ollama. It never
// fails the benchmark: if Ollama does not answer, it reports why.
func measureEmbedding(ctx context.Context, cfg config.Config, n int, lg *logger) EmbedResult {
	res := EmbedResult{Model: cfg.EmbedModel}
	if n == 0 {
		res.Reason = "no se midió (EmbedQueries = 0)"
		return res
	}
	oll, err := embedding.NewOllama(embedding.Options{
		BaseURL:   cfg.OllamaURL,
		Model:     cfg.EmbedModel,
		Dims:      cfg.EmbedDims,
		KeepAlive: cfg.EmbedKeepAlive,
	})
	if err != nil {
		res.Reason = err.Error()
		return res
	}
	if err := oll.Ping(ctx); err != nil {
		res.Reason = fmt.Sprintf("Ollama no responde en %s: %v", cfg.OllamaURL, err)
		lg.Printf("Embedding real: %s", res.Reason)
		return res
	}

	if n > len(embedPhrases) {
		lg.Printf("Solo hay %d frases distintas: se miden %d embeddings en lugar de %d.", len(embedPhrases), len(embedPhrases), n)
		n = len(embedPhrases)
	}
	lg.Printf("Midiendo %d embeddings reales con %s...", n, cfg.EmbedModel)
	for i := 0; i < embedWarmup; i++ {
		if _, err := oll.EmbedQuery(ctx, embedPhrases[i%len(embedPhrases)]); err != nil {
			res.Reason = fmt.Sprintf("falló el calentamiento del embedding: %v", err)
			return res
		}
	}
	samples := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		t := time.Now()
		if _, err := oll.EmbedQuery(ctx, embedPhrases[i]); err != nil {
			res.Reason = fmt.Sprintf("falló EmbedQuery: %v", err)
			return res
		}
		samples = append(samples, time.Since(t))
	}
	l := summarize(samples)
	res.Available, res.Queries, res.P50, res.P95 = true, n, l.P50, l.P95
	return res
}
