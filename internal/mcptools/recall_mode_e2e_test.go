package mcptools

import (
	"context"
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

type noEmbedder struct{ dims int }

func (e noEmbedder) EmbedDocuments(context.Context, []embedding.Document) ([][]float32, error) {
	return nil, embedding.ErrUnavailable
}

func (e noEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	return nil, embedding.ErrUnavailable
}

func (e noEmbedder) Model() string { return "unavailable" }
func (e noEmbedder) Dims() int     { return e.dims }

// S5 Modo lista y S6 Menos ruido probados a través de la interfaz MCP completa.
func TestRecallModeE2E(t *testing.T) {
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

	// Guardamos las fixtures F1, F2 y F3
	// F1: 2026-10-01
	// F2: 2026-10-05
	// F3: 2026-10-03
	remember(t, cs,
		itemAt("Albarán de gasoil", "Se apunta siempre: fecha, matrícula, empresa y litros.", "fact", "2026-10-01", "recurso-gasoil"),
		itemAt("Matrícula 3871LNY", "Matrícula 3871LNY: empresa VDM, concepto RECOL BK.", "fact", "2026-10-05", "vehiculo-3871lny", "recurso-gasoil"),
		itemAt("Lectura del gasoil", "Medición matinal del depósito de gasoil.", "fact", "2026-10-03", "recurso-gasoil"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	t.Run("S5 modo list ordena por fecha descendente F2 F3 F1", func(t *testing.T) {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "recall", Arguments: map[string]any{
			"mode":     "list",
			"tags_any": []any{"recurso-gasoil"},
		}})
		if err != nil {
			t.Fatalf("call recall falló: %v", err)
		}
		if res.IsError {
			t.Fatalf("call recall devolvió IsError: %v", res.Content)
		}
		out := res.Content[0].(*mcp.TextContent).Text

		idxF2 := strings.Index(out, "Matrícula 3871LNY")
		idxF3 := strings.Index(out, "Lectura del gasoil")
		idxF1 := strings.Index(out, "Albarán de gasoil")

		if idxF2 == -1 || idxF3 == -1 || idxF1 == -1 {
			t.Fatalf("la salida debe contener F1, F2 y F3:\n%s", out)
		}
		if !(idxF2 < idxF3 && idxF3 < idxF1) {
			t.Fatalf("el orden debe ser F2 (2026-10-05) antes de F3 (2026-10-03) y F3 antes de F1 (2026-10-01), pero los índices fueron F2=%d, F3=%d, F1=%d:\n%s", idxF2, idxF3, idxF1, out)
		}
	})

	t.Run("S5 mode no válido", func(t *testing.T) {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "recall", Arguments: map[string]any{
			"mode":  "otro",
			"query": "gasoil",
		}})
		if err != nil {
			t.Fatalf("error de llamada inesperado: %v", err)
		}
		if !res.IsError {
			t.Fatalf("se esperaba IsError=true para mode no válido")
		}
		out := res.Content[0].(*mcp.TextContent).Text
		want := "mode no válido: usa rank o list"
		if !strings.Contains(out, want) {
			t.Fatalf("error %q no contiene %q", out, want)
		}
	})

	t.Run("S5 mode list sin query ni filtros", func(t *testing.T) {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "recall", Arguments: map[string]any{
			"mode": "list",
		}})
		if err != nil {
			t.Fatalf("error de llamada inesperado: %v", err)
		}
		if !res.IsError {
			t.Fatalf("se esperaba IsError=true para mode list sin filtros ni query")
		}
		out := res.Content[0].(*mcp.TextContent).Text
		want := "mode list necesita query o algún filtro (tags_all, tags_any, date_from, date_to)"
		if !strings.Contains(out, want) {
			t.Fatalf("error %q no contiene %q", out, want)
		}
	})
}

// S6 Menos ruido: una llamada con queries de 3 preguntas y sin embeddings muestra la línea
// de embeddings una sola vez.
func TestRecallModeE2E_S6_MenosRuido_Embeddings(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)
	tc.Reset(t)
	cfg := tc.Cfg
	sessions := session.NewManager(tc.Pool, time.Duration(cfg.SessionIdleMinutes)*time.Minute)
	emb := noEmbedder{dims: testDims}
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

	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "recall", Arguments: map[string]any{
		"queries": []any{"gasoil", "facturas", "matricula"},
	}})
	if err != nil {
		t.Fatalf("call recall falló: %v", err)
	}
	if res.IsError {
		t.Fatalf("call recall devolvió IsError: %v", res.Content)
	}
	out := res.Content[0].(*mcp.TextContent).Text
	line := "(solo búsqueda por texto: el servicio de embeddings no está disponible)"
	count := strings.Count(out, line)
	if count != 1 {
		t.Fatalf("la línea de embeddings debería aparecer exactamente 1 vez, pero apareció %d veces:\n%s", count, out)
	}
}
