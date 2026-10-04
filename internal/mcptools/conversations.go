package mcptools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/session"
	"lodan/internal/topic"
)

// sessionInput is the input of the session tool.
type sessionInput struct {
	Action  string `json:"action" jsonschema:"last (la más reciente), list o end"`
	Topic   string `json:"topic,omitempty" jsonschema:"Solo sesiones que tocaron este tema"`
	Client  string `json:"client,omitempty" jsonschema:"Solo sesiones de este cliente (contiene el texto)"`
	Date    string `json:"date,omitempty" jsonschema:"Solo sesiones de este día, AAAA-MM-DD"`
	Summary string `json:"summary,omitempty" jsonschema:"Resumen breve de la conversación (end)"`
	Limit   int    `json:"limit,omitempty" jsonschema:"Máximo de sesiones en list (por defecto 5, máx. 20)"`
}

func (s *Server) handleSession(ctx context.Context, req *mcp.CallToolRequest, in sessionInput) (*mcp.CallToolResult, any, error) {
	tr, err := s.begin(ctx, req)
	if err != nil {
		return errorResult(err), nil, nil
	}

	switch in.Action {
	case "end":
		summary := strings.TrimSpace(in.Summary)
		if summary == "" {
			return errorResult(errors.New("end necesita un resumen (summary) no vacío")), nil, nil
		}
		id, err := tr.End(ctx, summary)
		if err != nil {
			return errorResult(err), nil, nil
		}
		return textResult(fmt.Sprintf("Sesión cerrada con resumen (id %s)", id)), nil, nil

	case "last", "list":
		f := session.Filter{
			TopicSlug: topic.Normalize(in.Topic),
			Client:    strings.TrimSpace(in.Client),
		}
		if strings.TrimSpace(in.Date) != "" {
			day, err := parseDay(in.Date)
			if err != nil {
				return errorResult(fmt.Errorf("date %q no es válida: usa AAAA-MM-DD", in.Date)), nil, nil
			}
			f.Date = day
		}

		if in.Action == "last" {
			f.ExcludeID = tr.ID()
			sess, err := s.d.Sessions.Last(ctx, f)
			if err != nil {
				return errorResult(err), nil, nil
			}
			if sess == nil {
				return textResult("No hay conversaciones anteriores que coincidan."), nil, nil
			}
			return textResult(session.Format(*sess)), nil, nil
		}

		list, err := s.d.Sessions.List(ctx, f, in.Limit)
		if err != nil {
			return errorResult(err), nil, nil
		}
		if len(list) == 0 {
			return textResult("No hay conversaciones que coincidan."), nil, nil
		}
		lines := make([]string, len(list))
		for i, sess := range list {
			lines[i] = session.Format(sess)
		}
		return textResult(strings.Join(lines, "\n")), nil, nil

	default:
		return errorResult(fmt.Errorf("acción %q no válida (válidas: last, list, end)", in.Action)), nil, nil
	}
}
