package recall

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"lodan/internal/database"
	"lodan/internal/embedding"
	"lodan/internal/memory"
	"lodan/internal/session"
	"lodan/internal/topic"
)

const (
	testDims     = 64
	testTimeout  = 90 * time.Second
	testIdle     = 30 * time.Minute
	testDup      = 0.95
	testTopicSim = 0.72
	// testDetectSim es el umbral de detección por defecto de la configuración.
	testDetectSim = 0.40
	// highThreshold evita detectar temas por casualidad con el embedder falso.
	highThreshold = 0.95
	// lowThreshold permite detectarlos con consultas de pocas palabras.
	lowThreshold = 0.3
)

// unavailableEmbedder simulates Ollama being down.
type unavailableEmbedder struct{ dims int }

func (e unavailableEmbedder) EmbedDocuments(context.Context, []embedding.Document) ([][]float32, error) {
	return nil, fmt.Errorf("prueba: %w", embedding.ErrUnavailable)
}

func (e unavailableEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	return nil, fmt.Errorf("prueba: %w", embedding.ErrUnavailable)
}

func (e unavailableEmbedder) Model() string { return "unavailable" }
func (e unavailableEmbedder) Dims() int     { return e.dims }

// failingEmbedder fails with an error that is not ErrUnavailable.
type failingEmbedder struct{ unavailableEmbedder }

func (e failingEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	return nil, errors.New("fallo de prueba")
}

// countingEmbedder counts the calls to EmbedQuery.
type countingEmbedder struct {
	*embedding.FakeEmbedder
	queries atomic.Int32
}

func (c *countingEmbedder) EmbedQuery(ctx context.Context, q string) ([]float32, error) {
	c.queries.Add(1)
	return c.FakeEmbedder.EmbedQuery(ctx, q)
}

// env is the state of one subtest: the services over the shared, freshly emptied database.
type env struct {
	ctx  context.Context
	pool *pgxpool.Pool
	emb  *embedding.FakeEmbedder
	mgr  *session.Manager
	tr   *session.Tracker
	mem  *memory.Service
}

func newEnv(ctx context.Context, tc *database.TestCluster) *env {
	emb := &embedding.FakeEmbedder{Dimensions: testDims}
	mgr := session.NewManager(tc.Pool, testIdle)
	res := topic.NewResolver(tc.Pool, emb, testTopicSim)
	return &env{
		ctx:  ctx,
		pool: tc.Pool,
		emb:  emb,
		mgr:  mgr,
		tr:   mgr.NewTracker("test"),
		mem:  memory.NewService(tc.Pool, emb, res, mgr, testDup, testTimeout),
	}
}

// service builds a recall Service that embeds queries with emb. Each Service has
// its own topic resolver, whose cache it loads by itself.
func (e *env) service(emb embedding.Embedder, o Options) *Service {
	return NewService(e.pool, emb, topic.NewResolver(e.pool, e.emb, testTopicSim), o)
}

// save stores one record and returns its id.
func (e *env) save(t *testing.T, it memory.Item) int64 {
	t.Helper()
	saved, err := e.mem.Remember(e.ctx, e.tr, []memory.Item{it})
	if err != nil {
		t.Fatalf("Remember(%q) falló: %v", it.Title, err)
	}
	if len(saved) != 1 || saved[0].Duplicate {
		t.Fatalf("Remember(%q) devolvió %+v", it.Title, saved)
	}
	return saved[0].ID
}

func (e *env) invalidate(t *testing.T, id int64) {
	t.Helper()
	if _, err := e.mem.Revise(e.ctx, e.tr, memory.Revision{ID: id, Action: "invalidate"}); err != nil {
		t.Fatalf("Revise(invalidate #%d) falló: %v", id, err)
	}
}

func (e *env) recall(t *testing.T, svc *Service, req Request) Result {
	t.Helper()
	res, err := svc.Recall(e.ctx, req)
	if err != nil {
		t.Fatalf("Recall(%+v) falló: %v", req, err)
	}
	return res
}

// day returns noon UTC of a day, as a pointer for memory.Item.OccurredAt.
func day(y int, m time.Month, d int) *time.Time {
	t := time.Date(y, m, d, 12, 0, 0, 0, time.UTC)
	return &t
}

