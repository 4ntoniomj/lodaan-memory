package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// jsonKey returns the top-level key that holds the servers for the format.
func (f format) jsonKey() string {
	switch f {
	case formatVSCode:
		return "servers"
	case formatOpencode:
		return "mcp"
	case formatZed:
		return "context_servers"
	default:
		return "mcpServers"
	}
}

// jsonEntry builds the lodan entry for a JSON format. It uses only the types
// produced by encoding/json when decoding (map[string]any, []any, string,
// bool) so that reflect.DeepEqual can compare it with an existing entry.
func (f format) jsonEntry(bin string, env map[string]string) map[string]any {
	envMap := map[string]any{}
	for k, v := range env {
		envMap[k] = v
	}
	switch f {
	case formatOpencode:
		return map[string]any{
			"type":        "local",
			"command":     []any{bin, "serve"},
			"environment": envMap,
			"enabled":     true,
		}
	case formatClaudeCode, formatVSCode:
		return map[string]any{
			"type":    "stdio",
			"command": bin,
			"args":    []any{"serve"},
			"env":     envMap,
		}
	default:
		return map[string]any{
			"command": bin,
			"args":    []any{"serve"},
			"env":     envMap,
		}
	}
}

// jsonParseObject parses a strict JSON object. Numbers are kept as json.Number
// so that they are written back exactly as they were read. An empty text is {}.
func jsonParseObject(text string) (map[string]any, error) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(text, "\uFEFF"))
	if trimmed == "" {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("hay contenido después del objeto JSON")
	}
	if doc == nil {
		return nil, errors.New("el archivo no contiene un objeto JSON")
	}
	return doc, nil
}

func jsonWarning(err error) *WarningError {
	return &WarningError{Msg: fmt.Sprintf("no se puede leer como JSON estricto (%v); si tiene comentarios (JSONC) añade la entrada a mano. No se ha modificado", err)}
}

// jsonEncode writes the document indented with two spaces, without escaping
// HTML characters. Object keys come out sorted (Go maps have no order), so the
// key order of the original file can change.
func jsonEncode(doc map[string]any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// jsonString returns s as a JSON string literal (also valid as a YAML
// double-quoted scalar).
func jsonString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}

// jsonTransform adds, replaces or removes the lodan entry in a JSON document
// keeping every other key. It returns the text unchanged when there is nothing
// to do.
func jsonTransform(text string, f format, bin string, env map[string]string, remove bool) (string, error) {
	if remove && strings.TrimSpace(text) == "" {
		return text, nil
	}
	doc, err := jsonParseObject(text)
	if err != nil {
		return "", jsonWarning(err)
	}
	key := f.jsonKey()
	var container map[string]any
	switch v := doc[key].(type) {
	case nil:
		if remove {
			return text, nil
		}
		container = map[string]any{}
	case map[string]any:
		container = v
	default:
		return "", &WarningError{Msg: fmt.Sprintf("la clave %q no es un objeto; no se ha modificado", key)}
	}
	if remove {
		if _, ok := container["lodan"]; !ok {
			return text, nil
		}
		delete(container, "lodan")
	} else {
		entry := f.jsonEntry(bin, env)
		if reflect.DeepEqual(container["lodan"], entry) {
			return text, nil
		}
		container["lodan"] = entry
	}
	doc[key] = container
	return jsonEncode(doc)
}

// jsonReadEntry returns the lodan entry of a JSON configuration, if any.
func jsonReadEntry(text string, f format) (entry any, present bool, err error) {
	doc, err := jsonParseObject(text)
	if err != nil {
		return nil, false, err
	}
	container, ok := doc[f.jsonKey()].(map[string]any)
	if !ok {
		return nil, false, nil
	}
	entry, present = container["lodan"]
	return entry, present, nil
}

// jsonEntryEqual reports whether entry is identical to the one lodan would write.
func jsonEntryEqual(entry any, f format, bin string, env map[string]string) bool {
	return reflect.DeepEqual(entry, f.jsonEntry(bin, env))
}

// jsonEntryCommand extracts the command (the binary) of a lodan entry.
func jsonEntryCommand(f format, entry any) string {
	m, ok := entry.(map[string]any)
	if !ok {
		return ""
	}
	if f == formatOpencode {
		if arr, ok := m["command"].([]any); ok && len(arr) > 0 {
			s, _ := arr[0].(string)
			return s
		}
		return ""
	}
	s, _ := m["command"].(string)
	return s
}
