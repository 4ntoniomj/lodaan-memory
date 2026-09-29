package mcptools

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/config"
	"lodan/internal/database"
	"lodan/internal/embedding"
	"lodan/internal/memory"
	"lodan/internal/recall"
	"lodan/internal/session"
	"lodan/internal/topic"
)

const (
	testDims    = 64
	callTimeout = 60 * time.Second
)

var savedIDRe = regexp.MustCompile(`#(\d+) guardado`)

// newStack builds the real services over tc and the MCP server on top of them.
func newStack(tc *database.TestCluster) (Deps, *Server) {
	emb := (&embedding.FakeEmbedder{Dimensions: testDims}).WithAlias("gym", "entrenamiento")
	cfg := tc.Cfg
	sessions := session.NewManager(tc.Pool, time.Duration(cfg.SessionIdleMinutes)*time.Minute)
	topics := topic.NewResolver(tc.Pool, emb, cfg.TopicSimilarity)
	d := Deps{
		Pool:     tc.Pool,
		Memory:   memory.NewService(tc.Pool, emb, topics, sessions, cfg.DupSimilarity, time.Duration(cfg.EmbedTimeoutMs)*time.Millisecond),
		Recall:   recall.NewService(tc.Pool, emb, topics, recall.Options{MaxBytes: cfg.RecallMaxBytes, TopicDetectThreshold: cfg.TopicDetectSimilarity}),
		Sessions: sessions,
		Topics:   topics,
		Embedder: emb,
		Cfg:      cfg,
		Version:  "test",
	}
	return d, NewServer(d)
}

// connect joins a new in-memory MCP client, announcing itself as name, to srv.
func connect(t *testing.T, srv *Server, name string) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ss, err := srv.MCP.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("Connect del servidor: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: name, Version: "0"}, nil).Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect del cliente %s: %v", name, err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// call invokes a tool and returns its only text block and its IsError flag.
func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): error de protocolo: %v", tool, err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("CallTool(%s): %d bloques de contenido, quería 1", tool, len(res.Content))
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("CallTool(%s): el contenido es %T, quería *mcp.TextContent", tool, res.Content[0])
	}
	if res.StructuredContent != nil {
		t.Errorf("CallTool(%s): no debería haber salida estructurada", tool)
	}
	return text.Text, res.IsError
}

// mustOK calls a tool and fails if it returns an error result.
func mustOK(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	text, isErr := call(t, cs, tool, args)
	if isErr {
		t.Fatalf("%s devolvió un error: %s", tool, text)
	}
	return text
}

func item(title, content, kind string, topics ...string) map[string]any {
	return map[string]any{"title": title, "content": content, "kind": kind, "topics": topics}
}

func remember(t *testing.T, cs *mcp.ClientSession, items ...map[string]any) string {
	t.Helper()
	list := make([]any, len(items))
	for i, it := range items {
		list[i] = it
	}
	return mustOK(t, cs, "remember", map[string]any{"items": list})
}

func firstID(t *testing.T, text string) string {
	t.Helper()
	m := savedIDRe.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no hay un id guardado en %q", text)
	}
	return m[1]
}

func wantContains(t *testing.T, got string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(got, s) {
			t.Errorf("la salida no contiene %q:\n%s", s, got)
		}
	}
}

