// Package mcptools exposes lodan to any AI through the Model Context Protocol.
//
// It registers exactly six tools (remember, recall, get, revise, session and
// status), the server instructions and the stdio and HTTP transports. Domain
// logic lives in packages memory, recall, session and topic: this package only
// adapts requests and responses, binds one session tracker to each MCP
// connection and formats compact Spanish plain text (fewer tokens than JSON).
package mcptools

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/config"
	"lodan/internal/embedding"
	"lodan/internal/memory"
	"lodan/internal/recall"
	"lodan/internal/session"
	"lodan/internal/topic"
)

// Instructions is delivered to every client in the MCP initialize handshake. It
// costs tokens in every session of every AI: keep it at 1500 characters or fewer.
const Instructions = `lodan es tu memoria persistente, compartida con el usuario entre todas sus IAs. Guarda sin preguntar lo que tenga sustancia (decisiones, datos estables, preferencias, eventos, notas); no guardes charla, órdenes de rol ni datos temporales. Cada registro: título corto, contenido autosuficiente con fechas absolutas (convierte «hoy» o «ayer» en AAAA-MM-DD, también en occurred_at), tipo (fact, preference, decision, event, note) y 1-3 temas que reutilicen los existentes; key para datos que cambian. Trabaja en lote: todas las preguntas en un recall (queries), todos los registros en un remember y todas las operaciones en un revise o session (ops). Tras guardar, revisa cada línea «revisa:»: si contradice lo nuevo (mismo asunto y momento, valor distinto), pregunta al usuario cuál es el bueno y aplica supersede o invalidate; si está relacionado aunque no se parezca, relaciónalo (relate o revise relate: related, part_of, contradicts); si no, ignóralo. Di lo guardado en una línea. En cuanto surja un tema, llama a recall antes de responder; get solo para el detalle. Responde con todo lo que lodan tenga sobre lo preguntado, buscando también por sus siglas y alias, y sin relleno: un dato por línea, sin párrafos y sin ofrecer aparte datos que ya tienes. Tras una compactación, vuelve a llamar a recall. Al terminar, session end con un resumen; para «la última conversación», session last.`

// Deps are the services the tools need. Everything is created by the caller
// (the CLI, or a test) and shared by all the MCP connections of the process.
type Deps struct {
	Pool     *pgxpool.Pool
	Memory   *memory.Service
	Recall   *recall.Service
	Sessions *session.Manager
	Topics   *topic.Resolver
	Embedder embedding.Embedder
	Cfg      config.Config
	Version  string
}

// Server is the lodan MCP server: the SDK server with the six tools registered,
// plus the state the tools share (the session tracker of each MCP connection).
type Server struct {
	// MCP is the underlying SDK server, to connect to a transport.
	MCP *mcp.Server

	d        Deps
	trackers *trackerMap
}

// NewServer builds the MCP server with the six lodan tools. It does not touch the
// services: they are used only when a tool is called.
func NewServer(d Deps) *Server {
	s := &Server{
		MCP: mcp.NewServer(
			&mcp.Implementation{Name: "lodan", Version: d.Version},
			&mcp.ServerOptions{Instructions: Instructions},
		),
		d:        d,
		trackers: newTrackerMap(d.Sessions),
	}
	s.register(s.MCP)
	return s
}

// Tool descriptions. Each character is paid in tokens by every session, so they
// are short (at most 200 characters).
const (
	descRemember = "Guarda registros (máx. 200 por llamada). Deduplica, resuelve temas, crea las relaciones de relate y lista con «revisa:» registros a comprobar por si contradicen o se relacionan."
	descRecall   = "Recupera lo relevante para una o varias preguntas (queries, máx. 200) en lenguaje natural: ficha del tema, últimos eventos y resultados. Llámala en cuanto surja un tema."
	descGet      = "Devuelve el contenido completo y las relaciones de registros concretos por id (máx. 200), por ejemplo tras recall."
	descRevise   = "Cambia registros, uno o varios en ops (máx. 200): update, supersede, invalidate, delete (con confirm) o relaciones (relate, confirm_relation, reject_relation, verdict)."
	descSession  = "Consulta o cierra conversaciones, una o varias en ops (máx. 200): last (filtrable por tema, cliente o fecha), list y end (cierra la actual con un resumen)."
	descStatus   = "Estado de la memoria: base de datos, registros, embeddings pendientes, temas, sesiones y Ollama."
)

