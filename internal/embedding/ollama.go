package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL   = "http://127.0.0.1:11434"
	defaultTimeout   = 30 * time.Second
	maxErrorBody     = 300      // bytes of an error body included in messages
	maxResponseBytes = 64 << 20 // sanity cap for a successful response
)

// Options configures the Ollama client.
type Options struct {
	// BaseURL is the Ollama server URL. Defaults to http://127.0.0.1:11434.
	BaseURL string
	// Model is the embedding model name, e.g. "embeddinggemma". Required.
	Model string
	// Dims is the expected (and requested) vector dimension. Required.
	Dims int
	// KeepAlive controls how long Ollama keeps the model loaded. An integer
	// string such as "-1" is sent as a JSON number (negative = forever);
	// anything else, such as "5m", is sent as a string. Empty omits it.
	KeepAlive string
	// Timeout is the HTTP timeout when HTTPClient is nil. Defaults to 30 s.
	Timeout time.Duration
	// HTTPClient overrides the HTTP client (mainly for tests).
	HTTPClient *http.Client
}

// Ollama is an Embedder backed by Ollama's /api/embed endpoint.
type Ollama struct {
	baseURL   string
	model     string
	dims      int
	keepAlive any
	client    *http.Client
	format    promptFormat
}

var (
	_ Embedder = (*Ollama)(nil)
	_ Embedder = (*FakeEmbedder)(nil)
)

// NewOllama builds an Ollama client. It fails if the model or the dimension
// are missing.
func NewOllama(opts Options) (*Ollama, error) {
	if strings.TrimSpace(opts.Model) == "" {
		return nil, errors.New("embedding: falta el nombre del modelo")
	}
	if opts.Dims <= 0 {
		return nil, fmt.Errorf("embedding: las dimensiones deben ser positivas (recibido %d)", opts.Dims)
	}
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		base = defaultBaseURL
	}
	client := opts.HTTPClient
	if client == nil {
		timeout := opts.Timeout
		if timeout <= 0 {
			timeout = defaultTimeout
		}
		client = &http.Client{Timeout: timeout}
	}
	return &Ollama{
		baseURL:   base,
		model:     opts.Model,
		dims:      opts.Dims,
		keepAlive: keepAliveValue(opts.KeepAlive),
		client:    client,
		format:    formatFor(opts.Model),
	}, nil
}

// keepAliveValue converts the configured string into the JSON value to send:
// a number if it is an integer, a string otherwise, nil if empty.
func keepAliveValue(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return s
}

type embedRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Truncate   bool     `json:"truncate"`
	KeepAlive  any      `json:"keep_alive,omitempty"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Model returns the configured model name.
func (o *Ollama) Model() string { return o.model }

// Dims returns the configured vector dimension.
func (o *Ollama) Dims() int { return o.dims }

// EmbedDocuments embeds docs in a single request, applying the document
// prompt format of the model.
func (o *Ollama) EmbedDocuments(ctx context.Context, docs []Document) ([][]float32, error) {
	if len(docs) == 0 {
		return nil, nil
	}
	inputs := make([]string, len(docs))
	for i, d := range docs {
		inputs[i] = o.format.doc(d.Title, d.Text)
	}
	return o.embed(ctx, inputs)
}

// EmbedQuery embeds a search query, applying the query prompt format.
func (o *Ollama) EmbedQuery(ctx context.Context, q string) ([]float32, error) {
	vecs, err := o.embed(ctx, []string{o.format.query(q)})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

// Ping checks that the Ollama server answers on GET /api/version.
func (o *Ollama) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL+"/api/version", nil)
	if err != nil {
		return fmt.Errorf("embedding: no se pudo crear la petición: %w", err)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return o.connError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return httpError(resp)
	}
	return nil
}

// embed sends the already-prefixed inputs and validates the response.
func (o *Ollama) embed(ctx context.Context, inputs []string) ([][]float32, error) {
	payload, err := json.Marshal(embedRequest{
		Model:      o.model,
		Input:      inputs,
		Truncate:   true,
		KeepAlive:  o.keepAlive,
		Dimensions: o.dims,
	})
	if err != nil {
		return nil, fmt.Errorf("embedding: no se pudo serializar la petición: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/embed", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("embedding: no se pudo crear la petición: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, o.connError(ctx, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, httpError(resp)
	}

	var out embedResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("embedding: petición cancelada: %w", ctx.Err())
		}
		return nil, fmt.Errorf("embedding: respuesta de Ollama ilegible: %w", err)
	}

	if len(out.Embeddings) != len(inputs) {
		return nil, fmt.Errorf("embedding: Ollama devolvió %d vectores para %d textos", len(out.Embeddings), len(inputs))
	}
	for i, v := range out.Embeddings {
		if len(v) != o.dims {
			return nil, fmt.Errorf("embedding: el vector %d tiene %d dimensiones y se esperaban %d (revisa embed_dims y el modelo %q)",
				i, len(v), o.dims, o.model)
		}
		if !normalizeL2(v) {
			return nil, fmt.Errorf("embedding: el vector %d es nulo o no es finito", i)
		}
	}
	return out.Embeddings, nil
}

// connError classifies a transport error: context cancellation is reported
// as is, any other failure is wrapped with ErrUnavailable.
func (o *Ollama) connError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("embedding: petición cancelada: %w", ctx.Err())
	}
	return fmt.Errorf("%w: no se pudo conectar con %s: %w", ErrUnavailable, o.baseURL, err)
}

// httpError builds an error from a non-2xx response, including up to
// maxErrorBody bytes of its body. 5xx responses are also wrapped with
// ErrUnavailable so that callers keep the record as pending; 4xx are not.
func httpError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	msg := strings.TrimSpace(string(b))
	detail := fmt.Sprintf("Ollama respondió con código %d", resp.StatusCode)
	if msg != "" {
		detail += ": " + msg
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("%w: %s", ErrUnavailable, detail)
	}
	return fmt.Errorf("embedding: %s", detail)
}
