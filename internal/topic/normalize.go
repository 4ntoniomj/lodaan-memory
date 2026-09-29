// Package topic resolves free-form topic names into stored topics.
//
// Topics have no catalog, but they must not multiply into synonyms ("gym" vs
// "entrenamiento"). Names are normalized into kebab-case slugs and, when a slug
// is new, compared by embedding similarity against the existing topics.
package topic

import (
	"strings"
	"unicode"
)

// maxSlugLen is the maximum length of a normalized slug.
const maxSlugLen = 60

// diacritics maps accented lowercase letters to their plain ASCII letter.
var diacritics = map[rune]rune{
	'á': 'a', 'à': 'a', 'ä': 'a', 'â': 'a',
	'é': 'e', 'è': 'e', 'ë': 'e', 'ê': 'e',
	'í': 'i', 'ì': 'i', 'ï': 'i', 'î': 'i',
	'ó': 'o', 'ò': 'o', 'ö': 'o', 'ô': 'o',
	'ú': 'u', 'ù': 'u', 'ü': 'u', 'û': 'u',
	'ñ': 'n', 'ç': 'c',
}

// Normalize turns a free-form topic name into a kebab-case slug: lowercase,
// without diacritics, with every run of characters outside a-z and 0-9
// replaced by a single "-", trimmed, and at most 60 characters long (cut at a
// "-" when possible). It returns "" if nothing usable remains.
func Normalize(raw string) string {
	var b strings.Builder
	lastDash := true // evita un "-" inicial
	for _, r := range raw {
		r = unicode.ToLower(r)
		if plain, ok := diacritics[r]; ok {
			r = plain
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	s := strings.TrimRight(b.String(), "-")

	if len(s) > maxSlugLen {
		cut := maxSlugLen
		// Si el carácter siguiente no es "-", la palabra queda partida: se
		// retrocede hasta el último "-" (si lo hay) para no dejarla a medias.
		if s[maxSlugLen] != '-' {
			if i := strings.LastIndexByte(s[:maxSlugLen], '-'); i > 0 {
				cut = i
			}
		}
		s = strings.TrimRight(s[:cut], "-")
	}
	return s
}
