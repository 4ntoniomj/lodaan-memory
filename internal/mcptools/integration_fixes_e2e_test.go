package mcptools

import (
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/database"
)

// savedLineRe matches the "#N guardado" line of each saved item.
var savedLineRe = regexp.MustCompile(`(?m)^#\d+ guardado`)

// TestIntegrationFixes_E2E reproduce los hallazgos del QA de costuras de la
// integración lodan-auditoria (H1, H2, H3) a través de la interfaz MCP.
func TestIntegrationFixes_E2E(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)

	run := func(name string, f func(t *testing.T, cs *mcp.ClientSession)) {
		t.Run(name, func(t *testing.T) {
			tc.Reset(t)
			_, srv := newStack(tc)
			f(t, connect(t, srv, "test-client"))
		})
	}

	// H1: dos casi duplicados en la misma llamada no se guardan los dos.
	run("H1 casi duplicado dentro del mismo lote", func(t *testing.T, cs *mcp.ClientSession) {
		base := "La bomba de riego del pozo dos se averió el martes por la tarde"
		item := func(content string) map[string]any {
			return map[string]any{"title": "Avería bomba", "content": content, "kind": "event", "tags": []any{"finca-norte"}}
		}
		out, isErr := call(t, cs, "remember", map[string]any{"items": []any{item(base + "."), item(base + "!")}})
		if isErr {
			t.Fatalf("remember devolvió error: %s", out)
		}
		want := "item 2 no guardado: casi idéntico al item 1 (parecido "
		if !strings.Contains(out, want) {
			t.Errorf("salida sin rechazo del item 2\nquería: %s\nobtenido:\n%s", want, out)
		}
		if n := len(savedLineRe.FindAllString(out, -1)); n != 1 {
			t.Errorf("se esperaba 1 registro guardado, hay %d:\n%s", n, out)
		}

		// Con force: true se guardan los dos y el segundo avisa del primero.
		forced := item(base + "?")
		forced["force"] = true
		out, _ = call(t, cs, "remember", map[string]any{"items": []any{item(base + " y el miércoles."), forced}})
		if strings.Contains(out, "no guardado: casi idéntico al item") {
			t.Errorf("force: true no debería rechazar el item 2:\n%s", out)
		}

		// La vista previa informa igual.
		out, _ = call(t, cs, "remember", map[string]any{"preview": true, "items": []any{item("Otro texto distinto sobre el tractor rojo del norte."), item("Otro texto distinto sobre el tractor rojo del norte!")}})
		if !strings.Contains(out, "casi idéntico al item 1 (parecido ") {
			t.Errorf("preview no informa del casi duplicado dentro del lote:\n%s", out)
		}
	})

	// H2: mode=list respeta include_history aunque se filtre por etiquetas.
	run("H2 mode list con include_history", func(t *testing.T, cs *mcp.ClientSession) {
		keyed := func(content, day string) map[string]any {
			it := itemAt("Precio del gasoil", content, "fact", day, "recurso-precios")
			it["key"] = "recurso-precios/gasoil"
			return it
		}
		old := remember(t, cs, keyed("Precio gasoil agrícola: 1,10 euros el litro.", "2026-10-01"))
		oldID := firstID(t, old)
		remember(t, cs, keyed("Precio gasoil agrícola: 1,25 euros el litro.", "2026-10-10"))

		out, _ := call(t, cs, "recall", map[string]any{
			"query": "precio gasoil", "mode": "list", "tags_any": []any{"recurso-precios"}, "include_history": true,
		})
		if !strings.Contains(out, "#"+oldID+" ") {
			t.Errorf("mode=list con include_history no muestra el registro sustituido #%s:\n%s", oldID, out)
		}

		// Sin include_history sigue sin aparecer.
		out, _ = call(t, cs, "recall", map[string]any{
			"query": "precio gasoil", "mode": "list", "tags_any": []any{"recurso-precios"},
		})
		if strings.Contains(out, "#"+oldID+" ") {
			t.Errorf("sin include_history no debería aparecer #%s:\n%s", oldID, out)
		}
	})

	// H3: el plural sin tilde encuentra el singular con tilde, y viceversa, sin avisos de búsqueda alternativa.
	run("H3 plural y singular con y sin tilde", func(t *testing.T, cs *mcp.ClientSession) {
		remember(t, cs,
			itemAt("Albarán antiguo", "Albarán de gasoil de la cooperativa.", "fact", "2026-10-01", "recurso-gasoil"),
			itemAt("Revisión de albaranes", "Los albaranes de abono se archivan en la carpeta azul.", "fact", "2026-10-02", "recurso-abono"),
		)
		cases := []struct{ query, want string }{
			{"albaranes", "Albarán antiguo"},
			{"albaranes gasoil", "Albarán antiguo"},
			{"albaran", "Albarán antiguo"},
			{"albarán", "Revisión de albaranes"},
			{"albaranes", "Revisión de albaranes"},
		}
		for _, c := range cases {
			out, _ := call(t, cs, "recall", map[string]any{"query": c.query, "mode": "list"})
			if !strings.Contains(out, c.want) {
				t.Errorf("recall %q no encuentra %q:\n%s", c.query, c.want, out)
			}
			if strings.Contains(out, "(sin coincidencia") {
				t.Errorf("recall %q usó una búsqueda alternativa:\n%s", c.query, out)
			}
		}
	})
}
