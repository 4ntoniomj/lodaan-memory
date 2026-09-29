package install

import (
	"regexp"
	"strconv"
	"strings"
)

// Hand-written YAML editing for ~/.hermes/config.yaml. There is no YAML
// library on purpose: the file (which can be thousands of lines long) is edited
// line by line so that everything outside the `lodan:` block under the
// top-level `mcp_servers:` key is preserved byte by byte.

var yamlPlainKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// yamlLayout describes where things are inside the mcp_servers section.
type yamlLayout struct {
	// top is the line of `mcp_servers:`.
	top int
	// end is the first line after the section (next top-level key or EOF).
	end int
	// childIndent is the indentation of the servers (2 if the section is empty).
	childIndent int
	// last is the last content line of the section (top when it is empty).
	last int
	// lodanStart and lodanEnd delimit the `lodan:` block; lodanStart is -1 if absent.
	lodanStart int
	lodanEnd   int
}

func yamlIndent(line string) int {
	n := 0
	for n < len(line) && (line[n] == ' ' || line[n] == '\t') {
		n++
	}
	return n
}

// yamlContent reports whether the line is neither blank nor a comment.
func yamlContent(line string) bool {
	t := strings.TrimSpace(line)
	return t != "" && !strings.HasPrefix(t, "#")
}

// yamlFindTop finds the top-level `mcp_servers:` key (not commented, no
// indentation) and returns its line and the inline value after the colon.
func yamlFindTop(lines []string) (int, string) {
	for i, ln := range lines {
		if !strings.HasPrefix(ln, "mcp_servers") {
			continue
		}
		rest := strings.TrimLeft(ln[len("mcp_servers"):], " \t")
		if !strings.HasPrefix(rest, ":") {
			continue
		}
		val := strings.TrimSpace(rest[1:])
		if strings.HasPrefix(val, "#") {
			val = ""
		} else if k := strings.Index(val, " #"); k >= 0 {
			val = strings.TrimSpace(val[:k])
		}
		return i, val
	}
	return -1, ""
}

func yamlEmptyValue(v string) bool {
	return v == "{}" || v == "null" || v == "~"
}

func yamlIsLodanKey(line string) bool {
	t := strings.TrimSpace(line)
	for _, k := range []string{"lodan", `"lodan"`, "'lodan'"} {
		if strings.HasPrefix(t, k) {
			if strings.HasPrefix(strings.TrimLeft(t[len(k):], " \t"), ":") {
				return true
			}
		}
	}
	return false
}

func yamlAnalyze(lines []string, top int) yamlLayout {
	lay := yamlLayout{top: top, end: len(lines), last: top, lodanStart: -1}
	for j := top + 1; j < len(lines); j++ {
		if yamlContent(lines[j]) && yamlIndent(lines[j]) == 0 {
			lay.end = j
			break
		}
	}
	for j := top + 1; j < lay.end; j++ {
		if !yamlContent(lines[j]) {
			continue
		}
		if lay.childIndent == 0 {
			lay.childIndent = yamlIndent(lines[j])
		}
		lay.last = j
	}
	if lay.childIndent == 0 {
		lay.childIndent = 2
	}
	for j := top + 1; j < lay.end; j++ {
		if yamlContent(lines[j]) && yamlIndent(lines[j]) == lay.childIndent && yamlIsLodanKey(lines[j]) {
			lay.lodanStart = j
			break
		}
	}
	if lay.lodanStart >= 0 {
		lay.lodanEnd = lay.lodanStart + 1
		for k := lay.lodanStart + 1; k < lay.end; k++ {
			if !yamlContent(lines[k]) {
				continue
			}
			if yamlIndent(lines[k]) <= lay.childIndent {
				break
			}
			lay.lodanEnd = k + 1
		}
	}
	return lay
}

func yamlKey(k string) string {
	if yamlPlainKey.MatchString(k) {
		return k
	}
	return jsonString(k)
}

