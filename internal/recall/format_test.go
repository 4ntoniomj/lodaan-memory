package recall

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"lodan/internal/topic"
)

// localDay returns noon of a day in local time, so Format prints that same day.
func localDay(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 12, 0, 0, 0, time.Local)
}

func TestFormat(t *testing.T) {
	res := Result{
		Query:         "entreno",
		Topic:         &topic.Topic{ID: 1, Slug: "entrenamiento"},
		TopicDetected: true,
		Profile: []Item{
			{ID: 12, Kind: "preference", Status: "active", Title: "Horario", Snippet: "Por la tarde", OccurredAt: localDay(2026, 9, 12)},
		},
		Events: []Item{
			{ID: 40, Kind: "event", Status: "active", Title: "Sesión de piernas", Snippet: "Sentadillas y prensa", OccurredAt: localDay(2026, 9, 28)},
		},
		Results: []Item{
			{ID: 77, Kind: "decision", Status: "active", Title: "Cambiar de rutina", Snippet: "Tres días", OccurredAt: localDay(2026, 8, 1)},
			{ID: 5, Kind: "note", Status: "superseded", SupersededBy: 77, Title: "Rutina antigua", Snippet: "Cinco días", OccurredAt: localDay(2025, 1, 2)},
			{ID: 6, Kind: "fact", Status: "invalidated", Title: "Peso erróneo", OccurredAt: localDay(2025, 3, 4)},
		},
	}
	want := strings.Join([]string{
		"Tema: entrenamiento (detectado)",
		"Datos estables:",
		"#12 [preferencia 2026-09-12] Horario — Por la tarde",
		"Últimos eventos:",
		"#40 [evento 2026-09-28] Sesión de piernas — Sentadillas y prensa",
		"Resultados:",
		"#77 [decisión 2026-08-01] Cambiar de rutina — Tres días",
		"#5 [nota 2025-01-02, sustituido por #77] Rutina antigua — Cinco días",
		"#6 [dato 2025-03-04, invalidado] Peso erróneo",
		"Usa get con los ids para ver el detalle.",
	}, "\n")
	if got := res.Format(6000); got != want {
		t.Errorf("Format =\n%s\nse esperaba\n%s", got, want)
	}
}

func TestFormatTopicRequestedAndSections(t *testing.T) {
	res := Result{
		Query:   "algo",
		Topic:   &topic.Topic{ID: 1, Slug: "cafe"},
		Results: []Item{{ID: 1, Kind: "note", Status: "active", Title: "Título", Snippet: "texto"}},
	}
	got := res.Format(6000)
	if !strings.HasPrefix(got, "Tema: cafe\n") {
		t.Errorf("un tema pedido no lleva «(detectado)»: %q", got)
	}
	for _, s := range []string{"Datos estables:", "Últimos eventos:"} {
		if strings.Contains(got, s) {
			t.Errorf("la sección vacía %q debería omitirse: %q", s, got)
		}
	}
	if strings.Contains(got, "2026") || !strings.Contains(got, "#1 [nota] Título — texto") {
		t.Errorf("sin fecha no debe haber fecha en la etiqueta: %q", got)
	}

	res.Topic = nil
	if got := res.Format(6000); strings.Contains(got, "Tema:") {
		t.Errorf("sin tema no debe haber línea de tema: %q", got)
	}
}

func TestFormatEmptyAndTextOnly(t *testing.T) {
	res := Result{Query: "  nada\nde nada "}
	if got, want := res.Format(6000), "Sin resultados para «nada de nada»."; got != want {
		t.Errorf("Format = %q, se esperaba %q", got, want)
	}

	res.TextOnly = true
	got := res.Format(6000)
	if !strings.HasPrefix(got, textOnlyLine+"\n") || !strings.Contains(got, "Sin resultados") {
		t.Errorf("TextOnly vacío: %q", got)
	}

	res.Results = []Item{{ID: 3, Kind: "note", Status: "active", Title: "T"}}
	if got := res.Format(6000); !strings.HasPrefix(got, textOnlyLine+"\n") {
		t.Errorf("la primera línea con TextOnly debe ser el aviso: %q", got)
	}
}

func TestFormatByteCap(t *testing.T) {
	items := make([]Item, 30)
	for i := range items {
		items[i] = Item{
			ID: int64(100 + i), Kind: "note", Status: "active",
			Title: fmt.Sprintf("Registro %d", i), Snippet: strings.Repeat("palabra ", 10),
			OccurredAt: localDay(2026, 1, 1+i%28),
		}
	}
	res := Result{Query: "q", Results: items}

	full := res.Format(0) // sin tope
	n := len(full)
	if strings.Contains(full, "recortada") {
		t.Fatalf("sin tope no debe recortarse")
	}
	if got := res.Format(n); got != full {
		t.Errorf("con un tope justo (%d) no debe recortarse", n)
	}

	for _, tope := range []int{n - 1, 600, 300} {
		got := res.Format(tope)
		if len(got) > tope {
			t.Errorf("Format(%d) mide %d bytes", tope, len(got))
		}
		lines := strings.Split(got, "\n")
		wantNotice := fmt.Sprintf("… (respuesta recortada al tope de %d bytes)", tope)
		if len(lines) < 3 || lines[len(lines)-2] != wantNotice || lines[len(lines)-1] != getHint {
			t.Errorf("Format(%d) debe acabar con el aviso y la pista: %q", tope, got)
		}
		if !strings.HasPrefix(got, "Resultados:\n#100 ") {
			t.Errorf("Format(%d) debería conservar el principio: %q", tope, got)
		}
	}

	// Con un tope absurdo se escriben igualmente el aviso y la pista.
	if got := res.Format(5); !strings.Contains(got, "recortada") || !strings.HasSuffix(got, getHint) {
		t.Errorf("Format(5) = %q", got)
	}
}
