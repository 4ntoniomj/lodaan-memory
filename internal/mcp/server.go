package mcp

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/lodan/memory/internal/memory"
	"github.com/modelcontextprotocol/go-sdk/server"
	"github.com/modelcontextprotocol/go-sdk/transport"
)

type Server struct {
	svc *memory.Service
	srv *server.Server
}

func NewServer(svc *memory.Service) *Server {
	return &Server{
		svc: svc,
		srv: server.NewServer("lodan-memory-mcp", "1.0.0"),
	}
}

func (s *Server) SetupTools() {
	s.srv.RegisterTool("memory_store", "Guarda información nueva en la memoria", func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		content, ok := args["memory"].(string)
		if !ok {
			return nil, fmt.Errorf("missing 'memory' argument")
		}
		
		item, err := s.svc.StoreMemory(ctx, content, "fact", "assistant")
		if err != nil {
			return nil, err
		}
		
		return fmt.Sprintf("Memoria guardada con ID: %s", item.ID), nil
	})

	s.srv.RegisterTool("memory_search", "Busca recuerdos usando lenguaje natural", func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		query, ok := args["query"].(string)
		if !ok {
			return nil, fmt.Errorf("missing 'query' argument")
		}
		
		items, err := s.svc.Search(ctx, query, 5)
		if err != nil {
			return nil, err
		}
		
		return items, nil
	})
	
	// En una implementación completa registraríamos memory_get, memory_history, memory_related, memory_get_conversation...
}

func (s *Server) RunHTTP(port string) error {
	addr := fmt.Sprintf("127.0.0.1:%s", port)
	log.Printf("Starting MCP HTTP server on %s", addr)
	
	httpTransport := transport.NewHTTPServerTransport("/mcp")
	
	http.Handle("/mcp", httpTransport)
	go func() {
		if err := s.srv.Serve(httpTransport); err != nil {
			log.Printf("MCP Server error: %v", err)
		}
	}()
	
	return http.ListenAndServe(addr, nil)
}

func (s *Server) RunStdio() error {
	stdioTransport := transport.NewStdioServerTransport()
	log.Printf("Starting MCP Stdio server")
	return s.srv.Serve(stdioTransport)
}
