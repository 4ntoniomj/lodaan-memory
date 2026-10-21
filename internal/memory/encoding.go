package memory

import (
	"strings"
	"unicode/utf8"
)

// runeToWin1252 maps Unicode runes in the 0x80..0x9F range (and the 5 undefined
// bytes 0x81, 0x8D, 0x8F, 0x90, 0x9D) to their Windows-1252 byte value.
var runeToWin1252 = map[rune]byte{
	0x20AC: 0x80, // €
	0x0081: 0x81, // byte no definido en Win-1252, aceptado tal cual
	0x201A: 0x82, // ‚
	0x0192: 0x83, // ƒ
	0x201E: 0x84, // „
	0x2026: 0x85, // …
	0x2020: 0x86, // †
	0x2021: 0x87, // ‡
	0x02C6: 0x88, // ˆ
	0x2030: 0x89, // ‰
	0x0160: 0x8A, // Š
	0x2039: 0x8B, // ‹
	0x0152: 0x8C, // Œ
	0x008D: 0x8D, // byte no definido en Win-1252, aceptado tal cual
	0x017D: 0x8E, // Ž
	0x008F: 0x8F, // byte no definido en Win-1252, aceptado tal cual
	0x0090: 0x90, // byte no definido en Win-1252, aceptado tal cual
	0x2018: 0x91, // ‘
	0x2019: 0x92, // ’
	0x201C: 0x93, // “
	0x201D: 0x94, // ”
	0x2022: 0x95, // •
	0x2013: 0x96, // –
	0x2014: 0x97, // —
	0x02DC: 0x98, // ˜
	0x2122: 0x99, // ™
	0x0161: 0x9A, // š
	0x203A: 0x9B, // ›
	0x0153: 0x9C, // œ
	0x009D: 0x9D, // byte no definido en Win-1252, aceptado tal cual
	0x017E: 0x9E, // ž
	0x0178: 0x9F, // Ÿ
}

// RepairMojibake comprueba si un texto UTF-8 contiene mojibake originado al leer
// UTF-8 como Windows-1252 y volver a guardarlo.
//
// Regla del contrato:
// Contiene 'Ã' o 'Â' y, al convertir el texto completo a bytes Windows-1252
// (aceptando los bytes 0x81, 0x8D, 0x8F, 0x90 y 0x9D tal cual), el resultado
// es UTF-8 válido y distinto del original.
// Ejemplos: "AlbarÃ¡n" -> "Albarán", "mediciÃ³n" -> "medición", "RaÃºl" -> "Raúl",
// "Ã­" (con guion blando) -> "í". Textos como "PÃO" se dejan intactos.
func RepairMojibake(s string) (string, bool) {
	if !strings.ContainsRune(s, 'Ã') && !strings.ContainsRune(s, 'Â') {
		return s, false
	}

	buf := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r <= 0x7F:
			buf = append(buf, byte(r))
		case r >= 0xA0 && r <= 0xFF:
			buf = append(buf, byte(r))
		default:
			if b, ok := runeToWin1252[r]; ok {
				buf = append(buf, b)
			} else {
				// Carácter no representable en Windows-1252; no es mojibake de esta fuente
				return s, false
			}
		}
	}

	if !utf8.Valid(buf) {
		return s, false
	}

	repaired := string(buf)
	if repaired == s {
		return s, false
	}
	return repaired, true
}
