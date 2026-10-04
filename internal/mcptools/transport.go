package mcptools

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// readHeaderTimeout bounds how long a client may take to send its request headers.
	readHeaderTimeout = 10 * time.Second
	// shutdownTimeout bounds the graceful HTTP shutdown (open SSE streams never end by themselves).
	shutdownTimeout = 3 * time.Second
)

// ServeStdio serves srv over stdin/stdout until ctx is cancelled or the client
// disconnects. Nothing else may write to stdout while it runs.
func ServeStdio(ctx context.Context, s *Server) error {
	return s.MCP.Run(ctx, &mcp.StdioTransport{})
}

// ValidateHTTPAddr checks that addr is host:port with host 127.0.0.1 or localhost.
func ValidateHTTPAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("dirección HTTP %q no válida (se espera host:puerto): %w", addr, err)
	}
	if host != "127.0.0.1" && host != "localhost" {
		return fmt.Errorf("dirección HTTP %q no es local: solo se admite 127.0.0.1 o localhost", addr)
	}
	return nil
}

// NewHTTPHandler returns the streamable HTTP handler of srv. The SDK's
// protection against DNS rebinding (403 for a non-local Host header on a local
// connection) stays enabled: DisableLocalhostProtection is never set.
func NewHTTPHandler(s *Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.MCP }, nil)
}

// ServeHTTP serves srv over HTTP on addr, which must be on 127.0.0.1 or
// localhost. When ctx is cancelled it shuts the server down and returns nil.
func ServeHTTP(ctx context.Context, s *Server, addr string) error {
	if err := ValidateHTTPAddr(addr); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("no se pudo escuchar en %s: %w", addr, err)
	}
	hs := &http.Server{
		Handler:           NewHTTPHandler(s),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := hs.Shutdown(shutCtx); err != nil {
		_ = hs.Close() // streams that do not end: force the close
	}
	<-errc
	return nil
}