func idsOf(items []Item) []int64 {
	out := make([]int64, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

// find returns the position of id in items, or -1.
func find(items []Item, id int64) int {
	return slices.IndexFunc(items, func(it Item) bool { return it.ID == id })
}

// all returns the items of the three sections of a result.
func (r Result) all() []Item {
	return slices.Concat(r.Profile, r.Events, r.Results)
}

func wantErr(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("se esperaba un error que contuviera %q, pero no hubo error", substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("error %q no contiene %q", err, substr)
	}
}

func TestValidation(t *testing.T) {
	// La validación ocurre antes de tocar la base de datos: no hace falta clúster.
	svc := NewService(nil, &embedding.FakeEmbedder{Dimensions: testDims}, nil, Options{TopicDetectThreshold: testDetectSim})
	ctx := context.Background()

	for _, q := range []string{"", "   ", "\n\t "} {
		_, err := svc.Recall(ctx, Request{Query: q})
		wantErr(t, err, "no puede estar vacía")
	}
	_, err := svc.Recall(ctx, Request{Query: strings.Repeat("á", maxQueryRunes+1)})
	wantErr(t, err, "el máximo es 1000")
}

func TestOptionsDefaultsAndLimit(t *testing.T) {
	svc := NewService(nil, &embedding.FakeEmbedder{}, nil, Options{TopicDetectThreshold: 0.5})
	o := svc.opts
	if o.MaxBytes != 6000 || o.Candidates != 400 || o.DefaultLimit != 8 || o.MaxLimit != 20 || o.CacheSize != 256 {
		t.Errorf("valores por defecto inesperados: %+v", o)
	}
	if svc.MaxBytes() != 6000 {
		t.Errorf("MaxBytes() = %d", svc.MaxBytes())
	}
	for in, want := range map[int]int{0: 8, 1: 1, 5: 5, 20: 20, 21: 20, 1000: 20, -3: 1} {
		if got := svc.clampLimit(in); got != want {
			t.Errorf("clampLimit(%d) = %d, se esperaba %d", in, got, want)
		}
	}
}

func TestSnippet(t *testing.T) {
	if got, want := snippet("una  línea\n\ncon \t saltos "), "una línea con saltos"; got != want {
		t.Errorf("snippet = %q, se esperaba %q", got, want)
	}
	exact := strings.Repeat("a", snippetRunes)
	if got := snippet(exact); got != exact {
		t.Errorf("un texto de %d runes no debe cortarse", snippetRunes)
	}
	got := snippet(strings.Repeat("ñ", 300))
	if !strings.HasSuffix(got, "…") || utf8.RuneCountInString(got) != snippetRunes+1 {
		t.Errorf("snippet largo: %d runes, %q", utf8.RuneCountInString(got), got)
	}
}

func TestLRU(t *testing.T) {
	c := newLRU(2)
	c.put("a", []float32{1})
	c.put("b", []float32{2})
	if _, ok := c.get("a"); !ok { // a pasa a ser la más reciente
		t.Fatal("a debería estar en la caché")
	}
	c.put("c", []float32{3}) // expulsa b, la menos reciente
	if _, ok := c.get("b"); ok {
		t.Error("b debería haberse expulsado")
	}
	for _, k := range []string{"a", "c"} {
		if _, ok := c.get(k); !ok {
			t.Errorf("%s debería seguir en la caché", k)
		}
	}
	c.put("a", []float32{9}) // reemplazo sin crecer
	if v, _ := c.get("a"); v[0] != 9 || c.len() != 2 {
		t.Errorf("reemplazo: v = %v, len = %d", v, c.len())
	}

	if got, want := cacheKey("  Hola   MUNDO\n"), "hola mundo"; got != want {
		t.Errorf("cacheKey = %q, se esperaba %q", got, want)
	}
}

func TestRecall(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)

	// run ejecuta un subtest sobre tablas vacías, con servicios nuevos (memory cachea el
	// id del modelo de embeddings y Reset reinicia las identidades).
	run := func(name string, f func(t *testing.T, e *env)) {
		t.Run(name, func(t *testing.T) {
			tc.Reset(t)
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			f(t, newEnv(ctx, tc))
		})
	}

	// Criterio 7.
	run("criterio 7: ficha del tema detectado, eventos y resultados", func(t *testing.T, e *env) {
		item := func(kind memory.Kind, title, content, key string, at *time.Time) memory.Item {
			return memory.Item{Title: title, Content: content, Kind: kind, Key: key, OccurredAt: at,
				Topics: []string{"entrenamiento"}}
		}
		// Las preferencias p1 y p2 comparten clave: p2 sustituye a p1.
		p1 := e.save(t, item(memory.KindPreference, "Horario de entreno", "Entrena por la mañana temprano", "entreno/horario", day(2026, 1, 10)))
		p2 := e.save(t, item(memory.KindPreference, "Horario de entreno", "Entrena por la tarde a las siete", "entreno/horario", day(2026, 9, 12)))
		p3 := e.save(t, item(memory.KindPreference, "Ropa de entreno", "Usa camiseta técnica y zapatillas de running", "", day(2026, 8, 1)))
		d1 := e.save(t, item(memory.KindDecision, "Cambiar de rutina", "Se decide pasar a una rutina de fuerza tres días por semana", "", day(2026, 8, 20)))
		e1 := e.save(t, item(memory.KindEvent, "Sesión de piernas", "Sentadillas y prensa con buena técnica", "", day(2026, 9, 20)))
		e2 := e.save(t, item(memory.KindEvent, "Carrera suave", "Cinco kilómetros a ritmo tranquilo", "", day(2026, 9, 28)))
		e3 := e.save(t, item(memory.KindEvent, "Sesión de espalda", "Dominadas y remo con barra", "", day(2026, 9, 25)))
		inv := e.save(t, item(memory.KindFact, "Objetivo de peso", "El objetivo era pesar sesenta kilos", "", day(2026, 9, 1)))
		e.invalidate(t, inv)
		n1 := e.save(t, item(memory.KindNote, "Reflexión", "Reflexión general sobre la constancia en el entrenamiento", "", day(2026, 9, 2)))

		svc := e.service(e.emb, Options{TopicDetectThreshold: lowThreshold})
		res := e.recall(t, svc, Request{Query: "último entrenamiento"})

		if res.Topic == nil || res.Topic.Slug != "entrenamiento" || !res.TopicDetected {
			t.Fatalf("Topic = %+v, TopicDetected = %v; se esperaba entrenamiento detectado", res.Topic, res.TopicDetected)
		}
		if res.TopicVia != TopicViaSimilarity {
			t.Errorf("TopicVia = %q, se esperaba %q", res.TopicVia, TopicViaSimilarity)
		}
		if res.TextOnly {
			t.Error("TextOnly no debería estar activo")
		}
		if got, want := idsOf(res.Profile), []int64{p2, d1, p3}; !slices.Equal(got, want) {
			t.Errorf("Profile = %v, se esperaba %v", got, want)
		}
		if got, want := idsOf(res.Events), []int64{e2, e3, e1}; !slices.Equal(got, want) {
			t.Errorf("Events = %v, se esperaba %v (el más reciente primero)", got, want)
		}
		if find(res.Results, n1) < 0 {
			t.Errorf("Results = %v debería contener la nota #%d", idsOf(res.Results), n1)
		}

		seen := make(map[int64]bool)
		for _, it := range res.all() {
			if it.ID == p1 || it.ID == inv {
				t.Errorf("no debería aparecer #%d (sustituido o invalidado)", it.ID)
			}
			if it.Status != "active" {
				t.Errorf("#%d tiene estado %q, se esperaba active", it.ID, it.Status)
			}
			if seen[it.ID] {
				t.Errorf("#%d aparece repetido", it.ID)
			}
			seen[it.ID] = true
		}

		// Etiquetas y fechas de la ficha.
		if res.Profile[0].Kind != "preference" || !res.Profile[0].OccurredAt.Equal(*day(2026, 9, 12)) {
			t.Errorf("Profile[0] = %+v", res.Profile[0])
		}
		out := res.Format(svc.MaxBytes())
		if len(out) > svc.MaxBytes() {
			t.Errorf("la respuesta mide %d bytes y el tope es %d", len(out), svc.MaxBytes())
		}
		for _, want := range []string{"Tema: entrenamiento (detectado)", "Datos estables:", "Últimos eventos:", "Resultados:"} {
			if !strings.Contains(out, want) {
				t.Errorf("Format no contiene %q:\n%s", want, out)
			}
		}
	})

	run("temas pedidos: se usa el primero que existe", func(t *testing.T, e *env) {
		pref := e.save(t, memory.Item{Title: "Café", Content: "Prefiere el café sin azúcar",
			Kind: memory.KindPreference, Topics: []string{"café"}})
		svc := e.service(e.emb, Options{TopicDetectThreshold: highThreshold})
		res := e.recall(t, svc, Request{Query: "azúcar", Topics: []string{"  ", "Inexistente", "CAFÉ"}})
		if res.Topic == nil || res.Topic.Slug != "cafe" || res.TopicDetected {
			t.Fatalf("Topic = %+v, TopicDetected = %v; se esperaba cafe pedido", res.Topic, res.TopicDetected)
		}
		if res.TopicVia != TopicViaRequested {
			t.Errorf("TopicVia = %q, se esperaba %q", res.TopicVia, TopicViaRequested)
		}
		if got := idsOf(res.Profile); !slices.Equal(got, []int64{pref}) {
			t.Errorf("Profile = %v, se esperaba [%d]", got, pref)
		}
	})

	// Detección por votación: la consulta no se parece al nombre del tema (con el umbral
	// alto no hay detección por similitud), pero los dos primeros resultados pertenecen a él.
	// Con el embedder falso, una consulta de dos palabras nunca supera 0,71 de coseno con
	// un tema de una sola palabra.
	seedVote := func(t *testing.T, e *env) (r1, r2, pref, other int64) {
		t.Helper()
		r1 = e.save(t, memory.Item{Title: "Ejercicio con pesas", Content: "Ejercicio de kettlebell pesado en el sótano",
			Kind: memory.KindNote, Topics: []string{"entrenamiento"}})
		r2 = e.save(t, memory.Item{Title: "Balanceo", Content: "Balanceo con kettlebell pesado",
			Kind: memory.KindNote, Topics: []string{"entrenamiento"}})
		pref = e.save(t, memory.Item{Title: "Horario", Content: "Entrena por la tarde",
			Kind: memory.KindPreference, Topics: []string{"entrenamiento"}})
		other = e.save(t, memory.Item{Title: "Matrícula", Content: "Matrícula del coche 1234",
			Kind: memory.KindNote, Topics: []string{"coche"}})
		return r1, r2, pref, other
	}
	checkVote := func(t *testing.T, svc *Service, e *env, textOnly bool) {
		t.Helper()
		r1, r2, pref, other := seedVote(t, e)
		res := e.recall(t, svc, Request{Query: "kettlebell pesado", Limit: 3})

		if res.Topic == nil || res.Topic.Slug != "entrenamiento" || !res.TopicDetected || res.TopicVia != TopicViaVote {
			t.Fatalf("Topic = %+v, TopicDetected = %v, TopicVia = %q; se esperaba entrenamiento por votación",
				res.Topic, res.TopicDetected, res.TopicVia)
		}
		if res.TextOnly != textOnly {
			t.Errorf("TextOnly = %v, se esperaba %v", res.TextOnly, textOnly)
		}
		// La ficha se lee tras la fusión.
		if got := idsOf(res.Profile); !slices.Equal(got, []int64{pref}) {
			t.Errorf("Profile = %v, se esperaba [%d]", got, pref)
		}
		// La preferencia ya sale en la ficha, así que no se repite; los resultados se
		// vuelven a cortar a Limit (3) con lo que queda.
		if find(res.Results, pref) >= 0 {
			t.Errorf("Results = %v repite #%d, que ya está en la ficha", idsOf(res.Results), pref)
		}
		for _, id := range []int64{r1, r2} {
			if find(res.Results, id) < 0 {
				t.Errorf("Results = %v debería contener #%d", idsOf(res.Results), id)
			}
		}
		if want := 3; !textOnly && len(res.Results) != want {
			t.Errorf("Results = %v, se esperaban %d tras quitar lo de la ficha", idsOf(res.Results), want)
		}
		if !textOnly && find(res.Results, other) < 0 {
			t.Errorf("Results = %v debería completarse con #%d", idsOf(res.Results), other)
		}
		if out := res.Format(svc.MaxBytes()); !strings.Contains(out, "Tema: entrenamiento (detectado)") {
			t.Errorf("Format debería mostrar el tema detectado:\n%s", out)
		}
	}
	run("votación: los resultados 1 y 2 comparten tema", func(t *testing.T, e *env) {
		checkVote(t, e.service(e.emb, Options{TopicDetectThreshold: highThreshold}), e, false)
	})
	run("votación sin embeddings (TextOnly)", func(t *testing.T, e *env) {
		checkVote(t, e.service(unavailableEmbedder{dims: testDims}, Options{TopicDetectThreshold: lowThreshold}), e, true)
	})

	run("votación: sin tema compartido no se detecta ninguno", func(t *testing.T, e *env) {
		e.save(t, memory.Item{Title: "Pesas", Content: "Ejercicio de kettlebell pesado",
			Kind: memory.KindNote, Topics: []string{"entrenamiento"}})
		e.save(t, memory.Item{Title: "Taller", Content: "Revisión con kettlebell pesado en el taller",
			Kind: memory.KindNote, Topics: []string{"coche"}})
		svc := e.service(e.emb, Options{TopicDetectThreshold: highThreshold})
		res := e.recall(t, svc, Request{Query: "kettlebell pesado"})
		if res.Topic != nil || res.TopicDetected || res.TopicVia != "" {
			t.Errorf("Topic = %+v, TopicDetected = %v, TopicVia = %q; no debía detectarse ninguno",
				res.Topic, res.TopicDetected, res.TopicVia)
		}
		if len(res.Results) != 2 {
			t.Errorf("Results = %v, se esperaban 2 resultados", idsOf(res.Results))
		}
	})

	run("votación: gana el tema presente en más resultados", func(t *testing.T, e *env) {
		both := []string{"zeta", "alfa"} // zeta se crea primero: id menor
		e.save(t, memory.Item{Title: "Uno", Content: "Primera nota de kettlebell pesado", Kind: memory.KindNote, Topics: both})
		e.save(t, memory.Item{Title: "Dos", Content: "Segunda nota de kettlebell pesado", Kind: memory.KindNote, Topics: both})
		e.save(t, memory.Item{Title: "Tres", Content: "Tercera nota de kettlebell pesado", Kind: memory.KindNote, Topics: []string{"alfa"}})
		svc := e.service(e.emb, Options{TopicDetectThreshold: highThreshold})
		res := e.recall(t, svc, Request{Query: "kettlebell pesado"})
		if res.Topic == nil || res.Topic.Slug != "alfa" || res.TopicVia != TopicViaVote {
			t.Errorf("Topic = %+v, TopicVia = %q; se esperaba alfa (en 3 de los resultados) por votación", res.Topic, res.TopicVia)
		}
	})

	run("votación: en empate gana el tema de menor id", func(t *testing.T, e *env) {
		both := []string{"zeta", "alfa"} // zeta se crea primero: id menor, aunque alfa vaya antes por orden alfabético
		e.save(t, memory.Item{Title: "Uno", Content: "Primera nota de kettlebell pesado", Kind: memory.KindNote, Topics: both})
		e.save(t, memory.Item{Title: "Dos", Content: "Segunda nota de kettlebell pesado", Kind: memory.KindNote, Topics: both})
		svc := e.service(e.emb, Options{TopicDetectThreshold: highThreshold})
		res := e.recall(t, svc, Request{Query: "kettlebell pesado"})
		if res.Topic == nil || res.Topic.Slug != "zeta" || res.TopicVia != TopicViaVote {
			t.Errorf("Topic = %+v, TopicVia = %q; se esperaba zeta (menor id) por votación", res.Topic, res.TopicVia)
		}
	})

	// Criterio 8.
	run("criterio 8: un código exacto aparece entre los 3 primeros", func(t *testing.T, e *env) {
		var target int64
		for i := 0; i < 12; i++ {
			if i == 6 {
				target = e.save(t, memory.Item{Title: "Matrícula del coche", Content: "La matrícula del coche es 1234-KLM",
					Kind: memory.KindNote, Topics: []string{"coche"}})
			}
			e.save(t, memory.Item{
				Title:   fmt.Sprintf("Nota %d", i),
				Content: fmt.Sprintf("Texto de la nota %d sobre la matrícula pendiente de revisar en el taller %d", i, i*7),
				Kind:    memory.KindNote, Topics: []string{"coche"},
			})
		}
		svc := e.service(e.emb, Options{TopicDetectThreshold: highThreshold})
		res := e.recall(t, svc, Request{Query: "1234-KLM"})
		pos := find(res.Results, target)
		if pos < 0 || pos > 2 {
			t.Errorf("la matrícula #%d está en la posición %d de Results %v; se esperaba entre las 3 primeras",
				target, pos, idsOf(res.Results))
		}
	})

	// Criterio 10.
	run("criterio 10: el historial incluye sustituidos e invalidados", func(t *testing.T, e *env) {
		note := func(title, content, key string) memory.Item {
			return memory.Item{Title: title, Content: content, Kind: memory.KindNote, Key: key, Topics: []string{"compras"}}
		}
		old := e.save(t, note("Plan de compra", "Plan antiguo de compra semanal", "compra"))
		cur := e.save(t, note("Plan de compra", "Plan nuevo de compra semanal", "compra"))
		bad := e.save(t, note("Plan descartado", "Plan descartado de compra mensual", ""))
		e.invalidate(t, bad)
		svc := e.service(e.emb, Options{TopicDetectThreshold: highThreshold})

		// Sin historial solo sale el vigente.
		res := e.recall(t, svc, Request{Query: "plan compra"})
		if got := idsOf(res.Results); !slices.Equal(got, []int64{cur}) {
			t.Errorf("sin historial Results = %v, se esperaba [%d]", got, cur)
		}

		// Con historial salen también el sustituido y el invalidado, con su estado.
		res = e.recall(t, svc, Request{Query: "plan compra", IncludeHistory: true})
		if i := find(res.Results, old); i < 0 || res.Results[i].Status != "superseded" || res.Results[i].SupersededBy != cur {
			t.Errorf("el sustituido #%d debería salir con SupersededBy = %d: %+v", old, cur, res.Results)
		}
		if i := find(res.Results, bad); i < 0 || res.Results[i].Status != "invalidated" || res.Results[i].SupersededBy != 0 {
			t.Errorf("el invalidado #%d debería salir como invalidated: %+v", bad, res.Results)
		}
		if find(res.Results, cur) < 0 {
			t.Errorf("el vigente #%d debería seguir saliendo", cur)
		}
		out := res.Format(svc.MaxBytes())
		if !strings.Contains(out, fmt.Sprintf("sustituido por #%d]", cur)) || !strings.Contains(out, ", invalidado]") {
			t.Errorf("Format debería marcar el estado y el enlace:\n%s", out)
		}

		// El sustituido, aunque no case con la consulta, se añade al final desde su relación.
		res = e.recall(t, svc, Request{Query: "nuevo", IncludeHistory: true})
		if got := idsOf(res.Results); !slices.Equal(got, []int64{cur, old}) {
			t.Errorf("Results = %v, se esperaba [%d %d] (vigente y luego lo que sustituyó)", got, cur, old)
		}
		if res.Results[1].SupersededBy != cur {
			t.Errorf("SupersededBy = %d, se esperaba %d", res.Results[1].SupersededBy, cur)
		}
	})

	run("TextOnly: sin servicio de embeddings solo se busca por texto", func(t *testing.T, e *env) {
		target := e.save(t, memory.Item{Title: "Matrícula del coche", Content: "La matrícula del coche es 1234-KLM",
			Kind: memory.KindNote, Topics: []string{"coche"}})
		e.save(t, memory.Item{Title: "Otra nota", Content: "Nada que ver con lo anterior",
			Kind: memory.KindNote, Topics: []string{"coche"}})

		svc := e.service(unavailableEmbedder{dims: testDims}, Options{TopicDetectThreshold: lowThreshold})
		res := e.recall(t, svc, Request{Query: "1234-KLM"})
		if !res.TextOnly {
			t.Fatal("TextOnly debería estar activo")
		}
		if res.Topic != nil {
			t.Errorf("sin embedding no se detecta tema, Topic = %+v", res.Topic)
		}
		if got := idsOf(res.Results); !slices.Equal(got, []int64{target}) {
			t.Errorf("Results = %v, se esperaba solo [%d]", got, target)
		}
		if out := res.Format(svc.MaxBytes()); !strings.HasPrefix(out, textOnlyLine+"\n") {
			t.Errorf("la primera línea debería ser el aviso de solo texto:\n%s", out)
		}
	})

	run("un error del embedder distinto de ErrUnavailable se devuelve", func(t *testing.T, e *env) {
		svc := e.service(failingEmbedder{unavailableEmbedder{dims: testDims}}, Options{TopicDetectThreshold: lowThreshold})
		_, err := svc.Recall(e.ctx, Request{Query: "algo"})
		wantErr(t, err, "fallo de prueba")
		if errors.Is(err, embedding.ErrUnavailable) {
			t.Error("el error no debería ser ErrUnavailable")
		}
	})

	run("caché: una consulta repetida no vuelve a calcular el embedding", func(t *testing.T, e *env) {
		e.save(t, memory.Item{Title: "Saludo", Content: "Hola mundo desde las pruebas", Kind: memory.KindNote})
		emb := &countingEmbedder{FakeEmbedder: e.emb}
		svc := e.service(emb, Options{TopicDetectThreshold: highThreshold})

		e.recall(t, svc, Request{Query: "Hola Mundo"})
		if n := emb.queries.Load(); n != 1 {
			t.Fatalf("tras la primera consulta hubo %d llamadas al embedder, se esperaba 1", n)
		}
		e.recall(t, svc, Request{Query: "  hola   MUNDO "}) // misma clave normalizada
		e.recall(t, svc, Request{Query: "Hola Mundo", Limit: 3})
		if n := emb.queries.Load(); n != 1 {
			t.Errorf("las consultas repetidas hicieron %d llamadas, se esperaba 1", n)
		}
		e.recall(t, svc, Request{Query: "otra cosa distinta"})
		if n := emb.queries.Load(); n != 2 {
			t.Errorf("una consulta nueva debería calcular su embedding: %d llamadas, se esperaban 2", n)
		}
	})

	run("caché: con CacheSize 1 se expulsa la consulta más antigua", func(t *testing.T, e *env) {
		emb := &countingEmbedder{FakeEmbedder: e.emb}
		svc := e.service(emb, Options{TopicDetectThreshold: highThreshold, CacheSize: 1})
		e.recall(t, svc, Request{Query: "uno"})
		e.recall(t, svc, Request{Query: "dos"})
		e.recall(t, svc, Request{Query: "uno"})
		if n := emb.queries.Load(); n != 3 {
			t.Errorf("hubo %d llamadas al embedder, se esperaban 3", n)
		}
	})

	run("sin datos devuelve un resultado vacío", func(t *testing.T, e *env) {
		svc := e.service(e.emb, Options{TopicDetectThreshold: lowThreshold})
		res := e.recall(t, svc, Request{Query: "nada de nada"})
		if len(res.all()) != 0 || res.Topic != nil {
			t.Errorf("se esperaba un resultado vacío: %+v", res)
		}
		if got, want := res.Format(svc.MaxBytes()), "Sin resultados para «nada de nada»."; got != want {
			t.Errorf("Format = %q, se esperaba %q", got, want)
		}
	})

	run("el límite corta los resultados", func(t *testing.T, e *env) {
		for i := 0; i < 6; i++ {
			e.save(t, memory.Item{Title: fmt.Sprintf("Nota %d", i), Content: fmt.Sprintf("contenido numero%d", i), Kind: memory.KindNote})
		}
		svc := e.service(e.emb, Options{TopicDetectThreshold: highThreshold, MaxLimit: 4})
		if n := len(e.recall(t, svc, Request{Query: "contenido", Limit: 2}).Results); n != 2 {
			t.Errorf("con Limit 2 hubo %d resultados", n)
		}
		if n := len(e.recall(t, svc, Request{Query: "contenido", Limit: 50}).Results); n != 4 {
			t.Errorf("con Limit 50 y MaxLimit 4 hubo %d resultados, se esperaban 4", n)
		}
	})

	// ---------------------------------------------------------------- S12/S13 filtros

	// saveTags crea tags concretas (slugs) en la BD; devuelve los slugs ya
	// normalizados (topic.Normalize) para que coincidan con la forma en que el
	// resolver de memory.Remember los vincularía (la normalización convierte "/"
	// en "-", así que sin normalizar terminarían creando tags duplicadas).
	saveTags := func(t *testing.T, e *env, rawSlugs ...string) []string {
		t.Helper()
		out := make([]string, 0, len(rawSlugs))
		for _, s := range rawSlugs {
			slug := topic.Normalize(s)
			if slug == "" {
				t.Fatalf("la etiqueta %q se normaliza a vacío", s)
			}
			if _, err := e.pool.Exec(e.ctx, `INSERT INTO topics (slug) VALUES ($1) ON CONFLICT (slug) DO NOTHING`, slug); err != nil {
				t.Fatalf("no se pudo crear la etiqueta %q: %v", slug, err)
			}
			out = append(out, slug)
		}
		return out
	}

	// relinkTopics recalcula memory_topics (active=true, kind y ts desde memories) para los
	// ids dados, simulando lo que hace Remember sin pasar por la embedding de los tags.
	relinkTopics := func(t *testing.T, e *env, id int64, tags []string) {
		t.Helper()
		if _, err := e.pool.Exec(e.ctx, `DELETE FROM memory_topics WHERE memory_id = $1`, id); err != nil {
			t.Fatalf("no se pudieron borrar las tags anteriores de #%d: %v", id, err)
		}
		for _, raw := range tags {
			slug := topic.Normalize(raw)
			if slug == "" {
				t.Fatalf("la etiqueta %q se normaliza a vacío", raw)
			}
			if _, err := e.pool.Exec(e.ctx,
				`INSERT INTO memory_topics (topic_id, memory_id, kind, active, ts)
				 SELECT t.id, $1, m.kind, m.status = 'active', m.occurred_at
				 FROM topics t, memories m WHERE t.slug = $2 AND m.id = $1`,
				id, slug); err != nil {
				t.Fatalf("no se pudo vincular #%d con la etiqueta %q: %v", id, slug, err)
			}
		}
	}

	run("S12/S13 tags_all: AND entre etiquetas", func(t *testing.T, e *env) {
		tags := saveTags(t, e, "proyecto-erp-agm", "proyecto-erp-agm/cosechas", "finca-rebelao")

		all := e.save(t, memory.Item{Title: "Cosecha Rebelao", Content: "Cosecha en Rebelao del proyecto erp-agm",
			Kind: memory.KindFact, Topics: tags})
		relinkTopics(t, e, all, tags)
		two := e.save(t, memory.Item{Title: "Cosecha Royalveg", Content: "Cosecha en Royalveg",
			Kind: memory.KindFact, Topics: []string{"proyecto-erp-agm", "finca-royalveg"}})
		relinkTopics(t, e, two, []string{"proyecto-erp-agm", "finca-royalveg"})
		one := e.save(t, memory.Item{Title: "Otro proyecto", Content: "Trabajo en proyecto independiente",
			Kind: memory.KindNote, Topics: []string{"proyecto-otro"}})
		relinkTopics(t, e, one, []string{"proyecto-otro"})

		svc := e.service(unavailableEmbedder{dims: testDims}, Options{TopicDetectThreshold: lowThreshold})
		res := e.recall(t, svc, Request{
			Query:   "cosecha proyecto",
			TagsAll: tags,
		})
		got := idsOf(res.Results)
		if !slices.Contains(got, all) {
			t.Errorf("tags_all=%v debería traer #%d: results=%v", tags, all, got)
		}
		for _, banned := range []int64{two, one} {
			if slices.Contains(got, banned) {
				t.Errorf("tags_all=%v NO debería traer #%d: results=%v", tags, banned, got)
			}
		}
	})

	run("S12/S13 tags_any: OR entre etiquetas", func(t *testing.T, e *env) {
		saveTags(t, e, "finca-rebelao", "finca-royalveg", "finca-pazo")
		rebelao := e.save(t, memory.Item{Title: "Rebelao", Content: "Cosecha en Rebelao",
			Kind: memory.KindFact, Topics: []string{"finca-rebelao"}})
		relinkTopics(t, e, rebelao, []string{"finca-rebelao"})
		royalveg := e.save(t, memory.Item{Title: "Royalveg", Content: "Cosecha en Royalveg",
			Kind: memory.KindFact, Topics: []string{"finca-royalveg"}})
		relinkTopics(t, e, royalveg, []string{"finca-royalveg"})
		pazo := e.save(t, memory.Item{Title: "Pazo", Content: "Cosecha en Pazo",
			Kind: memory.KindFact, Topics: []string{"finca-pazo"}})
		relinkTopics(t, e, pazo, []string{"finca-pazo"})
		other := e.save(t, memory.Item{Title: "Otro", Content: "Cosecha en otra finca",
			Kind: memory.KindFact, Topics: []string{"finca-otra"}})
		relinkTopics(t, e, other, []string{"finca-otra"})

		svc := e.service(unavailableEmbedder{dims: testDims}, Options{TopicDetectThreshold: lowThreshold})
		res := e.recall(t, svc, Request{
			Query:   "cosecha",
			TagsAny: []string{"finca-rebelao", "finca-royalveg"},
		})
		got := idsOf(res.Results)
		for _, want := range []int64{rebelao, royalveg} {
			if !slices.Contains(got, want) {
				t.Errorf("tags_any=[rebelao, royalveg] debería traer #%d: results=%v", want, got)
			}
		}
		for _, banned := range []int64{pazo, other} {
			if slices.Contains(got, banned) {
				t.Errorf("tags_any=[rebelao, royalveg] NO debería traer #%d: results=%v", banned, got)
			}
		}
	})

	run("S12/S13 date_from/date_to: rango de fechas", func(t *testing.T, e *env) {
		enero := e.save(t, memory.Item{Title: "Enero", Content: "Actividad en enero del proyecto",
			Kind: memory.KindEvent, Topics: []string{"proyecto-erp-agm"}, OccurredAt: day(2026, 1, 15)})
		marzo := e.save(t, memory.Item{Title: "Marzo", Content: "Actividad en marzo del proyecto",
			Kind: memory.KindEvent, Topics: []string{"proyecto-erp-agm"}, OccurredAt: day(2026, 3, 15)})
		mayo := e.save(t, memory.Item{Title: "Mayo", Content: "Actividad en mayo del proyecto",
			Kind: memory.KindEvent, Topics: []string{"proyecto-erp-agm"}, OccurredAt: day(2026, 5, 15)})

		svc := e.service(unavailableEmbedder{dims: testDims}, Options{TopicDetectThreshold: lowThreshold})

		// Solo date_from: trae marzo y mayo (enero queda fuera por la fecha).
		res := e.recall(t, svc, Request{
			Query:   "actividad",
			DateFrom: "2026-02-01",
		})
		got := idsOf(res.all())
		if !slices.Contains(got, mayo) || !slices.Contains(got, marzo) {
			t.Errorf("con date_from=2026-02-01 debería traer #%d y #%d: got=%v", mayo, marzo, got)
		}
		if slices.Contains(got, enero) {
			t.Errorf("con date_from=2026-02-01 NO debería traer #%d (enero): got=%v", enero, got)
		}

		// date_from + date_to: solo marzo.
		res = e.recall(t, svc, Request{
			Query:    "actividad",
			DateFrom: "2026-02-01",
			DateTo:   "2026-04-01",
		})
		got = idsOf(res.all())
		if !slices.Contains(got, marzo) {
			t.Errorf("con date_from=2026-02-01 y date_to=2026-04-01 debería traer #%d (marzo): got=%v", marzo, got)
		}
		for _, banned := range []int64{enero, mayo} {
			if slices.Contains(got, banned) {
				t.Errorf("con rango feb-abril NO debería traer #%d: got=%v", banned, got)
			}
		}
	})

	run("S12/S13 tags_all + tags_any: AND entre grupos", func(t *testing.T, e *env) {
		saveTags(t, e, "proyecto-erp-agm", "proyecto-erp-agm/cosechas", "finca-rebelao", "finca-royalveg")

		all3 := e.save(t, memory.Item{Title: "Rebelao cosechas", Content: "Cosecha Rebelao del proyecto erp-agm",
			Kind: memory.KindFact, Topics: []string{"proyecto-erp-agm", "proyecto-erp-agm/cosechas", "finca-rebelao"}})
		relinkTopics(t, e, all3, []string{"proyecto-erp-agm", "proyecto-erp-agm/cosechas", "finca-rebelao"})
		// Tiene las dos primeras (tags_all) y una de las any.
		partial := e.save(t, memory.Item{Title: "Royalveg cosechas", Content: "Cosecha Royalveg del proyecto erp-agm",
			Kind: memory.KindFact, Topics: []string{"proyecto-erp-agm", "proyecto-erp-agm/cosechas", "finca-royalveg"}})
		relinkTopics(t, e, partial, []string{"proyecto-erp-agm", "proyecto-erp-agm/cosechas", "finca-royalveg"})
		// Solo una de las tags_all: queda fuera aunque tenga finca-rebelao.
		one := e.save(t, memory.Item{Title: "Otro proyecto Rebelao", Content: "Cosecha Rebelao pero de otro proyecto",
			Kind: memory.KindFact, Topics: []string{"proyecto-otro", "finca-rebelao"}})
		relinkTopics(t, e, one, []string{"proyecto-otro", "finca-rebelao"})

		svc := e.service(unavailableEmbedder{dims: testDims}, Options{TopicDetectThreshold: lowThreshold})
		res := e.recall(t, svc, Request{
			Query:   "cosecha",
			TagsAll: []string{"proyecto-erp-agm", "proyecto-erp-agm/cosechas"},
			TagsAny: []string{"finca-rebelao", "finca-royalveg"},
		})
		got := idsOf(res.all())
		for _, want := range []int64{all3, partial} {
			if !slices.Contains(got, want) {
				t.Errorf("AND-OR debería traer #%d: got=%v", want, got)
			}
		}
		if slices.Contains(got, one) {
			t.Errorf("AND-OR NO debería traer #%d (le falta tags_all): got=%v", one, got)
		}
	})

	run("S12/S13 fecha + tags: combinación de filtros", func(t *testing.T, e *env) {
		saveTags(t, e, "proyecto-erp-agm", "finca-rebelao")
		recent := e.save(t, memory.Item{Title: "Reciente", Content: "Cosecha Rebelao reciente",
			Kind: memory.KindEvent, Topics: []string{"proyecto-erp-agm", "finca-rebelao"}, OccurredAt: day(2026, 5, 10)})
		relinkTopics(t, e, recent, []string{"proyecto-erp-agm", "finca-rebelao"})
		old := e.save(t, memory.Item{Title: "Antiguo", Content: "Cosecha Rebelao antigua",
			Kind: memory.KindEvent, Topics: []string{"proyecto-erp-agm", "finca-rebelao"}, OccurredAt: day(2025, 5, 10)})
		relinkTopics(t, e, old, []string{"proyecto-erp-agm", "finca-rebelao"})

		svc := e.service(unavailableEmbedder{dims: testDims}, Options{TopicDetectThreshold: lowThreshold})
		res := e.recall(t, svc, Request{
			Query:    "cosecha rebelao",
			TagsAll:  []string{"proyecto-erp-agm", "finca-rebelao"},
			DateFrom: "2026-01-01",
		})
		got := idsOf(res.Results)
		if !slices.Contains(got, recent) {
			t.Errorf("fecha + tags debería traer #%d (reciente): results=%v", recent, got)
		}
		if slices.Contains(got, old) {
			t.Errorf("fecha + tags NO debería traer #%d (antiguo): results=%v", old, got)
		}
	})

	run("S12/S13 query vacía + tags: devuelve por filtro sin texto", func(t *testing.T, e *env) {
		saveTags(t, e, "proyecto-erp-agm", "finca-rebelao")
		recent := e.save(t, memory.Item{Title: "Rebelao reciente", Content: "Cosecha de Rebelao del proyecto erp-agm",
			Kind: memory.KindEvent, Topics: []string{"proyecto-erp-agm", "finca-rebelao"}, OccurredAt: day(2026, 5, 10)})
		relinkTopics(t, e, recent, []string{"proyecto-erp-agm", "finca-rebelao"})
		other := e.save(t, memory.Item{Title: "Otro tema", Content: "Cosecha de otra finca",
			Kind: memory.KindEvent, Topics: []string{"finca-otra"}, OccurredAt: day(2026, 5, 10)})
		relinkTopics(t, e, other, []string{"finca-otra"})

		svc := e.service(unavailableEmbedder{dims: testDims}, Options{TopicDetectThreshold: lowThreshold})
		// Sin query pero con tags: debe funcionar.
		res, err := svc.Recall(e.ctx, Request{
			TagsAll: []string{"proyecto-erp-agm", "finca-rebelao"},
		})
		if err != nil {
			t.Fatalf("query vacía + tags_all no debería fallar: %v", err)
		}
		got := idsOf(res.Results)
		if !slices.Contains(got, recent) {
			t.Errorf("query vacía + tags debería traer #%d: results=%v", recent, got)
		}
		if slices.Contains(got, other) {
			t.Errorf("query vacía + tags NO debería traer #%d (no tiene los tags): results=%v", other, got)
		}
	})

	run("S12/S13 tags inválidas: se ignoran; tags_all todas inválidas → error", func(t *testing.T, e *env) {
		saveTags(t, e, "finca-rebelao")
		recent := e.save(t, memory.Item{Title: "Rebelao", Content: "Cosecha en Rebelao",
			Kind: memory.KindFact, Topics: []string{"finca-rebelao"}})
		relinkTopics(t, e, recent, []string{"finca-rebelao"})

		svc := e.service(unavailableEmbedder{dims: testDims}, Options{TopicDetectThreshold: lowThreshold})

		// Una tag inválida mezclada con una válida: la inválida se ignora.
		res, err := svc.Recall(e.ctx, Request{
			Query:   "cosecha",
			TagsAll: []string{"finca-rebelao", "tag-inexistente"},
		})
		if err != nil {
			t.Fatalf("tag inválida mezclada con válida no debería fallar: %v", err)
		}
		got := idsOf(res.Results)
		if !slices.Contains(got, recent) {
			t.Errorf("tag inválida ignorada debería dejar pasar #%d: results=%v", recent, got)
		}

		// tags_any con una inválida: se ignora silenciosamente (no rompe la query).
		res, err = svc.Recall(e.ctx, Request{
			Query:   "cosecha",
			TagsAny: []string{"finca-rebelao", "tag-inexistente"},
		})
		if err != nil {
			t.Fatalf("tags_any con inválida no debería fallar: %v", err)
		}
		if !slices.Contains(idsOf(res.Results), recent) {
			t.Errorf("tags_any con inválida debería traer #%d: results=%v", recent, idsOf(res.Results))
		}

		// tags_all con TODAS inválidas: error.
		_, err = svc.Recall(e.ctx, Request{
			Query:   "cosecha",
			TagsAll: []string{"tag-inexistente-1", "tag-inexistente-2"},
		})
		wantErr(t, err, "tags_all: ninguna tag existe")
	})

	run("S12/S13 backward compat: llamadas sin los nuevos campos funcionan idéntico a antes", func(t *testing.T, e *env) {
		e.save(t, memory.Item{Title: "Matrícula", Content: "La matrícula del coche es 1234-KLM",
			Kind: memory.KindNote, Topics: []string{"coche"}})

		svc := e.service(unavailableEmbedder{dims: testDims}, Options{TopicDetectThreshold: lowThreshold})

		// Llamada sin filtros nuevos.
		baseline, err := svc.Recall(e.ctx, Request{Query: "matrícula"})
		if err != nil {
			t.Fatalf("baseline sin filtros falló: %v", err)
		}
		// Llamada con los campos en su valor cero (slice vacío y string vacío).
		zero, err := svc.Recall(e.ctx, Request{
			Query:    "matrícula",
			TagsAll:  nil,
			TagsAny:  nil,
			DateFrom: "",
			DateTo:   "",
		})
		if err != nil {
			t.Fatalf("llamada con filtros vacíos falló: %v", err)
		}
		if !slices.Equal(idsOf(baseline.Results), idsOf(zero.Results)) {
			t.Errorf("con filtros vacíos el resultado debe coincidir con la baseline: baseline=%v zero=%v",
				idsOf(baseline.Results), idsOf(zero.Results))
		}
	})
}

// TestRecall_FullTextOnly verifica que recall devuelve resultados cuando el
// embedder está caído (ErrUnavailable): el camino textOnly=true (recall.go
// línea ~285) desactiva la búsqueda semántica y deja la full-text + tags como
// única fuente. Esto es el comportamiento que sostiene S6 (recall sin Ollama).
//
// RED: si el camino textOnly se rompe (p.ej. porque alguien devuelve el error
// en lugar de degradar), este test falla. Pasa en cuanto el camino
// errores.Is(ErrUnavailable) está correctamente cableado.
func TestRecall_FullTextOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	tc := database.NewTestCluster(t, testDims)
	tc.Reset(t)

	emb := &embedding.FakeEmbedder{Dimensions: testDims}
	mgr := session.NewManager(tc.Pool, testIdle)
	res := topic.NewResolver(tc.Pool, emb, testTopicSim)

	mem := memory.NewService(tc.Pool, emb, res, mgr, testDup, testTimeout)
	tr := mgr.NewTracker("test")
	saved, err := mem.Remember(ctx, tr, []memory.Item{{
		Title:   "Volvo S60",
		Content: "El Volvo S60 tiene matrícula 4860-NGL",
		Kind:    memory.KindFact,
		Topics:  []string{"coche-volvo-s60"},
	}})
	if err != nil || len(saved) != 1 {
		t.Fatalf("Remember: %v, %d guardados", err, len(saved))
	}
	target := saved[0].ID

	// recall con unavailableEmbedder en lugar del fake.
	svc := NewService(tc.Pool, unavailableEmbedder{dims: testDims},
		topic.NewResolver(tc.Pool, emb, testTopicSim),
		Options{TopicDetectThreshold: lowThreshold})

	res2, err := svc.Recall(ctx, Request{Query: "Volvo S60"})
	if err != nil {
		t.Fatalf("Recall sin embeddings: %v", err)
	}
	if !res2.TextOnly {
		t.Errorf("TextOnly = false, quería true (modo sin embeddings)")
	}
	if res2.Topic != nil {
		t.Errorf("sin embeddings no se detecta tema, Topic = %+v", res2.Topic)
	}
	if got := idsOf(res2.Results); !slices.Equal(got, []int64{target}) {
		t.Errorf("Results = %v, quería [%d]", got, target)
	}
	if out := res2.Format(svc.MaxBytes()); !strings.HasPrefix(out, textOnlyLine+"\n") {
		t.Errorf("la primera línea debería ser el aviso de solo texto:\n%s", out)
	}
}