func TestE2E(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)

	// run ejecuta un subtest sobre tablas vacías y con servicios nuevos (memory
	// cachea el id del modelo de embeddings y Reset reinicia las identidades).
	run := func(name string, f func(t *testing.T, d Deps, srv *Server)) {
		t.Run(name, func(t *testing.T) {
			tc.Reset(t)
			d, srv := newStack(tc)
			f(t, d, srv)
		})
	}

	run("tools_list e instrucciones", func(t *testing.T, _ Deps, srv *Server) {
		cs := connect(t, srv, "test-client")
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()

		res, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
		if err != nil {
			t.Fatalf("ListTools: %v", err)
		}
		if len(res.Tools) != 6 {
			t.Fatalf("tools/list devolvió %d herramientas, quería 6", len(res.Tools))
		}
		names := map[string]bool{}
		for _, tool := range res.Tools {
			names[tool.Name] = true
			if n := utf8.RuneCountInString(tool.Description); n == 0 || n > 200 {
				t.Errorf("la descripción de %s tiene %d caracteres (máximo 200)", tool.Name, n)
			}
			if tool.OutputSchema != nil {
				t.Errorf("%s no debería tener esquema de salida", tool.Name)
			}
		}
		for _, want := range []string{"remember", "recall", "get", "revise", "session", "status"} {
			if !names[want] {
				t.Errorf("falta la herramienta %s", want)
			}
		}
		raw, _ := json.Marshal(res.Tools)
		t.Logf("tools/list: %d bytes", len(raw))
		wantContains(t, string(raw), `"enum"`, `"decision"`, `"confirm_relation"`, `"last"`)

		ir := cs.InitializeResult()
		if ir == nil || ir.Instructions != Instructions {
			t.Fatalf("las instrucciones del handshake no coinciden con Instructions")
		}
		if n := utf8.RuneCountInString(ir.Instructions); n > 1500 {
			t.Errorf("las instrucciones tienen %d caracteres (máximo 1500)", n)
		}
	})

	run("recordar, buscar, obtener e invalidar", func(t *testing.T, _ Deps, srv *Server) {
		cs := connect(t, srv, "test-client")

		out := remember(t, cs,
			item("Rutina de entrenamiento", "Entrenar pierna los lunes y espalda los jueves en el gimnasio", "preference", "entrenamiento"),
			item("Horario del gimnasio", "El gimnasio abre a las siete de la mañana", "fact", "gym"),
		)
		lines := strings.Split(out, "\n")
		if len(lines) != 2 {
			t.Fatalf("remember devolvió %d líneas, quería 2:\n%s", len(lines), out)
		}
		wantContains(t, lines[0], "guardado · temas: entrenamiento")
		wantContains(t, lines[1], "guardado · temas: entrenamiento (gym→entrenamiento)")
		id := firstID(t, lines[0])

		// Repetirlo es un duplicado.
		dup := remember(t, cs, item("Rutina de entrenamiento", "entrenar pierna los lunes y espalda los jueves en el gimnasio", "preference", "entrenamiento"))
		wantContains(t, dup, "#"+id+" ya existía (duplicado): «Rutina de entrenamiento»")

		found := mustOK(t, cs, "recall", map[string]any{"query": "rutina de entrenamiento en el gimnasio", "topics": []string{"entrenamiento"}})
		wantContains(t, found, "#"+id, "Rutina de entrenamiento", "Tema: entrenamiento")

		got := mustOK(t, cs, "get", map[string]any{"ids": []int64{1, 9999}})
		wantContains(t, got, "#1 [preferencia · vigente · ", "temas: entrenamiento", "Entrenar pierna", "no encontrados: #9999")

		mustOK(t, cs, "revise", map[string]any{"id": 1, "action": "relate", "with_id": 2})
		got = mustOK(t, cs, "get", map[string]any{"ids": []int64{2}})
		wantContains(t, got, "relaciones: relacionado con #1 «Rutina de entrenamiento» (confirmada)")

		inv := mustOK(t, cs, "revise", map[string]any{"id": 1, "action": "invalidate"})
		wantContains(t, inv, "#1 invalidado")
		after := mustOK(t, cs, "recall", map[string]any{"query": "rutina de entrenamiento en el gimnasio", "topics": []string{"entrenamiento"}})
		if strings.Contains(after, "Rutina de entrenamiento") {
			t.Errorf("recall sigue mostrando un registro invalidado:\n%s", after)
		}
		got = mustOK(t, cs, "get", map[string]any{"ids": []int64{1}})
		wantContains(t, got, "invalidado")
	})

	run("fecha, clave y validación", func(t *testing.T, _ Deps, srv *Server) {
		cs := connect(t, srv, "test-client")

		remember(t, cs, map[string]any{
			"title": "Reunión con Ana", "content": "Reunión de seguimiento con Ana", "kind": "event",
			"topics": []string{"trabajo"}, "occurred_at": "2026-09-28",
		})
		wantContains(t, mustOK(t, cs, "get", map[string]any{"ids": []int64{1}}), "#1 [evento · vigente · 2026-09-28]")

		remember(t, cs, map[string]any{"title": "Horario", "content": "Entreno a las 7", "kind": "fact", "key": "entrenamiento/horario"})
		out := remember(t, cs, map[string]any{"title": "Horario", "content": "Entreno a las 8", "kind": "fact", "key": "entrenamiento/horario"})
		wantContains(t, out, "sustituye #2")

		// Un tipo fuera del enum se rechaza como resultado de error, no como error de protocolo.
		text, isErr := call(t, cs, "remember", map[string]any{"items": []any{item("X", "Y", "cosa", "t")}})
		if !isErr {
			t.Errorf("un tipo no válido debería ser IsError: %s", text)
		}
		text, isErr = call(t, cs, "remember", map[string]any{"items": []any{map[string]any{
			"title": "X", "content": "Y", "kind": "note", "occurred_at": "ayer",
		}}})
		if !isErr || !strings.Contains(text, "occurred_at") {
			t.Errorf("una fecha no válida debería ser IsError con el motivo: %v %s", isErr, text)
		}
	})

	run("sesiones por conexión", func(t *testing.T, d Deps, srv *Server) {
		c1 := connect(t, srv, "test-client")
		c2 := connect(t, srv, "otro-cliente")

		text := mustOK(t, c1, "session", map[string]any{"action": "last"})
		wantContains(t, text, "No hay conversaciones anteriores que coincidan.")

		remember(t, c1, item("Nota del primero", "contenido del primero", "note", "uno"))
		remember(t, c2, item("Nota del segundo", "contenido del segundo", "note", "dos"))

		var n int
		var clients []string
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		if err := d.Pool.QueryRow(ctx, `SELECT count(DISTINCT id), array_agg(client ORDER BY client) FROM sessions`).Scan(&n, &clients); err != nil {
			t.Fatalf("consulta de sesiones: %v", err)
		}
		if n != 2 || len(clients) != 2 || clients[0] != "otro-cliente" || clients[1] != "test-client" {
			t.Fatalf("sesiones = %d %v, quería 2 [otro-cliente test-client]", n, clients)
		}

		// El segundo cliente ve la conversación del primero, no la suya.
		last := mustOK(t, c2, "session", map[string]any{"action": "last"})
		wantContains(t, last, "test-client", "temas: uno")
		if strings.Contains(last, "otro-cliente") {
			t.Errorf("last debe excluir la sesión propia: %s", last)
		}

		// Filtros.
		wantContains(t, mustOK(t, c1, "session", map[string]any{"action": "last", "client": "otro"}), "otro-cliente")
		wantContains(t, mustOK(t, c1, "session", map[string]any{"action": "last", "topic": "dos"}), "otro-cliente")
		today := time.Now().Format("2006-01-02")
		wantContains(t, mustOK(t, c2, "session", map[string]any{"action": "last", "date": today}), "test-client")
		wantContains(t, mustOK(t, c2, "session", map[string]any{"action": "last", "date": "2000-01-01"}), "No hay conversaciones")
		if _, isErr := call(t, c2, "session", map[string]any{"action": "last", "date": "ayer"}); !isErr {
			t.Error("una fecha no válida debería ser IsError")
		}

		list := mustOK(t, c1, "session", map[string]any{"action": "list"})
		if got := len(strings.Split(list, "\n")); got != 2 {
			t.Errorf("list devolvió %d líneas, quería 2:\n%s", got, list)
		}
	})

	run("cerrar sesión con resumen", func(t *testing.T, d Deps, srv *Server) {
		c1 := connect(t, srv, "test-client")
		remember(t, c1, item("Algo", "contenido de algo", "note", "tema"))

		if _, isErr := call(t, c1, "session", map[string]any{"action": "end"}); !isErr {
			t.Error("end sin resumen debería ser IsError")
		}
		out := mustOK(t, c1, "session", map[string]any{"action": "end", "summary": "Resumen de prueba"})
		wantContains(t, out, "Sesión cerrada con resumen (id ")

		var summary string
		var ended bool
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		if err := d.Pool.QueryRow(ctx, `SELECT COALESCE(summary, ''), ended_at IS NOT NULL FROM sessions`).Scan(&summary, &ended); err != nil {
			t.Fatalf("consulta de sesiones: %v", err)
		}
		if summary != "Resumen de prueba" || !ended {
			t.Errorf("sesión guardada: resumen %q, cerrada %v", summary, ended)
		}

		c2 := connect(t, srv, "otro-cliente")
		wantContains(t, mustOK(t, c2, "session", map[string]any{"action": "last"}), "cerrada", "resumen: Resumen de prueba")
	})

	run("borrado con confirmación", func(t *testing.T, _ Deps, srv *Server) {
		cs := connect(t, srv, "test-client")
		remember(t, cs, item("Borrable", "contenido borrable", "note", "tema"))

		text, isErr := call(t, cs, "revise", map[string]any{"id": 1, "action": "delete"})
		if !isErr || !strings.Contains(text, "confirm") {
			t.Errorf("delete sin confirm debería ser IsError con el motivo: %v %s", isErr, text)
		}
		wantContains(t, mustOK(t, cs, "get", map[string]any{"ids": []int64{1}}), "Borrable")

		wantContains(t, mustOK(t, cs, "revise", map[string]any{"id": 1, "action": "delete", "confirm": true}), "borrado definitivamente")
		wantContains(t, mustOK(t, cs, "get", map[string]any{"ids": []int64{1}}), "no encontrados: #1")
	})

	run("status", func(t *testing.T, _ Deps, srv *Server) {
		cs := connect(t, srv, "test-client")
		out := mustOK(t, cs, "status", map[string]any{})
		wantContains(t, out, "BD: ok", "registros vigentes: 0", "pendientes de embedding: 0", "temas: 0", "sesiones: 0", "Ollama: no aplica")

		// status no abre sesión.
		remember(t, cs, item("Una", "contenido uno", "note", "tema"))
		wantContains(t, mustOK(t, cs, "status", map[string]any{}), "registros vigentes: 1", "sesiones: 1")
	})
}

