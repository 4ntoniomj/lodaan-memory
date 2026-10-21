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


// TestRecallQA_PersonaAntonio executes the complete QA protocol as persona Antonio.
// Persona: Antonio asking questions quickly, without accents, with typos and various options.
func TestRecallQA_PersonaAntonio(t *testing.T) {
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
	cs := connect(t, srv, "qa-client-antonio")

	// Seed fixtures: F1, F2, F3 + realistic records for persona Antonio
	remember(t, cs,
		itemAt("Albarán de gasoil", "Se apunta siempre: fecha, matrícula, empresa y litros.", "fact", "2026-10-01", "recurso-gasoil"),
		itemAt("Matrícula 3871LNY", "Matrícula 3871LNY: empresa VDM, concepto RECOL BK.", "fact", "2026-10-05", "vehiculo-3871lny", "recurso-gasoil"),
		itemAt("Lectura del gasoil", "Medición matinal del depósito de gasoil.", "fact", "2026-10-03", "recurso-gasoil"),
		itemAt("Proyecto AGM Cosechas: corte de lechuga", "Corte matinal de lechuga en Finca La Vega para cliente AGM.", "event", "2026-10-18", "proyecto-agm-cosechas", "finca-la-vega"),
		itemAt("Proyecto AGM Cosechas: camión frigorífico", "Carga en camión frigorífico matrícula 4860NGL para entrega urgente.", "fact", "2026-10-19", "proyecto-agm-cosechas", "vehiculo-4860ngl"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	call := func(args map[string]any) (string, bool, error) {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "recall", Arguments: args})
		if err != nil {
			return "", false, err
		}
		var text string
		if len(res.Content) > 0 {
			if tc, ok := res.Content[0].(*mcp.TextContent); ok {
				text = tc.Text
			}
		}
		return text, res.IsError, nil
	}

	// QA Scenario 1: "¿qué matrícula tiene el coche de Recol BK?" (natural language with question marks)
	t.Run("QA1_pregunta_natural_recol_bk", func(t *testing.T) {
		out, isErr, err := call(map[string]any{"query": "¿qué matrícula tiene el coche de Recol BK?"})
		if err != nil || isErr {
			t.Fatalf("call failed: err=%v, isErr=%v, out=%s", err, isErr, out)
		}
		t.Logf("QA1 Output:\n%s", out)
		if !strings.Contains(out, "3871LNY") {
			t.Errorf("expected 3871LNY in results, got: %s", out)
		}
	})

	// QA Scenario 2: "albaranes de gasoil" (plural, fast typing)
	t.Run("QA2_plural_albaranes_gasoil", func(t *testing.T) {
		out, isErr, err := call(map[string]any{"query": "albaranes de gasoil"})
		if err != nil || isErr {
			t.Fatalf("call failed: err=%v, isErr=%v, out=%s", err, isErr, out)
		}
		t.Logf("QA2 Output:\n%s", out)
		if !strings.Contains(out, "Albarán de gasoil") {
			t.Errorf("expected 'Albarán de gasoil' in results, got: %s", out)
		}
	})

	// QA Scenario 3: "lo último del proyecto agm-cosechas" with mode="list"
	t.Run("QA3_modo_list_lo_ultimo_agm_cosechas", func(t *testing.T) {
		out, isErr, err := call(map[string]any{
			"mode":     "list",
			"tags_any": []any{"proyecto-agm-cosechas"},
		})
		if err != nil || isErr {
			t.Fatalf("call failed: err=%v, isErr=%v, out=%s", err, isErr, out)
		}
		t.Logf("QA3 Output:\n%s", out)
		idx1 := strings.Index(out, "camión frigorífico")
		idx2 := strings.Index(out, "corte de lechuga")
		if idx1 == -1 || idx2 == -1 || idx1 >= idx2 {
			t.Errorf("expected 2026-10-19 before 2026-10-18 in list mode, got: %s", out)
		}
	})

	// QA Scenario 4: Fast typing without accents: "albaran" vs "albarán" vs "ALBARAN"
	t.Run("QA4_tildes_y_mayusculas", func(t *testing.T) {
		for _, q := range []string{"albaran", "albarán", "ALBARAN"} {
			out, isErr, err := call(map[string]any{"query": q})
			if err != nil || isErr {
				t.Fatalf("query %q failed: err=%v, isErr=%v", q, err, isErr)
			}
			if !strings.Contains(out, "Albarán de gasoil") {
				t.Errorf("query %q did not find 'Albarán de gasoil': %s", q, out)
			}
		}
	})

	// QA Scenario 5: Errata: "matricla" -> triggers fuzzy fallback
	t.Run("QA5_errata_fuzzy_fallback", func(t *testing.T) {
		out, isErr, err := call(map[string]any{"query": "matricla"})
		if err != nil || isErr {
			t.Fatalf("call failed: err=%v, isErr=%v", err, isErr)
		}
		t.Logf("QA5 Output:\n%s", out)
		if !strings.Contains(out, "(sin coincidencia exacta: resultados parecidos)") {
			t.Errorf("expected fuzzy notice, got: %s", out)
		}
		if !strings.Contains(out, "3871LNY") {
			t.Errorf("expected 3871LNY, got: %s", out)
		}
	})

	// QA Scenario 6: Extra words: "albaran gasoil inexistentepalabra" -> triggers OR fallback
	t.Run("QA6_palabra_de_mas_or_fallback", func(t *testing.T) {
		out, isErr, err := call(map[string]any{"query": "albaran gasoil inexistentepalabra"})
		if err != nil || isErr {
			t.Fatalf("call failed: err=%v, isErr=%v", err, isErr)
		}
		t.Logf("QA6 Output:\n%s", out)
		if !strings.Contains(out, "(sin coincidencia con todas las palabras: resultados con alguna de ellas)") {
			t.Errorf("expected OR notice, got: %s", out)
		}
		if !strings.Contains(out, "Albarán de gasoil") {
			t.Errorf("expected 'Albarán de gasoil', got: %s", out)
		}
	})

	// QA Scenario 7: Mode list adversarial cases
	t.Run("QA7_mode_list_adversarial", func(t *testing.T) {
		// (a) Invalid mode
		out, isErr, err := call(map[string]any{"mode": "invalido", "query": "gasoil"})
		if err != nil || !isErr {
			t.Fatalf("expected tool error for invalid mode, got err=%v, isErr=%v, out=%s", err, isErr, out)
		}
		if !strings.Contains(out, "mode no válido: usa rank o list") {
			t.Errorf("expected exact error message, got: %s", out)
		}

		// (b) List without query or filters
		out, isErr, err = call(map[string]any{"mode": "list"})
		if err != nil || !isErr {
			t.Fatalf("expected tool error for empty list, got err=%v, isErr=%v, out=%s", err, isErr, out)
		}
		if !strings.Contains(out, "mode list necesita query o algún filtro (tags_all, tags_any, date_from, date_to)") {
			t.Errorf("expected exact error message, got: %s", out)
		}
	})

	// QA Scenario 8: Adversarial inputs: special characters, quotes, emoji, excessive spaces
	t.Run("QA8_adversarial_caracteres_especiales", func(t *testing.T) {
		adversarialQueries := []string{
			`"gasoil"`,
			`   gasoil    `,
			`gasoil!@#$%^&*()`,
			`gasoil 🚀🌾`,
		}
		for _, aq := range adversarialQueries {
			out, isErr, err := call(map[string]any{"query": aq})
			if err != nil || isErr {
				t.Errorf("query %q caused error: err=%v, isErr=%v, out=%s", aq, err, isErr, out)
				continue
			}
			if !strings.Contains(out, "gasoil") && !strings.Contains(out, "Gasoil") {
				t.Errorf("query %q did not return expected gasoil record: %s", aq, out)
			}
		}
	})
}
