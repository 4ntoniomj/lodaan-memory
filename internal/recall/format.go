package recall

import (
	"fmt"
	"strings"
)

const (
	textOnlyLine = "(solo búsqueda por texto: el servicio de embeddings no está disponible)"
	getHint      = "Usa get con los ids para ver el detalle."
	dateLayout   = "2006-01-02"
)

// kindLabels are the Spanish names of the record kinds.
var kindLabels = map[string]string{
	"fact":       "dato",
	"preference": "preferencia",
	"decision":   "decisión",
	"event":      "evento",
	"note":       "nota",
}

// Format renders the result as compact plain text in Spanish.
//
// The output never exceeds maxBytes: lines are added while they fit, leaving
// room for the closing hint and, when something is left out, for the notice
// "… (respuesta recortada al tope de <n> bytes)", which goes before the hint.
// A maxBytes <= 0 disables the cap. If maxBytes is too small even for the
// notice and the hint, those two lines are still written.
func (r Result) Format(maxBytes int) string {
	lines := r.lines()
	if len(lines) == 0 {
		empty := fmt.Sprintf("Sin resultados para «%s».", oneLine(r.Query))
		if r.TextOnly {
			return textOnlyLine + "\n" + empty
		}
		return empty
	}

	if maxBytes <= 0 {
		return strings.Join(append(lines, getHint), "\n")
	}

	// Cada línea cuesta su longitud más un salto; el último salto no existe, así que el
	// presupuesto es maxBytes+1.
	cost := func(s string) int { return len(s) + 1 }
	budget := maxBytes + 1

	// Si todo cabe, no hay recorte ni aviso.
	total := cost(getHint)
	for _, l := range lines {
		total += cost(l)
	}
	if total <= budget {
		return strings.Join(append(lines, getHint), "\n")
	}

	// Si no, cada línea debe dejar sitio al aviso y a la pista final.
	notice := fmt.Sprintf("… (respuesta recortada al tope de %d bytes)", maxBytes)
	reserved := cost(notice) + cost(getHint)
	used := 0
	out := make([]string, 0, len(lines)+2)
	for _, l := range lines {
		if used+cost(l)+reserved > budget {
			break
		}
		out = append(out, l)
		used += cost(l)
	}
	out = append(out, notice, getHint)
	return strings.Join(out, "\n")
}

// lines returns the lines of the answer before the closing hint. It is empty
// when there is nothing to show.
func (r Result) lines() []string {
	if len(r.Profile)+len(r.Events)+len(r.Results) == 0 {
		return nil
	}
	var out []string
	if r.TextOnly {
		out = append(out, textOnlyLine)
	}
	if r.Topic != nil {
		l := "Tema: " + r.Topic.Slug
		if r.TopicDetected {
			l += " (detectado)"
		}
		out = append(out, l)
	}
	section := func(title string, items []Item) {
		if len(items) == 0 {
			return
		}
		out = append(out, title)
		for _, it := range items {
			out = append(out, it.line())
		}
	}
	section("Datos estables:", r.Profile)
	section("Últimos eventos:", r.Events)
	section("Resultados:", r.Results)
	return out
}

// line renders one item as "#id [tipo fecha, estado] título — fragmento".
func (it Item) line() string {
	label, ok := kindLabels[it.Kind]
	if !ok {
		label = it.Kind
	}
	if !it.OccurredAt.IsZero() {
		label += " " + it.OccurredAt.Local().Format(dateLayout)
	}
	switch it.Status {
	case "superseded":
		if it.SupersededBy > 0 {
			label += fmt.Sprintf(", sustituido por #%d", it.SupersededBy)
		} else {
			label += ", sustituido"
		}
	case "invalidated":
		label += ", invalidado"
	}
	s := fmt.Sprintf("#%d [%s] %s", it.ID, label, oneLine(it.Title))
	if snip := oneLine(it.Snippet); snip != "" {
		s += " — " + snip
	}
	return s
}

// oneLine collapses every run of whitespace, line breaks included, into one space.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