// register adds the six tools to srv. The input schemas are inferred from the
// input structs (the jsonschema tag is the property description) and the enums,
// which the tag cannot express, are added by hand.
func (s *Server) register(srv *mcp.Server) {
	remember := schemaFor[rememberInput]()
	setEnum(remember.Properties["items"].Items, "kind", kindNames...)
	mcp.AddTool(srv, &mcp.Tool{Name: "remember", Description: descRemember, InputSchema: remember}, s.handleRemember)

	mcp.AddTool(srv, &mcp.Tool{Name: "recall", Description: descRecall, InputSchema: schemaFor[recallInput](), Annotations: readOnly()}, s.handleRecall)
	mcp.AddTool(srv, &mcp.Tool{Name: "get", Description: descGet, InputSchema: schemaFor[getInput](), Annotations: readOnly()}, s.handleGet)

	revise := schemaFor[reviseInput]()
	setEnum(revise, "action", reviseActions...)
	setEnum(revise, "relation", relationKinds...)
	setEnum(revise.Properties["ops"].Items, "action", reviseActions...)
	setEnum(revise.Properties["ops"].Items, "relation", relationKinds...)
	mcp.AddTool(srv, &mcp.Tool{Name: "revise", Description: descRevise, InputSchema: revise}, s.handleRevise)

	sess := schemaFor[sessionInput]()
	setEnum(sess, "action", sessionActions...)
	setEnum(sess.Properties["ops"].Items, "action", sessionActions...)
	mcp.AddTool(srv, &mcp.Tool{Name: "session", Description: descSession, InputSchema: sess}, s.handleSession)

	mcp.AddTool(srv, &mcp.Tool{Name: "status", Description: descStatus, InputSchema: schemaFor[statusInput](), Annotations: readOnly()}, s.handleStatus)
}

// readOnly marks a tool that only reads, so clients may run several calls of
// it at the same time.
func readOnly() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true}
}

// Enumerations of the tool inputs.
var kindNames = []string{
	string(memory.KindFact), string(memory.KindPreference), string(memory.KindDecision),
	string(memory.KindEvent), string(memory.KindNote),
}

var reviseActions = []string{"update", "supersede", "invalidate", "delete", "relate", "confirm_relation", "reject_relation", "alias", "reject_alias", "verdict", "merge"}

var relationKinds = []string{"related", "contradicts", "part_of", "supersedes"}

var sessionActions = []string{"last", "list", "end"}

// textResult is the successful result of a tool: a single text block.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// errorResult reports a validation or domain error to the AI as a tool result
// with IsError set (a protocol error would hide the message from the model).
func errorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
		IsError: true,
	}
}

// begin returns the session tracker of the request's MCP connection and records
// activity on its session.
func (s *Server) begin(ctx context.Context, req *mcp.CallToolRequest) (*session.Tracker, error) {
	tr := s.tracker(req)
	if err := tr.Touch(ctx); err != nil {
		return nil, err
	}
	return tr, nil
}

// tracker returns the session tracker of the request's MCP connection, creating
// it on first use with the client name announced in the handshake.
func (s *Server) tracker(req *mcp.CallToolRequest) *session.Tracker {
	return s.trackers.forSession(req.Session, func() string { return clientName(req) })
}

// clientName returns the name the MCP client gave in its handshake, or "desconocido".
//
// ServerRequest.ClientInfo reads the per-request _meta (protocol 2026-07-28) and
// falls back to ServerSession.InitializeParams().ClientInfo (earlier protocols).
func clientName(req *mcp.CallToolRequest) string {
	if ci := req.ClientInfo(); ci != nil && strings.TrimSpace(ci.Name) != "" {
		return ci.Name
	}
	return unknownClient
}

// oneLine collapses every run of whitespace, line breaks included, into one space.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
