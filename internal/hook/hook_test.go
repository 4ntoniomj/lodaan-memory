package hook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"lodan/internal/database"
	"lodan/internal/embedding"
	"lodan/internal/hook"
	"lodan/internal/memory"
	"lodan/internal/session"
	"lodan/internal/topic"
)

const (
	testDims = 64
	opTime   = 120 * time.Second
)

type env struct {
	pool *pgxpool.Pool
	deps *hook.Deps
	mgr  *session.Manager
	svc  *memory.Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	tc := database.NewTestCluster(t, testDims)
	emb := &embedding.FakeEmbedder{Dimensions: testDims}
	mgr := session.NewManager(tc.Pool, 30*time.Minute)
	topics := topic.NewResolver(tc.Pool, emb, tc.Cfg.TopicSimilarity)
	svc := memory.NewService(tc.Pool, emb, topics, mgr, tc.Cfg.DupSimilarity, 30*time.Second)
	return &env{pool: tc.Pool, mgr: mgr, svc: svc, deps: &hook.Deps{Pool: tc.Pool, Memory: svc, Sessions: mgr}}
}

func ctxT() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), opTime)
}

func jsonOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

type sessRow struct {
	id      string
	client  string
	summary *string
	ended   *time.Time
}

func (e *env) sessions(t *testing.T) []sessRow {
	t.Helper()
	ctx, cancel := ctxT()
	defer cancel()
	rows, err := e.pool.Query(ctx, `SELECT id, client, summary, ended_at FROM sessions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []sessRow
	for rows.Next() {
		var r sessRow
		if err := rows.Scan(&r.id, &r.client, &r.summary, &r.ended); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// openSession creates an open session of client and sets its last activity.
func (e *env) openSession(t *testing.T, client string, lastActivity time.Time) string {
	t.Helper()
	ctx, cancel := ctxT()
	defer cancel()
	id, err := e.mgr.NewTracker(client).Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE sessions SET last_activity_at=$2 WHERE id=$1`, id, lastActivity); err != nil {
		t.Fatal(err)
	}
	return id
}

