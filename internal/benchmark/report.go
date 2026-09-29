package benchmark

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Machine describes the hardware and OS the benchmark ran on.
type Machine struct {
	CPU        string // empty if unknown
	Cores      int
	RAMTotalMB int64 // 0 if unknown
	OS, Arch   string
}

// Scale holds the results measured at one table size.
type Scale struct {
	Rows      int
	LoadTime  time.Duration
	HNSWBuild time.Duration
	GINBuild  time.Duration

	Q1, Q2, Q3, Q4, Total Latency

	// Recall is the mean recall@10 of Q1 + Q2 against the exact search, in [0, 1].
	Recall float64

	TableBytes, HNSWBytes, GINBytes, TotalBytes int64

	// RAMBytes is the summed RSS of the PostgreSQL processes; meaningful only if RAMAvailable.
	RAMBytes     int64
	RAMAvailable bool
	// RAMReason explains why RAM is not available.
	RAMReason string
}

// EmbedResult is the latency of a real query embedding against Ollama.
type EmbedResult struct {
	Available bool
	// Reason explains why the measurement is not available.
	Reason  string
	Model   string
	Queries int
	P50     time.Duration
	P95     time.Duration
}

// Report is the outcome of a benchmark run.
type Report struct {
	Date          time.Time
	Machine       Machine
	PGVersion     string
	VectorVersion string
	Dims          int

	Opts   Options
	Scales []Scale
	Embed  EmbedResult

	// Incomplete is non-empty when the run stopped early; it holds the reason.
	Incomplete string
}

// pgvector defaults for hnsw indexes (the migration does not override them).
const (
	defaultHNSWM              = 16
	defaultHNSWEfConstruction = 64
	buildMaintenanceWorkMem   = "1GB"
)

// readMachine collects CPU, cores, total RAM and OS. CPU and RAM are only read on Linux.
func readMachine() Machine {
	m := Machine{Cores: runtime.NumCPU(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	if runtime.GOOS != "linux" {
		return m
	}
	if f, err := os.Open("/proc/cpuinfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			key, val, ok := strings.Cut(sc.Text(), ":")
			if ok && strings.TrimSpace(key) == "model name" {
				m.CPU = strings.TrimSpace(val)
				break
			}
		}
		f.Close()
	}
	if f, err := os.Open("/proc/meminfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			key, val, ok := strings.Cut(sc.Text(), ":")
			if ok && key == "MemTotal" {
				if fields := strings.Fields(val); len(fields) > 0 {
					if kb, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
						m.RAMTotalMB = kb / 1024
					}
				}
				break
			}
		}
		f.Close()
	}
	return m
}

// ms formats a duration in milliseconds with one decimal.
func ms(d time.Duration) string {
	return fmt.Sprintf("%.1f", float64(d)/float64(time.Millisecond))
}

// p50p95 formats "p50 / p95" of a latency in milliseconds.
func p50p95(l Latency) string { return ms(l.P50) + " / " + ms(l.P95) }

// secs formats a duration in seconds with two decimals.
func secs(d time.Duration) string { return fmt.Sprintf("%.2f", d.Seconds()) }

// mb formats bytes as MB (MiB) with one decimal.
func mb(b int64) string { return fmt.Sprintf("%.1f", float64(b)/(1<<20)) }

