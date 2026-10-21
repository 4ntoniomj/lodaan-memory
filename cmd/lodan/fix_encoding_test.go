package main

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"lodan/internal/database"
)

// TestFixEncoding_S4 verifica el comando `lodan db fix-encoding` y `--apply` (criterio S4).
func TestFixEncoding_S4(t *testing.T) {
	tc := database.NewTestCluster(t, 64)
	tc.Reset(t)

	// Conectamos runDB al cluster de prueba a través de variables de entorno
	t.Setenv("LODAN_DATA_DIR", tc.Cfg.DataDir)
	t.Setenv("LODAN_PG_PORT", strconv.Itoa(tc.Cfg.PGPort))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Insertamos fixtures F1, F2, F3 y F4 directamente en la BD
	// F1: Albarán de gasoil
	// F2: Matrícula 3871LNY
	// F3: Lectura del gasoil
	// F4: AlbarÃ¡n antiguo (mojibake)
	var f4ID int64
	err := tc.Pool.QueryRow(ctx, `
		INSERT INTO memories (kind, status, title, content, content_hash, occurred_at)
		VALUES ('fact', 'active', 'AlbarÃ¡n antiguo', 'VersiÃ³n definitiva del albarÃ¡n.', decode(md5('albarán antiguo versión definitiva del albarán.'), 'hex'), '2026-09-30T00:00:00Z')
		RETURNING id
	`).Scan(&f4ID)
	if err != nil {
		t.Fatalf("insert F4: %v", err)
	}

	// Insertamos F1 sin mojibake
	_, err = tc.Pool.Exec(ctx, `
		INSERT INTO memories (kind, status, title, content, content_hash, occurred_at)
		VALUES ('fact', 'active', 'Albarán de gasoil', 'Se apunta siempre: fecha, matrícula, empresa y litros.', decode(md5('f1'), 'hex'), '2026-10-01T00:00:00Z')
	`)
	if err != nil {
		t.Fatalf("insert F1: %v", err)
	}

	// 1. Simulación: lodan db fix-encoding
	var stdoutSim, stderrSim bytes.Buffer
	codeSim := runDB([]string{"fix-encoding"}, &stdoutSim, &stderrSim)
	if codeSim != 0 {
		t.Fatalf("runDB fix-encoding simulación devolvió código %d, stderr: %s", codeSim, stderrSim.String())
	}
	outSim := stdoutSim.String()
	expectedF4Line := fmt.Sprintf("#%d AlbarÃ¡n antiguo", f4ID)
	if !strings.Contains(outSim, expectedF4Line) {
		t.Errorf("simulación no listó F4 esperado (%s):\n%s", expectedF4Line, outSim)
	}
	expectedSimEnd := "1 registros con texto mal codificado (simulación: no se ha cambiado nada; usa --apply)"
	if !strings.Contains(outSim, expectedSimEnd) {
		t.Errorf("simulación no terminó con la línea esperada:\n%s", outSim)
	}

	// Verificamos que la BD no cambió tras la simulación
	var simTitle, simContent string
	var simUpdatedAt time.Time
	err = tc.Pool.QueryRow(ctx, "SELECT title, content, updated_at FROM memories WHERE id = $1", f4ID).Scan(&simTitle, &simContent, &simUpdatedAt)
	if err != nil {
		t.Fatalf("consulta post-simulación: %v", err)
	}
	if simTitle != "AlbarÃ¡n antiguo" || simContent != "VersiÃ³n definitiva del albarÃ¡n." {
		t.Errorf("la simulación modificó el registro F4: title=%q content=%q", simTitle, simContent)
	}

	// 2. Aplicar: lodan db fix-encoding --apply
	var stdoutApply, stderrApply bytes.Buffer
	codeApply := runDB([]string{"fix-encoding", "--apply"}, &stdoutApply, &stderrApply)
	if codeApply != 0 {
		t.Fatalf("runDB fix-encoding --apply devolvió código %d, stderr: %s", codeApply, stderrApply.String())
	}
	outApply := stdoutApply.String()
	if !strings.Contains(outApply, "1 registros reparados") {
		t.Errorf("salida de --apply no contiene '1 registros reparados':\n%s", outApply)
	}

	// Verificamos que la BD se actualizó con título y contenido reparados
	var appTitle, appContent string
	var appUpdatedAt time.Time
	err = tc.Pool.QueryRow(ctx, "SELECT title, content, updated_at FROM memories WHERE id = $1", f4ID).Scan(&appTitle, &appContent, &appUpdatedAt)
	if err != nil {
		t.Fatalf("consulta post-apply: %v", err)
	}
	if appTitle != "Albarán antiguo" {
		t.Errorf("título tras apply = %q, quería %q", appTitle, "Albarán antiguo")
	}
	if appContent != "Versión definitiva del albarán." {
		t.Errorf("contenido tras apply = %q, quería %q", appContent, "Versión definitiva del albarán.")
	}
	if !appUpdatedAt.After(simUpdatedAt) && !appUpdatedAt.Equal(simUpdatedAt.Add(time.Nanosecond)) {
		t.Errorf("updated_at no se actualizó (antes %v, después %v)", simUpdatedAt, appUpdatedAt)
	}

	// 3. Segunda ejecución: 0 registros con texto mal codificado
	var stdout2, stderr2 bytes.Buffer
	code2 := runDB([]string{"fix-encoding"}, &stdout2, &stderr2)
	if code2 != 0 {
		t.Fatalf("segunda ejecución falló con código %d", code2)
	}
	if !strings.Contains(stdout2.String(), "0 registros con texto mal codificado") {
		t.Errorf("segunda ejecución debía decir 0 registros con texto mal codificado:\n%s", stdout2.String())
	}
}