// endedSession creates an ended session of client with summary and the given ended_at.
func (e *env) endedSession(t *testing.T, client, summary string, endedAt time.Time) string {
	t.Helper()
	ctx, cancel := ctxT()
	defer cancel()
	id, err := e.mgr.NewTracker(client).End(ctx, summary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE sessions SET ended_at=$2 WHERE id=$1`, id, endedAt); err != nil {
		t.Fatal(err)
	}
	return id
}

func find(rows []sessRow, id string) *sessRow {
	for i := range rows {
		if rows[i].id == id {
			return &rows[i]
		}
	}
	return nil
}

func postCompact(summary string) string {
	return jsonOf(map[string]any{"hook_event_name": "PostCompact", "cwd": "/x", "session_id": "abc", "compact_summary": summary})
}

// S10: PostCompact stores the summary in the most recently active open session, without closing it.
func TestCompactPostCompactGuardaResumenEnSesionAbierta(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	older := e.openSession(t, "claude-code", now.Add(-2*time.Hour))
	newer := e.openSession(t, "claude-code", now.Add(-1*time.Minute))
	other := e.openSession(t, "otro-cliente", now)

	var out bytes.Buffer
	ctx, cancel := ctxT()
	defer cancel()
	if err := hook.Compact(ctx, e.deps, "claude-code", strings.NewReader(postCompact("resumen de compactación")), &out); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("salida = %q, se esperaba vacía", out.String())
	}
	rows := e.sessions(t)
	if len(rows) != 3 {
		t.Fatalf("sesiones = %d, se esperaban 3", len(rows))
	}
	n := find(rows, newer)
	if n.summary == nil || *n.summary != "resumen de compactación" {
		t.Errorf("resumen de la sesión más reciente = %v", n.summary)
	}
	if n.ended != nil {
		t.Errorf("la sesión no debe cerrarse, ended_at = %v", n.ended)
	}
	if o := find(rows, older); o.summary != nil || o.ended != nil {
		t.Errorf("la sesión antigua no debe cambiar: %+v", o)
	}
	if o := find(rows, other); o.summary != nil || o.ended != nil {
		t.Errorf("la sesión de otro cliente no debe cambiar: %+v", o)
	}
}

// S10: with no open session of that client, an ended session with the summary is created.
func TestCompactPostCompactSinSesionAbiertaCreaSesionCerrada(t *testing.T) {
	e := newEnv(t)
	other := e.openSession(t, "otro-cliente", time.Now())

	var out bytes.Buffer
	ctx, cancel := ctxT()
	defer cancel()
	if err := hook.Compact(ctx, e.deps, "claude-code", strings.NewReader(postCompact("resumen nuevo")), &out); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("salida = %q, se esperaba vacía", out.String())
	}
	rows := e.sessions(t)
	if len(rows) != 2 {
		t.Fatalf("sesiones = %d, se esperaban 2", len(rows))
	}
	if o := find(rows, other); o.summary != nil || o.ended != nil {
		t.Errorf("la sesión de otro cliente no debe cambiar: %+v", o)
	}
	var got *sessRow
	for i := range rows {
		if rows[i].client == "claude-code" {
			got = &rows[i]
		}
	}
	if got == nil || got.summary == nil || *got.summary != "resumen nuevo" || got.ended == nil {
		t.Errorf("sesión creada incorrecta: %+v", got)
	}
}

// S10: another event prints the recovery instructions followed by the last ended session summary.
func TestCompactOtroEventoImprimeInstruccionesYUltimaSesion(t *testing.T) {
	e := newEnv(t)
	e.endedSession(t, "claude-code", "resumen viejo", time.Date(2026, 3, 1, 12, 0, 0, 0, time.Local))
	e.endedSession(t, "claude-code", "resumen reciente", time.Date(2026, 3, 5, 12, 0, 0, 0, time.Local))
	e.endedSession(t, "otro-cliente", "resumen ajeno", time.Date(2026, 3, 9, 12, 0, 0, 0, time.Local))
	e.openSession(t, "claude-code", time.Now())

	var out bytes.Buffer
	ctx, cancel := ctxT()
	defer cancel()
	in := `{"hook_event_name":"SessionStart","source":"compact","cwd":"/x","session_id":"abc"}`
	if err := hook.Compact(ctx, e.deps, "claude-code", strings.NewReader(in), &out); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\r\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("líneas = %d (%q), se esperaban 2", len(lines), out.String())
	}
	if strings.TrimRight(lines[0], "\r") != hook.RecoveryInstructions {
		t.Errorf("línea 1 = %q", lines[0])
	}
	want := "Última sesión de lodan (claude-code, 2026-03-05): resumen reciente"
	if strings.TrimRight(lines[1], "\r") != want {
		t.Errorf("línea 2 = %q, se esperaba %q", lines[1], want)
	}
}

// S10: empty or invalid stdin behave as "any other event"; no ended session with summary -> only instructions.
func TestCompactEntradaVaciaOInvalidaSoloInstrucciones(t *testing.T) {
	e := newEnv(t)
	e.endedSession(t, "claude-code", "", time.Now().Add(-time.Hour)) // empty summary: not shown
	for name, in := range map[string]string{"vacia": "", "invalida": "esto no es json {"} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			ctx, cancel := ctxT()
			defer cancel()
			if err := hook.Compact(ctx, e.deps, "claude-code", strings.NewReader(in), &out); err != nil {
				t.Fatalf("Compact: %v", err)
			}
			if strings.TrimSpace(out.String()) != hook.RecoveryInstructions {
				t.Errorf("salida = %q, se esperaban solo las instrucciones", out.String())
			}
		})
	}
}

// S10: PostCompact with an empty summary counts as "any other event".
func TestCompactPostCompactSinResumenImprimeInstrucciones(t *testing.T) {
	e := newEnv(t)
	var out bytes.Buffer
	ctx, cancel := ctxT()
	defer cancel()
	if err := hook.Compact(ctx, e.deps, "claude-code", strings.NewReader(postCompact("")), &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != hook.RecoveryInstructions {
		t.Errorf("salida = %q", out.String())
	}
	if n := len(e.sessions(t)); n != 0 {
		t.Errorf("no debe crear sesiones, hay %d", n)
	}
}

// S10, S12: without a database only the instructions are printed and no error is returned.
func TestCompactSinBaseDeDatos(t *testing.T) {
	for name, in := range map[string]string{
		"sessionstart": `{"hook_event_name":"SessionStart","source":"compact"}`,
		"invalida":     "{{{",
		"vacia":        "",
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := hook.Compact(ctx, nil, "claude-code", strings.NewReader(in), &out); err != nil {
				t.Fatalf("Compact con deps nil: %v", err)
			}
			if strings.TrimSpace(out.String()) != hook.RecoveryInstructions {
				t.Errorf("salida = %q", out.String())
			}
		})
	}
	// PostCompact with nowhere to store it: no error.
	var out bytes.Buffer
	if err := hook.Compact(context.Background(), nil, "claude-code", strings.NewReader(postCompact("algo")), &out); err != nil {
		t.Fatalf("Compact PostCompact con deps nil: %v", err)
	}
}

// S10: exact recovery text.
func TestRecoveryInstructionsTexto(t *testing.T) {
	want := "lodan: el contexto se acaba de compactar. Antes de seguir, llama una vez a recall de lodan en lote (queries) con los temas en curso de esta conversación y continúa con lo que devuelva."
	if hook.RecoveryInstructions != want {
		t.Errorf("RecoveryInstructions = %q", hook.RecoveryInstructions)
	}
}

// S11: extraction rules.
func TestExtractKeyLearningsBasico(t *testing.T) {
	msg := "Resultado del trabajo.\n\n## Key Learnings\n1. La API devuelve fechas en formato UTC siempre\n2) Hay que reiniciar el servicio tras migrar\n\nTexto suelto.\n"
	got := hook.ExtractKeyLearnings(msg)
	want := []string{"La API devuelve fechas en formato UTC siempre", "Hay que reiniciar el servicio tras migrar"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractKeyLearningsSinBloque(t *testing.T) {
	for _, msg := range []string{"", "nada que ver\n1. un punto numerado cualquiera de la lista", "## Key Learnings extra\n1. no es el encabezado exacto del bloque"} {
		if got := hook.ExtractKeyLearnings(msg); len(got) != 0 {
			t.Errorf("msg %q: got %q, se esperaba vacío", msg, got)
		}
	}
}

func TestExtractKeyLearningsUsaElUltimoBloqueYEspaciosFinales(t *testing.T) {
	msg := "## Key Learnings\n1. Primer bloque que se debe ignorar por completo\n\n## Key Learnings   \n1. Segundo bloque que sí se debe usar aquí\n"
	got := hook.ExtractKeyLearnings(msg)
	want := []string{"Segundo bloque que sí se debe usar aquí"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractKeyLearningsTerminaEnSiguienteEncabezado(t *testing.T) {
	msg := "## Key Learnings\n1. Este elemento pertenece al bloque actual\n# Otro\n2. Este elemento ya queda fuera del bloque\n"
	got := hook.ExtractKeyLearnings(msg)
	want := []string{"Este elemento pertenece al bloque actual"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractKeyLearningsMaximoCinco(t *testing.T) {
	msg := "## Key Learnings\n1. Elemento número uno del listado\n2. Elemento número dos del listado\n3. Elemento número tres del listado\n4. Elemento número cuatro del listado\n5. Elemento número cinco del listado\n6. Elemento número seis del listado\n7. Elemento número siete del listado\n"
	got := hook.ExtractKeyLearnings(msg)
	if len(got) != 5 || got[0] != "Elemento número uno del listado" || got[4] != "Elemento número cinco del listado" {
		t.Errorf("got %q", got)
	}
}

func TestExtractKeyLearningsDescartaCortosYPocasPalabras(t *testing.T) {
	msg := "## Key Learnings\n1. Muy corto\n2. unapalabramuylargasinespacios1234567890\n3. tres palabras aquí\n4. Este sí tiene suficientes palabras y longitud\n"
	got := hook.ExtractKeyLearnings(msg)
	want := []string{"Este sí tiene suficientes palabras y longitud"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractKeyLearningsDescartaSecretosYDocumentos(t *testing.T) {
	bad := []string{
		"La clave es AGE-SECRET-KEY-1ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 para sync",
		"Se guardó -----BEGIN OPENSSH PRIVATE KEY----- en el servidor remoto",
		"Configurar password: hunter2hunter2 en el entorno de pruebas",
		"Configurar Contraseña = miClaveSecreta para el entorno de pruebas",
		"El token: abcdef123456 se usa para el despliegue manual",
		"El SECRET=valorsecreto se usa para firmar las cookies",
		"La api key: ABC123XYZ se usa para consultar el servicio",
		"La api_key=ABC123XYZ se usa para consultar el servicio",
		"La APIKEY: ABC123XYZ se usa para consultar el servicio",
		"Usar sk-abcdefghijklmnop1234 como clave de la cuenta de pruebas",
		"Usar ghp_abcdefghijklmnopqrst1234 como acceso al repositorio privado",
		"Usar gho_abcdefghijklmnopqrst1234 como acceso al repositorio privado",
		"Usar ghu_abcdefghijklmnopqrst1234 como acceso al repositorio privado",
		"Usar ghs_abcdefghijklmnopqrst1234 como acceso al repositorio privado",
		"Usar ghr_abcdefghijklmnopqrst1234 como acceso al repositorio privado",
		"El DNI del cliente de pruebas es 12345678Z según el contrato",
		"El NIE del cliente de pruebas es X1234567L según el contrato",
		"El NIE del cliente de pruebas es Y1234567L según el contrato",
		"El NIE del cliente de pruebas es Z1234567L según el contrato",
	}
	for _, s := range bad {
		msg := "## Key Learnings\n1. " + s + "\n2. Este elemento limpio sí debe conservarse siempre\n"
		got := hook.ExtractKeyLearnings(msg)
		want := []string{"Este elemento limpio sí debe conservarse siempre"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("elemento %q no descartado: got %q", s, got)
		}
	}
}

func TestExtractKeyLearningsNoDescartaTextoNormal(t *testing.T) {
	msg := "## Key Learnings\n1. El token de acceso caduca a los treinta minutos\n2. La contraseña se rota cada trimestre por política\n3. Usar sk-corto como prefijo de nombres internos\n"
	got := hook.ExtractKeyLearnings(msg)
	if len(got) != 3 {
		t.Errorf("got %q, se esperaban 3 elementos", got)
	}
}

func gitRepo(t *testing.T, name string) (repo, sub string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no disponible")
	}
	repo = filepath.Join(t.TempDir(), name)
	sub = filepath.Join(repo, "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	return repo, sub
}

// S11: ProjectTopic inside a git working copy.
func TestProjectTopicRaizGit(t *testing.T) {
	_, sub := gitRepo(t, "mi-repo")
	if got := hook.ProjectTopic(sub); got != "mi-repo" {
		t.Errorf("ProjectTopic(subcarpeta) = %q, se esperaba mi-repo", got)
	}
}

// S11: ProjectTopic outside git.
func TestProjectTopicSinGit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "carpeta-suelta")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := hook.ProjectTopic(dir); got != "carpeta-suelta" {
		t.Errorf("ProjectTopic = %q", got)
	}
}

type memRow struct {
	id      int64
	title   string
	content string
	kind    string
	status  string
	session *string
}

func (e *env) memories(t *testing.T) []memRow {
	t.Helper()
	ctx, cancel := ctxT()
	defer cancel()
	rows, err := e.pool.Query(ctx, `SELECT id, title, content, kind::text, status::text, session_id FROM memories ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []memRow
	for rows.Next() {
		var r memRow
		if err := rows.Scan(&r.id, &r.title, &r.content, &r.kind, &r.status, &r.session); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func stopEvent(msg, cwd, transcript string) string {
	return jsonOf(map[string]any{
		"hook_event_name": "SubagentStop", "cwd": cwd, "session_id": "s1",
		"last_assistant_message": msg, "agent_type": "general", "agent_transcript_path": transcript,
	})
}

func postToolEvent(msg, cwd string) string {
	return jsonOf(map[string]any{
		"hook_event_name": "PostToolUse", "tool_name": "SubagentHandback", "cwd": cwd, "session_id": "s1",
		"tool_input": map[string]any{"message": msg},
	})
}

func stop(t *testing.T, e *env, in string) int {
	t.Helper()
	ctx, cancel := ctxT()
	defer cancel()
	n, err := hook.SubagentStop(ctx, e.deps, "claude-code", strings.NewReader(in))
	if err != nil {
		t.Fatalf("SubagentStop: %v", err)
	}
	return n
}

// S11: items of last_assistant_message are saved as notes in the project topic, in the client's session.
func TestSubagentStopGuardaNotas(t *testing.T) {
	e := newEnv(t)
	_, sub := gitRepo(t, "Proyecto Ejemplo")
	msg := "Hecho.\n\n## Key Learnings\n1. La caché de embeddings se invalida al cambiar el modelo\n2) Las migraciones deben ejecutarse antes de arrancar el servicio\n"
	if n := stop(t, e, stopEvent(msg, sub, "")); n != 2 {
		t.Fatalf("guardados = %d, se esperaban 2", n)
	}
	mems := e.memories(t)
	if len(mems) != 2 {
		t.Fatalf("registros = %d", len(mems))
	}
	ctx, cancel := ctxT()
	defer cancel()
	sessions := e.sessions(t)
	wantTopic := topic.Normalize(hook.ProjectTopic(sub))
	contents := map[string]bool{}
	for _, m := range mems {
		contents[m.content] = true
		if m.kind != "note" || m.status != "active" {
			t.Errorf("registro %d: kind=%s status=%s", m.id, m.kind, m.status)
		}
		if m.title != m.content {
			t.Errorf("título = %q, se esperaba el ítem completo", m.title)
		}
		if m.session == nil {
			t.Errorf("registro %d sin sesión", m.id)
		} else if s := find(sessions, *m.session); s == nil || s.client != "claude-code" {
			t.Errorf("registro %d: sesión de cliente incorrecto: %+v", m.id, s)
		}
		full, err := e.svc.Get(ctx, []int64{m.id})
		if err != nil || len(full) != 1 {
			t.Fatalf("Get: %v", err)
		}
		if !reflect.DeepEqual(full[0].Topics, []string{wantTopic}) {
			t.Errorf("temas = %v, se esperaba [%s]", full[0].Topics, wantTopic)
		}
	}
	if !contents["La caché de embeddings se invalida al cambiar el modelo"] || !contents["Las migraciones deben ejecutarse antes de arrancar el servicio"] {
		t.Errorf("contenidos = %v", contents)
	}
}

// S11: title cut at 80 characters at a word boundary with an ellipsis; whole item when <= 80.
func TestSubagentStopTituloCortado(t *testing.T) {
	e := newEnv(t)
	long := "Este aprendizaje es bastante largo y describe con detalle cómo funciona la sincronización entre máquinas distintas"
	exact := strings.Repeat("palabra ", 9) + "final" // 77 chars
	msg := "## Key Learnings\n1. " + long + "\n2. " + exact + "\n"
	if n := stop(t, e, stopEvent(msg, t.TempDir(), "")); n != 2 {
		t.Fatalf("guardados = %d", n)
	}
	for _, m := range e.memories(t) {
		switch m.content {
		case long:
			if !strings.HasSuffix(m.title, "…") {
				t.Errorf("título sin elipsis: %q", m.title)
			}
			body := strings.TrimSuffix(m.title, "…")
			if utf8.RuneCountInString(body) > 80 {
				t.Errorf("título demasiado largo: %q", m.title)
			}
			if !strings.HasPrefix(long, body) {
				t.Errorf("el título no es prefijo del ítem: %q", m.title)
			}
			if rest := strings.TrimPrefix(long, body); !strings.HasPrefix(rest, " ") {
				t.Errorf("corte a mitad de palabra: %q", m.title)
			}
		case exact:
			if m.title != exact {
				t.Errorf("título = %q, se esperaba el ítem completo", m.title)
			}
		default:
			t.Errorf("contenido inesperado %q", m.content)
		}
	}
}

// S11: no block -> 0 and nothing saved.
func TestSubagentStopSinBloque(t *testing.T) {
	e := newEnv(t)
	if n := stop(t, e, stopEvent("Sin nada que guardar aquí.", t.TempDir(), "")); n != 0 {
		t.Fatalf("guardados = %d", n)
	}
	if len(e.memories(t)) != 0 {
		t.Error("no debe guardar nada")
	}
}

// S11: secrets and Spanish ID numbers are not saved.
func TestSubagentStopNoGuardaSecretos(t *testing.T) {
	e := newEnv(t)
	msg := "## Key Learnings\n1. La clave es AGE-SECRET-KEY-1ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 para sync\n2. El DNI del cliente de pruebas es 12345678Z según el contrato\n3. Este elemento limpio sí debe conservarse siempre\n"
	if n := stop(t, e, stopEvent(msg, t.TempDir(), "")); n != 1 {
		t.Fatalf("guardados = %d", n)
	}
	mems := e.memories(t)
	if len(mems) != 1 || mems[0].content != "Este elemento limpio sí debe conservarse siempre" {
		t.Errorf("registros = %+v", mems)
	}
}

// S11: duplicates of an active record are not counted.
func TestSubagentStopDuplicadosNoSeCuentan(t *testing.T) {
	e := newEnv(t)
	msg := "## Key Learnings\n1. Este aprendizaje se repite entre dos subagentes\n"
	cwd := t.TempDir()
	for i, want := range []int{1, 0} {
		if n := stop(t, e, stopEvent(msg, cwd, "")); n != want {
			t.Fatalf("llamada %d: %d, se esperaba %d", i+1, n, want)
		}
	}
	if len(e.memories(t)) != 1 {
		t.Errorf("registros = %d, se esperaba 1", len(e.memories(t)))
	}
}

func writeTranscript(t *testing.T, msgs ...string) string {
	t.Helper()
	lines := []string{`{"type":"user","message":{"role":"user","content":"hola"}}`}
	for _, m := range msgs {
		lines = append(lines, jsonOf(map[string]any{
			"type": "assistant",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "pensando"},
				map[string]any{"type": "tool_use", "name": "SubagentHandback", "input": map[string]any{"message": m}},
			}},
		}))
	}
	lines = append(lines, jsonOf(map[string]any{
		"type": "assistant",
		"message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"message": "## Key Learnings\n1. Esto viene de otra herramienta y no cuenta nada"}},
		}},
	}))
	p := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// S11: without a block in last_assistant_message, the last SubagentHandback of the transcript is used.
