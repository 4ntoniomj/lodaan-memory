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
const Instructions = `lodan es tu memoria persistente, compartida con el usuario entre todas sus IAs. Guarda sin preguntar lo que tenga sustancia: decisiones, datos estables, preferencias, eventos y notas del usuario (p. ej. «quiero que la base de datos use embeddings»). No guardes charla, órdenes de rol (p. ej. «eres el agente orquestador»), datos temporales ni lo ya guardado. Tras guardar, díselo al usuario en una sola línea. Cada registro: título corto, contenido autosuficiente, tipo (fact, preference, decision, event, note) y 1-3 temas libres (reutiliza los existentes). Usa key para datos que cambian (p. ej. entrenamiento/horario): el nuevo sustituye al anterior. En cuanto la conversación toque un tema, llama a recall una vez con la pregunta en lenguaje natural: devuelve la ficha del tema, los últimos eventos y resultados; usa get solo si necesitas el detalle. Si algo deja de valer, usa revise (supersede o invalidate); delete solo si el usuario pide borrarlo definitivamente. Tras una compactación de contexto, vuelve a llamar a recall. Al terminar la conversación, llama a session con action end y un resumen breve. Para «la última conversación» usa session con action last.`

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
	descRemember = "Guarda registros en la memoria compartida (dato, preferencia, decisión, evento o nota). Deduplica, resuelve los temas y avisa de parecidos."
	descRecall   = "Recupera de la memoria lo relevante para una pregunta en lenguaje natural: ficha del tema, últimos eventos y resultados. Llámala en cuanto surja un tema."
	descGet      = "Devuelve el contenido completo y las relaciones de registros concretos por id (por ejemplo, tras recall)."
	descRevise   = "Cambia un registro: update, supersede (lo sustituye otro), invalidate, delete (definitivo, con confirm) o gestiona relaciones (relate, confirm_relation, reject_relation)."
	descSession  = "Consulta o cierra conversaciones: last (la última; filtrable por tema, cliente o fecha), list y end (cierra la actual con un resumen)."
	descStatus   = "Estado de la memoria: base de datos, registros, embeddings pendientes, temas, sesiones y Ollama."
)

// register adds the six tools to srv. The input schemas are inferred from the
// input structs (the jsonschema tag is the property description) and the enums,
// which the tag cannot express, are added by hand.
func (s *Server) register(srv *mcp.Server) {
	remember := schemaFor[rememberInput]()
	setEnum(remember.Properties["items"].Items, "kind", kindNames...)
	mcp.AddTool(srv, &mcp.Tool{Name: "remember", Description: descRemember, InputSchema: remember}, s.handleRemember)

	mcp.AddTool(srv, &mcp.Tool{Name: "recall", Description: descRecall, InputSchema: schemaFor[recallInput]()}, s.handleRecall)
	mcp.AddTool(srv, &mcp.Tool{Name: "get", Description: descGet, InputSchema: schemaFor[getInput]()}, s.handleGet)

	revise := schemaFor[reviseInput]()
	setEnum(revise, "action", reviseActions...)
	setEnum(revise, "relation", relationKinds...)
	mcp.AddTool(srv, &mcp.Tool{Name: "revise", Description: descRevise, InputSchema: revise}, s.handleRevise)

	sess := schemaFor[sessionInput]()
	setEnum(sess, "action", sessionActions...)
	mcp.AddTool(srv, &mcp.Tool{Name: "session", Description: descSession, InputSchema: sess}, s.handleSession)

	mcp.AddTool(srv, &mcp.Tool{Name: "status", Description: descStatus, InputSchema: schemaFor[statusInput]()}, s.handleStatus)
}

// Enumerations of the tool inputs.
var kindNames = []string{
	string(memory.KindFact), string(memory.KindPreference), string(memory.KindDecision),
	string(memory.KindEvent), string(memory.KindNote),
}

var reviseActions = []string{"update", "supersede", "invalidate", "delete", "relate", "confirm_relation", "reject_relation"}

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
