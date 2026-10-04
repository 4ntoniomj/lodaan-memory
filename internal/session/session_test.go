package session

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"lodan/internal/database"
)

// testIdle is the idle time used by the tests.
const testIdle = 30 * time.Minute

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newTestManager returns a Manager driven by clk.
func newTestManager(pool *pgxpool.Pool, clk *fakeClock) *Manager {
	m := NewManager(pool, testIdle)
	m.now = clk.Now
	return m
}

// row is a sessions row as seen directly in the database.
type row struct {
	Client   string
	Started  time.Time
	Activity time.Time
	Ended    *time.Time
	Summary  *string
}

func fetch(t *testing.T, pool *pgxpool.Pool, id string) row {
	t.Helper()
	var r row
	err := pool.QueryRow(context.Background(),
		`SELECT client, started_at, last_activity_at, ended_at, summary FROM sessions WHERE id = $1`, id).
		Scan(&r.Client, &r.Started, &r.Activity, &r.Ended, &r.Summary)
	if err != nil {
		t.Fatalf("no se pudo leer la sesión %q: %v", id, err)
	}
	return r
}

// count runs a query that returns a single integer.
func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("consulta %q falló: %v", sql, err)
	}
	return n
}

func countSessions(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	return count(t, pool, `SELECT count(*) FROM sessions`)
}

// insertTopic creates a topic directly, without depending on the topic package.
func insertTopic(t *testing.T, pool *pgxpool.Pool, slug string) int32 {
	t.Helper()
	var id int32
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO topics (slug) VALUES ($1) RETURNING id`, slug).Scan(&id); err != nil {
		t.Fatalf("no se pudo crear el tema %q: %v", slug, err)
	}
	return id
}

// insertSession creates a session directly; its last activity equals its start.
func insertSession(t *testing.T, pool *pgxpool.Pool, id, client string, started time.Time, ended *time.Time, summary string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO sessions (id, client, started_at, last_activity_at, ended_at, summary)
		VALUES ($1, $2, $3, $3, $4, NULLIF($5::text, ''))`, id, client, started, ended, summary)
	if err != nil {
		t.Fatalf("no se pudo insertar la sesión %q: %v", id, err)
	}
}

