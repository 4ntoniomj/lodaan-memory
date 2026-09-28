package mcp

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/lodan/memory/internal/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Server struct {
	svc *memory.Service
	srv *mcp.Server
}

func NewServer(svc *memory.Service) *Server {
	return &Server{
		svc: svc,
		srv: mcp.NewServer(&mcp.Implementation{
			Name:    "lodan-memory-mcp",
			Version: "1.0.0",
		}, nil),
	}
}

type MemoryStoreInput struct {
	// Información o recuerdo en lenguaje natural para guardar, requerido
	Memory string `json:"memory" jsonschema:"required"`
}

type MemorySearchInput struct {
	// Consulta en lenguaje natural para buscar recuerdos, requerido
	Query string `json:"query" jsonschema:"required"`
	// Número máximo de recuerdos a recuperar
	Limit int    `json:"limit,omitempty"`
}

func (s *Server) SetupTools() {
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "memory_store",
		Description: "Guarda información nueva en la memoria",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input MemoryStoreInput) (*mcp.CallToolResult, any, error) {
		if input.Memory == "" {
			return nil, nil, fmt.Errorf("missing 'memory' argument")
		}

		item, err := s.svc.StoreMemory(ctx, input.Memory, "fact", "assistant", "", nil)
		if err != nil {
			return nil, nil, err
		}

		return nil, fmt.Sprintf("Memoria guardada con ID: %s", item.ID), nil
	})

	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "memory_search",
		Description: "Busca recuerdos usando lenguaje natural",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input MemorySearchInput) (*mcp.CallToolResult, any, error) {
		if input.Query == "" {
			return nil, nil, fmt.Errorf("missing 'query' argument")
		}

		limit := input.Limit
		if limit <= 0 {
			limit = 5
		}

		items, err := s.svc.Search(ctx, input.Query, limit)
		if err != nil {
			return nil, nil, err
		}

		return nil, items, nil
	})

	// En una implementación completa registraríamos memory_get, memory_history, memory_related, memory_get_conversation...
}

func (s *Server) RunHTTP(port string) error {
	addr := fmt.Sprintf("127.0.0.1:%s", port)
	log.Printf("Starting MCP HTTP server on %s", addr)

	httpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return s.srv
	}, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", httpHandler)

	return http.ListenAndServe(addr, mux)
}

func (s *Server) RunStdio() error {
	log.Printf("Starting MCP Stdio server")
	return s.srv.Run(context.Background(), &mcp.StdioTransport{})
}