// newHTTPServer returns a server that needs no database: the HTTP tests never call a tool.
func newHTTPServer() *Server {
	return NewServer(Deps{Cfg: config.Default(), Version: "test"})
}

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"raw","version":"0"}}}`

// TestHTTPLocalhostProtection checks criterion 18: a request whose Host header is
// not local is rejected. The SDK applies the protection (403) when the
// connection arrives on a loopback address, which is the case of httptest.
func TestHTTPLocalhostProtection(t *testing.T) {
	ts := httptest.NewServer(NewHTTPHandler(newHTTPServer()))
	t.Cleanup(ts.Close)

	post := func(host string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(initializeBody))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if host != "" {
			req.Host = host
		}
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST con Host %q: %v", host, err)
		}
		defer resp.Body.Close() // sin leer el cuerpo: cierra la conexión
		return resp.StatusCode
	}

	if code := post("evil.example.com"); code != http.StatusForbidden {
		t.Errorf("Host evil.example.com: código %d, quería 403", code)
	}
	// Control: con el Host local (el de ts.URL) la petición no se rechaza por el Host.
	if code := post(""); code == http.StatusForbidden {
		t.Errorf("Host local: la petición se rechazó con 403")
	}
}

func TestServeHTTPRejectsNonLocalAddr(t *testing.T) {
	srv := newHTTPServer()
	for _, addr := range []string{"0.0.0.0:7438", ":7438", "example.com:7438"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := ServeHTTP(ctx, srv, addr)
		cancel()
		if err == nil {
			t.Errorf("ServeHTTP(%q) debería devolver un error", addr)
		}
	}
}

func TestServeHTTPShutdown(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- ServeHTTP(ctx, newHTTPServer(), addr) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("el servidor no empezó a escuchar en %s: %v", addr, err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Errorf("ServeHTTP devolvió %v al apagarse, quería nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ServeHTTP no terminó tras cancelar el contexto")
	}
}
