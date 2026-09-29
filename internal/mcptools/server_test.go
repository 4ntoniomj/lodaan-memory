package mcptools

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"lodan/internal/memory"
	"lodan/internal/session"
	"lodan/internal/topic"
)

func TestInstructionsLength(t *testing.T) {
	if n := utf8.RuneCountInString(Instructions); n > 1500 {
		t.Errorf("Instructions tiene %d caracteres y el máximo es 1500", n)
	}
}

func TestTrackerPrune(t *testing.T) {
	m := newTrackerMap(session.NewManager(nil, time.Minute))
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }

	// Sin conexión MCP real no hay *ServerSession: la clave nil basta para el mapa.
	tr1 := m.forSession(nil, func() string { return "a" })
	if tr2 := m.forSession(nil, func() string { return "b" }); tr1 != tr2 {
		t.Fatal("la misma sesión MCP debe devolver el mismo tracker")
	}
	if got := m.size(); got != 1 {
		t.Fatalf("size = %d, quería 1", got)
	}

	now = now.Add(29 * time.Minute)
	if n := m.prune(30 * time.Minute); n != 0 {
		t.Errorf("prune eliminó %d trackers antes de tiempo", n)
	}
	now = now.Add(2 * time.Minute)
	if n := m.prune(30 * time.Minute); n != 1 || m.size() != 0 {
		t.Errorf("prune eliminó %d trackers y quedan %d, quería 1 y 0", n, m.size())
	}
}

func TestValidateHTTPAddr(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:7438", "localhost:7438"} {
		if err := ValidateHTTPAddr(ok); err != nil {
			t.Errorf("ValidateHTTPAddr(%q) = %v, quería nil", ok, err)
		}
	}
	for _, bad := range []string{"0.0.0.0:7438", ":7438", "example.com:7438", "192.168.1.5:7438", "7438"} {
		if err := ValidateHTTPAddr(bad); err == nil {
			t.Errorf("ValidateHTTPAddr(%q) debería fallar", bad)
		}
	}
}

func TestParseWhen(t *testing.T) {
	if got, err := parseWhen(""); err != nil || got != nil {
		t.Errorf("vacío: %v, %v", got, err)
	}
	got, err := parseWhen("2026-09-28")
	if err != nil || got == nil || got.Location() != time.Local || got.Hour() != 0 || got.Day() != 28 {
		t.Errorf("AAAA-MM-DD: %v, %v", got, err)
	}
	got, err = parseWhen("2026-09-28T10:30:00+02:00")
	if err != nil || got == nil || !got.Equal(time.Date(2026, 9, 28, 8, 30, 0, 0, time.UTC)) {
		t.Errorf("RFC3339: %v, %v", got, err)
	}
	if _, err := parseWhen("ayer"); err == nil {
		t.Error("una fecha inválida debería fallar")
	}
}

func TestFormatSaved(t *testing.T) {
	got := formatSaved([]memory.Saved{
		{
			ID:    123,
			Title: "T",
			Topics: []topic.Resolution{{
				Topic: topic.Topic{ID: 1, Slug: "entrenamiento"}, Requested: "gym", Equivalent: true,
			}},
			Pending:    true,
			Superseded: []int64{45},
			Similar:    []memory.Neighbor{{ID: 12, Title: "otro", Similarity: 0.93}},
		},
		{ID: 45, Title: "Igual", Duplicate: true},
	})
	want := "#123 guardado · temas: entrenamiento (gym→entrenamiento) · embedding pendiente · sustituye #45 · parecidos: #12 «otro» (0.93)\n" +
		"#45 ya existía (duplicado): «Igual»"
	if got != want {
		t.Errorf("formatSaved =\n%s\nquería\n%s", got, want)
	}
}

func TestFormatFull(t *testing.T) {
	got := formatFull(memory.Full{
		ID:         123,
		Kind:       memory.KindDecision,
		Status:     memory.StatusActive,
		Title:      "Título",
		Content:    "contenido",
		Key:        "x",
		Topics:     []string{"a", "b"},
		OccurredAt: time.Date(2026, 9, 28, 12, 0, 0, 0, time.Local),
		Relations: []memory.Relation{
			{Kind: "supersedes", State: "confirmed", OtherID: 45, OtherTitle: "t", Outgoing: true},
			{Kind: "related", State: "suggested", OtherID: 7, OtherTitle: "u", Outgoing: true},
			{Kind: "supersedes", State: "confirmed", OtherID: 9, OtherTitle: "v", Outgoing: false},
		},
	})
	want := strings.Join([]string{
		"#123 [decisión · vigente · 2026-09-28] Título",
		"temas: a, b · clave: x",
		"contenido",
		"relaciones: sustituye a #45 «t» (confirmada); relacionado con #7 «u» (sugerida); sustituido por #9 «v» (confirmada)",
	}, "\n")
	if got != want {
		t.Errorf("formatFull =\n%s\nquería\n%s", got, want)
	}

	// Sin temas, clave ni relaciones no se emiten líneas vacías.
	got = formatFull(memory.Full{
		ID: 1, Kind: memory.KindNote, Status: memory.StatusInvalidated, Title: "N", Content: "c",
		OccurredAt: time.Date(2026, 1, 2, 12, 0, 0, 0, time.Local),
	})
	if want := "#1 [nota · invalidado · 2026-01-02] N\nc"; got != want {
		t.Errorf("formatFull mínimo =\n%s\nquería\n%s", got, want)
	}
}

func TestFormatGetMissing(t *testing.T) {
	got := formatGet([]int64{1, 9, 9}, []memory.Full{{ID: 1, Kind: memory.KindFact, Status: memory.StatusActive, Title: "A", OccurredAt: time.Now()}})
	if !strings.HasSuffix(got, "\n\nno encontrados: #9") {
		t.Errorf("formatGet = %q", got)
	}
}