func TestSubagentStopUsaTranscript(t *testing.T) {
	e := newEnv(t)
	tr := writeTranscript(t,
		"## Key Learnings\n1. Este es el primer handback y debe ignorarse\n",
		"## Key Learnings\n1. Este es el último handback y debe guardarse\n")
	if n := stop(t, e, stopEvent("Mensaje final sin bloque.", t.TempDir(), tr)); n != 1 {
		t.Fatalf("guardados = %d", n)
	}
	mems := e.memories(t)
	if len(mems) != 1 || mems[0].content != "Este es el último handback y debe guardarse" {
		t.Errorf("registros = %+v", mems)
	}
}

// S11: a block in last_assistant_message wins over the transcript.
func TestSubagentStopMensajeTienePrioridadSobreTranscript(t *testing.T) {
	e := newEnv(t)
	tr := writeTranscript(t, "## Key Learnings\n1. Este viene del transcript y no debe guardarse\n")
	msg := "## Key Learnings\n1. Este viene del mensaje final y sí se guarda\n"
	if n := stop(t, e, stopEvent(msg, t.TempDir(), tr)); n != 1 {
		t.Fatalf("guardados = %d", n)
	}
	mems := e.memories(t)
	if len(mems) != 1 || mems[0].content != "Este viene del mensaje final y sí se guarda" {
		t.Errorf("registros = %+v", mems)
	}
}

