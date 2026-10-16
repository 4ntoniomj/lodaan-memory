package mcptools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/memory"
	"lodan/internal/session"
	"lodan/internal/topic"
)

// sessionInput is the input of the session tool: one operation in the
// top-level fields, several in ops, or both (the top-level one goes first).
type sessionInput struct {
	Action  string      `json:"action,omitempty" jsonschema:"last (la más reciente), list o end"`
	Topic   string      `json:"topic,omitempty" jsonschema:"Solo sesiones que tocaron este tema"`
	Client  string      `json:"client,omitempty" jsonschema:"Solo sesiones de este cliente (contiene el texto)"`
	Date    string      `json:"date,omitempty" jsonschema:"Solo sesiones de este día, AAAA-MM-DD"`
	Summary string      `json:"summary,omitempty" jsonschema:"Resumen breve de la conversación (end)"`
	Limit   int         `json:"limit,omitempty" jsonschema:"Máximo de sesiones en list (por defecto 5, máx. 20)"`
	Ops     []sessionOp `json:"ops,omitempty" jsonschema:"Varias operaciones en una sola llamada (máx. 200 en total), con los mismos campos; se ejecutan en orden"`
}

// sessionOp is one operation of the session tool.
type sessionOp struct {
	Action  string `json:"action" jsonschema:"last, list o end"`
	Topic   string `json:"topic,omitempty" jsonschema:"Solo sesiones que tocaron este tema"`
	Client  string `json:"client,omitempty" jsonschema:"Solo sesiones de este cliente"`
	Date    string `json:"date,omitempty" jsonschema:"Solo sesiones de este día, AAAA-MM-DD"`
	Summary string `json:"summary,omitempty" jsonschema:"Resumen (end)"`
	Limit   int    `json:"limit,omitempty" jsonschema:"Máximo de sesiones en list"`
}

func (s *Server) handleSession(ctx context.Context, req *mcp.CallToolRequest, in sessionInput) (*mcp.CallToolResult, any, error) {
	var ops []sessionOp
	if in.Action != "" {
		ops = append(ops, sessionOp{Action: in.Action, Topic: in.Topic, Client: in.Client, Date: in.Date, Summary: in.Summary, Limit: in.Limit})
	}
	ops = append(ops, in.Ops...)
	if len(ops) == 0 {
		return errorResult(errors.New("hay que enviar una operación (action) u ops")), nil, nil
	}
	if len(ops) > memory.MaxBatch {
		return errorResult(batchSizeError(len(ops), "operaciones")), nil, nil
	}

	tr, err := s.begin(ctx, req)
	if err != nil {
		return errorResult(err), nil, nil
	}
	results := make([]opResult, len(ops))
	for i, op := range ops {
		text, err := s.runSession(ctx, tr, op)
		results[i] = opResult{text: text, err: err}
	}
	return batchResult(results), nil, nil
}

// runSession executes one operation of the session tool.
func (s *Server) runSession(ctx context.Context, tr *session.Tracker, in sessionOp) (string, error) {
	switch in.Action {
	case "end":
		summary := strings.TrimSpace(in.Summary)
		if summary == "" {
			return "", errors.New("end necesita un resumen (summary) no vacío")
		}
		id, err := tr.End(ctx, summary)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Sesión cerrada con resumen (id %s)", id), nil

	case "last", "list":
		f := session.Filter{
			TopicSlug: topic.Normalize(in.Topic),
			Client:    strings.TrimSpace(in.Client),
		}
		if strings.TrimSpace(in.Date) != "" {
			day, err := parseDay(in.Date)
			if err != nil {
				return "", fmt.Errorf("date %q no es válida: usa AAAA-MM-DD", in.Date)
			}
			f.Date = day
		}

		if in.Action == "last" {
			f.ExcludeID = tr.ID()
			sess, err := s.d.Sessions.Last(ctx, f)
			if err != nil {
				return "", err
			}
			if sess == nil {
				return "No hay conversaciones anteriores que coincidan.", nil
			}
			return session.Format(*sess), nil
		}

		list, err := s.d.Sessions.List(ctx, f, in.Limit)
		if err != nil {
			return "", err
		}
		if len(list) == 0 {
			return "No hay conversaciones que coincidan.", nil
		}
		lines := make([]string, len(list))
		for i, sess := range list {
			lines[i] = session.Format(sess)
		}
		return strings.Join(lines, "\n"), nil

	default:
		return "", fmt.Errorf("acción %q no válida (válidas: last, list, end)", in.Action)
	}
}
