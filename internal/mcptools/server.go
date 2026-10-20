// Package mcptools exposes lodan to any AI through the Model Context Protocol.
//
// It registers the lodan tools (remember, recall, get, revise, session, status
// and tags_index), the server instructions and the stdio and HTTP transports.
// Domain logic lives in packages memory, recall, session and topic: this
// package only adapts requests and responses, binds one session tracker to
// each MCP connection and formats compact Spanish plain text (fewer tokens
// than JSON).
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

// ProductName is the name this build advertises in the MCP handshake, so the
// model can tell at a glance that it is talking to the lite edition (no Ollama
// dependency) rather than the full one.
const ProductName = "lodan lite"

// Instructions is delivered to every client in the MCP initialize handshake. It
// costs tokens in every session of every AI: keep it at 1500 characters or fewer.
const Instructions = `lodan lite (lodan-lite) es tu memoria persistente, compartida entre las IAs del usuario. Edición sin Ollama: la búsqueda es full-text + etiquetas concretas, no hay embeddings. Guarda sin preguntar lo que tenga sustancia (decisiones, datos, preferencias, eventos, notas); no charla ni órdenes de rol. Cada registro: título corto, contenido autosuficiente con fechas absolutas (convierte «hoy» o «ayer» en AAAA-MM-DD), tipo (fact, preference, decision, event, note), al menos una etiqueta concreta de las 12 familias (proyecto-, persona-, contacto-, proveedor-, cliente-, finca-, vehiculo-, coche-, skill-, herramienta-, recurso-, evento-), 1-3 temas reutilizados y key para datos que cambian. Trabaja en lote: preguntas en recall (queries), registros en remember, operaciones en revise o session (ops). recall acepta tags_all, tags_any, date_from, date_to, mode=list; llama tags_index al empezar. Tras guardar revisa cada «revisa:»: si contradice lo nuevo, pregunta cuál es el bueno y aplica supersede o invalidate; si está relacionado aunque no se parezca, relaciónalo (relate o revise relate); si no, ignóralo. Di lo guardado en una línea. En cuanto surja un tema, recall antes de responder; get solo para el detalle. Responde con todo lo que haya, buscando por siglas y alias, sin relleno: un dato por línea, sin párrafos. Tras compactación, recall otra vez. Al terminar, session end con un resumen; para «la última conversación», session last.`

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
	// WithEmbeddings mirrors Cfg.WithEmbeddings so the maintenance loop can
	// gate FillPending without going through the full Config. False (default):
	// lodan-sin-ollama runs without Ollama; full-text + tags cubren recall.
	WithEmbeddings bool
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
			&mcp.Implementation{Name: ProductName, Version: d.Version},
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
	descRemember  = "Guarda registros (máx. 200 por llamada) con etiquetas y alias; deduplica y propone relaciones y líneas «revisa:». `preview: true` corre todo sin guardar, úsalo antes de algo caro."
	descRecall    = "Recupera lo relevante para una o varias preguntas (queries, máx. 200): ficha del tema, últimos eventos y resultados. Sigue etiquetas concretas; trae lo conectado por rareza. Llámala al surgir un tema."
	descGet       = "Devuelve el contenido completo, etiquetas, clave y relaciones de registros concretos por id (máx. 200), por ejemplo tras recall."
	descRevise    = "Cambia registros, uno o varios en ops (máx. 200): update, supersede, invalidate, delete; relaciones; aliases; veredictos sobre conflictos; o junta duplicados (merge, con huella)."
	descSession   = "Consulta o cierra conversaciones, una o varias en ops (máx. 200): last (filtrable por tema, cliente o fecha), list y end (cierra la actual con un resumen)."
	descStatus    = "Estado de la memoria: base de datos, registros, embeddings pendientes, temas, sesiones, Ollama, alias confirmados y pendientes, contradicciones abiertas y veredictos pendientes."
	descTagsIndex = "Índice de tags concretas (12 familias de D2) con jerarquía y conteos de registros vigentes. Sirve para saber qué tags existen antes de guardar."
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

	tagsIdx := schemaFor[tagsIndexInput]()
	setEnum(tagsIdx, "scope", "trabajo", "personal")
	mcp.AddTool(srv, &mcp.Tool{Name: "tags_index", Description: descTagsIndex, InputSchema: tagsIdx, Annotations: readOnly()}, s.handleTagsIndex)
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
