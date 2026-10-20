// Package topic resolves free-form topic names into stored topics.
//
// Topics (shown to the AIs as tags) have no catalog. Names are normalized into
// kebab-case slugs; a slug equal to an existing one once every "-" is removed
// resolves to that one. Similar meanings are never merged automatically: the
// alias dictionary decides which tags are the same.
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
// without diacritics, with every run of characters outside a-z, 0-9 and the
// hierarchy separator "/" replaced by a single "-", trimmed, and at most 60
// characters long (cut at a "-" when possible). It returns "" if nothing
// usable remains.
//
// "/" is preserved (it marks the hierarchy boundary defined in D2 §5 of
// odd/tasks/lodan-sin-ollama.md): "coche-volvo-s60/matricula" stays as is, not
// "coche-volvo-s60-matricula".
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
		if r == '/' {
			b.WriteRune(r)
			lastDash = true // un "-" seguido de "/" se colapsa en un solo "/"
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
		// Si el carácter siguiente no es "-" ni "/", la palabra queda partida:
		// se retrocede hasta el último separador (si lo hay) para no dejarla a medias.
		if s[maxSlugLen] != '-' && s[maxSlugLen] != '/' {
			if i := strings.LastIndexAny(s[:maxSlugLen], "-/"); i > 0 {
				cut = i
			}
		}
		s = strings.TrimRight(s[:cut], "-")
	}
	return s
}
