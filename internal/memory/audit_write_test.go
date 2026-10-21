package memory

import (
	"context"
	"testing"
	"time"

	"lodan/internal/database"
)

// TestAuditWrite_MemoryInvariants verifica que Service.Remember (usado por lodan hook
// y servicios internos) mantiene los invariantes de la base de datos sin aplicar
// las validaciones que corresponden exclusivamente a la capa MCP de remember:
// - S1 hook invariant: el hook guarda registros sin obligar a etiqueta concreta.
// - S6 sin regresiones: duplicados exactos y sustitución por key siguen funcionando.
func TestAuditWrite_MemoryInvariants(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)
	tc.Reset(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	e := newEnv(ctx, tc)

	t.Run("hook invariant: Remember guarda sin etiqueta concreta", func(t *testing.T) {
		saved, err := e.svc.Remember(ctx, e.tr, []Item{
			{
				Title:   "Registro sin etiqueta concreta",
				Content: "Contenido guardado directamente por memoria o hook",
				Kind:    KindFact,
				Tags:    []string{"agridemur"}, // solo etiqueta general
			},
		})
		if err != nil {
			t.Fatalf("Remember falló: %v", err)
		}
		if len(saved) != 1 {
			t.Fatalf("se esperaba 1 registro guardado, hay %d", len(saved))
		}
		if saved[0].ID == 0 {
			t.Errorf("se esperaba ID asignado > 0, hay %d", saved[0].ID)
		}
		if saved[0].Duplicate {
			t.Errorf("no debía ser duplicado")
		}
	})

	t.Run("S6 sin regresiones: duplicado exacto detectado por hash", func(t *testing.T) {
		item := Item{
			Title:   "Albarán de gasoil",
			Content: "Se apunta siempre: fecha, matrícula, empresa y litros.",
			Kind:    KindFact,
			Tags:    []string{"recurso-gasoil"},
		}
		saved1, err := e.svc.Remember(ctx, e.tr, []Item{item})
		if err != nil {
			t.Fatalf("Remember 1 falló: %v", err)
		}
		if saved1[0].Duplicate {
			t.Fatalf("primer guardado no debía ser duplicado")
		}

		saved2, err := e.svc.Remember(ctx, e.tr, []Item{item})
		if err != nil {
			t.Fatalf("Remember 2 falló: %v", err)
		}
		if !saved2[0].Duplicate {
			t.Errorf("segundo guardado debía ser duplicado")
		}
		if saved2[0].ID != saved1[0].ID {
			t.Errorf("ID duplicado = %d, quería %d", saved2[0].ID, saved1[0].ID)
		}
	})

	t.Run("S6 sin regresiones: sustitución por key", func(t *testing.T) {
		item1 := Item{
			Title:   "Dato inicial",
			Content: "Contenido 1 con clave",
			Kind:    KindFact,
			Key:     "proyecto-test/estado",
			Tags:    []string{"proyecto-test"},
		}
		saved1, err := e.svc.Remember(ctx, e.tr, []Item{item1})
		if err != nil {
			t.Fatalf("Remember 1 falló: %v", err)
		}

		item2 := Item{
			Title:   "Dato actualizado",
			Content: "Contenido 2 con la misma clave",
			Kind:    KindFact,
			Key:     "proyecto-test/estado",
			Tags:    []string{"proyecto-test"},
		}
		saved2, err := e.svc.Remember(ctx, e.tr, []Item{item2})
		if err != nil {
			t.Fatalf("Remember 2 falló: %v", err)
		}
		if len(saved2[0].Superseded) != 1 || saved2[0].Superseded[0] != saved1[0].ID {
			t.Errorf("Superseded = %v, quería [%d]", saved2[0].Superseded, saved1[0].ID)
		}
	})
}