// yamlBlockLines renders the `lodan:` block with the given indentation.
func yamlBlockLines(indent int, bin string, env map[string]string, eol string) []string {
	p1 := strings.Repeat(" ", indent)
	p2 := p1 + "  "
	p3 := p2 + "  "
	lines := []string{
		p1 + "lodan:" + eol,
		p2 + "command: " + jsonString(bin) + eol,
		p2 + "args:" + eol,
		p3 + "- " + jsonString("serve") + eol,
	}
	if len(env) == 0 {
		return append(lines, p2+"env: {}"+eol)
	}
	lines = append(lines, p2+"env:"+eol)
	for _, k := range sortedEnvKeys(env) {
		lines = append(lines, p3+yamlKey(k)+": "+jsonString(env[k])+eol)
	}
	return lines
}

// yamlFixNewlines makes sure that every line but the last ends with a newline.
func yamlFixNewlines(lines []string, eol string) []string {
	for i := 0; i < len(lines)-1; i++ {
		if !strings.HasSuffix(lines[i], "\n") {
			lines[i] += eol
		}
	}
	return lines
}

// yamlTransform adds, replaces or removes the lodan server under the
// top-level `mcp_servers:` key. It returns the text unchanged when there is
// nothing to do.
func yamlTransform(text, bin string, env map[string]string, remove bool) (string, error) {
	eol := detectEOL(text)
	lines := splitLinesKeep(text)
	top, value := yamlFindTop(lines)
	if top < 0 {
		if remove {
			return text, nil
		}
		out := append([]string{}, lines...)
		if n := len(out); n > 0 {
			if !strings.HasSuffix(out[n-1], "\n") {
				out[n-1] += eol
			}
			if !lineIsBlank(out[n-1]) {
				out = append(out, eol)
			}
		}
		out = append(out, "mcp_servers:"+eol)
		out = append(out, yamlBlockLines(2, bin, env, eol)...)
		return strings.Join(out, ""), nil
	}
	if value != "" {
		if !yamlEmptyValue(value) {
			return "", &WarningError{Msg: "mcp_servers no es un bloque YAML estándar (valor en la misma línea); no se ha modificado, añade la entrada a mano"}
		}
		if remove {
			return text, nil
		}
		lines[top] = "mcp_servers:" + eol
	}
	lay := yamlAnalyze(lines, top)
	if remove {
		if lay.lodanStart < 0 {
			return text, nil
		}
		othersExist := false
		for j := top + 1; j < lay.end; j++ {
			if j >= lay.lodanStart && j < lay.lodanEnd {
				continue
			}
			if yamlContent(lines[j]) {
				othersExist = true
				break
			}
		}
		out := removeLines(lines, lay.lodanStart, lay.lodanEnd)
		if !othersExist {
			// The section only held lodan: an empty `mcp_servers:` would be
			// read as null by some programs, so the key goes too.
			out = removeLines(out, top, top+1)
		}
		return strings.Join(out, ""), nil
	}
	block := yamlBlockLines(lay.childIndent, bin, env, eol)
	var out []string
	if lay.lodanStart >= 0 {
		out = spliceLines(lines, lay.lodanStart, lay.lodanEnd, block)
	} else {
		at := lay.last + 1
		out = spliceLines(lines, at, at, block)
	}
	return strings.Join(yamlFixNewlines(out, eol), ""), nil
}

// yamlScalar decodes a YAML scalar written as plain, 'single' or "double" quoted.
func yamlScalar(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, `"`):
		if end := strings.LastIndex(s, `"`); end > 0 {
			if v, err := strconv.Unquote(s[:end+1]); err == nil {
				return v
			}
			return s[1:end]
		}
	case strings.HasPrefix(s, "'"):
		if end := strings.LastIndex(s, "'"); end > 0 {
			return strings.ReplaceAll(s[1:end], "''", "'")
		}
	}
	if k := strings.Index(s, " #"); k >= 0 {
		s = strings.TrimSpace(s[:k])
	}
	return s
}

// yamlEntryCommand returns the command of the lodan server, if it exists.
func yamlEntryCommand(text string) (string, bool) {
	lines := splitLinesKeep(text)
	top, value := yamlFindTop(lines)
	if top < 0 || value != "" {
		return "", false
	}
	lay := yamlAnalyze(lines, top)
	if lay.lodanStart < 0 {
		return "", false
	}
	for j := lay.lodanStart + 1; j < lay.lodanEnd; j++ {
		t := strings.TrimSpace(lines[j])
		if strings.HasPrefix(t, "command:") {
			return yamlScalar(t[len("command:"):]), true
		}
	}
	return "", true
}
