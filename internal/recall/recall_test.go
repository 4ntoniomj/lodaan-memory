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
	testTopicSim = 0.85
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
	return NewService(e.pool, emb, topic.NewResolver(e.pool, e.emb, o.TopicThreshold), o)
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
	svc := NewService(nil, &embedding.FakeEmbedder{Dimensions: testDims}, nil, Options{TopicThreshold: testTopicSim})
	ctx := context.Background()

	for _, q := range []string{"", "   ", "\n\t "} {
		_, err := svc.Recall(ctx, Request{Query: q})
		wantErr(t, err, "no puede estar vacía")
	}
	_, err := svc.Recall(ctx, Request{Query: strings.Repeat("á", maxQueryRunes+1)})
	wantErr(t, err, "el máximo es 1000")
}

func TestOptionsDefaultsAndLimit(t *testing.T) {
	svc := NewService(nil, &embedding.FakeEmbedder{}, nil, Options{TopicThreshold: 0.5})
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

		svc := e.service(e.emb, Options{TopicThreshold: lowThreshold})
		res := e.recall(t, svc, Request{Query: "último entrenamiento"})

		if res.Topic == nil || res.Topic.Slug != "entrenamiento" || !res.TopicDetected {
			t.Fatalf("Topic = %+v, TopicDetected = %v; se esperaba entrenamiento detectado", res.Topic, res.TopicDetected)
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
		svc := e.service(e.emb, Options{TopicThreshold: highThreshold})
		res := e.recall(t, svc, Request{Query: "azúcar", Topics: []string{"  ", "Inexistente", "CAFÉ"}})
		if res.Topic == nil || res.Topic.Slug != "cafe" || res.TopicDetected {
			t.Fatalf("Topic = %+v, TopicDetected = %v; se esperaba cafe pedido", res.Topic, res.TopicDetected)
		}
		if got := idsOf(res.Profile); !slices.Equal(got, []int64{pref}) {
			t.Errorf("Profile = %v, se esperaba [%d]", got, pref)
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
		svc := e.service(e.emb, Options{TopicThreshold: highThreshold})
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
		svc := e.service(e.emb, Options{TopicThreshold: highThreshold})

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

		svc := e.service(unavailableEmbedder{dims: testDims}, Options{TopicThreshold: lowThreshold})
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
		svc := e.service(failingEmbedder{unavailableEmbedder{dims: testDims}}, Options{TopicThreshold: lowThreshold})
		_, err := svc.Recall(e.ctx, Request{Query: "algo"})
		wantErr(t, err, "fallo de prueba")
		if errors.Is(err, embedding.ErrUnavailable) {
			t.Error("el error no debería ser ErrUnavailable")
		}
	})

	run("caché: una consulta repetida no vuelve a calcular el embedding", func(t *testing.T, e *env) {
		e.save(t, memory.Item{Title: "Saludo", Content: "Hola mundo desde las pruebas", Kind: memory.KindNote})
		emb := &countingEmbedder{FakeEmbedder: e.emb}
		svc := e.service(emb, Options{TopicThreshold: highThreshold})

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
		svc := e.service(emb, Options{TopicThreshold: highThreshold, CacheSize: 1})
		e.recall(t, svc, Request{Query: "uno"})
		e.recall(t, svc, Request{Query: "dos"})
		e.recall(t, svc, Request{Query: "uno"})
		if n := emb.queries.Load(); n != 3 {
			t.Errorf("hubo %d llamadas al embedder, se esperaban 3", n)
		}
	})

	run("sin datos devuelve un resultado vacío", func(t *testing.T, e *env) {
		svc := e.service(e.emb, Options{TopicThreshold: lowThreshold})
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
		svc := e.service(e.emb, Options{TopicThreshold: highThreshold, MaxLimit: 4})
		if n := len(e.recall(t, svc, Request{Query: "contenido", Limit: 2}).Results); n != 2 {
			t.Errorf("con Limit 2 hubo %d resultados", n)
		}
		if n := len(e.recall(t, svc, Request{Query: "contenido", Limit: 50}).Results); n != 4 {
			t.Errorf("con Limit 50 y MaxLimit 4 hubo %d resultados, se esperaban 4", n)
		}
	})
}
