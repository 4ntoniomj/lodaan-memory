package benchmark

import (
	"context"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"lodan/internal/database"
	"lodan/internal/memory"
	"lodan/internal/recall"
)

// fixedNow is the reference instant of the generator in tests, so they do not depend on the clock.
var fixedNow = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

// take generates n rows, cloning the reused vector buffer.
func take(g *generator, n int) []row {
	rows := make([]row, n)
	for i := range rows {
		rows[i] = g.nextRow()
		rows[i].vec = slices.Clone(rows[i].vec)
	}
	return rows
}

func TestGeneradorDeterminista(t *testing.T) {
	a := take(newGenerator(7, 16, 5, 10, fixedNow), 200)
	b := take(newGenerator(7, 16, 5, 10, fixedNow), 200)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("la misma semilla debe producir exactamente las mismas filas")
	}

	c := take(newGenerator(8, 16, 5, 10, fixedNow), 200)
	if reflect.DeepEqual(a, c) {
		t.Fatal("semillas distintas deben producir filas distintas")
	}
}

func TestGeneradorFilas(t *testing.T) {
	const dims, topics = 16, 10
	g := newGenerator(3, dims, 5, topics, fixedNow)
	tokenRe := regexp.MustCompile(`\b\d{4}-[A-Z]{3}\b`)

	withToken := 0
	for _, r := range take(g, 3000) {
		titleWords := len(strings.Fields(r.title))
		if titleWords < 4 || titleWords > 8 {
			t.Fatalf("el título tiene %d palabras, se esperaban 4-8: %q", titleWords, r.title)
		}
		contentWords := len(strings.Fields(r.content))
		if contentWords < 15 || contentWords > 61 { // 60 palabras + el token exacto
			t.Fatalf("el contenido tiene %d palabras, se esperaban 15-60 (más el token): %q", contentWords, r.content)
		}
		if !slices.Contains(memoryKinds, r.kind) {
			t.Fatalf("tipo inesperado %q", r.kind)
		}
		if r.topic < 0 || r.topic >= topics {
			t.Fatalf("tema fuera de rango: %d", r.topic)
		}
		if r.occurredAt.After(fixedNow) || r.occurredAt.Before(fixedNow.AddDate(-historyYears, 0, -1)) {
			t.Fatalf("occurred_at fuera de los últimos %d años: %v", historyYears, r.occurredAt)
		}
		if len(r.vec) != dims {
			t.Fatalf("el vector tiene %d dimensiones, se esperaban %d", len(r.vec), dims)
		}
		var norm float64
		for _, x := range r.vec {
			norm += float64(x) * float64(x)
		}
		if math.Abs(norm-1) > 1e-4 {
			t.Fatalf("el vector no está normalizado: |v|² = %v", norm)
		}
		if tokenRe.MatchString(r.content) {
			withToken++
		}
	}
	// Se esperan unos 30 (1 % de 3000).
	if withToken < 10 || withToken > 70 {
		t.Errorf("filas con token exacto = %d, se esperaban unas 30", withToken)
	}
	if len(g.exactTokens) == 0 {
		t.Error("el generador no ha recordado ningún token exacto")
	}
}

func TestVocabulario(t *testing.T) {
	if len(vocabulary) < 250 {
		t.Errorf("el vocabulario tiene %d palabras, se esperaban al menos 250", len(vocabulary))
	}
	seen := map[string]bool{}
	for _, w := range vocabulary {
		if seen[w] {
			t.Errorf("palabra repetida en el vocabulario: %q", w)
		}
		seen[w] = true
	}
	phrases := map[string]bool{}
	for _, p := range embedPhrases {
		if phrases[p] {
			t.Errorf("frase repetida: %q", p)
		}
		phrases[p] = true
	}
}

func TestConjuntoDeConsultasDeterminista(t *testing.T) {
	g := newGenerator(5, 16, 5, 10, fixedNow)
	ids := []int64{11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	exact := []string{"1234-ABC", "9999-ZZZ"}

	a := newQuerySet(g, 100, ids, exact)
	b := newQuerySet(g, 100, ids, exact)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("el conjunto de consultas debe ser determinista")
	}
	nExact := 0
	for _, s := range a.texts {
		if slices.Contains(exact, s) {
			nExact++
		}
		if slices.Contains(exact, s) {
			continue
		}
		f := strings.Fields(s)
		if len(f) != 2 || f[0] == f[1] {
			t.Fatalf("la consulta de texto debe tener 2 palabras distintas: %q", s)
		}
	}
	if nExact == 0 || nExact > 30 {
		t.Errorf("consultas de token exacto = %d de 100, se esperaba alrededor de 10", nExact)
	}
	for _, id := range a.topics {
		if !slices.Contains(ids, id) {
			t.Fatalf("tema de consulta desconocido: %d", id)
		}
	}
}

