package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"lodan/internal/database"
	"lodan/internal/embedding"
	"lodan/internal/session"
	"lodan/internal/topic"
)

const (
	testDims      = 64
	testDup       = 0.7 // umbral bajo: con 64 dimensiones el embedder falso es ruidoso
	testTopicSim  = 0.85
	testIdle      = 30 * time.Minute
	testTimeout   = 90 * time.Second
	nearWordCount = 40
	// testEmbedTimeout es el plazo de embedding de los servicios normales: nunca vence.
	testEmbedTimeout = 30 * time.Second
)

// slowEmbedder simulates Ollama answering later than the embedding timeout:
// it waits for delay or for its context, like the real client does.
type slowEmbedder struct {
	embedding.FakeEmbedder
	delay time.Duration
}

func (e *slowEmbedder) EmbedDocuments(ctx context.Context, docs []embedding.Document) ([][]float32, error) {
	select {
	case <-time.After(e.delay):
		return e.FakeEmbedder.EmbedDocuments(ctx, docs)
	case <-ctx.Done():
		return nil, fmt.Errorf("prueba: petición cancelada: %w", ctx.Err())
	}
}

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

// env is the state of one subtest: a Service (with its own resolver and
// tracker) over the shared, freshly emptied database.
type env struct {
	ctx  context.Context
	pool *pgxpool.Pool
	emb  *embedding.FakeEmbedder
	mgr  *session.Manager
	tr   *session.Tracker
	svc  *Service
}

func newEnv(ctx context.Context, tc *database.TestCluster) *env {
	emb := &embedding.FakeEmbedder{Dimensions: testDims}
	mgr := session.NewManager(tc.Pool, testIdle)
	e := &env{ctx: ctx, pool: tc.Pool, emb: emb, mgr: mgr, tr: mgr.NewTracker("test")}
	e.svc = e.serviceWith(emb)
	return e
}

// serviceWith builds another Service over the same database that uses emb for
// the records (topics always use the fake embedder).
func (e *env) serviceWith(emb embedding.Embedder) *Service {
	return e.serviceWithTimeout(emb, testEmbedTimeout)
}

// serviceWithTimeout is serviceWith with a given embedding timeout.
func (e *env) serviceWithTimeout(emb embedding.Embedder, timeout time.Duration) *Service {
	res := topic.NewResolver(e.pool, e.emb, testTopicSim)
	return NewService(e.pool, emb, res, e.mgr, testDup, timeout)
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(e.ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("consulta %q falló: %v", sql, err)
	}
	return n
}

