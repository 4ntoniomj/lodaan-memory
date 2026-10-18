package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const probeClaude = `{
  "env": {"FOO": "1"},
  "permissions": {"allow": ["Bash(git status)"]},
  "statusLine": {"type": "command", "command": "x"},
  "hooks": {
    "SessionStart": [{"matcher": "startup", "hooks": [{"type": "command", "command": "gentle-ai hook session-start", "timeout": 30}]}],
    "Stop": [{"hooks": [{"type": "command", "command": "gentle-ai hook stop"}]}],
    "SubagentStop": [{"matcher": "", "hooks": [{"type": "command", "command": "gentle-ai hook subagent-stop"}]}],
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "codegraph hook"}]}]
  }
}`

const probeGemini = `{"herdr": {"PreInvocation": [{"command": "herdr run", "timeout": 5, "type": "command"}]}}`

func parse(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, raw)
	}
	return v
}

func TestProbeHooksInstallKeepsUserHooks(t *testing.T) {
	home := t.TempDir()
	env := Env{GOOS: "windows", Home: home}
	claude := filepath.Join(home, ".claude", "settings.json")
	gem := filepath.Join(home, ".gemini", "config", "hooks.json")
	_ = os.MkdirAll(filepath.Dir(claude), 0o755)
	_ = os.MkdirAll(filepath.Dir(gem), 0o755)
	_ = os.MkdirAll(filepath.Join(home, ".config", "opencode", "plugins"), 0o755)
	userPlugin := filepath.Join(home, ".config", "opencode", "plugins", "other.js")
	_ = os.WriteFile(userPlugin, []byte("export default {}\n"), 0o644)
	_ = os.WriteFile(claude, []byte(probeClaude), 0o600)
	_ = os.WriteFile(gem, []byte(probeGemini), 0o600)
	var origClaude, origGem map[string]any
	_ = json.Unmarshal([]byte(probeClaude), &origClaude)
	_ = json.Unmarshal([]byte(probeGemini), &origGem)

	bin := `C:\Program Files\lodan dir\lodan.exe`
	changed, err := InstallHooks(env, bin, false)
	if err != nil || len(changed) != 3 {
		t.Fatalf("InstallHooks = %v, %v", changed, err)
	}
	c := parse(t, claude)
	for _, k := range []string{"env", "permissions", "statusLine"} {
		if !reflect.DeepEqual(c[k], origClaude[k]) {
			t.Errorf("setting %s changed: %v", k, c[k])
		}
	}
	h := c["hooks"].(map[string]any)
	oh := origClaude["hooks"].(map[string]any)
	for ev, groups := range oh {
		got := h[ev].([]any)
		for i, g := range groups.([]any) {
			if !reflect.DeepEqual(got[i], g) {
				t.Errorf("user group %s[%d] changed: %v", ev, i, got[i])
			}
		}
	}
	raw, _ := os.ReadFile(claude)
	if strings.Contains(string(raw), "30.0") || strings.Contains(string(raw), "1e") {
		t.Errorf("numbers rewritten: %s", raw)
	}
	if !strings.Contains(string(raw), `\"C:/Program Files/lodan dir/lodan.exe\" hook compact --client claude-code`) {
		t.Errorf("command not quoted with forward slashes:\n%s", raw)
	}
	for _, ev := range []string{"SessionStart", "PostCompact", "SubagentStop", "PostToolUse"} {
		n := strings.Count(string(raw), "") // placeholder
		_ = n
		cnt := 0
		for _, g := range h[ev].([]any) {
			for _, hh := range g.(map[string]any)["hooks"].([]any) {
				if isLodanHookCommand(hh.(map[string]any)["command"].(string)) {
					cnt++
				}
			}
		}
		want := 1
		if ev == "SessionStart" {
			want = 2 // session-start (startup|resume|clear) + compact
		}
		if cnt != want {
			t.Errorf("%s has %d lodan handlers, want %d", ev, cnt, want)
		}
	}
	if _, err := os.Stat(claude + ".bak-lodan"); err != nil {
		t.Errorf("no .bak-lodan: %v", err)
	}
	g := parse(t, gem)
	if !reflect.DeepEqual(g["herdr"], origGem["herdr"]) {
		t.Errorf("herdr group changed: %v", g["herdr"])
	}
	if ok, d := CheckHooks(env, bin); !ok {
		t.Errorf("CheckHooks after install: %s", d)
	}

	// Idempotent.
	changed, err = InstallHooks(env, bin, false)
	if err != nil || len(changed) != 0 {
		t.Errorf("second InstallHooks = %v, %v", changed, err)
	}
	// Uninstall restores the user's documents.
	if _, err := UninstallHooks(env, false); err != nil {
		t.Fatal(err)
	}
	if c := parse(t, claude); !reflect.DeepEqual(c, origClaude) {
		t.Errorf("after uninstall claude settings differ:\n%v\n%v", c, origClaude)
	}
	if g := parse(t, gem); !reflect.DeepEqual(g, origGem) {
		t.Errorf("after uninstall gemini hooks differ: %v", g)
	}
	if _, err := os.Stat(userPlugin); err != nil {
		t.Errorf("user plugin removed")
	}
	if _, err := os.Stat(opencodePluginPath(env)); err == nil {
		t.Errorf("lodan plugin still there")
	}
	raw, _ = os.ReadFile(claude)
	t.Logf("claude after uninstall (key order check):\n%s", raw)
}

func TestProbeHooksNoClientFolders(t *testing.T) {
	home := t.TempDir()
	changed, err := InstallHooks(Env{GOOS: "windows", Home: home}, "C:/lodan.exe", false)
	if err != nil || len(changed) != 0 {
		t.Errorf("InstallHooks without clients = %v %v", changed, err)
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 0 {
		t.Errorf("created %v", entries)
	}
}
