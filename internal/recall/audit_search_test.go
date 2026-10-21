package recall

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"lodan/internal/database"
	"lodan/internal/memory"
)

// seedAuditFixtures inserts F1, F2 and F3 from the frozen contract.
func seedAuditFixtures(t *testing.T, e *env) (f1, f2, f3 int64) {
	t.Helper()
	d := func(y, m, day int) *time.Time {
		t := time.Date(y, time.Month(m), day, 12, 0, 0, 0, time.UTC)
		return &t
	}
	f1 = e.save(t, memory.Item{
		Title:      "Albarán de gasoil",
		Content:    "Se apunta siempre: fecha, matrícula, empresa y litros.",
		Kind:       "fact",
		Tags:       []string{"recurso-gasoil"},
		OccurredAt: d(2026, 10, 1),
	})
	f2 = e.save(t, memory.Item{
		Title:      "Matrícula 3871LNY",
		Content:    "Matrícula 3871LNY: empresa VDM, concepto RECOL BK.",
		Kind:       "fact",
		Tags:       []string{"vehiculo-3871lny", "recurso-gasoil"},
		OccurredAt: d(2026, 10, 5),
	})
	f3 = e.save(t, memory.Item{
		Title:      "Lectura del gasoil",
		Content:    "Medición matinal del depósito de gasoil.",
		Kind:       "fact",
		Tags:       []string{"recurso-gasoil"},
		OccurredAt: d(2026, 10, 3),
	})
	return f1, f2, f3
}

// S1 Tildes: con F1, recall query="albaran", query="albarán" y query="ALBARAN" devuelven F1 en los tres casos.
func TestAudit_S1_Tildes(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)
	tc.Reset(t)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	e := newEnv(ctx, tc)
	f1, _, _ := seedAuditFixtures(t, e)
	svc := e.service(e.emb, Options{TopicDetectThreshold: highThreshold})

	for _, q := range []string{"albaran", "albarán", "ALBARAN"} {
		t.Run(q, func(t *testing.T) {
			res := e.recall(t, svc, Request{Query: q})
			if find(res.all(), f1) == -1 {
				t.Fatalf("query %q no devolvió F1 (#%d):\n%s", q, f1, res.Format(svc.MaxBytes()))
			}
		})
	}
}

// S2 Palabras de más: con F1–F3, recall query="albaran gasoil inexistentepalabra" devuelve F1
// y su bloque empieza por (sin coincidencia con todas las palabras: resultados con alguna de ellas).
// Si la búsqueda con todas las palabras encuentra algo, no hay aviso y el resultado es el de hoy.
func TestAudit_S2_PalabrasDeMas(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)
	tc.Reset(t)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	e := newEnv(ctx, tc)
	f1, _, _ := seedAuditFixtures(t, e)
	svc := e.service(e.emb, Options{TopicDetectThreshold: highThreshold})

	t.Run("con palabra de más", func(t *testing.T) {
		res := e.recall(t, svc, Request{Query: "albaran gasoil inexistentepalabra"})
		if find(res.all(), f1) == -1 {
			t.Fatalf("query con palabra de más no devolvió F1 (#%d):\n%s", f1, res.Format(svc.MaxBytes()))
		}
		formatted := res.Format(svc.MaxBytes())
		wantNotice := "(sin coincidencia con todas las palabras: resultados con alguna de ellas)"
		if !strings.Contains(formatted, wantNotice) {
			t.Fatalf("la salida no contiene el aviso esperado %q:\n%s", wantNotice, formatted)
		}
	})

	t.Run("con todas las palabras coincidentes sin aviso", func(t *testing.T) {
		res := e.recall(t, svc, Request{Query: "albaran gasoil"})
		if find(res.all(), f1) == -1 {
			t.Fatalf("query 'albaran gasoil' no devolvió F1 (#%d):\n%s", f1, res.Format(svc.MaxBytes()))
		}
		formatted := res.Format(svc.MaxBytes())
		wantNotice := "(sin coincidencia con todas las palabras: resultados con alguna de ellas)"
		if strings.Contains(formatted, wantNotice) {
			t.Fatalf("la salida no debería contener el aviso %q:\n%s", wantNotice, formatted)
		}
	})
}