// save stores one item and returns its result.
func (e *env) save(t *testing.T, it Item) Saved {
	t.Helper()
	saved, err := e.svc.Remember(e.ctx, e.tr, []Item{it})
	if err != nil {
		t.Fatalf("Remember falló: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("Remember devolvió %d resultados, se esperaba 1", len(saved))
	}
	return saved[0]
}

func (e *env) get(t *testing.T, id int64) Full {
	t.Helper()
	got, err := e.svc.Get(e.ctx, []int64{id})
	if err != nil {
		t.Fatalf("Get(#%d) falló: %v", id, err)
	}
	if len(got) != 1 {
		t.Fatalf("Get(#%d) devolvió %d registros, se esperaba 1", id, len(got))
	}
	return got[0]
}

func (e *env) revise(t *testing.T, r Revision) string {
	t.Helper()
	msg, err := e.svc.Revise(e.ctx, e.tr, r)
	if err != nil {
		t.Fatalf("Revise(%+v) falló: %v", r, err)
	}
	return msg
}

// wantErr checks that err is not nil and contains substr.
func wantErr(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("se esperaba un error que contuviera %q, pero no hubo error", substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("error %q no contiene %q", err, substr)
	}
}

// words returns n distinct words with the given prefix ("ab0 ab1 ab2 ...").
func words(prefix string, n int) string {
	w := make([]string, n)
	for i := range w {
		w[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	return strings.Join(w, " ")
}

func note(title, content string) Item {
	return Item{Title: title, Content: content, Kind: KindNote, Topics: []string{"lodan"}}
}

// nearPair saves two records whose content differs in one word out of many
// and returns their results: b has a as a similar record.
func nearPair(t *testing.T, e *env) (a, b Saved) {
	t.Helper()
	a = e.save(t, note("Lista de términos", words("termino", nearWordCount)))
	b = e.save(t, note("Lista de términos", words("termino", nearWordCount-1)+" distinto"))
	if a.Duplicate || b.Duplicate {
		t.Fatalf("los registros casi iguales no deberían ser duplicados exactos: %+v %+v", a, b)
	}
	if len(b.Similar) == 0 || b.Similar[0].ID != a.ID {
		t.Fatalf("b debería tener a a=#%d como parecido, Similar = %+v", a.ID, b.Similar)
	}
	return a, b
}

func TestParseKind(t *testing.T) {
	for _, k := range []string{"fact", "preference", "decision", "event", "note", " Decision "} {
		if _, err := ParseKind(k); err != nil {
			t.Errorf("ParseKind(%q) dio error: %v", k, err)
		}
	}
	if got, _ := ParseKind(" Decision "); got != KindDecision {
		t.Errorf("ParseKind(\" Decision \") = %q, se esperaba decision", got)
	}
	for _, k := range []string{"", "otro", "facts"} {
		if _, err := ParseKind(k); err == nil {
			t.Errorf("ParseKind(%q) debería fallar", k)
		}
	}
}

func TestNormalizeContentAndHash(t *testing.T) {
	if got, want := NormalizeContent("  Hola\n\tMUNDO   Ñandú "), "hola mundo ñandú"; got != want {
		t.Errorf("NormalizeContent = %q, se esperaba %q", got, want)
	}
	a, b := ContentHash("Me gusta   entrenar\npor la MAÑANA"), ContentHash(" me gusta entrenar por la mañana ")
	if string(a) != string(b) {
		t.Error("contenidos equivalentes deberían tener el mismo hash")
	}
	if len(a) != 32 {
		t.Errorf("el hash tiene %d bytes, se esperaban 32", len(a))
	}
	if string(a) == string(ContentHash("otra cosa")) {
		t.Error("contenidos distintos no deberían tener el mismo hash")
	}
}

func TestService(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)

	// run ejecuta un subtest sobre tablas vacías, con un servicio nuevo (el id del
	// modelo de embeddings se cachea y Reset reinicia las identidades).
	run := func(name string, f func(t *testing.T, e *env)) {
		t.Run(name, func(t *testing.T) {
			tc.Reset(t)
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			f(t, newEnv(ctx, tc))
		})
	}

	// Criterio 1.
	run("guardar crea el registro y la sesión", func(t *testing.T, e *env) {
		occ := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
		saved, err := e.svc.Remember(e.ctx, e.tr, []Item{{
			Title: "Usar PostgreSQL", Content: "Se decide usar PostgreSQL con pgvector",
			Kind: KindDecision, Topics: []string{"lodan"}, OccurredAt: &occ,
		}})
		if err != nil {
			t.Fatalf("Remember falló: %v", err)
		}
		if len(saved) != 1 || saved[0].ID <= 0 || saved[0].Duplicate || saved[0].Pending {
			t.Fatalf("resultado inesperado: %+v", saved)
		}
		if saved[0].Title != "Usar PostgreSQL" {
			t.Errorf("Title = %q", saved[0].Title)
		}
		if len(saved[0].Topics) != 1 || saved[0].Topics[0].Topic.Slug != "lodan" {
			t.Errorf("Topics = %+v, se esperaba [lodan]", saved[0].Topics)
		}

		sid := e.tr.ID()
		if sid == "" {
			t.Fatal("el tracker debería tener una sesión tras el primer Remember")
		}
		if n := e.count(t, `SELECT count(*) FROM sessions WHERE id = $1`, sid); n != 1 {
			t.Errorf("hay %d sesiones con id %s, se esperaba 1", n, sid)
		}

		var status, kind, gotSession string
		var hasEmb bool
		var model *int16
		var gotOcc time.Time
		err = e.pool.QueryRow(e.ctx, `SELECT status::text, kind::text, session_id, embedding IS NOT NULL,
			embedding_model, occurred_at FROM memories WHERE id = $1`, saved[0].ID).
			Scan(&status, &kind, &gotSession, &hasEmb, &model, &gotOcc)
		if err != nil {
			t.Fatalf("no se pudo leer el registro: %v", err)
		}
		if status != "active" || kind != "decision" || gotSession != sid || !hasEmb || model == nil || !gotOcc.Equal(occ) {
			t.Errorf("registro inesperado: status=%s kind=%s session=%s embedding=%v model=%v occurred=%v",
				status, kind, gotSession, hasEmb, model, gotOcc)
		}
		if n := e.count(t, `SELECT count(*) FROM memory_topics WHERE memory_id = $1 AND active AND kind = 'decision' AND ts = $2`,
			saved[0].ID, occ); n != 1 {
			t.Errorf("memory_topics activos = %d, se esperaba 1", n)
		}
		if n := e.count(t, `SELECT count(*) FROM session_topics WHERE session_id = $1`, sid); n != 1 {
			t.Errorf("session_topics = %d, se esperaba 1", n)
		}
		if n := e.count(t, `SELECT count(*) FROM embedding_models WHERE name = 'fake' AND dims = $1`, testDims); n != 1 {
			t.Errorf("filas de embedding_models = %d, se esperaba 1", n)
		}
	})

	run("tema y fecha por defecto", func(t *testing.T, e *env) {
		saved := e.save(t, Item{Title: "Sin tema", Content: "contenido sin tema ni fecha", Kind: KindNote})
		if len(saved.Topics) != 1 || saved.Topics[0].Topic.Slug != "general" {
			t.Errorf("Topics = %+v, se esperaba [general]", saved.Topics)
		}
		var occ time.Time
		if err := e.pool.QueryRow(e.ctx, `SELECT occurred_at FROM memories WHERE id = $1`, saved.ID).Scan(&occ); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(occ); d < -time.Minute || d > time.Minute {
			t.Errorf("occurred_at = %v, se esperaba ahora", occ)
		}

		// Temas que se normalizan a vacío también caen en "general".
		saved = e.save(t, Item{Title: "Tema inválido", Content: "otro contenido distinto", Kind: KindNote, Topics: []string{"!!!", "  "}})
		if len(saved.Topics) != 1 || saved.Topics[0].Topic.Slug != "general" {
			t.Errorf("Topics = %+v, se esperaba [general]", saved.Topics)
		}
	})

	// Criterio 2.
	run("duplicado exacto", func(t *testing.T, e *env) {
		first := e.save(t, note("Horario", "Me gusta entrenar por la mañana temprano"))
		dup := e.save(t, note("Otro título", "  ME GUSTA   entrenar\npor la MAÑANA temprano "))
		if !dup.Duplicate || dup.ID != first.ID || dup.Title != "Horario" {
			t.Errorf("se esperaba duplicado de #%d (Horario), se obtuvo %+v", first.ID, dup)
		}
		if len(dup.Topics) != 0 || dup.Pending || len(dup.Similar) != 0 {
			t.Errorf("un duplicado no debería llevar temas, pendiente ni parecidos: %+v", dup)
		}
		if n := e.count(t, `SELECT count(*) FROM memories`); n != 1 {
			t.Errorf("hay %d registros, se esperaba 1", n)
		}
	})

	run("duplicado dentro del mismo lote", func(t *testing.T, e *env) {
		saved, err := e.svc.Remember(e.ctx, e.tr, []Item{
			note("Uno", "contenido repetido en el lote"),
			note("Dos", "Contenido   REPETIDO en el lote"),
		})
		if err != nil {
			t.Fatalf("Remember falló: %v", err)
		}
		if saved[0].Duplicate || !saved[1].Duplicate || saved[1].ID != saved[0].ID || saved[1].Title != "Uno" {
			t.Errorf("resultado inesperado: %+v", saved)
		}
		if n := e.count(t, `SELECT count(*) FROM memories`); n != 1 {
			t.Errorf("hay %d registros, se esperaba 1", n)
		}
	})

	// Criterio 3.
	run("la clave sustituye al anterior", func(t *testing.T, e *env) {
		old := e.save(t, Item{Title: "Horario", Content: "Entreno a las 7", Kind: KindFact,
			Topics: []string{"entrenamiento"}, Key: "entrenamiento/horario"})
		cur := e.save(t, Item{Title: "Horario", Content: "Ahora entreno por la tarde", Kind: KindFact,
			Topics: []string{"entrenamiento"}, Key: "entrenamiento/horario"})
		if cur.ID == old.ID || len(cur.Superseded) != 1 || cur.Superseded[0] != old.ID {
			t.Fatalf("Superseded = %v, se esperaba [%d]", cur.Superseded, old.ID)
		}
		if got := e.get(t, old.ID); got.Status != StatusSuperseded {
			t.Errorf("el antiguo tiene estado %s, se esperaba superseded", got.Status)
		}
		if got := e.get(t, cur.ID); got.Status != StatusActive || got.Key != "entrenamiento/horario" {
			t.Errorf("el nuevo: estado %s, clave %q", got.Status, got.Key)
		}
		if n := e.count(t, `SELECT count(*) FROM memory_relations
			WHERE source_id = $1 AND target_id = $2 AND kind = 'supersedes' AND state = 'confirmed'`, cur.ID, old.ID); n != 1 {
			t.Errorf("relaciones supersedes confirmadas = %d, se esperaba 1", n)
		}
		if n := e.count(t, `SELECT count(*) FROM memory_topics WHERE memory_id = $1 AND active`, old.ID); n != 0 {
			t.Errorf("el antiguo conserva %d temas activos", n)
		}
		if n := e.count(t, `SELECT count(*) FROM memory_topics WHERE memory_id = $1 AND active`, cur.ID); n != 1 {
			t.Errorf("el nuevo tiene %d temas activos, se esperaba 1", n)
		}
		// El sustituido no debe salir como parecido aunque el contenido se parezca.
		if len(cur.Similar) != 0 {
			for _, n := range cur.Similar {
				if n.ID == old.ID {
					t.Errorf("el registro sustituido no debería salir como parecido: %+v", cur.Similar)
				}
			}
		}
	})

	// Criterio 4.
	run("un contenido casi igual sugiere una relación", func(t *testing.T, e *env) {
		a, b := nearPair(t, e)
		if b.Similar[0].Title != "Lista de términos" || b.Similar[0].Similarity < testDup {
			t.Errorf("parecido inesperado: %+v", b.Similar[0])
		}
		if len(a.Similar) != 0 {
			t.Errorf("el primero no debería tener parecidos: %+v", a.Similar)
		}
		if n := e.count(t, `SELECT count(*) FROM memory_relations
			WHERE source_id = $1 AND target_id = $2 AND kind = 'related' AND state = 'suggested'`, b.ID, a.ID); n != 1 {
			t.Errorf("relaciones related suggested = %d, se esperaba 1", n)
		}

		// Un contenido sin palabras en común no se sugiere.
		c := e.save(t, note("Otra cosa", words("distinto", nearWordCount)))
		if len(c.Similar) != 0 {
			t.Errorf("un contenido distinto no debería tener parecidos: %+v", c.Similar)
		}
	})

	// Criterio 5.
	run("sin Ollama queda pendiente y FillPending lo rellena", func(t *testing.T, e *env) {
		down := e.serviceWith(unavailableEmbedder{dims: testDims})
		var ids []int64
		for batch := 0; batch < 2; batch++ {
			items := make([]Item, 20)
			for i := range items {
				n := batch*20 + i
				items[i] = note(fmt.Sprintf("Pendiente %d", n), words(fmt.Sprintf("p%dx", n), 5))
			}
			saved, err := down.Remember(e.ctx, e.tr, items)
			if err != nil {
				t.Fatalf("Remember sin Ollama falló: %v", err)
			}
			for _, s := range saved {
				if !s.Pending || s.ID <= 0 || len(s.Similar) != 0 {
					t.Fatalf("se esperaba un registro pendiente: %+v", s)
				}
				ids = append(ids, s.ID)
			}
		}
		if n := e.count(t, `SELECT count(*) FROM memories WHERE embedding IS NULL AND embedding_model IS NULL`); n != 40 {
			t.Fatalf("hay %d registros pendientes, se esperaban 40", n)
		}

		// Con Ollama caído, FillPending no rellena nada y devuelve el error.
		n, err := down.FillPending(e.ctx, 10)
		if n != 0 || !errors.Is(err, embedding.ErrUnavailable) {
			t.Fatalf("FillPending sin Ollama = %d, %v; se esperaba 0 y ErrUnavailable", n, err)
		}
		if got := e.count(t, `SELECT count(*) FROM memories WHERE embedding IS NULL`); got != 40 {
			t.Fatalf("tras el fallo hay %d pendientes, se esperaban 40", got)
		}

		// Con Ollama disponible: un lote limitado, luego el resto en varios lotes.
		if n, err := e.svc.FillPending(e.ctx, 5); err != nil || n != 5 {
			t.Fatalf("FillPending(5) = %d, %v", n, err)
		}
		if n, err := e.svc.FillPending(e.ctx, 100); err != nil || n != 35 {
			t.Fatalf("FillPending(100) = %d, %v; se esperaban 35", n, err)
		}
		if n, err := e.svc.FillPending(e.ctx, 100); err != nil || n != 0 {
			t.Fatalf("FillPending sin pendientes = %d, %v", n, err)
		}
		if got := e.count(t, `SELECT count(*) FROM memories WHERE embedding IS NULL OR embedding_model IS NULL`); got != 0 {
			t.Errorf("quedan %d registros sin embedding o sin modelo", got)
		}
		if len(ids) != 40 {
			t.Errorf("se guardaron %d ids, se esperaban 40", len(ids))
		}
	})

	// Criterio 5 (plazo de embedding).
	run("un embedding más lento que el plazo deja el registro pendiente", func(t *testing.T, e *env) {
		slow := e.serviceWithTimeout(&slowEmbedder{FakeEmbedder: embedding.FakeEmbedder{Dimensions: testDims}, delay: 30 * time.Second}, 100*time.Millisecond)
		start := time.Now()
		saved, err := slow.Remember(e.ctx, e.tr, []Item{note("Lento", words("lento", 5))})
		if err != nil {
			t.Fatalf("Remember con Ollama lento falló: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Errorf("Remember tardó %v: no respetó el plazo de embedding", elapsed)
		}
		if len(saved) != 1 || !saved[0].Pending || saved[0].ID <= 0 || len(saved[0].Similar) != 0 {
			t.Fatalf("se esperaba un registro pendiente: %+v", saved)
		}
		if n := e.count(t, `SELECT count(*) FROM memories WHERE id = $1 AND embedding IS NULL AND embedding_model IS NULL`, saved[0].ID); n != 1 {
			t.Fatalf("el registro #%d debería tener el embedding NULL", saved[0].ID)
		}

		// El cálculo se completa después, con un embedder normal.
		if n, err := e.svc.FillPending(e.ctx, 10); err != nil || n != 1 {
			t.Fatalf("FillPending = %d, %v; se esperaba 1", n, err)
		}
	})

	// Criterio 5 (parecidos en segundo plano).
	run("FillPending sugiere los parecidos de un registro pendiente", func(t *testing.T, e *env) {
		a := e.save(t, note("Lista de términos", words("termino", nearWordCount)))
		down := e.serviceWith(unavailableEmbedder{dims: testDims})
		saved, err := down.Remember(e.ctx, e.tr, []Item{note("Lista de términos", words("termino", nearWordCount-1)+" distinto")})
		if err != nil || len(saved) != 1 || !saved[0].Pending {
			t.Fatalf("Remember sin Ollama = %+v, %v; se esperaba un registro pendiente", saved, err)
		}
		b := saved[0]

		// Un pendiente que deja de estar vigente antes de completarse no recibe relaciones.
		savedInv, err := down.Remember(e.ctx, e.tr, []Item{note("Lista de términos", words("termino", nearWordCount-1)+" invalidado")})
		if err != nil || len(savedInv) != 1 || !savedInv[0].Pending {
			t.Fatalf("Remember sin Ollama = %+v, %v; se esperaba un registro pendiente", savedInv, err)
		}
		e.revise(t, Revision{ID: savedInv[0].ID, Action: "invalidate"})

		if n := e.count(t, `SELECT count(*) FROM memory_relations`); n != 0 {
			t.Fatalf("antes de FillPending hay %d relaciones, se esperaban 0", n)
		}
		if n, err := e.svc.FillPending(e.ctx, 10); err != nil || n != 2 {
			t.Fatalf("FillPending = %d, %v; se esperaban 2", n, err)
		}
		if n := e.count(t, `SELECT count(*) FROM memory_relations
			WHERE source_id = $1 AND target_id = $2 AND kind = 'related' AND state = 'suggested'`, b.ID, a.ID); n != 1 {
			t.Errorf("relaciones related suggested #%d -> #%d = %d, se esperaba 1", b.ID, a.ID, n)
		}
		if n := e.count(t, `SELECT count(*) FROM memory_relations WHERE source_id = $1`, savedInv[0].ID); n != 0 {
			t.Errorf("el registro invalidado tiene %d relaciones, se esperaban 0", n)
		}
		if n := e.count(t, `SELECT count(*) FROM memory_relations WHERE source_id = $1 OR target_id = $1`, b.ID); n != 1 {
			t.Errorf("el registro #%d tiene %d relaciones, se esperaba solo 1 (con el vigente)", b.ID, n)
		}

		// Ejecutarlo otra vez no duplica nada.
		if n, err := e.svc.FillPending(e.ctx, 10); err != nil || n != 0 {
			t.Fatalf("segundo FillPending = %d, %v; se esperaba 0", n, err)
		}
	})

	// Criterio 11.
	run("get con relaciones", func(t *testing.T, e *env) {
		a := e.save(t, Item{Title: "Lista de términos", Content: words("termino", nearWordCount), Kind: KindFact,
			Topics: []string{"zeta-tema-largo", "alfa-tema-largo"}, Key: "lista/terminos"})
		b := e.save(t, note("Lista de términos", words("termino", nearWordCount-1)+" distinto"))
		if len(b.Similar) == 0 {
			t.Fatalf("b debería tener parecidos: %+v", b)
		}

		got, err := e.svc.Get(e.ctx, []int64{b.ID, 9999, a.ID, b.ID})
		if err != nil {
			t.Fatalf("Get falló: %v", err)
		}
		if len(got) != 2 || got[0].ID != b.ID || got[1].ID != a.ID {
			t.Fatalf("Get debería devolver [b, a] en ese orden y omitir el que no existe: %+v", got)
		}

		fa, fb := got[1], got[0]
		if fa.Kind != KindFact || fa.Status != StatusActive || fa.Key != "lista/terminos" ||
			fa.Content != words("termino", nearWordCount) || fa.SessionID != e.tr.ID() {
			t.Errorf("campos de a inesperados: %+v", fa)
		}
		if !sort.StringsAreSorted(fa.Topics) || len(fa.Topics) != 2 {
			t.Errorf("los temas de a deberían ser 2 y estar ordenados: %v", fa.Topics)
		}
		if fa.CreatedAt.IsZero() || fa.UpdatedAt.IsZero() || fa.OccurredAt.IsZero() {
			t.Errorf("faltan fechas en a: %+v", fa)
		}

		wantOut := Relation{Kind: "related", State: "suggested", OtherID: a.ID, OtherTitle: "Lista de términos", Outgoing: true}
		wantIn := Relation{Kind: "related", State: "suggested", OtherID: b.ID, OtherTitle: "Lista de términos", Outgoing: false}
		if !containsRelation(fb.Relations, wantOut) {
			t.Errorf("b debería tener %+v en %+v", wantOut, fb.Relations)
		}
		if !containsRelation(fa.Relations, wantIn) {
			t.Errorf("a debería tener %+v en %+v", wantIn, fa.Relations)
		}

		if _, err := e.svc.Get(e.ctx, nil); err == nil {
			t.Error("Get sin ids debería fallar")
		}
		many := make([]int64, 21)
		for i := range many {
			many[i] = int64(i + 1)
		}
		wantErr(t, func() error { _, err := e.svc.Get(e.ctx, many); return err }(), "máximo")

		none, err := e.svc.Get(e.ctx, []int64{424242})
		if err != nil || len(none) != 0 {
			t.Errorf("Get de un id inexistente = %v, %v; se esperaba vacío", none, err)
		}
	})

	// Criterio 12.
	run("revise update", func(t *testing.T, e *env) {
		a := e.save(t, note("Título viejo", words("viejo", 8)))
		before := e.get(t, a.ID)
		newContent := words("nuevo", 20)

		msg := e.revise(t, Revision{ID: a.ID, Action: "update", Title: "Título nuevo", Content: newContent, Topics: []string{"otro-tema"}})
		if !strings.Contains(msg, fmt.Sprintf("#%d", a.ID)) {
			t.Errorf("el mensaje %q debería citar el id", msg)
		}
		got := e.get(t, a.ID)
		if got.Title != "Título nuevo" || got.Content != newContent {
			t.Errorf("título o contenido sin actualizar: %+v", got)
		}
		if len(got.Topics) != 1 || got.Topics[0] != "otro-tema" {
			t.Errorf("Topics = %v, se esperaba [otro-tema]", got.Topics)
		}
		if !got.UpdatedAt.After(before.UpdatedAt) {
			t.Errorf("updated_at no avanzó: %v -> %v", before.UpdatedAt, got.UpdatedAt)
		}
		if n := e.count(t, `SELECT count(*) FROM memories WHERE id = $1 AND content_hash = $2 AND embedding IS NOT NULL`,
			a.ID, ContentHash(newContent)); n != 1 {
			t.Error("el hash o el embedding no se actualizaron")
		}
		if n := e.count(t, `SELECT count(*) FROM memory_topics WHERE memory_id = $1 AND active`, a.ID); n != 1 {
			t.Errorf("memory_topics activos = %d, se esperaba 1", n)
		}

		// Solo el título: el contenido no cambia.
		e.revise(t, Revision{ID: a.ID, Action: "update", Title: "Solo título"})
		if got := e.get(t, a.ID); got.Title != "Solo título" || got.Content != newContent {
			t.Errorf("update de solo título: %+v", got)
		}

		// Un contenido que ya tiene otro registro activo se rechaza.
		b := e.save(t, note("Otro", words("ajeno", 8)))
		_, err := e.svc.Revise(e.ctx, e.tr, Revision{ID: b.ID, Action: "update", Content: strings.ToUpper(newContent)})
		wantErr(t, err, fmt.Sprintf("ya existe un registro igual #%d", a.ID))

		// Errores.
		_, err = e.svc.Revise(e.ctx, nil, Revision{ID: a.ID, Action: "update"})
		wantErr(t, err, "al menos")
		_, err = e.svc.Revise(e.ctx, nil, Revision{ID: 9999, Action: "update", Title: "x"})
		wantErr(t, err, "#9999 no existe")
		_, err = e.svc.Revise(e.ctx, nil, Revision{ID: a.ID, Action: "update", Title: strings.Repeat("x", 201)})
		wantErr(t, err, "título")
		_, err = e.svc.Revise(e.ctx, nil, Revision{ID: a.ID, Action: "update", Topics: []string{"!!!"}})
		wantErr(t, err, "temas")
		e.revise(t, Revision{ID: b.ID, Action: "invalidate"})
		_, err = e.svc.Revise(e.ctx, nil, Revision{ID: b.ID, Action: "update", Title: "x"})
		wantErr(t, err, "no está vigente")
	})

	run("revise update sin Ollama deja el embedding pendiente", func(t *testing.T, e *env) {
		a := e.save(t, note("Título", words("antes", 6)))
		down := e.serviceWith(unavailableEmbedder{dims: testDims})
		msg, err := down.Revise(e.ctx, nil, Revision{ID: a.ID, Action: "update", Content: words("despues", 6)})
		if err != nil {
			t.Fatalf("Revise falló: %v", err)
		}
		if !strings.Contains(msg, "pendiente") {
			t.Errorf("el mensaje %q debería avisar del embedding pendiente", msg)
		}
		if n := e.count(t, `SELECT count(*) FROM memories WHERE id = $1 AND embedding IS NULL AND embedding_model IS NULL`, a.ID); n != 1 {
			t.Error("el embedding debería quedar NULL")
		}
		if n, err := e.svc.FillPending(e.ctx, 10); err != nil || n != 1 {
			t.Errorf("FillPending = %d, %v; se esperaba 1", n, err)
		}
	})

	run("revise supersede", func(t *testing.T, e *env) {
		old := e.save(t, note("Viejo", words("viejo", 8)))
		cur := e.save(t, note("Nuevo", words("nuevo", 8)))

		msg := e.revise(t, Revision{ID: old.ID, Action: "supersede", WithID: cur.ID})
		if !strings.Contains(msg, fmt.Sprintf("#%d", old.ID)) || !strings.Contains(msg, fmt.Sprintf("#%d", cur.ID)) {
			t.Errorf("el mensaje %q debería citar los dos ids", msg)
		}
		if got := e.get(t, old.ID); got.Status != StatusSuperseded {
			t.Errorf("estado del antiguo = %s", got.Status)
		}
		if got := e.get(t, cur.ID); got.Status != StatusActive {
			t.Errorf("estado del nuevo = %s", got.Status)
		}
		if n := e.count(t, `SELECT count(*) FROM memory_relations
			WHERE source_id = $1 AND target_id = $2 AND kind = 'supersedes' AND state = 'confirmed'`, cur.ID, old.ID); n != 1 {
			t.Errorf("relaciones supersedes = %d, se esperaba 1", n)
		}
		if n := e.count(t, `SELECT count(*) FROM memory_topics WHERE memory_id = $1 AND active`, old.ID); n != 0 {
			t.Errorf("el antiguo conserva %d temas activos", n)
		}

		_, err := e.svc.Revise(e.ctx, nil, Revision{ID: old.ID, Action: "supersede", WithID: cur.ID})
		wantErr(t, err, "no está vigente")
		other := e.save(t, note("Otro", words("otro", 8)))
		_, err = e.svc.Revise(e.ctx, nil, Revision{ID: other.ID, Action: "supersede", WithID: old.ID})
		wantErr(t, err, "debe estar vigente")
		_, err = e.svc.Revise(e.ctx, nil, Revision{ID: other.ID, Action: "supersede", WithID: 9999})
		wantErr(t, err, "#9999 no existe")
		_, err = e.svc.Revise(e.ctx, nil, Revision{ID: other.ID, Action: "supersede", WithID: other.ID})
		wantErr(t, err, "sí mismo")
		_, err = e.svc.Revise(e.ctx, nil, Revision{ID: other.ID, Action: "supersede"})
		wantErr(t, err, "with_id")
	})

	run("revise invalidate", func(t *testing.T, e *env) {
		a := e.save(t, note("A invalidar", words("inval", 8)))
		e.revise(t, Revision{ID: a.ID, Action: "invalidate"})
		if got := e.get(t, a.ID); got.Status != StatusInvalidated {
			t.Errorf("estado = %s, se esperaba invalidated", got.Status)
		}
		if n := e.count(t, `SELECT count(*) FROM memory_topics WHERE memory_id = $1 AND active`, a.ID); n != 0 {
			t.Errorf("quedan %d temas activos", n)
		}
		if msg := e.revise(t, Revision{ID: a.ID, Action: "invalidate"}); !strings.Contains(msg, "ya estaba") {
			t.Errorf("el mensaje %q debería indicar que ya estaba invalidado", msg)
		}
		_, err := e.svc.Revise(e.ctx, nil, Revision{ID: 9999, Action: "invalidate"})
		wantErr(t, err, "#9999 no existe")
	})

	run("revise delete", func(t *testing.T, e *env) {
		a, b := nearPair(t, e) // hay una relación y temas asociados
		_, err := e.svc.Revise(e.ctx, e.tr, Revision{ID: a.ID, Action: "delete"})
		wantErr(t, err, "para borrar definitivamente hay que confirmar (confirm: true)")
		if n := e.count(t, `SELECT count(*) FROM memories WHERE id = $1`, a.ID); n != 1 {
			t.Fatal("el registro no debería haberse borrado sin confirmar")
		}

		e.revise(t, Revision{ID: a.ID, Action: "delete", Confirm: true})
		if n := e.count(t, `SELECT count(*) FROM memories WHERE id = $1`, a.ID); n != 0 {
			t.Error("el registro debería haberse borrado")
		}
		if n := e.count(t, `SELECT count(*) FROM memory_relations WHERE source_id = $1 OR target_id = $1`, a.ID); n != 0 {
			t.Errorf("quedan %d relaciones", n)
		}
		if n := e.count(t, `SELECT count(*) FROM memory_topics WHERE memory_id = $1`, a.ID); n != 0 {
			t.Errorf("quedan %d temas asociados", n)
		}
		if n := e.count(t, `SELECT count(*) FROM memories WHERE id = $1`, b.ID); n != 1 {
			t.Error("el otro registro no debería borrarse")
		}
		_, err = e.svc.Revise(e.ctx, nil, Revision{ID: a.ID, Action: "delete", Confirm: true})
		wantErr(t, err, fmt.Sprintf("#%d no existe", a.ID))
	})

	// Criterio 13.
	run("revise relate", func(t *testing.T, e *env) {
		a := e.save(t, note("A", words("aaa", 8)))
		b := e.save(t, note("B", words("bbb", 8)))

		e.revise(t, Revision{ID: a.ID, Action: "relate", WithID: b.ID, Relation: "contradicts"})
		if n := e.count(t, `SELECT count(*) FROM memory_relations
			WHERE source_id = $1 AND target_id = $2 AND kind = 'contradicts' AND state = 'confirmed'`, a.ID, b.ID); n != 1 {
			t.Errorf("relaciones contradicts confirmadas = %d, se esperaba 1", n)
		}
		e.revise(t, Revision{ID: a.ID, Action: "relate", WithID: b.ID}) // por defecto related
		e.revise(t, Revision{ID: a.ID, Action: "relate", WithID: b.ID}) // idempotente
		if n := e.count(t, `SELECT count(*) FROM memory_relations WHERE source_id = $1 AND target_id = $2 AND kind = 'related'`, a.ID, b.ID); n != 1 {
			t.Errorf("relaciones related = %d, se esperaba 1", n)
		}

		_, err := e.svc.Revise(e.ctx, nil, Revision{ID: a.ID, Action: "relate", WithID: b.ID, Relation: "supersedes"})
		wantErr(t, err, "no válida")
		_, err = e.svc.Revise(e.ctx, nil, Revision{ID: a.ID, Action: "relate", WithID: 9999})
		wantErr(t, err, "#9999 no existe")
		_, err = e.svc.Revise(e.ctx, nil, Revision{ID: a.ID, Action: "relate"})
		wantErr(t, err, "with_id")
		_, err = e.svc.Revise(e.ctx, nil, Revision{ID: a.ID, Action: "relate", WithID: a.ID})
		wantErr(t, err, "consigo mismo")
	})

	run("revise relate confirma una sugerencia", func(t *testing.T, e *env) {
		a, b := nearPair(t, e)
		e.revise(t, Revision{ID: b.ID, Action: "relate", WithID: a.ID})
		if n := e.count(t, `SELECT count(*) FROM memory_relations
			WHERE source_id = $1 AND target_id = $2 AND kind = 'related' AND state = 'confirmed'`, b.ID, a.ID); n != 1 {
			t.Errorf("la sugerencia debería quedar confirmada (%d)", n)
		}
	})

	run("revise confirm_relation y reject_relation", func(t *testing.T, e *env) {
		a, b := nearPair(t, e) // relación related suggested b -> a

		// En el sentido contrario al de la relación.
		e.revise(t, Revision{ID: a.ID, Action: "confirm_relation", WithID: b.ID})
		if n := e.count(t, `SELECT count(*) FROM memory_relations WHERE source_id = $1 AND target_id = $2 AND state = 'confirmed'`, b.ID, a.ID); n != 1 {
			t.Errorf("la relación debería estar confirmada (%d)", n)
		}
		// En el mismo sentido.
		e.revise(t, Revision{ID: b.ID, Action: "reject_relation", WithID: a.ID, Relation: "related"})
		if n := e.count(t, `SELECT count(*) FROM memory_relations WHERE source_id = $1 AND target_id = $2 AND state = 'rejected'`, b.ID, a.ID); n != 1 {
			t.Errorf("la relación debería estar rechazada (%d)", n)
		}

		_, err := e.svc.Revise(e.ctx, nil, Revision{ID: a.ID, Action: "confirm_relation", WithID: b.ID, Relation: "contradicts"})
		wantErr(t, err, "no existe una relación contradicts")
		_, err = e.svc.Revise(e.ctx, nil, Revision{ID: a.ID, Action: "reject_relation", WithID: 9999})
		wantErr(t, err, "no existe una relación")
		_, err = e.svc.Revise(e.ctx, nil, Revision{ID: a.ID, Action: "confirm_relation"})
		wantErr(t, err, "with_id")
		_, err = e.svc.Revise(e.ctx, nil, Revision{ID: a.ID, Action: "confirm_relation", WithID: b.ID, Relation: "raro"})
		wantErr(t, err, "no válida")
	})

	run("revise acción o id no válidos", func(t *testing.T, e *env) {
		a := e.save(t, note("A", words("aaa", 8)))
		_, err := e.svc.Revise(e.ctx, nil, Revision{ID: a.ID, Action: "explotar"})
		wantErr(t, err, "no válida")
		_, err = e.svc.Revise(e.ctx, nil, Revision{Action: "invalidate"})
		wantErr(t, err, "id")
	})

	run("validaciones de Remember", func(t *testing.T, e *env) {
		many := make([]Item, 21)
		for i := range many {
			many[i] = note(fmt.Sprintf("T%d", i), words(fmt.Sprintf("m%dx", i), 3))
		}
		cases := []struct {
			name  string
			items []Item
			want  string
		}{
			{"sin ítems", nil, "al menos un ítem"},
			{"demasiados ítems", many, "el máximo es 20"},
			{"título vacío", []Item{{Title: "   ", Content: "x", Kind: KindNote}}, "título"},
			{"título largo", []Item{{Title: strings.Repeat("ñ", 201), Content: "x", Kind: KindNote}}, "título"},
			{"contenido vacío", []Item{{Title: "t", Content: " \n ", Kind: KindNote}}, "contenido"},
			{"contenido largo", []Item{{Title: "t", Content: strings.Repeat("ñ", 8001), Kind: KindNote}}, "contenido"},
			{"tipo no válido", []Item{{Title: "t", Content: "x", Kind: "otro"}}, "tipo de registro"},
			{"tipo vacío", []Item{{Title: "t", Content: "x"}}, "tipo de registro"},
			{"el error indica el ítem", []Item{note("ok", "contenido válido"), {Title: "t", Content: "x"}}, "ítem 2"},
		}
		for _, c := range cases {
			_, err := e.svc.Remember(e.ctx, e.tr, c.items)
			wantErr(t, err, c.want)
		}
		if n := e.count(t, `SELECT count(*) FROM memories`); n != 0 {
			t.Errorf("una validación fallida no debe guardar nada (%d registros)", n)
		}

		// Los límites exactos se aceptan (en caracteres, no en bytes).
		saved := e.save(t, Item{Title: strings.Repeat("ñ", 200), Content: strings.Repeat("ñ", 8000), Kind: KindNote})
		if saved.ID <= 0 {
			t.Errorf("los límites exactos deberían aceptarse: %+v", saved)
		}
	})

	run("lote con varios ítems y temas equivalentes", func(t *testing.T, e *env) {
		saved, err := e.svc.Remember(e.ctx, e.tr, []Item{
			{Title: "Uno", Content: words("uno", 6), Kind: KindFact, Topics: []string{"mi-coche", "Mi Coche", "otro-tema"}},
			{Title: "Dos", Content: words("dos", 6), Kind: KindEvent, Topics: []string{"otro-tema"}},
		})
		if err != nil {
			t.Fatalf("Remember falló: %v", err)
		}
		if len(saved) != 2 || saved[0].ID == saved[1].ID {
			t.Fatalf("resultado inesperado: %+v", saved)
		}
		if len(saved[0].Topics) != 2 || len(saved[1].Topics) != 1 || saved[1].Topics[0].Topic.Slug != "otro-tema" {
			t.Errorf("temas inesperados: %+v", saved)
		}
		if n := e.count(t, `SELECT count(*) FROM memory_topics WHERE active`); n != 3 {
			t.Errorf("memory_topics = %d, se esperaban 3", n)
		}
		if n := e.count(t, `SELECT count(*) FROM session_topics`); n != 2 {
			t.Errorf("session_topics = %d, se esperaban 2", n)
		}
	})

	run("Remember sin tracker no crea sesión", func(t *testing.T, e *env) {
		saved, err := e.svc.Remember(e.ctx, nil, []Item{note("Sin sesión", words("ss", 5))})
		if err != nil || len(saved) != 1 {
			t.Fatalf("Remember falló: %v", err)
		}
		if got := e.get(t, saved[0].ID); got.SessionID != "" {
			t.Errorf("SessionID = %q, se esperaba vacío", got.SessionID)
		}
		if n := e.count(t, `SELECT count(*) FROM sessions`); n != 0 {
			t.Errorf("hay %d sesiones, se esperaba 0", n)
		}
	})

	run("Nearest no devuelve registros inactivos", func(t *testing.T, e *env) {
		a, b := nearPair(t, e)
		vec := vectorOf(t, e, "Lista de términos", words("termino", nearWordCount-1)+" distinto")

		near, err := Nearest(e.ctx, e.pool, vec, testDims, 50, 5, 0.5, nil)
		if err != nil {
			t.Fatalf("Nearest falló: %v", err)
		}
		if len(near) != 2 || near[0].ID != b.ID || near[1].ID != a.ID {
			t.Fatalf("Nearest = %+v, se esperaba [b, a]", near)
		}
		if near[0].Similarity < 0.99 || near[0].Similarity > 1.0001 || near[1].Similarity >= near[0].Similarity {
			t.Errorf("similitudes inesperadas: %+v", near)
		}

		// Excluir ids y umbral.
		near, err = Nearest(e.ctx, e.pool, vec, testDims, 50, 5, 0.5, []int64{b.ID})
		if err != nil || len(near) != 1 || near[0].ID != a.ID {
			t.Errorf("Nearest excluyendo b = %+v, %v", near, err)
		}
		if near, err = Nearest(e.ctx, e.pool, vec, testDims, 50, 5, 0.9999, []int64{a.ID}); err != nil || len(near) != 1 {
			t.Errorf("Nearest con umbral alto = %+v, %v", near, err)
		}
		if near, err = Nearest(e.ctx, e.pool, vec, testDims, 50, 1, 0.5, nil); err != nil || len(near) != 1 || near[0].ID != b.ID {
			t.Errorf("Nearest con límite 1 = %+v, %v", near, err)
		}

		// Dentro de una transacción del llamador.
		tx, err := e.pool.Begin(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		near, err = Nearest(e.ctx, tx, vec, testDims, 50, 5, 0.5, nil)
		_ = tx.Rollback(e.ctx)
		if err != nil || len(near) != 2 {
			t.Errorf("Nearest en transacción = %+v, %v", near, err)
		}

		// Los registros sustituidos e invalidados desaparecen.
		e.revise(t, Revision{ID: a.ID, Action: "invalidate"})
		near, err = Nearest(e.ctx, e.pool, vec, testDims, 50, 5, 0.5, nil)
		if err != nil || len(near) != 1 || near[0].ID != b.ID {
			t.Errorf("tras invalidar a: %+v, %v", near, err)
		}
		c := e.save(t, note("Otro", words("otro", 10)))
		e.revise(t, Revision{ID: b.ID, Action: "supersede", WithID: c.ID})
		near, err = Nearest(e.ctx, e.pool, vec, testDims, 50, 5, 0.5, nil)
		if err != nil || len(near) != 0 {
			t.Errorf("tras sustituir b: %+v, %v", near, err)
		}

		// Errores de uso.
		if _, err := Nearest(e.ctx, e.pool, vec[:10], testDims, 50, 5, 0.5, nil); err == nil {
			t.Error("un vector de otra dimensión debería fallar")
		}
		if _, err := Nearest(e.ctx, struct{ database.Querier }{}, vec, testDims, 50, 5, 0.5, nil); err == nil {
			t.Error("un Querier que no es pool ni transacción debería fallar")
		}
	})

	run("Nearest ignora los registros pendientes", func(t *testing.T, e *env) {
		down := e.serviceWith(unavailableEmbedder{dims: testDims})
		if _, err := down.Remember(e.ctx, e.tr, []Item{note("Pendiente", words("pend", 10))}); err != nil {
			t.Fatal(err)
		}
		vec := vectorOf(t, e, "Pendiente", words("pend", 10))
		near, err := Nearest(e.ctx, e.pool, vec, testDims, 50, 5, 0, nil)
		if err != nil || len(near) != 0 {
			t.Errorf("Nearest = %+v, %v; se esperaba vacío", near, err)
		}
	})
}

// vectorOf embeds a document with the fake embedder.
func vectorOf(t *testing.T, e *env, title, text string) []float32 {
	t.Helper()
	vecs, err := e.emb.EmbedDocuments(e.ctx, []embedding.Document{{Title: title, Text: text}})
	if err != nil || len(vecs) != 1 {
		t.Fatalf("no se pudo calcular el vector: %v", err)
	}
	return vecs[0]
}

func containsRelation(rels []Relation, want Relation) bool {
	for _, r := range rels {
		if r == want {
			return true
		}
	}
	return false
}