// Markdown renders the report.
func (r Report) Markdown() string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	w("# Benchmark de lodan\n\n")
	w("Generado por `lodan bench` el %s.\n\n", r.Date.Format("2006-01-02 15:04:05 MST"))
	if r.Incomplete != "" {
		w("> **Informe incompleto:** la ejecución se detuvo antes de terminar: %s\n\n", r.Incomplete)
	}

	w("## Entorno\n\n")
	cpu := r.Machine.CPU
	if cpu == "" {
		cpu = "no disponible"
	}
	w("- CPU: %s\n", cpu)
	w("- Núcleos: %d\n", r.Machine.Cores)
	if r.Machine.RAMTotalMB > 0 {
		w("- RAM total: %d MB\n", r.Machine.RAMTotalMB)
	} else {
		w("- RAM total: no disponible\n")
	}
	w("- Sistema: %s/%s\n", r.Machine.OS, r.Machine.Arch)
	w("- PostgreSQL: %s\n", r.PGVersion)
	w("- pgvector: %s\n\n", r.VectorVersion)

	w("## Parámetros\n\n")
	w("- Dimensiones de los vectores: %d (`halfvec`)\n", r.Dims)
	w("- `hnsw.ef_search` = %d\n", efSearch)
	w("- HNSW: `m` = %d y `ef_construction` = %d (valores por defecto de pgvector; la migración no los fija)\n",
		defaultHNSWM, defaultHNSWEfConstruction)
	w("- `maintenance_work_mem` = %s al construir los índices\n", buildMaintenanceWorkMem)
	rows := make([]string, len(r.Opts.Rows))
	for i, n := range r.Opts.Rows {
		rows[i] = strconv.Itoa(n)
	}
	w("- Escalas (filas): %s\n", strings.Join(rows, ", "))
	w("- Consultas medidas por escala: %d, tras %d de calentamiento\n", r.Opts.Queries, warmup)
	w("- Consultas para el recall@10: %d\n", r.Opts.TruthQueries)
	w("- Clústeres de vectores: %d; temas: %d; semilla: %d\n\n", r.Opts.Clusters, r.Opts.Topics, r.Opts.Seed)

	w("## Resultados por escala\n\n")
	w("### Construcción, recall, tamaños y RAM\n\n")
	w("| Filas | Carga (s) | Índice HNSW (s) | Índice GIN (s) | recall@10 | Tabla (MB) | HNSW (MB) | GIN (MB) | Total con índices (MB) | RAM postgres (MB) |\n")
	w("|---|---|---|---|---|---|---|---|---|---|\n")
	for _, s := range r.Scales {
		ram := "no disponible"
		if s.RAMAvailable {
			ram = mb(s.RAMBytes)
		}
		w("| %d | %s | %s | %s | %.3f | %s | %s | %s | %s | %s |\n",
			s.Rows, secs(s.LoadTime), secs(s.HNSWBuild), secs(s.GINBuild), s.Recall,
			mb(s.TableBytes), mb(s.HNSWBytes), mb(s.GINBytes), mb(s.TotalBytes), ram)
	}
	w("\n")

	w("### Latencia p50 / p95 (ms)\n\n")
	w("| Filas | Q1 candidatos | Q2 reordenación | Q3 texto | Q4 ficha de tema | Total |\n")
	w("|---|---|---|---|---|---|\n")
	for _, s := range r.Scales {
		w("| %d | %s | %s | %s | %s | %s |\n", s.Rows,
			p50p95(s.Q1), p50p95(s.Q2), p50p95(s.Q3), p50p95(s.Q4), p50p95(s.Total))
	}
	w("\n")

	w("### Latencia p99 (ms)\n\n")
	w("| Filas | Q1 candidatos | Q2 reordenación | Q3 texto | Q4 ficha de tema | Total |\n")
	w("|---|---|---|---|---|---|\n")
	for _, s := range r.Scales {
		w("| %d | %s | %s | %s | %s | %s |\n", s.Rows,
			ms(s.Q1.P99), ms(s.Q2.P99), ms(s.Q3.P99), ms(s.Q4.P99), ms(s.Total.P99))
	}
	w("\n")

	w("## Embedding real de una consulta\n\n")
	if r.Embed.Available {
		w("| Modelo | Consultas | p50 (ms) | p95 (ms) |\n|---|---|---|---|\n")
		w("| %s | %d | %s | %s |\n\n", r.Embed.Model, r.Embed.Queries, ms(r.Embed.P50), ms(r.Embed.P95))
	} else {
		reason := r.Embed.Reason
		if reason == "" {
			reason = "sin motivo indicado"
		}
		w("no disponible: %s\n\n", reason)
	}

	w("## Notas\n\n")
	w("- **Datos sintéticos por clústeres.** Los vectores se generan alrededor de centroides aleatorios "+
		"normalizados, con ruido gaussiano (σ = %.3f por componente sobre el vector sin normalizar) y se "+
		"normalizan después. Las consultas de prueba se generan igual, con centroides y ruido nuevos.\n", noiseSigma)
	if r.Dims > 0 {
		noiseNorm := noiseSigma * math.Sqrt(float64(r.Dims))
		w("- **Estructura de los clústeres.** El centroide tiene norma 1 y la norma del ruido es ≈ σ·√D = "+
			"%.3f·√%d ≈ %.2f, así que el coseno esperado entre una fila y su centroide es 1/√(1 + %.2f²) ≈ %.2f. "+
			"Se eligió σ para acercarse a la estructura de embeddings reales; con un σ mayor el ruido domina "+
			"y los datos apenas forman clústeres.\n",
			noiseSigma, r.Dims, noiseNorm, noiseNorm, 1/math.Sqrt(1+noiseNorm*noiseNorm))
	}
	w("- **El recall sobre datos reales puede diferir.** Los embeddings reales tienen otra estructura que estos " +
		"clústeres artificiales, así que el recall@10 medido aquí es una referencia y no una garantía. Conviene " +
		"repetirlo con datos y un modelo reales.\n")
	w("- **Recall@10.** Media de la intersección entre el top 10 de Q1 + Q2 (índice binario y reordenación con " +
		"el vector completo) y el top 10 de la búsqueda exacta, dividida entre 10.\n")
	w("- **Latencias.** Cada consulta se mide con el reloj del cliente, incluida la ida y vuelta local con " +
		"PostgreSQL. Q1 + Q2 + Q3 + Q4 se ejecutan en secuencia en cada repetición, y «Total» es el tiempo de la " +
		"secuencia completa. Q4 son dos consultas: hasta 8 datos estables y hasta 5 eventos.\n")
	w("- **Texto.** Los textos salen de un vocabulario español de unas %d palabras, así que cada palabra aparece "+
		"en una fracción grande de las filas. Las consultas de vocabulario de Q3 son siempre de 2 palabras "+
		"distintas, que `websearch_to_tsquery` combina con AND, de modo que cada consulta encuentra solo las "+
		"filas que contienen ambas. El 10 %% de las consultas de texto buscan en cambio un token exacto tipo "+
		"matrícula (`1234-ABC`), presente en el 1 %% de las filas.\n",
		len(vocabulary))
	w("- **Carga por escalas.** Las escalas crecen de forma incremental: se añaden filas hasta llegar a cada " +
		"tamaño. Antes de cada carga se eliminan el índice HNSW y el GIN de `tsv`, y se recrean después con " +
		"la misma definición de la migración.\n")
	w("- **RAM.** Suma del RSS de los procesos de PostgreSQL del clúster (solo en Linux). Como cuenta la memoria " +
		"compartida una vez por cada proceso que la ha tocado, sobrestima el uso real.\n")
	w("- **Base de datos.** Todo se ejecuta en la base `lodan_bench`, separada de la de datos reales.\n")

	return b.String()
}