func TestEscapeCopyTexto(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"hola mundo", "hola mundo"},
		{"a\tb", `a\tb`},
		{"a\nb", `a\nb`},
		{"a\rb", `a\rb`},
		{`a\b`, `a\\b`},
		{"\\\t\n", `\\\t\n`},
		{`\n`, `\\n`}, // barra invertida seguida de n literal: no es un salto de línea
		{"ñandú — ✓", "ñandú — ✓"},
	}
	for _, tt := range tests {
		if got := string(appendCopyText(nil, tt.in)); got != tt.want {
			t.Errorf("appendCopyText(%q) = %q, se esperaba %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatosDeCopy(t *testing.T) {
	if got := string(appendVector(nil, []float32{0.5, -1.25, 0})); got != "[0.5,-1.25,0]" {
		t.Errorf("appendVector = %q", got)
	}
	if got := string(appendBytea(nil, []byte{0x00, 0xab, 0xff})); got != `\\x00abff` {
		t.Errorf("appendBytea = %q", got)
	}
	if got := string(appendTimestamp(nil, time.Date(2024, 3, 5, 7, 8, 9, 0, time.UTC))); got != "2024-03-05 07:08:09+00" {
		t.Errorf("appendTimestamp = %q", got)
	}
}

func TestPercentiles(t *testing.T) {
	samples := make([]time.Duration, 100)
	for i := range samples {
		samples[100-1-i] = time.Duration(i+1) * time.Millisecond // desordenadas
	}
	l := summarize(samples)
	if l.P50 != 50*time.Millisecond || l.P95 != 95*time.Millisecond || l.P99 != 99*time.Millisecond {
		t.Errorf("percentiles = %v, se esperaba 50/95/99 ms", l)
	}
	if got := summarize(nil); got != (Latency{}) {
		t.Errorf("summarize(nil) = %v, se esperaba el valor cero", got)
	}
}

func TestOpcionesPorDefecto(t *testing.T) {
	o, err := Options{}.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(o.Rows, []int{10_000, 100_000, 1_000_000}) || o.Queries != 500 || o.TruthQueries != 100 ||
		o.Clusters != 2000 || o.Topics != 1000 || o.Out != DefaultOut || o.Log == nil ||
		!slices.Equal(o.Candidates, []int{100}) || o.Reuse {
		t.Errorf("valores por defecto inesperados: %+v", o)
	}

	o, err = Options{Rows: []int{1000, 500, 1000}, Candidates: []int{50, 20, 50}, EmbedQueries: -1}.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(o.Rows, []int{500, 1000}) || o.EmbedQueries != 30 {
		t.Errorf("las filas deben quedar ordenadas y sin repetir, y EmbedQueries = 30: %+v", o)
	}
	if !slices.Equal(o.Candidates, []int{20, 50}) {
		t.Errorf("los candidatos deben quedar ordenados y sin repetir: %v", o.Candidates)
	}

	if _, err := (Options{Rows: []int{0}}).withDefaults(); err == nil {
		t.Error("0 filas debería ser un error")
	}
	if _, err := (Options{Candidates: []int{20, 0}}).withDefaults(); err == nil {
		t.Error("0 candidatos debería ser un error")
	}
}

func TestSumSamples(t *testing.T) {
	a := []time.Duration{1, 2, 3}
	b := []time.Duration{10, 20, 30, 40}
	if got := sumSamples(a, b); !slices.Equal(got, []time.Duration{11, 22, 33}) {
		t.Errorf("sumSamples = %v, se esperaba [11 22 33]", got)
	}
	if got := sumSamples(); got != nil {
		t.Errorf("sumSamples() = %v, se esperaba nil", got)
	}
}

func TestConsultasSonLasDeProduccion(t *testing.T) {
	// El benchmark no puede tener SQL propio para Q1-Q4: debe usar exactamente el de producción.
	s := newSearchSQL(64)
	for name, pair := range map[string][2]string{
		"Q1":  {s.candidates, memory.CandidatesSQL(64)},
		"Q2":  {s.rerank, memory.RerankSQL(64)},
		"Q3":  {s.text, recall.TextCandidatesSQL(false)},
		"Q4a": {s.profile, recall.ProfileSQL},
		"Q4b": {s.events, recall.EventsSQL},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s no usa el SQL de producción:\n%s\n%s", name, pair[0], pair[1])
		}
	}
}

func TestReportMarkdownVacio(t *testing.T) {
	// No debe entrar en pánico con un informe sin escalas.
	md := Report{}.Markdown()
	if !strings.Contains(md, "## Notas") || !strings.Contains(md, "no disponible") {
		t.Errorf("markdown inesperado:\n%s", md)
	}
}

func TestRunPequeno(t *testing.T) {
	tc := database.NewTestCluster(t, 64)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	opts := Options{
		Rows:         []int{500, 1000},
		Candidates:   []int{20, 50},
		Queries:      20,
		TruthQueries: 5,
		EmbedQueries: 0,
		Clusters:     20,
		Topics:       10,
		Seed:         1,
	}
	rep, err := run(ctx, tc.Pool, tc.Cfg.PGDataDir(), tc.Cfg, opts)
	if err != nil {
		t.Fatalf("run falló: %v", err)
	}

	if len(rep.Scales) != 2 {
		t.Fatalf("escalas = %d, se esperaban 2", len(rep.Scales))
	}
	md := rep.Markdown()
	for i, rows := range []int{500, 1000} {
		s := rep.Scales[i]
		if s.Rows != rows {
			t.Errorf("escala %d: filas = %d, se esperaban %d", i, s.Rows, rows)
		}
		if !strings.Contains(md, "| "+strconv.Itoa(rows)+" |") {
			t.Errorf("el markdown no contiene la fila de la escala %d:\n%s", rows, md)
		}
		if s.Q3.P50 <= 0 || s.Q4.P50 <= 0 || s.HNSWBytes <= 0 || s.GINBytes <= 0 || s.TableBytes <= 0 {
			t.Errorf("mediciones vacías en la escala %d: %+v", rows, s)
		}
		if len(s.Candidates) != 2 || s.Candidates[0].Candidates != 20 || s.Candidates[1].Candidates != 50 {
			t.Fatalf("escala %d: candidatos medidos = %+v, se esperaban 20 y 50", rows, s.Candidates)
		}
		for _, c := range s.Candidates {
			if c.Recall < 0 || c.Recall > 1 || math.IsNaN(c.Recall) {
				t.Errorf("recall@10 de %d filas y %d candidatos = %v, se esperaba un valor entre 0 y 1", rows, c.Candidates, c.Recall)
			}
			if c.Q1.P50 <= 0 || c.Q2.P50 <= 0 || c.Total.P50 <= 0 {
				t.Errorf("mediciones vacías en la escala %d con %d candidatos: %+v", rows, c.Candidates, c)
			}
			if want := memory.EfSearch(c.Candidates); c.EfSearch != want {
				t.Errorf("hnsw.ef_search = %d con %d candidatos, se esperaba %d", c.EfSearch, c.Candidates, want)
			}
			// Una fila por escala y número de candidatos en las tablas de recall y de latencia.
			if want := "| " + strconv.Itoa(rows) + " | " + strconv.Itoa(c.Candidates) + " | "; strings.Count(md, want) < 3 {
				t.Errorf("el markdown no tiene la fila «%s» en las tablas de recall, latencia p50/p95 y p99:\n%s", want, md)
			}
		}
		if s.LoadTime <= 0 || s.HNSWBuild <= 0 || s.GINBuild <= 0 {
			t.Errorf("tiempos vacíos en la escala %d: %+v", rows, s)
		}
	}
	if rep.PGVersion == "" || rep.VectorVersion == "" {
		t.Errorf("faltan las versiones: postgres=%q pgvector=%q", rep.PGVersion, rep.VectorVersion)
	}
	if rep.Embed.Available {
		t.Error("con EmbedQueries = 0 no debería medirse el embedding")
	}

	// La base queda con las filas de la última escala y memory_topics coherente.
	var memories, links, topics int
	if err := tc.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM memories), (SELECT count(*) FROM memory_topics), (SELECT count(*) FROM topics)").
		Scan(&memories, &links, &topics); err != nil {
		t.Fatal(err)
	}
	if memories != 1000 || links != 1000 || topics != 10 {
		t.Errorf("memories=%d memory_topics=%d topics=%d, se esperaba 1000/1000/10", memories, links, topics)
	}

	// Los índices parciales de la ficha (migración 0002) existen, y el antiguo ya no.
	for name, want := range map[string]bool{
		profileIndex: true, eventsIndex: true, "memory_topics_topic_id_kind_ts_idx": false,
	} {
		var exists bool
		if err := tc.Pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != want {
			t.Errorf("índice %s: existe = %v, se esperaba %v", name, exists, want)
		}
	}
	if !strings.Contains(md, profileIndex) || !strings.Contains(md, eventsIndex) {
		t.Errorf("el markdown no informa del uso de los índices de la ficha:\n%s", md)
	}

	// Con Reuse no se carga nada: se miden solo las 1000 filas que ya hay, y los datos siguen ahí.
	var maxIDBefore int64
	if err := tc.Pool.QueryRow(ctx, "SELECT max(id) FROM memories").Scan(&maxIDBefore); err != nil {
		t.Fatal(err)
	}
	reuseOpts := opts
	reuseOpts.Reuse = true
	repReuse, err := run(ctx, tc.Pool, tc.Cfg.PGDataDir(), tc.Cfg, reuseOpts)
	if err != nil {
		t.Fatalf("la ejecución con Reuse falló: %v", err)
	}
	if !repReuse.Reused || len(repReuse.Scales) != 1 || repReuse.Scales[0].Rows != 1000 {
		t.Fatalf("con Reuse se esperaba una sola escala de 1000 filas reutilizada: reused=%v escalas=%+v",
			repReuse.Reused, repReuse.Scales)
	}
	sr := repReuse.Scales[0]
	if sr.LoadTime != 0 || sr.HNSWBuild != 0 || sr.GINBuild != 0 {
		t.Errorf("con Reuse los tiempos de carga e índice deben quedar vacíos: %+v", sr)
	}
	if len(sr.Candidates) != 2 || sr.Q3.P50 <= 0 || sr.HNSWBytes <= 0 {
		t.Errorf("con Reuse se esperaban las mediciones de 2 candidatos: %+v", sr)
	}
	if !strings.Contains(repReuse.Markdown(), "datos reutilizados") {
		t.Errorf("el markdown debería indicar «datos reutilizados»:\n%s", repReuse.Markdown())
	}
	var maxIDAfter int64
	if err := tc.Pool.QueryRow(ctx, "SELECT max(id) FROM memories").Scan(&maxIDAfter); err != nil {
		t.Fatal(err)
	}
	if maxIDAfter != maxIDBefore {
		t.Errorf("Reuse no debe recargar los datos: max(id) pasó de %d a %d", maxIDBefore, maxIDAfter)
	}

	// Con Reuse y un número de filas que no coincide, error claro y sin tocar los datos.
	badOpts := reuseOpts
	badOpts.Rows = []int{500, 2000}
	if _, err := run(ctx, tc.Pool, tc.Cfg.PGDataDir(), tc.Cfg, badOpts); err == nil ||
		!strings.Contains(err.Error(), "--reuse") || !strings.Contains(err.Error(), "2000") {
		t.Errorf("Reuse con otro número de filas debería fallar con un error claro: %v", err)
	}
	if err := tc.Pool.QueryRow(ctx, "SELECT count(*) FROM memories").Scan(&memories); err != nil || memories != 1000 {
		t.Errorf("tras el error de Reuse memories = %d (error %v), se esperaban 1000 filas intactas", memories, err)
	}

	// Una segunda ejecución vacía los datos anteriores y da el mismo resultado.
	opts.Rows = []int{500}
	rep2, err := run(ctx, tc.Pool, tc.Cfg.PGDataDir(), tc.Cfg, opts)
	if err != nil {
		t.Fatalf("la segunda ejecución falló: %v", err)
	}
	if err := tc.Pool.QueryRow(ctx, "SELECT count(*) FROM memories").Scan(&memories); err != nil {
		t.Fatal(err)
	}
	if memories != 500 || len(rep2.Scales) != 1 {
		t.Errorf("tras la segunda ejecución: %d filas y %d escalas, se esperaban 500 y 1", memories, len(rep2.Scales))
	}
}
