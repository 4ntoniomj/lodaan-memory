package install

import (
	"fmt"
	"regexp"
	"strings"
)

// Hand-written TOML editing for ~/.codex/config.toml. There is no TOML library
// on purpose: the file is edited line by line so that everything outside the
// [mcp_servers.lodan] tables is preserved byte by byte (comments included).

// tomlHeader is a table header found at a given line.
type tomlHeader struct {
	line int
	path []string
}

var (
	tomlRootConflict  = regexp.MustCompile(`^mcp_servers\s*(=|\.\s*(lodan|"lodan"|'lodan')\s*[.=])`)
	tomlTableConflict = regexp.MustCompile(`^(lodan|"lodan"|'lodan')\s*[.=]`)
)

func isBareKeyChar(c byte) bool {
	return c == '_' || c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// tomlReadQuoted reads a basic ("...") or literal ('...') string starting at
// t[i] and returns its value and the index after the closing quote.
func tomlReadQuoted(t string, i int) (string, int, bool) {
	if i >= len(t) {
		return "", i, false
	}
	switch t[i] {
	case '"':
		var sb strings.Builder
		j := i + 1
		for j < len(t) && t[j] != '"' {
			if t[j] == '\\' && j+1 < len(t) {
				j++
				switch t[j] {
				case 'n':
					sb.WriteByte('\n')
				case 't':
					sb.WriteByte('\t')
				case 'r':
					sb.WriteByte('\r')
				default:
					sb.WriteByte(t[j])
				}
			} else {
				sb.WriteByte(t[j])
			}
			j++
		}
		if j >= len(t) {
			return "", i, false
		}
		return sb.String(), j + 1, true
	case '\'':
		j := strings.IndexByte(t[i+1:], '\'')
		if j < 0 {
			return "", i, false
		}
		return t[i+1 : i+1+j], i + 1 + j + 1, true
	}
	return "", i, false
}

// tomlParseHeader parses a trimmed line as a table header ([a.b] or [[a.b]]).
func tomlParseHeader(t string) ([]string, bool) {
	double := strings.HasPrefix(t, "[[")
	i := 1
	if double {
		i = 2
	}
	var path []string
	for {
		for i < len(t) && (t[i] == ' ' || t[i] == '\t') {
			i++
		}
		if i >= len(t) {
			return nil, false
		}
		var key string
		switch t[i] {
		case '"', '\'':
			k, next, ok := tomlReadQuoted(t, i)
			if !ok {
				return nil, false
			}
			key, i = k, next
		default:
			j := i
			for j < len(t) && isBareKeyChar(t[j]) {
				j++
			}
			if j == i {
				return nil, false
			}
			key, i = t[i:j], j
		}
		path = append(path, key)
		for i < len(t) && (t[i] == ' ' || t[i] == '\t') {
			i++
		}
		if i >= len(t) {
			return nil, false
		}
		switch t[i] {
		case '.':
			i++
		case ']':
			i++
			if double {
				if i >= len(t) || t[i] != ']' {
					return nil, false
				}
				i++
			}
			rest := strings.TrimSpace(t[i:])
			if rest == "" || strings.HasPrefix(rest, "#") {
				return path, true
			}
			return nil, false
		default:
			return nil, false
		}
	}
}

// tomlScan advances the multi-line string and array state over one line.
func tomlScan(line, inMulti string, depth int) (string, int) {
	i := 0
	for i < len(line) {
		if inMulti != "" {
			j := strings.Index(line[i:], inMulti)
			if j < 0 {
				return inMulti, depth
			}
			i += j + 3
			inMulti = ""
			continue
		}
		c := line[i]
		switch {
		case c == '#':
			return inMulti, depth
		case strings.HasPrefix(line[i:], `"""`):
			inMulti = `"""`
			i += 3
		case strings.HasPrefix(line[i:], `'''`):
			inMulti = `'''`
			i += 3
		case c == '"':
			j := i + 1
			for j < len(line) {
				if line[j] == '\\' {
					j += 2
					continue
				}
				if line[j] == '"' {
					j++
					break
				}
				j++
			}
			i = j
		case c == '\'':
			j := strings.IndexByte(line[i+1:], '\'')
			if j < 0 {
				i = len(line)
			} else {
				i += j + 2
			}
		case c == '[':
			depth++
			i++
		case c == ']':
			if depth > 0 {
				depth--
			}
			i++
		default:
			i++
		}
	}
	return inMulti, depth
}

// tomlScanHeaders returns the table headers of the document. conflict is true
// when mcp_servers.lodan is defined with dotted or inline keys, which this
// editor cannot rewrite safely.
func tomlScanHeaders(lines []string) (headers []tomlHeader, conflict bool) {
	var cur []string
	inMulti := ""
	depth := 0
	for i, ln := range lines {
		if inMulti == "" && depth == 0 {
			t := strings.TrimSpace(ln)
			if strings.HasPrefix(t, "[") {
				if path, ok := tomlParseHeader(t); ok {
					headers = append(headers, tomlHeader{line: i, path: path})
					cur = path
					continue
				}
			} else if t != "" && !strings.HasPrefix(t, "#") {
				switch {
				case len(cur) == 0 && tomlRootConflict.MatchString(t):
					conflict = true
				case len(cur) == 1 && cur[0] == "mcp_servers" && tomlTableConflict.MatchString(t):
					conflict = true
				}
			}
		}
		inMulti, depth = tomlScan(ln, inMulti, depth)
	}
	return headers, conflict
}

func tomlIsLodanPath(path []string) bool {
	return len(path) >= 2 && path[0] == "mcp_servers" && path[1] == "lodan"
}

// tomlLodanSpans returns the [start, end) line ranges of every lodan table and
// subtable. Blank and comment lines after the last content line of a table
// are not part of it (they belong to whatever comes next).
func tomlLodanSpans(lines []string, headers []tomlHeader) [][2]int {
	var spans [][2]int
	for k, h := range headers {
		if !tomlIsLodanPath(h.path) {
			continue
		}
		next := len(lines)
		if k+1 < len(headers) {
			next = headers[k+1].line
		}
		end := h.line + 1
		for j := h.line + 1; j < next; j++ {
			t := strings.TrimSpace(lines[j])
			if t != "" && !strings.HasPrefix(t, "#") {
				end = j + 1
			}
		}
		spans = append(spans, [2]int{h.line, end})
	}
	return spans
}

func tomlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '"':
			b.WriteString(`\"`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func tomlKey(k string) string {
	if k != "" {
		bare := true
		for i := 0; i < len(k); i++ {
			if !isBareKeyChar(k[i]) {
				bare = false
				break
			}
		}
		if bare {
			return k
		}
	}
	return tomlQuote(k)
}

// tomlBlock renders the [mcp_servers.lodan] table. The env table is omitted
// when empty.
func tomlBlock(bin string, env map[string]string, eol string) []string {
	lines := []string{
		"[mcp_servers.lodan]" + eol,
		"command = " + tomlQuote(bin) + eol,
		`args = ["serve"]` + eol,
	}
	if len(env) > 0 {
		var parts []string
		for _, k := range sortedEnvKeys(env) {
			parts = append(parts, tomlKey(k)+" = "+tomlQuote(env[k]))
		}
		lines = append(lines, "env = { "+strings.Join(parts, ", ")+" }"+eol)
	}
	return lines
}

// tomlTransform adds, replaces or removes the lodan tables of a Codex config.
// It returns the text unchanged when there is nothing to do.
func tomlTransform(text, bin string, env map[string]string, remove bool) (string, error) {
	lines := splitLinesKeep(text)
	headers, conflict := tomlScanHeaders(lines)
	if conflict {
		return "", &WarningError{Msg: "mcp_servers.lodan está definido con claves inline o con puntos; no se ha modificado, edítalo a mano"}
	}
	spans := tomlLodanSpans(lines, headers)
	if remove {
		if len(spans) == 0 {
			return text, nil
		}
		for i := len(spans) - 1; i >= 0; i-- {
			lines = removeLines(lines, spans[i][0], spans[i][1])
		}
		return strings.Join(lines, ""), nil
	}
	eol := detectEOL(text)
	block := tomlBlock(bin, env, eol)
	if len(spans) == 0 {
		out := text
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += eol
		}
		if strings.TrimSpace(out) != "" && !endsWithBlankLine(out) {
			out += eol
		}
		return out + strings.Join(block, ""), nil
	}
	// Later spans (the env subtable, for example) are removed first so that
	// the indexes of the first one stay valid; the first is replaced in place.
	for i := len(spans) - 1; i >= 1; i-- {
		lines = removeLines(lines, spans[i][0], spans[i][1])
	}
	lines = spliceLines(lines, spans[0][0], spans[0][1], block)
	return strings.Join(lines, ""), nil
}

// tomlKeyString returns the string value of `key = "value"` on a line.
func tomlKeyString(line, key string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, key) {
		return "", false
	}
	rest := strings.TrimLeft(t[len(key):], " \t")
	if !strings.HasPrefix(rest, "=") {
		return "", false
	}
	rest = strings.TrimLeft(rest[1:], " \t")
	v, _, ok := tomlReadQuoted(rest, 0)
	return v, ok
}

// tomlEntryCommand returns the command of [mcp_servers.lodan], if the table exists.
func tomlEntryCommand(text string) (string, bool) {
	lines := splitLinesKeep(text)
	headers, _ := tomlScanHeaders(lines)
	for k, h := range headers {
		if len(h.path) != 2 || !tomlIsLodanPath(h.path) {
			continue
		}
		next := len(lines)
		if k+1 < len(headers) {
			next = headers[k+1].line
		}
		for j := h.line + 1; j < next; j++ {
			if v, ok := tomlKeyString(lines[j], "command"); ok {
				return v, true
			}
		}
		return "", true
	}
	return "", false
}
