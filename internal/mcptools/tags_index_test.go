package mcptools

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/database"
)

// TestTagsIndex cubre S15 de odd/tasks/lodan-sin-ollama.md: tags_index devuelve el
// Ã¡rbol de tags concretas (las 12 familias de D2) con jerarquÃ­a y conteos de
// registros vigentes, opcionalmente filtrado por la etiqueta general `trabajo`
// o `personal`.
//
// Sigue el patrÃ³n de e2e_test.go: helpers newStack, connect, call, mustOK, wantContains.
func TestTagsIndex(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)
	run := func(name string, f func(t *testing.T, d Deps, cs *mcp.ClientSession)) {
		t.Run(name, func(t *testing.T) {
			tc.Reset(t)
			d, srv := newStack(tc)
			f(t, d, connect(t, srv, "test-client"))
		})
	}

	// Semilla con tags concretas de varias familias, con jerarquÃ­a y mezcladas con
	// generales (trabajo, personal) que NO deben aparecer en el Ã­ndice.
	seedMixed := func(t *testing.T, cs *mcp.ClientSession) {
		t.Helper()
		remember(t, cs,
			// 4 records de coche-volvo-s60 (3 con atributos, 1 sin atributo)
			tagItem("MatrÃ­cula 1", "matrÃ­cula uno", "coche-volvo-s60", "coche-volvo-s60/matricula", "trabajo"),
			tagItem("MatrÃ­cula 2", "matrÃ­cula dos", "coche-volvo-s60", "coche-volvo-s60/matricula"),
			tagItem("ITV pasada", "itv del coche", "coche-volvo-s60", "coche-volvo-s60/itv"),
			tagItem("Seguro", "seguro", "coche-volvo-s60", "coche-volvo-s60/seguro", "trabajo"),
			// 3 records de proyecto-erp-agm
			tagItem("DecisiÃ³n 1", "decision uno del proyecto", "proyecto-erp-agm", "trabajo"),
			tagItem("DecisiÃ³n 2", "decision dos del proyecto", "proyecto-erp-agm"),
			tagItem("DecisiÃ³n 3", "decision tres del proyecto", "proyecto-erp-agm", "trabajo"),
			// 2 records de persona-antonio (personal)
			tagItem("Cumple de Antonio", "30 aÃ±os", "persona-antonio", "personal"),
			tagItem("DirecciÃ³n de Antonio", "calle uno", "persona-antonio", "personal"),
			// 1 record sÃ³lo con tag general (no debe contar)
			tagItem("Nota suelta", "contenido sin tag concreta", "general"),
		)
	}

	run("ReturnsTree: Ã¡rbol con jerarquÃ­a y conteos correctos", func(t *testing.T, _ Deps, cs *mcp.ClientSession) {
		seedMixed(t, cs)
		out := mustOK(t, cs, "tags_index", map[string]any{})

		// LÃ­neas que el Ã¡rbol debe contener, con la sangrÃ­a esperada.
		wantContains(t, out,
			"coche-volvo-s60 (4)",
			"  coche-volvo-s60/matricula (2)",
			"  coche-volvo-s60/itv (1)",
			"  coche-volvo-s60/seguro (1)",
			"persona-antonio (2)",
			"proyecto-erp-agm (3)",
		)
		// El padre aparece antes que sus hijos (orden por slug).
		if idxParent := strings.Index(out, "coche-volvo-s60 (4)"); idxParent < 0 {
			t.Errorf("falta la lÃ­nea padre coche-volvo-s60 (4):\n%s", out)
		} else {
			for _, child := range []string{
				"coche-volvo-s60/matricula",
				"coche-volvo-s60/itv",
				"coche-volvo-s60/seguro",
			} {
				if idxChild := strings.Index(out, child); idxChild < 0 {
					t.Errorf("falta la lÃ­nea %q en el Ã¡rbol:\n%s", child, out)
				} else if idxChild < idxParent {
					t.Errorf("el hijo %q aparece antes que su padre:\n%s", child, out)
				}
			}
		}

		// Las tags generales no deben aparecer en el Ã­ndice.
		for _, forbidden := range []string{"#general", "general (", "trabajo (", "personal ("} {
			if strings.Contains(out, forbidden) {
				t.Errorf("la salida contiene la tag general %q:\n%s", forbidden, out)
			}
		}
	})

	run("ScopeFilter trabajo: solo registros con #trabajo", func(t *testing.T, _ Deps, cs *mcp.ClientSession) {
		seedMixed(t, cs)
		out := mustOK(t, cs, "tags_index", map[string]any{"scope": "trabajo"})

		// SÃ³lo cuentan los registros con #trabajo.
		// coche-volvo-s60: records 1 (matrÃ­cula) y 4 (seguro) â†’ 2.
		wantContains(t, out,
			"coche-volvo-s60 (2)",
			"  coche-volvo-s60/matricula (1)",
			"  coche-volvo-s60/seguro (1)",
			"proyecto-erp-agm (2)",
		)
		if strings.Contains(out, "persona-antonio") {
			t.Errorf("scope=trabajo no debe listar persona-antonio:\n%s", out)
		}
		if strings.Contains(out, "coche-volvo-s60/itv") {
			t.Errorf("coche-volvo-s60/itv no tiene #trabajo, no debe listarse:\n%s", out)
		}
	})

	run("ScopeFilter personal: solo registros con #personal", func(t *testing.T, _ Deps, cs *mcp.ClientSession) {
		seedMixed(t, cs)
		out := mustOK(t, cs, "tags_index", map[string]any{"scope": "personal"})

		wantContains(t, out, "persona-antonio (2)")
		if strings.Contains(out, "coche-volvo-s60") {
			t.Errorf("scope=personal no debe listar coche-volvo-s60:\n%s", out)
		}
		if strings.Contains(out, "proyecto-erp-agm") {
			t.Errorf("scope=personal no debe listar proyecto-erp-agm:\n%s", out)
		}
	})

	run("EmptyDB: BD vacÃ­a no entra en panic y devuelve mensaje", func(t *testing.T, _ Deps, cs *mcp.ClientSession) {
		out := mustOK(t, cs, "tags_index", map[string]any{})
		if strings.Contains(out, "panic") || strings.Contains(out, "PANIC") {
			t.Errorf("la salida no deberÃ­a contener un panic: %s", out)
		}
		if out == "" {
			t.Errorf("la salida no deberÃ­a estar vacÃ­a, esperaba un mensaje de aviso")
		}
	})

	run("scope invÃ¡lido devuelve error de validaciÃ³n, no de protocolo", func(t *testing.T, _ Deps, cs *mcp.ClientSession) {
		text, isErr := call(t, cs, "tags_index", map[string]any{"scope": "vacaciones"})
		if !isErr {
			t.Errorf("scope invÃ¡lido deberÃ­a ser IsError: %s", text)
		}
		if !strings.Contains(text, "scope") {
			t.Errorf("el error deberÃ­a mencionar el campo scope: %s", text)
		}
	})

	run("descripciÃ³n â‰¤ 200 caracteres y anotaciÃ³n readOnly", func(t *testing.T, _ Deps, cs *mcp.ClientSession) {
		tools := listTools(t, cs)
		var ti *mcp.Tool
		found := false
		for _, tool := range tools.Tools {
			if tool.Name == "tags_index" {
				ti = tool
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("tags_index no estÃ¡ registrada en tools/list")
		}
		if n := runeCount(ti.Description); n == 0 || n > 200 {
			t.Errorf("descripciÃ³n de tags_index tiene %d caracteres (mÃ¡ximo 200)", n)
		}
		if ti.Annotations == nil || !ti.Annotations.ReadOnlyHint {
			t.Errorf("tags_index deberÃ­a tener ReadOnlyHint: true")
		}
	})
}

// listTools devuelve tools/list como helper para no duplicar ListTools en cada test.
func listTools(t *testing.T, cs *mcp.ClientSession) *mcp.ListToolsResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	out, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	return out
}

func runeCount(s string) int { return len([]rune(s)) }