// S11 (PostToolUse on SubagentHandback): tool_input.message is the message and wins over
// last_assistant_message and the transcript.
func TestSubagentStopPostToolUseGuardaNotas(t *testing.T) {
	e := newEnv(t)
	cwd := t.TempDir()
	msg := "## Key Learnings\n1. Este aprendizaje llega por el hook de PostToolUse\n"
	if n := stop(t, e, postToolEvent(msg, cwd)); n != 1 {
		t.Fatalf("guardados = %d", n)
	}
	mems := e.memories(t)
	if len(mems) != 1 || mems[0].content != "Este aprendizaje llega por el hook de PostToolUse" || mems[0].kind != "note" {
		t.Errorf("registros = %+v", mems)
	}
}

func TestSubagentStopToolInputTienePrioridad(t *testing.T) {
	e := newEnv(t)
	tr := writeTranscript(t, "## Key Learnings\n1. Este viene del transcript y no debe guardarse\n")
	in := jsonOf(map[string]any{
		"hook_event_name": "PostToolUse", "tool_name": "SubagentHandback", "cwd": t.TempDir(),
		"tool_input":             map[string]any{"message": "## Key Learnings\n1. Este viene de tool_input y sí se guarda aquí\n"},
		"last_assistant_message": "## Key Learnings\n1. Este viene del último mensaje y no se guarda\n",
		"agent_transcript_path":  tr,
	})
	if n := stop(t, e, in); n != 1 {
		t.Fatalf("guardados = %d", n)
	}
	mems := e.memories(t)
	if len(mems) != 1 || mems[0].content != "Este viene de tool_input y sí se guarda aquí" {
		t.Errorf("registros = %+v", mems)
	}
}

