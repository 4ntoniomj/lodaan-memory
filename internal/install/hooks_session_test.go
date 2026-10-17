package install

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const (
	hkSessionClaude = `"C:/x/lodan.exe" hook session-start --client claude-code`
	hkSessionAnti   = `C:/x/lodan.exe hook session-start --client antigravity-client --format antigravity`
	hkSessMatcher   = "startup|resume|clear"
)

// S16: Claude Code gets the session-start group besides compact.
func TestHooksSessionStartClaude(t *testing.T) {
	e := hkNew(t, ".claude")
	hkInstall(t, e, hkBin)
	s := hkJSON(t, e.path(".claude/settings.json"))
	hs := hkFindHandlers(t, s, "SessionStart", hkSessMatcher, hkSessionClaude)
	if len(hs) != 1 {
		t.Fatalf("handlers session-start = %d, quería 1", len(hs))
	}
	if hs[0]["type"] != "command" || hs[0]["timeout"] != float64(15) {
		t.Errorf("handler = %v", hs[0])
	}
	if len(hkFindHandlers(t, s, "SessionStart", "compact", hkCompact)) != 1 {
		t.Errorf("falta el grupo compact existente")
	}
}

// S16: Antigravity gets a second SessionStart handler.
func TestHooksSessionStartAntigravity(t *testing.T) {
	e := hkNew(t, ".gemini/config")
	hkInstall(t, e, hkBin)
	got := hkJSON(t, e.path(".gemini/config/hooks.json"))
	lodan, _ := got["lodan"].(map[string]any)
	list, _ := lodan["SessionStart"].([]any)
	if len(list) != 2 {
		t.Fatalf("handlers = %v, quería 2", list)
	}
	second, _ := list[1].(map[string]any)
	if second["command"] != hkSessionAnti || second["type"] != "command" || second["timeout"] != float64(15) {
		t.Errorf("segundo handler = %v", second)
	}
}

// S16: the opencode plugin reacts to session.created.
func TestHooksSessionStartOpencode(t *testing.T) {
	e := hkNew(t, ".config/opencode")
	hkInstall(t, e, hkBin)
	js := hkRead(t, e.path(".config/opencode/plugins/lodan.js"))
	for _, w := range []string{"session.created", "session-start"} {
		if !strings.Contains(js, w) {
			t.Errorf("el plugin no contiene %q", w)
		}
	}
}

// S16: a second install changes nothing.
func TestHooksSessionStartIdempotente(t *testing.T) {
	e := hkNew(t, ".claude", ".config/opencode", ".gemini/config")
	hkInstall(t, e, hkBin)
	if files := hkInstall(t, e, hkBin); len(files) != 0 {
		t.Errorf("la segunda instalación cambió %v", files)
	}
	s := hkJSON(t, e.path(".claude/settings.json"))
	if n := hkCountCmd(t, s, "SessionStart", hkSessionClaude); n != 1 {
		t.Errorf("handlers session-start = %d", n)
	}
}

// S16: uninstall removes the session-start handlers too.
func TestHooksSessionStartDesinstalar(t *testing.T) {
	e := hkNew(t, ".claude", ".config/opencode", ".gemini/config")
	hkInstall(t, e, hkBin)
	if _, err := UninstallHooks(e.env, false); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".claude/settings.json", ".gemini/config/hooks.json"} {
		if hkExists(e.path(p)) && strings.Contains(hkRead(t, e.path(p)), "hook session-start") {
			t.Errorf("%s conserva session-start", p)
		}
	}
	if hkExists(e.path(".config/opencode/plugins/lodan.js")) {
		t.Errorf("el plugin sigue existiendo")
	}
}

// S16: doctor detects a missing session-start group.
func TestHooksSessionStartCheck(t *testing.T) {
	e := hkNew(t, ".claude")
	hkInstall(t, e, hkBin)
	if ok, d := CheckHooks(e.env, hkBin); !ok {
		t.Fatalf("tras instalar: %q", d)
	}
	path := e.path(".claude/settings.json")
	s := hkJSON(t, path)
	hooks := s["hooks"].(map[string]any)
	var keep []any
	for _, g := range hooks["SessionStart"].([]any) {
		if m, _ := g.(map[string]any)["matcher"].(string); m != hkSessMatcher {
			keep = append(keep, g)
		}
	}
	hooks["SessionStart"] = keep
	b, _ := json.Marshal(s)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	ok, d := CheckHooks(e.env, hkBin)
	if ok || !strings.Contains(d, "lodan install") {
		t.Errorf("ok=%v detail=%q", ok, d)
	}
}