func linkTopics(t *testing.T, pool *pgxpool.Pool, sessionID string, topicIDs ...int32) {
	t.Helper()
	for _, tid := range topicIDs {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO session_topics (session_id, topic_id) VALUES ($1, $2)`, sessionID, tid); err != nil {
			t.Fatalf("no se pudo asociar el tema %d a %q: %v", tid, sessionID, err)
		}
	}
}

func ids(list []Session) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = s.ID
	}
	return out
}

func TestSesiones(t *testing.T) {
	tc := database.NewTestCluster(t, 64)
	pool := tc.Pool
	ctx := context.Background()

	// Each subtest starts from empty tables.
	run := func(name string, fn func(t *testing.T)) {
		t.Run(name, func(t *testing.T) {
			tc.Reset(t)
			fn(t)
		})
	}

	run("creacion perezosa", func(t *testing.T) {
		clk := newClock()
		m := newTestManager(pool, clk)
		tr := m.NewTracker("claude-code")

		if tr.ID() != "" {
			t.Fatalf("un Tracker nuevo no debería tener sesión, tiene %q", tr.ID())
		}
		if n := countSessions(t, pool); n != 0 {
			t.Fatalf("no debería haber sesiones antes de Current, hay %d", n)
		}

		id, err := tr.Current(ctx)
		if err != nil {
			t.Fatalf("Current falló: %v", err)
		}
		if _, err := ulid.ParseStrict(id); err != nil {
			t.Errorf("el id %q no es un ULID válido: %v", id, err)
		}
		r := fetch(t, pool, id)
		if r.Client != "claude-code" {
			t.Errorf("client = %q, se esperaba claude-code", r.Client)
		}
		if r.Ended != nil {
			t.Errorf("la sesión nueva no debería estar cerrada: %v", r.Ended)
		}
		if !r.Started.Equal(clk.Now()) || !r.Activity.Equal(clk.Now()) {
			t.Errorf("started/last_activity = %v/%v, se esperaba %v", r.Started, r.Activity, clk.Now())
		}

		id2, err := tr.Current(ctx)
		if err != nil {
			t.Fatalf("segunda llamada a Current falló: %v", err)
		}
		if id2 != id || tr.ID() != id {
			t.Errorf("Current debería devolver la misma sesión: %q, %q, ID()=%q", id, id2, tr.ID())
		}
		if n := countSessions(t, pool); n != 1 {
			t.Errorf("hay %d sesiones, se esperaba 1", n)
		}
	})

	run("cliente normalizado", func(t *testing.T) {
		m := newTestManager(pool, newClock())
		casos := []struct {
			nombre, entrada string
			runes           int
			esperado        string
		}{
			{"vacío", "", 0, "desconocido"},
			{"solo espacios", "   ", 0, "desconocido"},
			{"largo ascii", strings.Repeat("a", 150), 100, ""},
			{"largo multibyte", strings.Repeat("ñ", 150), 100, ""},
		}
		for _, c := range casos {
			id, err := m.NewTracker(c.entrada).Current(ctx)
			if err != nil {
				t.Fatalf("%s: Current falló: %v", c.nombre, err)
			}
			got := fetch(t, pool, id).Client
			if c.esperado != "" && got != c.esperado {
				t.Errorf("%s: client = %q, se esperaba %q", c.nombre, got, c.esperado)
			}
			if c.runes > 0 && utf8.RuneCountInString(got) != c.runes {
				t.Errorf("%s: client tiene %d caracteres, se esperaban %d", c.nombre, utf8.RuneCountInString(got), c.runes)
			}
		}
	})

	run("trackers concurrentes crean sesiones distintas", func(t *testing.T) {
		m := NewManager(pool, testIdle)
		const n = 8

		var wg sync.WaitGroup
		got := make([]string, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id, err := m.NewTracker(fmt.Sprintf("cliente-%d", i)).Current(ctx)
				if err != nil {
					t.Errorf("Current falló en la goroutine %d: %v", i, err)
					return
				}
				got[i] = id
			}()
		}
		wg.Wait()

		seen := map[string]bool{}
		for _, id := range got {
			if id == "" || seen[id] {
				t.Errorf("ids repetidos o vacíos: %v", got)
				break
			}
			seen[id] = true
		}
		if c := countSessions(t, pool); c != n {
			t.Errorf("hay %d sesiones, se esperaban %d", c, n)
		}
	})

	run("un mismo tracker concurrente crea una sola sesión", func(t *testing.T) {
		m := NewManager(pool, testIdle)
		tr := m.NewTracker("claude-code")
		const n = 8

		var wg sync.WaitGroup
		got := make([]string, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id, err := tr.Current(ctx)
				if err != nil {
					t.Errorf("Current falló: %v", err)
					return
				}
				got[i] = id
			}()
		}
		wg.Wait()

		for _, id := range got {
			if id != got[0] {
				t.Errorf("las llamadas devolvieron sesiones distintas: %v", got)
				break
			}
		}
		if c := countSessions(t, pool); c != 1 {
			t.Errorf("hay %d sesiones, se esperaba 1", c)
		}
	})

	run("cierre por inactividad", func(t *testing.T) {
		clk := newClock()
		m := newTestManager(pool, clk)
		tr := m.NewTracker("claude-code")
		inicio := clk.Now()

		id1, err := tr.Current(ctx)
		if err != nil {
			t.Fatal(err)
		}

		// Con actividad dentro del margen se mantiene la sesión y se actualiza la actividad.
		clk.Advance(10 * time.Minute)
		actividad := clk.Now()
		id, err := tr.Current(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if id != id1 {
			t.Fatalf("la sesión no debería haber caducado: %q != %q", id, id1)
		}
		if r := fetch(t, pool, id1); !r.Activity.Equal(actividad) {
			t.Errorf("last_activity_at = %v, se esperaba %v", r.Activity, actividad)
		}

		// Pasado el margen se cierra en su última actividad y se abre otra.
		clk.Advance(testIdle + time.Minute)
		id2, err := tr.Current(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if id2 == id1 {
			t.Fatal("tras la inactividad debería crearse otra sesión")
		}
		r1 := fetch(t, pool, id1)
		if r1.Ended == nil || !r1.Ended.Equal(actividad) {
			t.Errorf("ended_at de la sesión caducada = %v, se esperaba %v", r1.Ended, actividad)
		}
		if !r1.Started.Equal(inicio) {
			t.Errorf("started_at cambió: %v", r1.Started)
		}
		if r2 := fetch(t, pool, id2); r2.Ended != nil || !r2.Started.Equal(clk.Now()) {
			t.Errorf("la sesión nueva es incorrecta: %+v", r2)
		}
	})

	run("cierre desde otra instancia", func(t *testing.T) {
		clk := newClock()
		m := newTestManager(pool, clk)
		tr := m.NewTracker("claude-code")

		id1, err := tr.Current(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE sessions SET ended_at = $2 WHERE id = $1`, id1, clk.Now()); err != nil {
			t.Fatal(err)
		}
		id2, err := tr.Current(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if id2 == id1 {
			t.Error("una sesión cerrada por otra instancia debería sustituirse por una nueva")
		}
	})

	run("touch", func(t *testing.T) {
		clk := newClock()
		m := newTestManager(pool, clk)
		tr := m.NewTracker("claude-code")

		// Sin sesión no hace nada ni crea ninguna.
		if err := tr.Touch(ctx); err != nil {
			t.Fatalf("Touch sin sesión falló: %v", err)
		}
		if n := countSessions(t, pool); n != 0 || tr.ID() != "" {
			t.Fatalf("Touch no debe crear sesiones: %d, ID()=%q", n, tr.ID())
		}

		id, err := tr.Current(ctx)
		if err != nil {
			t.Fatal(err)
		}

		// Vigente: actualiza la actividad.
		clk.Advance(10 * time.Minute)
		actividad := clk.Now()
		if err := tr.Touch(ctx); err != nil {
			t.Fatal(err)
		}
		if tr.ID() != id {
			t.Errorf("la sesión vigente debería conservarse, ID()=%q", tr.ID())
		}
		if r := fetch(t, pool, id); !r.Activity.Equal(actividad) {
			t.Errorf("last_activity_at = %v, se esperaba %v", r.Activity, actividad)
		}

		// Caducada: se olvida, se cierra y no se crea otra.
		clk.Advance(testIdle + time.Minute)
		if err := tr.Touch(ctx); err != nil {
			t.Fatal(err)
		}
		if tr.ID() != "" {
			t.Errorf("la sesión caducada debería olvidarse, ID()=%q", tr.ID())
		}
		if n := countSessions(t, pool); n != 1 {
			t.Errorf("hay %d sesiones, se esperaba 1", n)
		}
		if r := fetch(t, pool, id); r.Ended == nil || !r.Ended.Equal(actividad) {
			t.Errorf("ended_at = %v, se esperaba %v", r.Ended, actividad)
		}
		id2, err := tr.Current(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if id2 == id {
			t.Error("Current tras una sesión caducada debería crear otra")
		}
	})

	run("end con sesion previa", func(t *testing.T) {
		clk := newClock()
		m := newTestManager(pool, clk)
		tr := m.NewTracker("claude-code")

		id, err := tr.Current(ctx)
		if err != nil {
			t.Fatal(err)
		}
		clk.Advance(5 * time.Minute)
		got, err := tr.End(ctx, "  Hablamos del esquema.  ")
		if err != nil {
			t.Fatalf("End falló: %v", err)
		}
		if got != id {
			t.Errorf("End devolvió %q, se esperaba %q", got, id)
		}
		r := fetch(t, pool, id)
		if r.Summary == nil || *r.Summary != "Hablamos del esquema." {
			t.Errorf("summary = %v", r.Summary)
		}
		if r.Ended == nil || !r.Ended.Equal(clk.Now()) {
			t.Errorf("ended_at = %v, se esperaba %v", r.Ended, clk.Now())
		}
		if tr.ID() != "" {
			t.Errorf("tras End el Tracker no debería tener sesión, ID()=%q", tr.ID())
		}
		if n := countSessions(t, pool); n != 1 {
			t.Errorf("hay %d sesiones, se esperaba 1", n)
		}

		next, err := tr.Current(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if next == id {
			t.Error("Current tras End debería abrir otra sesión")
		}
	})

	run("end sin sesion previa", func(t *testing.T) {
		clk := newClock()
		m := newTestManager(pool, clk)
		tr := m.NewTracker("gemini-cli")

		id, err := tr.End(ctx, "Resumen suelto")
		if err != nil {
			t.Fatalf("End falló: %v", err)
		}
		if _, err := ulid.ParseStrict(id); err != nil {
			t.Errorf("el id %q no es un ULID válido: %v", id, err)
		}
		r := fetch(t, pool, id)
		if r.Client != "gemini-cli" || r.Summary == nil || *r.Summary != "Resumen suelto" {
			t.Errorf("sesión incorrecta: %+v", r)
		}
		if r.Ended == nil || !r.Ended.Equal(clk.Now()) || !r.Started.Equal(clk.Now()) {
			t.Errorf("la sesión debería nacer cerrada: %+v", r)
		}
		if tr.ID() != "" {
			t.Errorf("el Tracker no debería tener sesión, ID()=%q", tr.ID())
		}
	})

	run("end recorta el resumen", func(t *testing.T) {
		m := newTestManager(pool, newClock())
		id, err := m.NewTracker("x").End(ctx, strings.Repeat("ñ", 2500))
		if err != nil {
			t.Fatal(err)
		}
		r := fetch(t, pool, id)
		if r.Summary == nil || utf8.RuneCountInString(*r.Summary) != 2000 {
			t.Errorf("el resumen debería tener 2000 caracteres: %v", r.Summary)
		}
	})

	run("add topics", func(t *testing.T) {
		m := newTestManager(pool, newClock())
		id, err := m.NewTracker("claude-code").Current(ctx)
		if err != nil {
			t.Fatal(err)
		}
		a := insertTopic(t, pool, "lodan")
		b := insertTopic(t, pool, "entrenamiento")
		c := insertTopic(t, pool, "cocina")

		if err := m.AddTopics(ctx, pool, id, nil); err != nil {
			t.Fatalf("AddTopics con lista vacía falló: %v", err)
		}
		if err := m.AddTopics(ctx, pool, id, []int32{a, b}); err != nil {
			t.Fatalf("AddTopics falló: %v", err)
		}
		if err := m.AddTopics(ctx, pool, id, []int32{a, a, b}); err != nil {
			t.Fatalf("AddTopics no es idempotente: %v", err)
		}
		if n := count(t, pool, `SELECT count(*) FROM session_topics WHERE session_id = $1`, id); n != 2 {
			t.Errorf("hay %d enlaces, se esperaban 2", n)
		}

		// Dentro de una transacción que se revierte no queda nada.
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.AddTopics(ctx, tx, id, []int32{c}); err != nil {
			t.Fatalf("AddTopics dentro de la transacción falló: %v", err)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if n := count(t, pool, `SELECT count(*) FROM session_topics WHERE session_id = $1`, id); n != 2 {
			t.Errorf("tras el rollback hay %d enlaces, se esperaban 2", n)
		}

		// Un tema inexistente devuelve error.
		if err := m.AddTopics(ctx, pool, id, []int32{9999}); err == nil {
			t.Error("se esperaba un error con un tema inexistente")
		}

		s, err := m.Last(ctx, Filter{})
		if err != nil {
			t.Fatal(err)
		}
		if s == nil || !slices.Equal(s.Topics, []string{"entrenamiento", "lodan"}) {
			t.Errorf("Topics = %v", s)
		}
	})

	// Datos comunes de los tests de consulta.
	//   A: claude-code, 27 sep 10:00 UTC, cerrada, tema lodan
	//   B: Gemini-CLI, 28 sep 23:30 UTC (29 sep 01:30 en UTC+2), cerrada sin resumen, temas lodan y entrenamiento
	//   C: claude-code, 29 sep 09:00 UTC, abierta, sin temas
	seedConsulta := func(t *testing.T) {
		t.Helper()
		lodan := insertTopic(t, pool, "lodan")
		entrenamiento := insertTopic(t, pool, "entrenamiento")
		a := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
		b := time.Date(2026, 9, 28, 23, 30, 0, 0, time.UTC)
		c := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
		aFin, bFin := a.Add(time.Hour), b.Add(time.Hour)
		insertSession(t, pool, "A", "claude-code", a, &aFin, "sesión a")
		insertSession(t, pool, "B", "Gemini-CLI", b, &bFin, "")
		insertSession(t, pool, "C", "claude-code", c, nil, "sesión c")
		linkTopics(t, pool, "A", lodan)
		linkTopics(t, pool, "B", lodan, entrenamiento)
	}

	run("last y list con filtros", func(t *testing.T) {
		seedConsulta(t)
		m := NewManager(pool, testIdle)
		utc := func(y int, mo time.Month, d int) *time.Time {
			v := time.Date(y, mo, d, 15, 0, 0, 0, time.UTC)
			return &v
		}
		mas2 := time.FixedZone("UTC+2", 2*3600)
		dia29Mas2 := time.Date(2026, 9, 29, 12, 0, 0, 0, mas2)

		casos := []struct {
			nombre string
			filtro Filter
			quiere []string
		}{
			{"sin filtro", Filter{}, []string{"C", "B", "A"}},
			{"tema lodan", Filter{TopicSlug: "lodan"}, []string{"B", "A"}},
			{"tema entrenamiento", Filter{TopicSlug: "entrenamiento"}, []string{"B"}},
			{"tema inexistente", Filter{TopicSlug: "nada"}, nil},
			{"cliente en minúsculas", Filter{Client: "gemini"}, []string{"B"}},
			{"cliente parcial", Filter{Client: "claude"}, []string{"C", "A"}},
			{"cliente con comodín literal", Filter{Client: "%"}, nil},
			{"fecha 29 UTC", Filter{Date: utc(2026, 9, 29)}, []string{"C"}},
			{"fecha 28 UTC", Filter{Date: utc(2026, 9, 28)}, []string{"B"}},
			{"fecha 27 UTC", Filter{Date: utc(2026, 9, 27)}, []string{"A"}},
			{"fecha sin sesiones", Filter{Date: utc(2026, 9, 1)}, nil},
			{"fecha 29 en UTC+2", Filter{Date: &dia29Mas2}, []string{"C", "B"}},
			{"excluir C", Filter{ExcludeID: "C"}, []string{"B", "A"}},
			{"cliente y exclusión", Filter{Client: "claude", ExcludeID: "C"}, []string{"A"}},
			{"tema y cliente", Filter{TopicSlug: "lodan", Client: "claude"}, []string{"A"}},
			{"todo excluido", Filter{TopicSlug: "entrenamiento", ExcludeID: "B"}, nil},
		}
		for _, c := range casos {
			list, err := m.List(ctx, c.filtro, 20)
			if err != nil {
				t.Errorf("%s: List falló: %v", c.nombre, err)
				continue
			}
			if got := ids(list); !slices.Equal(got, c.quiere) && !(len(got) == 0 && len(c.quiere) == 0) {
				t.Errorf("%s: List = %v, se esperaba %v", c.nombre, got, c.quiere)
			}

			last, err := m.Last(ctx, c.filtro)
			if err != nil {
				t.Errorf("%s: Last falló: %v", c.nombre, err)
				continue
			}
			if len(c.quiere) == 0 {
				if last != nil {
					t.Errorf("%s: Last = %v, se esperaba nil", c.nombre, last.ID)
				}
			} else if last == nil || last.ID != c.quiere[0] {
				t.Errorf("%s: Last = %v, se esperaba %s", c.nombre, last, c.quiere[0])
			}
		}
	})

	run("last rellena los campos y los temas", func(t *testing.T) {
		seedConsulta(t)
		m := NewManager(pool, testIdle)

		b, err := m.Last(ctx, Filter{Client: "gemini"})
		if err != nil || b == nil {
			t.Fatalf("Last = %v, %v", b, err)
		}
		if b.ID != "B" || b.Client != "Gemini-CLI" || b.Summary != "" || b.EndedAt == nil {
			t.Errorf("sesión B incorrecta: %+v", b)
		}
		if !b.StartedAt.Equal(time.Date(2026, 9, 28, 23, 30, 0, 0, time.UTC)) {
			t.Errorf("StartedAt = %v", b.StartedAt)
		}
		if !slices.Equal(b.Topics, []string{"entrenamiento", "lodan"}) {
			t.Errorf("Topics = %v", b.Topics)
		}

		c, err := m.Last(ctx, Filter{})
		if err != nil || c == nil {
			t.Fatalf("Last = %v, %v", c, err)
		}
		if c.ID != "C" || c.EndedAt != nil || c.Summary != "sesión c" || len(c.Topics) != 0 {
			t.Errorf("sesión C incorrecta: %+v", c)
		}
	})

	run("last sin sesiones", func(t *testing.T) {
		m := NewManager(pool, testIdle)
		s, err := m.Last(ctx, Filter{})
		if err != nil || s != nil {
			t.Errorf("Last = %v, %v; se esperaba nil, nil", s, err)
		}
	})

	run("list respeta el límite", func(t *testing.T) {
		m := NewManager(pool, testIdle)
		base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
		for i := range 25 {
			insertSession(t, pool, fmt.Sprintf("S%02d", i), "claude-code", base.Add(time.Duration(i)*time.Minute), nil, "")
		}

		casos := []struct{ limite, quiere int }{
			{0, 5}, {-3, 5}, {1, 1}, {3, 3}, {20, 20}, {21, 20}, {100, 20},
		}
		for _, c := range casos {
			list, err := m.List(ctx, Filter{}, c.limite)
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != c.quiere {
				t.Errorf("List(limit=%d) devolvió %d, se esperaban %d", c.limite, len(list), c.quiere)
			}
			if len(list) > 0 && list[0].ID != "S24" {
				t.Errorf("List(limit=%d): la primera es %s, se esperaba S24", c.limite, list[0].ID)
			}
		}
		list, err := m.List(ctx, Filter{}, 3)
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(list); !slices.Equal(got, []string{"S24", "S23", "S22"}) {
			t.Errorf("orden incorrecto: %v", got)
		}
	})

	run("close idle", func(t *testing.T) {
		clk := newClock()
		m := newTestManager(pool, clk)
		ahora := clk.Now()

		viejaInicio := ahora.Add(-time.Hour)
		recienteInicio := ahora.Add(-5 * time.Minute)
		hechaInicio, hechaFin := ahora.Add(-2*time.Hour), ahora.Add(-50*time.Minute)
		insertSession(t, pool, "vieja", "claude-code", viejaInicio, nil, "")
		insertSession(t, pool, "reciente", "claude-code", recienteInicio, nil, "")
		insertSession(t, pool, "hecha", "claude-code", hechaInicio, &hechaFin, "")

		n, err := m.CloseIdle(ctx)
		if err != nil {
			t.Fatalf("CloseIdle falló: %v", err)
		}
		if n != 1 {
			t.Errorf("CloseIdle cerró %d sesiones, se esperaba 1", n)
		}
		if r := fetch(t, pool, "vieja"); r.Ended == nil || !r.Ended.Equal(viejaInicio) {
			t.Errorf("ended_at de vieja = %v, se esperaba %v", r.Ended, viejaInicio)
		}
		if r := fetch(t, pool, "reciente"); r.Ended != nil {
			t.Errorf("reciente no debería cerrarse: %v", r.Ended)
		}
		if r := fetch(t, pool, "hecha"); r.Ended == nil || !r.Ended.Equal(hechaFin) {
			t.Errorf("hecha no debería modificarse: %v", r.Ended)
		}

		n, err = m.CloseIdle(ctx)
		if err != nil || n != 0 {
			t.Errorf("segunda llamada = %d, %v; se esperaba 0, nil", n, err)
		}
	})
}

func TestFormat(t *testing.T) {
	inicio := time.Date(2026, 9, 29, 14, 32, 0, 0, time.Local)
	fin := inicio.Add(time.Hour)

	casos := []struct {
		nombre string
		s      Session
		quiere string
	}{
		{
			"abierta con temas y resumen",
			Session{Client: "claude-code", StartedAt: inicio, Topics: []string{"lodan", "entrenamiento"}, Summary: "Diseñado el esquema"},
			"2026-09-29 14:32 · claude-code · temas: lodan, entrenamiento · abierta · resumen: Diseñado el esquema",
		},
		{
			"cerrada sin temas ni resumen",
			Session{Client: "gemini-cli", StartedAt: inicio, EndedAt: &fin},
			"2026-09-29 14:32 · gemini-cli · cerrada · sin resumen",
		},
		{
			"resumen en varias líneas",
			Session{Client: "x", StartedAt: inicio, EndedAt: &fin, Summary: "uno\n  dos\ttres "},
			"2026-09-29 14:32 · x · cerrada · resumen: uno dos tres",
		},
		{
			"resumen solo espacios",
			Session{Client: "x", StartedAt: inicio, Summary: " \n "},
			"2026-09-29 14:32 · x · abierta · sin resumen",
		},
		{
			"hora en zona local",
			Session{Client: "x", StartedAt: inicio.UTC()},
			"2026-09-29 14:32 · x · abierta · sin resumen",
		},
	}
	for _, c := range casos {
		if got := Format(c.s); got != c.quiere {
			t.Errorf("%s:\n got  %q\n want %q", c.nombre, got, c.quiere)
		}
	}
}