// S11: the same item saved from PostToolUse and then from SubagentStop (transcript fallback) is not duplicated.
func TestSubagentStopPostToolUseYTranscriptNoDuplican(t *testing.T) {
	e := newEnv(t)
	cwd := t.TempDir()
	msg := "## Key Learnings\n1. Este aprendizaje llega por dos caminos distintos\n"
	if n := stop(t, e, postToolEvent(msg, cwd)); n != 1 {
		t.Fatalf("PostToolUse: guardados = %d, se esperaba 1", n)
	}
	tr := writeTranscript(t, msg)
	if n := stop(t, e, stopEvent("Mensaje final sin bloque.", cwd, tr)); n != 0 {
		t.Fatalf("SubagentStop: guardados = %d, se esperaba 0", n)
	}
	if len(e.memories(t)) != 1 {
		t.Errorf("registros = %d, se esperaba 1", len(e.memories(t)))
	}
}

// S12: transcript missing or unreadable is not an error.
func TestSubagentStopTranscriptInexistente(t *testing.T) {
	e := newEnv(t)
	if n := stop(t, e, stopEvent("sin bloque", t.TempDir(), filepath.Join(t.TempDir(), "no-existe.jsonl"))); n != 0 {
		t.Fatalf("guardados = %d", n)
	}
}

// S12: without a database nothing is saved and no error is returned.
func TestSubagentStopSinBaseDeDatos(t *testing.T) {
	msg := "## Key Learnings\n1. Este aprendizaje no se puede guardar sin base de datos\n"
	n, err := hook.SubagentStop(context.Background(), nil, "claude-code", strings.NewReader(stopEvent(msg, t.TempDir(), "")))
	if err != nil || n != 0 {
		t.Fatalf("SubagentStop con deps nil = %d, %v", n, err)
	}
}
