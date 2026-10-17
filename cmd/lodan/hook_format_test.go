package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"lodan/internal/hook"
)

const hfStartText = "lodan: conversación nueva. Antes de responder sobre un tema, llama una vez a recall de lodan en lote (queries) con los temas de la conversación."

const hfAntigravityStdin = `{"conversationId":"x","workspacePaths":["C:/p"],"transcriptPath":"t"}`

// hfParseOne parses stdout as exactly one JSON object and nothing else.
func hfParseOne(t *testing.T, out string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		t.Fatalf("stdout no es un objeto JSON: %q (%v)", out, err)
	}
	if dec.More() {
		t.Errorf("stdout contiene más de un objeto JSON: %q", out)
	}
	return obj
}

func hfExpected(text string) map[string]any {
	return map[string]any{"injectSteps": []any{map[string]any{"systemMessage": map[string]any{"systemMessage": text}}}}
}

// S13 details: the constant hook.StartInstructions has the specified text.
func TestStartInstructionsTexto(t *testing.T) {
	if hook.StartInstructions != hfStartText {
		t.Errorf("StartInstructions = %q, quería %q", hook.StartInstructions, hfStartText)
	}
}

// S13 details: --format antigravity with Antigravity's stdin and no database.
func TestHookCompactFormatAntigravityConStdin(t *testing.T) {
	dir := t.TempDir()
	code, out, errOut := runHook(t, dir, hfAntigravityStdin, "hook", "compact", "--client", "antigravity-client", "--format", "antigravity")
	if code != 0 {
		t.Fatalf("código = %d (stderr: %s)", code, errOut)
	}
	if got := hfParseOne(t, out); !reflect.DeepEqual(got, hfExpected(hook.StartInstructions)) {
		t.Errorf("stdout = %q, quería solo las instrucciones de inicio", out)
	}
	assertNoPG(t, dir)
}

// S13 details: --format antigravity with empty stdin still prints the JSON.
func TestHookCompactFormatAntigravityStdinVacio(t *testing.T) {
	dir := t.TempDir()
	code, out, errOut := runHook(t, dir, "", "hook", "compact", "--client", "antigravity-client", "--format", "antigravity")
	if code != 0 {
		t.Fatalf("código = %d (stderr: %s)", code, errOut)
	}
	if got := hfParseOne(t, out); !reflect.DeepEqual(got, hfExpected(hook.StartInstructions)) {
		t.Errorf("stdout = %q, quería solo las instrucciones de inicio", out)
	}
}

// S13 details: an unknown --format value exits with code 2.
func TestHookCompactFormatDesconocido(t *testing.T) {
	code, _, _ := runHook(t, t.TempDir(), "", "hook", "compact", "--client", "x", "--format", "yaml")
	if code != 2 {
		t.Errorf("código = %d, quería 2", code)
	}
}

// S13 details / S10: --format text equals the default output.
func TestHookCompactFormatTextIgualQueDefecto(t *testing.T) {
	in := `{"hook_event_name":"SessionStart","source":"compact"}`
	c1, def, _ := runHook(t, t.TempDir(), in, "hook", "compact", "--client", "claude-code")
	c2, txt, _ := runHook(t, t.TempDir(), in, "hook", "compact", "--client", "claude-code", "--format", "text")
	if c1 != 0 || c2 != 0 {
		t.Fatalf("códigos = %d, %d", c1, c2)
	}
	if def != txt {
		t.Errorf("--format text = %q, defecto = %q", txt, def)
	}
	if !strings.Contains(txt, hookInstrucciones) {
		t.Errorf("stdout = %q, falta la instrucción de recuperación", txt)
	}
}
