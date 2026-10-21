package mcptools

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/database"
)

var idRe = regexp.MustCompile(`#(\d+)`)

// TestWriteAudit_E2E verifica los criterios S1, S2, S3, S5 y S6 a través de la interfaz MCP.
func TestWriteAudit_E2E(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)

	run := func(name string, f func(t *testing.T, d Deps, cs *mcp.ClientSession)) {
		t.Run(name, func(t *testing.T) {
			tc.Reset(t)
			d, srv := newStack(tc)
			f(t, d, connect(t, srv, "test-client"))
		})
	}

	// ---------------------------------------------------------------- S1
	run("S1 etiqueta concreta obligatoria en remember", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		// 1. Item individual sin etiqueta concreta
		out, isErr := call(t, cs, "remember", map[string]any{
			"items": []any{
				map[string]any{
					"title":   "Falta etiqueta concreta",
					"content": "Solo lleva etiqueta general",
					"kind":    "fact",
					"tags":    []any{"agridemur"},
				},
			},
		})
		if isErr {
			t.Fatalf("remember devolvió error de transporte MCP inesperado: %s", out)
		}
		expectedErrLine := "item 1 no guardado: falta una etiqueta concreta (proyecto-, persona-, contacto-, proveedor-, cliente-, finca-, vehiculo-, coche-, skill-, herramienta-, recurso-, evento-)"
		if !strings.Contains(out, expectedErrLine) {
			t.Errorf("salida no contiene línea esperada de rechazo\nEsperado: %s\nObtenido:\n%s", expectedErrLine, out)
		}

		// 2. Lote mixto: el ítem sin etiqueta concreta se rechaza, el que la tiene se guarda
		out2, _ := call(t, cs, "remember", map[string]any{
			"items": []any{
				map[string]any{
					"title":   "Item 1 invalido",
					"content": "Contenido sin etiqueta concreta",
					"kind":    "fact",
					"tags":    []any{"agridemur"},
				},
				map[string]any{
					"title":   "Item 2 valido",
					"content": "Contenido con etiqueta concreta",
					"kind":    "fact",
					"tags":    []any{"proyecto-auditoria", "agridemur"},
				},
			},
		})
		if !strings.Contains(out2, expectedErrLine) {
			t.Errorf("lote mixto no informó de ítem 1 no guardado: %s", out2)
		}
		if !strings.Contains(out2, "guardado") {
			t.Errorf("lote mixto no guardó ítem 2 válido: %s", out2)
		}

		// 3. Vista previa (preview: true) informa de lo mismo
		prevOut, _ := call(t, cs, "remember", map[string]any{
			"preview": true,
			"items": []any{
				map[string]any{
					"title":   "Item previo",
					"content": "Sin etiqueta concreta en preview",
					"kind":    "fact",
					"tags":    []any{"agridemur"},
				},
			},
		})
		if !strings.Contains(prevOut, "falta una etiqueta concreta (proyecto-, persona-, contacto-, proveedor-, cliente-, finca-, vehiculo-, coche-, skill-, herramienta-, recurso-, evento-)") {
			t.Errorf("preview no informó de falta de etiqueta concreta:\n%s", prevOut)
		}
	})

	// ---------------------------------------------------------------- S2
	run("S2 casi duplicados con pg_trgm", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		// Guardamos F2: Matrícula 3871LNY: empresa VDM, concepto RECOL BK.
		outF2, _ := call(t, cs, "remember", map[string]any{
			"items": []any{
				map[string]any{
					"title":       "Matrícula 3871LNY",
					"content":     "Matrícula 3871LNY: empresa VDM, concepto RECOL BK.",
					"kind":        "fact",
					"tags":        []any{"vehiculo-3871lny", "recurso-gasoil"},
					"occurred_at": "2026-10-05",
				},
			},
		})
		m := idRe.FindStringSubmatch(outF2)
		if len(m) < 2 {
			t.Fatalf("no se pudo extraer ID de F2 de: %s", outF2)
		}
		f2ID := m[1]

		// Intento 1: contenido con parecido >= 0.90 sin force -> rechazado
		nearContent := "Matrícula 3871LNY: empresa VDM y concepto RECOL BK."
		outNear, _ := call(t, cs, "remember", map[string]any{
			"items": []any{
				map[string]any{
					"title":   "Matrícula 3871LNY casi igual",
					"content": nearContent,
					"kind":    "fact",
					"tags":    []any{"recurso-gasoil"},
				},
			},
		})
		expectedNearPrefix := fmt.Sprintf("item 1 no guardado: casi idéntico a #%s (parecido ", f2ID)
		if !strings.Contains(outNear, expectedNearPrefix) {
			t.Errorf("no rechazó casi duplicado >=0.90\nEsperado conteniendo: %s\nObtenido:\n%s", expectedNearPrefix, outNear)
		}
		if !strings.Contains(outNear, "; si es un cambio, usa revise update o key, o force: true") {
			t.Errorf("mensaje de casi duplicado no contiene sugerencia esperada:\n%s", outNear)
		}

		// Intento 2: con force: true -> se guarda
		outForce, _ := call(t, cs, "remember", map[string]any{
			"items": []any{
				map[string]any{
					"title":   "Matrícula 3871LNY forzada",
					"content": nearContent,
					"kind":    "fact",
					"tags":    []any{"recurso-gasoil"},
					"force":   true,
				},
			},
		})
		if !strings.Contains(outForce, "guardado") {
			t.Errorf("force: true debía guardar el casi duplicado:\n%s", outForce)
		}

		// Intento 3: sustitución por key no se bloquea por casi duplicado
		outKey1, _ := call(t, cs, "remember", map[string]any{
			"items": []any{
				map[string]any{
					"title":   "Ficha vehículo clave",
					"content": "Vehículo registrado para transporte de cosecha.",
					"kind":    "fact",
					"key":     "vehiculo-3871lny/ficha",
					"tags":    []any{"vehiculo-3871lny"},
				},
			},
		})
		mKey := idRe.FindStringSubmatch(outKey1)
		if len(mKey) < 2 {
			t.Fatalf("no se pudo extraer ID de clave: %s", outKey1)
		}
		keyID1 := mKey[1]

		outKey2, _ := call(t, cs, "remember", map[string]any{
			"items": []any{
				map[string]any{
					"title":   "Ficha vehículo clave modificada",
					"content": "Vehículo registrado para el transporte de cosecha.", // casi idéntico
					"kind":    "fact",
					"key":     "vehiculo-3871lny/ficha",
					"tags":    []any{"vehiculo-3871lny"},
				},
			},
		})
		if !strings.Contains(outKey2, "sustituye #"+keyID1) {
			t.Errorf("sustitución por key debía proceder sin bloqueo por casi duplicado:\n%s", outKey2)
		}

		// Intento 4: parecido entre 0.75 y 0.90 con etiqueta en común -> se guarda con advertencia ¿duplicado?
		// "Matrícula 3871LNY: empresa VDM, concepto RECOL BK."
		// Texto con similitud ~0.80:
		warnContent := "Matrícula 3871LNY: empresa VDM, concepto RECOL BK, semana 42." // similarity 0.825 (integration fix: the previous text measured 0.712)
		outWarn, _ := call(t, cs, "remember", map[string]any{
			"items": []any{
				map[string]any{
					"title":   "Matrícula parecida",
					"content": warnContent,
					"kind":    "fact",
					"tags":    []any{"recurso-gasoil"},
				},
			},
		})
		if !strings.Contains(outWarn, "guardado") {
			t.Fatalf("debía guardarse con advertencia de duplicado:\n%s", outWarn)
		}
		expectedWarnPrefix := fmt.Sprintf("  ¿duplicado?: #%s (parecido ", f2ID)
		if !strings.Contains(outWarn, expectedWarnPrefix) {
			t.Errorf("no mostró línea de advertencia ¿duplicado? esperada (%s):\n%s", expectedWarnPrefix, outWarn)
		}
	})

	// ---------------------------------------------------------------- S3
	run("S3 reparación de mojibake al guardar", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		out, _ := call(t, cs, "remember", map[string]any{
			"items": []any{
				map[string]any{
					"title":   "AlbarÃ¡n",
					"content": "VersiÃ³n definitiva",
					"kind":    "fact",
					"tags":    []any{"recurso-gasoil"},
				},
			},
		})
		if !strings.Contains(out, "texto reparado (codificación)") {
			t.Errorf("salida de guardado con mojibake no contiene 'texto reparado (codificación)':\n%s", out)
		}

		m := idRe.FindStringSubmatch(out)
		if len(m) < 2 {
			t.Fatalf("no se pudo extraer ID de registro guardado: %s", out)
		}
		savedID := m[1]

		// Verificamos persistencia en BD
		var title, content string
		err := d.Pool.QueryRow(context.Background(), "SELECT title, content FROM memories WHERE id = $1", savedID).Scan(&title, &content)
		if err != nil {
			t.Fatalf("consulta en memories: %v", err)
		}
		if title != "Albarán" {
			t.Errorf("título en BD = %q, quería %q", title, "Albarán")
		}
		if content != "Versión definitiva" {
			t.Errorf("contenido en BD = %q, quería %q", content, "Versión definitiva")
		}

		// Texto normal sin mojibake no debe mostrar la nota de reparación
		outNormal, _ := call(t, cs, "remember", map[string]any{
			"items": []any{
				map[string]any{
					"title":   "Texto correcto",
					"content": "Versión sin fallos de codificación",
					"kind":    "fact",
					"tags":    []any{"recurso-gasoil"},
				},
			},
		})
		if strings.Contains(outNormal, "texto reparado (codificación)") {
			t.Errorf("texto normal no debía incluir aviso de reparación:\n%s", outNormal)
		}
	})

	// ---------------------------------------------------------------- S5
	run("S5 mensaje de embeddings pendiente según WithEmbeddings", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		// 1. Por defecto en el stack lite, WithEmbeddings es false
		d.WithEmbeddings = false
		srvLite := NewServer(d)
		csLite := connect(t, srvLite, "lite-client")

		outLite, _ := call(t, csLite, "remember", map[string]any{
			"items": []any{
				map[string]any{
					"title":   "Prueba sin embeddings",
					"content": "Contenido para verificar mensaje lite",
					"kind":    "fact",
					"tags":    []any{"recurso-gasoil"},
				},
			},
		})
		if strings.Contains(outLite, "embedding y parecidos pendientes") {
			t.Errorf("con WithEmbeddings=false no debe aparecer mensaje de embeddings pendientes:\n%s", outLite)
		}

		// 2. Con WithEmbeddings = true
		d.WithEmbeddings = true
		srvFull := NewServer(d)
		csFull := connect(t, srvFull, "full-client")

		outFull, _ := call(t, csFull, "remember", map[string]any{
			"items": []any{
				map[string]any{
					"title":   "Prueba con embeddings",
					"content": "Contenido para verificar mensaje con embeddings",
					"kind":    "fact",
					"tags":    []any{"recurso-gasoil"},
				},
			},
		})
		if !strings.Contains(outFull, "embedding y parecidos pendientes (se calculan en segundo plano)") {
			t.Errorf("con WithEmbeddings=true debe aparecer mensaje de embeddings pendientes:\n%s", outFull)
		}
	})

	// ---------------------------------------------------------------- S6
	run("S6 sin regresiones en mcptools", func(t *testing.T, d Deps, cs *mcp.ClientSession) {
		item := map[string]any{
			"title":   "Albarán de gasoil",
			"content": "Se apunta siempre: fecha, matrícula, empresa y litros.",
			"kind":    "fact",
			"tags":    []any{"recurso-gasoil"},
		}
		out1, _ := call(t, cs, "remember", map[string]any{"items": []any{item}})
		if !strings.Contains(out1, "guardado") {
			t.Fatalf("primer guardado falló: %s", out1)
		}
		m := idRe.FindStringSubmatch(out1)
		id := m[1]

		out2, _ := call(t, cs, "remember", map[string]any{"items": []any{item}})
		expectedDup := fmt.Sprintf("#%s ya existía (duplicado): «Albarán de gasoil»", id)
		if !strings.Contains(out2, expectedDup) {
			t.Errorf("no reportó duplicado exacto:\nEsperado: %s\nObtenido:\n%s", expectedDup, out2)
		}
	})
}
