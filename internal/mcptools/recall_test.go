package mcptools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/database"
	"lodan/internal/embedding"
	"lodan/internal/memory"
	"lodan/internal/recall"
	"lodan/internal/session"
	"lodan/internal/topic"
)

// TestRecallInputSchemaIncludesFilters comprueba que el esquema JSON de recallInput expone
// los filtros de primera clase de S12 (tags_all, tags_any, date_from, date_to) en español.
func TestRecallInputSchemaIncludesFilters(t *testing.T) {
	schema := schemaFor[recallInput]()
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("no se pudo serializar el esquema: %v", err)
	}
	text := string(raw)
	for _, want := range []string{"tags_all", "tags_any", "date_from", "date_to"} {
		if !strings.Contains(text, want) {
			t.Errorf("el esquema de recallInput no contiene %q: %s", want, text)
		}
	}
}

// TestRecallQueriesAllowsEmptyWhenFilters comprueba que recallQueries admite
// query vacía cuando hay al menos un filtro (tags o fechas). Sin query ni filtros
// debe seguir fallando con el mismo mensaje de antes.
func TestRecallQueriesAllowsEmptyWhenFilters(t *testing.T) {
	cases := []struct {
		name   string
		in     recallInput
		passes bool
	}{
		{
			name:   "query vacía + tags_all",
			in:     recallInput{TagsAll: []string{"proyecto-erp-agm"}},
			passes: true,
		},
		{
			name:   "query vacía + tags_any",
			in:     recallInput{TagsAny: []string{"finca-rebelao"}},
			passes: true,
		},
		{
			name:   "query vacía + date_from",
			in:     recallInput{DateFrom: "2026-01-01"},
			passes: true,
		},
		{
			name:   "query vacía + date_to",
			in:     recallInput{DateTo: "2026-12-31"},
			passes: true,
		},
		{
			name:   "sin query ni filtros",
			in:     recallInput{},
			passes: false,
		},
		{
			name:   "query con espacios y filtros",
			in:     recallInput{Query: "   ", TagsAll: []string{"proyecto-erp-agm"}},
			passes: true,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := recallQueries(tt.in)
			if tt.passes && err != nil {
				t.Errorf("recallQueries(%+v) = %v, quería éxito", tt.in, err)
			}
			if !tt.passes && err == nil {
				t.Errorf("recallQueries(%+v) = nil, quería error", tt.in)
			}
		})
	}
}

// TestHandleRecallTagsAllThroughMCP monta el server MCP y comprueba que un recall
// con tags_all y query vacía devuelve los registros correctos sin pasar por el texto.
func TestHandleRecallTagsAllThroughMCP(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)
	tc.Reset(t)
	cfg := tc.Cfg
	sessions := session.NewManager(tc.Pool, time.Duration(cfg.SessionIdleMinutes)*time.Minute)
	emb := &embedding.FakeEmbedder{Dimensions: testDims}
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
	srv := NewServer(d)
	cs := connect(t, srv, "test-client")

	// Guardamos dos registros con tags concretas vía remember.
	remember(t, cs,
		item("Cosecha Rebelao", "Cosecha en Rebelao del proyecto erp-agm", "fact", "proyecto-erp-agm", "finca-rebelao"),
		item("Cosecha Royalveg", "Cosecha en Royalveg del proyecto erp-agm", "fact", "proyecto-erp-agm", "finca-royalveg"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "recall", Arguments: map[string]any{
		"tags_all": []any{"proyecto-erp-agm", "finca-rebelao"},
	}})
	if err != nil {
		t.Fatalf("recall con tags_all falló: %v", err)
	}
	if res.IsError {
		t.Fatalf("recall con tags_all devolvió IsError: %v", res.Content)
	}
	out := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(out, "Cosecha Rebelao") {
		t.Errorf("la respuesta debería contener el registro de Rebelao:\n%s", out)
	}
	if strings.Contains(out, "Cosecha Royalveg") {
		t.Errorf("la respuesta NO debería contener el registro de Royalveg (no tiene finca-rebelao):\n%s", out)
	}
}