// S3 Erratas: con F2, recall query="matricla" devuelve F2 con el aviso (sin coincidencia exacta: resultados parecidos).
// Solo se usa cuando S2 tampoco encuentra nada.
func TestAudit_S3_Erratas(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)
	tc.Reset(t)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	e := newEnv(ctx, tc)
	_, f2, _ := seedAuditFixtures(t, e)
	svc := e.service(e.emb, Options{TopicDetectThreshold: highThreshold})

	res := e.recall(t, svc, Request{Query: "matricla"})
	if find(res.all(), f2) == -1 {
		t.Fatalf("query 'matricla' no devolvió F2 (#%d):\n%s", f2, res.Format(svc.MaxBytes()))
	}
	formatted := res.Format(svc.MaxBytes())
	wantNotice := "(sin coincidencia exacta: resultados parecidos)"
	if !strings.Contains(formatted, wantNotice) {
		t.Fatalf("la salida no contiene el aviso esperado %q:\n%s", wantNotice, formatted)
	}
}

// S4 Filtros antes del límite: con 2100 registros vigentes, de los que 5 (los más antiguos)
// llevan proyecto-filtro, recall query="" tags_all=["proyecto-filtro"] devuelve los 5; con date_from/date_to igual.
func TestAudit_S4_FiltrosAntesDelLimite(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)
	tc.Reset(t)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	e := newEnv(ctx, tc)

	// Insertamos 2100 registros: los primeros 5 llevan proyecto-filtro y son los más antiguos (año 2020).
	// Los otros 2095 son de 2026 sin esa tag.
	const total = 2100
	const targetCount = 5

	targetIDs := make([]int64, 0, targetCount)
	for i := 0; i < targetCount; i++ {
		tOld := time.Date(2020, 1, i+1, 10, 0, 0, 0, time.UTC)
		id := e.save(t, memory.Item{
			Title:      fmt.Sprintf("Registro antiguo con filtro %d", i+1),
			Content:    fmt.Sprintf("Contenido de prueba para el registro antiguo con filtro %d", i+1),
			Kind:       "fact",
			Tags:       []string{"proyecto-filtro"},
			OccurredAt: &tOld,
		})
		targetIDs = append(targetIDs, id)
	}

	// Insertamos los 2095 restantes en lotes para mayor velocidad.
	batch := make([]memory.Item, 0, 100)
	for i := targetCount; i < total; i++ {
		tRecent := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
		batch = append(batch, memory.Item{
			Title:      fmt.Sprintf("Registro de relleno %d", i+1),
			Content:    fmt.Sprintf("Contenido de relleno para saturar los 2000 candidatos %d", i+1),
			Kind:       "fact",
			Tags:       []string{"recurso-relleno"},
			OccurredAt: &tRecent,
		})
		if len(batch) == 100 || i == total-1 {
			if _, err := e.mem.Remember(e.ctx, e.tr, batch); err != nil {
				t.Fatalf("error insertando lote de relleno: %v", err)
			}
			batch = batch[:0]
		}
	}

	svc := e.service(e.emb, Options{DefaultLimit: 20, MaxLimit: 50, TopicDetectThreshold: highThreshold})

	t.Run("tags_all con query vacía", func(t *testing.T) {
		res := e.recall(t, svc, Request{
			Query:   "",
			TagsAll: []string{"proyecto-filtro"},
			Limit:   20,
		})
		for _, targetID := range targetIDs {
			if find(res.all(), targetID) == -1 {
				t.Errorf("no se encontró el registro antiguo #%d entre los devueltos", targetID)
			}
		}
	})

	t.Run("date_to con query vacía", func(t *testing.T) {
		res := e.recall(t, svc, Request{
			Query:  "",
			DateTo: "2020-12-31",
			Limit:  20,
		})
		for _, targetID := range targetIDs {
			if find(res.all(), targetID) == -1 {
				t.Errorf("date_to no devolvió el registro antiguo #%d", targetID)
			}
		}
	})
}

// S6 Menos ruido: Con un tema detectado (no pedido en topics), «Datos estables» y «Últimos eventos»
// solo muestran los registros del tema que coinciden con la consulta; si ninguno coincide, esas secciones
// no aparecen. Con el tema pedido en topics, la ficha se muestra como hoy.
func TestAudit_S6_MenosRuido(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)
	tc.Reset(t)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	e := newEnv(ctx, tc)
	d := func(y, m, day int) *time.Time {
		t := time.Date(y, time.Month(m), day, 12, 0, 0, 0, time.UTC)
		return &t
	}

	// Creamos tema "contabilidad" con datos estables y eventos.
	e.save(t, memory.Item{
		Title:      "Facturas de proveedores",
		Content:    "Registro de facturas recibidas de proveedores.",
		Kind:       "fact",
		Tags:       []string{"proyecto-contabilidad"},
		OccurredAt: d(2026, 9, 1),
	})
	e.save(t, memory.Item{
		Title:      "Cierre contable trimestral",
		Content:    "Procedimiento del cierre contable de fin de trimestre.",
		Kind:       "fact",
		Tags:       []string{"proyecto-contabilidad"},
		OccurredAt: d(2026, 9, 15),
	})
	e.save(t, memory.Item{
		Title:      "Reunión de balance",
		Content:    "Reunión sobre el balance general del ejercicio.",
		Kind:       "event",
		Tags:       []string{"proyecto-contabilidad"},
		OccurredAt: d(2026, 9, 20),
	})

	svc := e.service(e.emb, Options{TopicDetectThreshold: lowThreshold})

	t.Run("tema detectado sin coincidencia en ficha no muestra ficha", func(t *testing.T) {
		// La consulta detecta "proyecto-contabilidad" pero pregunta por "nomina", que no está en la ficha.
		res := e.recall(t, svc, Request{Query: "contabilidad nomina empleado"})
		if res.Topic != nil && res.TopicDetected {
			if len(res.Profile) > 0 || len(res.Events) > 0 {
				t.Fatalf("con tema detectado pero sin coincidencia en ficha, Profile y Events deberían estar vacíos, pero se obtuvieron: profile=%d, events=%d", len(res.Profile), len(res.Events))
			}
			formatted := res.Format(svc.MaxBytes())
			if strings.Contains(formatted, "Datos estables:") || strings.Contains(formatted, "Últimos eventos:") {
				t.Fatalf("no deberían mostrarse las secciones de ficha si no coinciden con la consulta:\n%s", formatted)
			}
		}
	})

	t.Run("tema pedido en topics muestra la ficha completa", func(t *testing.T) {
		res := e.recall(t, svc, Request{
			Query:  "contabilidad nomina",
			Topics: []string{"proyecto-contabilidad"},
		})
		if res.Topic == nil || res.Topic.Slug != "proyecto-contabilidad" {
			t.Fatalf("se esperaba tema pedido 'proyecto-contabilidad', se obtuvo: %+v", res.Topic)
		}
		if len(res.Profile) == 0 {
			t.Fatalf("con tema pedido, Profile debería mostrar los datos estables")
		}
		formatted := res.Format(svc.MaxBytes())
		if !strings.Contains(formatted, "Datos estables:") {
			t.Fatalf("con tema pedido, debe mostrar 'Datos estables:':\n%s", formatted)
		}
	})
}

// S7 Sin regresiones: tags_all, tags_any, fechas, include_history y «Conectado por etiquetas»
// se comportan como en la base.
func TestAudit_S7_SinRegresiones(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)
	tc.Reset(t)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	e := newEnv(ctx, tc)
	d := func(y, m, day int) *time.Time {
		t := time.Date(y, time.Month(m), day, 12, 0, 0, 0, time.UTC)
		return &t
	}

	id1 := e.save(t, memory.Item{
		Title:      "Albarán activo",
		Content:    "Detalle de albarán activo de prueba.",
		Kind:       "fact",
		Tags:       []string{"proyecto-test", "finca-uno"},
		OccurredAt: d(2026, 8, 10),
	})
	id2 := e.save(t, memory.Item{
		Title:      "Albarán sustituido",
		Content:    "Detalle de albarán que luego fue sustituido.",
		Kind:       "fact",
		Tags:       []string{"proyecto-test", "finca-dos"},
		OccurredAt: d(2026, 8, 12),
	})
	e.invalidate(t, id2)

	svc := e.service(e.emb, Options{TopicDetectThreshold: highThreshold})

	t.Run("include_history false omite invalidados", func(t *testing.T) {
		res := e.recall(t, svc, Request{Query: "albarán", IncludeHistory: false})
		if find(res.all(), id1) == -1 {
			t.Errorf("debería encontrar el activo #%d", id1)
		}
		if find(res.all(), id2) != -1 {
			t.Errorf("NO debería encontrar el invalidado #%d", id2)
		}
	})

	t.Run("include_history true incluye invalidados", func(t *testing.T) {
		res := e.recall(t, svc, Request{Query: "albarán", IncludeHistory: true})
		if find(res.all(), id1) == -1 {
			t.Errorf("debería encontrar el activo #%d", id1)
		}
		if find(res.all(), id2) == -1 {
			t.Errorf("debería encontrar el invalidado #%d", id2)
		}
	})

	t.Run("tags_all filtra correctamente", func(t *testing.T) {
		res := e.recall(t, svc, Request{Query: "albarán", TagsAll: []string{"proyecto-test", "finca-uno"}})
		if find(res.all(), id1) == -1 {
			t.Errorf("debería encontrar #%d", id1)
		}
	})
}
