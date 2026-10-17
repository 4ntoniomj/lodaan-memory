package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	hkBin     = `C:\x\lodan.exe`
	hkBinFwd  = "C:/x/lodan.exe"
	hkBin2    = `D:\otro\lodan.exe`
	hkCompact = `"C:/x/lodan.exe" hook compact --client claude-code`
	hkSubStop = `"C:/x/lodan.exe" hook subagent-stop --client claude-code`
	hkHeader  = "// installed by lodan"
)

type hkEnvT struct {
	env  Env
	home string
}

// hkNew returns a Linux environment over a temporary home with the given client folders.
func hkNew(t *testing.T, dirs ...string) hkEnvT {
	t.Helper()
	home := t.TempDir()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(home, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return hkEnvT{env: Env{GOOS: "linux", Home: home}, home: home}
}

func (e hkEnvT) path(rel string) string { return filepath.Join(e.home, filepath.FromSlash(rel)) }

func hkWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hkRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func hkJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(hkRead(t, path)), &m); err != nil {
		t.Fatalf("%s no es JSON válido: %v", path, err)
	}
	return m
}

func hkExists(path string) bool { _, err := os.Stat(path); return err == nil }

func hkInstall(t *testing.T, e hkEnvT, bin string) []string {
	t.Helper()
	files, err := InstallHooks(e.env, bin, false)
	if err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	return files
}

func hkHasSuffix(files []string, suffix string) bool {
	for _, f := range files {
		if strings.HasSuffix(filepath.ToSlash(f), suffix) {
			return true
		}
	}
	return false
}

func hkGroups(t *testing.T, settings map[string]any, event string) []map[string]any {
	t.Helper()
	hooks, _ := settings["hooks"].(map[string]any)
	list, _ := hooks[event].([]any)
	var out []map[string]any
	for _, g := range list {
		m, ok := g.(map[string]any)
		if !ok {
			t.Fatalf("grupo no es objeto: %v", g)
		}
		out = append(out, m)
	}
	return out
}

func hkHandlers(g map[string]any) []map[string]any {
	var out []map[string]any
	list, _ := g["hooks"].([]any)
	for _, h := range list {
		if m, ok := h.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func hkIsLodan(cmd string) bool {
	return strings.Contains(cmd, " hook compact --client ") || strings.Contains(cmd, " hook subagent-stop --client ")
}

// hkCountLodan counts lodan handlers in the whole settings file.
func hkCountLodan(settings map[string]any) int {
	n := 0
	hooks, _ := settings["hooks"].(map[string]any)
	for _, v := range hooks {
		list, _ := v.([]any)
		for _, g := range list {
			gm, _ := g.(map[string]any)
			for _, h := range hkHandlers(gm) {
				if c, _ := h["command"].(string); hkIsLodan(c) {
					n++
				}
			}
		}
	}
	return n
}

// hkCountCmd counts handlers with exactly this command in an event.
func hkCountCmd(t *testing.T, settings map[string]any, event, cmd string) int {
	n := 0
	for _, g := range hkGroups(t, settings, event) {
		for _, h := range hkHandlers(g) {
			if c, _ := h["command"].(string); c == cmd {
				n++
			}
		}
	}
	return n
}

// hkFindHandlers returns the handlers with cmd inside groups of an event with the given matcher.
func hkFindHandlers(t *testing.T, settings map[string]any, event, matcher, cmd string) []map[string]any {
	var out []map[string]any
	for _, g := range hkGroups(t, settings, event) {
		m, _ := g["matcher"].(string)
		if m != matcher {
			continue
		}
		for _, h := range hkHandlers(g) {
			if c, _ := h["command"].(string); c == cmd {
				out = append(out, h)
			}
		}
	}
	return out
}

const hkSettingsExisting = `{
  "model": "x",
  "permissions": {"allow": ["Bash(ls:*)"]},
  "hooks": {
    "SessionStart": [
      {"matcher": "startup|resume|clear|compact", "hooks": [{"type": "command", "command": "gentle-ai hook session-start"}]}
    ],
    "SubagentStop": [
      {"matcher": "", "hooks": [{"type": "command", "command": "other-tool subagent-done"}]}
    ],
    "UserPromptSubmit": [
      {"hooks": [{"type": "command", "command": "gentle-ai hook prompt"}]}
    ]
  }
}`

func hkAssertForeignKept(t *testing.T, settings map[string]any, orig map[string]any) {
	t.Helper()
	if settings["model"] != "x" {
		t.Errorf("model = %v, quería x", settings["model"])
	}
	if !reflect.DeepEqual(settings["permissions"], orig["permissions"]) {
		t.Errorf("permissions cambiado: %v", settings["permissions"])
	}
	if hkCountCmd(t, settings, "SessionStart", "gentle-ai hook session-start") != 1 {
		t.Errorf("falta el hook SessionStart de gentle-ai")
	}
	if hkCountCmd(t, settings, "SubagentStop", "other-tool subagent-done") != 1 {
		t.Errorf("falta el hook SubagentStop ajeno")
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if !reflect.DeepEqual(hooks["UserPromptSubmit"], orig["hooks"].(map[string]any)["UserPromptSubmit"]) {
		t.Errorf("UserPromptSubmit cambiado: %v", hooks["UserPromptSubmit"])
	}
}

func hkOrig(t *testing.T) map[string]any {
	t.Helper()
	var orig map[string]any
	if err := json.Unmarshal([]byte(hkSettingsExisting), &orig); err != nil {
		t.Fatal(err)
	}
	return orig
}

// S13 details: Claude Code gets the four groups with the exact commands, type and timeout.
func TestHooksClaudeGruposExactos(t *testing.T) {
	e := hkNew(t, ".claude")
	files := hkInstall(t, e, hkBin)
	if !hkHasSuffix(files, ".claude/settings.json") {
		t.Errorf("files = %v, falta settings.json", files)
	}
	s := hkJSON(t, e.path(".claude/settings.json"))
	cases := []struct{ event, matcher, cmd string }{
		{"SessionStart", "compact", hkCompact},
		{"PostCompact", "", hkCompact},
		{"SubagentStop", "", hkSubStop},
		{"PostToolUse", "SubagentHandback", hkSubStop},
	}
	for _, c := range cases {
		hs := hkFindHandlers(t, s, c.event, c.matcher, c.cmd)
		if len(hs) != 1 {
			t.Errorf("%s matcher %q: %d handlers con %q, quería 1", c.event, c.matcher, len(hs), c.cmd)
			continue
		}
		if hs[0]["type"] != "command" || hs[0]["timeout"] != float64(15) {
			t.Errorf("%s: handler = %v, quería type command y timeout 15", c.event, hs[0])
		}
	}
	if n := hkCountLodan(s); n != 4 {
		t.Errorf("handlers de lodan = %d, quería 4", n)
	}
}

// S13 details: existing hooks and settings are kept intact, and a backup is made.
func TestHooksClaudeConservaAjenoYHaceBackup(t *testing.T) {
	e := hkNew(t, ".claude")
	hkWrite(t, e.path(".claude/settings.json"), hkSettingsExisting)

	hkInstall(t, e, hkBin)
	s := hkJSON(t, e.path(".claude/settings.json"))
	hkAssertForeignKept(t, s, hkOrig(t))
	if n := hkCountLodan(s); n != 4 {
		t.Errorf("handlers de lodan = %d, quería 4", n)
	}
	if got := hkRead(t, e.path(".claude/settings.json.bak-lodan")); got != hkSettingsExisting {
		t.Errorf("el backup no tiene el contenido original: %q", got)
	}
}

// S13 details: settings.json missing with .claude present is created.
func TestHooksClaudeCreaSettingsSiFalta(t *testing.T) {
	e := hkNew(t, ".claude")
	hkInstall(t, e, hkBin)
	if !hkExists(e.path(".claude/settings.json")) {
		t.Fatal("settings.json no se creó")
	}
	if n := hkCountLodan(hkJSON(t, e.path(".claude/settings.json"))); n != 4 {
		t.Errorf("handlers de lodan = %d, quería 4", n)
	}
}

// S13 details: a client is touched only if its folder exists.
func TestHooksSoloClientesDetectados(t *testing.T) {
	e := hkNew(t)
	files := hkInstall(t, e, hkBin)
	if len(files) != 0 {
		t.Errorf("files = %v, quería ninguno", files)
	}
	for _, p := range []string{".claude", ".config/opencode", ".gemini"} {
		if hkExists(e.path(p)) {
			t.Errorf("%s no debía crearse", p)
		}
	}
}

// S13 details: idempotent, no duplicates, no changed files the second time.
func TestHooksInstalarDosVecesNoCambiaNada(t *testing.T) {
	e := hkNew(t, ".claude", ".config/opencode", ".gemini/config")
	hkWrite(t, e.path(".claude/settings.json"), hkSettingsExisting)
	hkWrite(t, e.path(".gemini/config/hooks.json"), `{"herdr":{"SessionStart":[{"command":"herdr x","type":"command"}]}}`)
	first := hkInstall(t, e, hkBin)
	if len(first) < 3 {
		t.Fatalf("primera instalación: files = %v, quería 3 clientes", first)
	}
	paths := []string{".claude/settings.json", ".config/opencode/plugins/lodan.js", ".gemini/config/hooks.json"}
	before := map[string]string{}
	for _, p := range paths {
		before[p] = hkRead(t, e.path(p))
	}
	second := hkInstall(t, e, hkBin)
	if len(second) != 0 {
		t.Errorf("segunda instalación: files = %v, quería ninguno", second)
	}
	for _, p := range paths {
		if hkRead(t, e.path(p)) != before[p] {
			t.Errorf("%s cambió en la segunda instalación", p)
		}
	}
	if n := hkCountLodan(hkJSON(t, e.path(".claude/settings.json"))); n != 4 {
		t.Errorf("handlers de lodan = %d, quería 4", n)
	}
}

// S13 details: a different bin replaces lodan's commands without duplicates.
func TestHooksOtroBinReemplaza(t *testing.T) {
	e := hkNew(t, ".claude", ".gemini/config")
	hkWrite(t, e.path(".claude/settings.json"), hkSettingsExisting)
	hkInstall(t, e, hkBin)
	files := hkInstall(t, e, hkBin2)
	if !hkHasSuffix(files, ".claude/settings.json") || !hkHasSuffix(files, ".gemini/config/hooks.json") {
		t.Errorf("files = %v, quería settings.json y hooks.json", files)
	}
	if strings.Contains(hkRead(t, e.path(".claude/settings.json")), "C:/x/lodan.exe") {
		t.Errorf("queda el bin anterior en settings.json")
	}
	s := hkJSON(t, e.path(".claude/settings.json"))
	if n := hkCountLodan(s); n != 4 {
		t.Errorf("handlers de lodan = %d, quería 4", n)
	}
	hkAssertForeignKept(t, s, hkOrig(t))
	if strings.Contains(hkRead(t, e.path(".gemini/config/hooks.json")), "C:/x/lodan.exe") {
		t.Errorf("queda el bin anterior en hooks.json")
	}
}

// S13 details: opencode plugin content, header, idempotence, other plugins untouched.
func TestHooksOpencodePlugin(t *testing.T) {
	e := hkNew(t, ".config/opencode")
	herdr := "// herdr plugin\nexport default {}\n"
	hkWrite(t, e.path(".config/opencode/plugins/herdr-agent-state.js"), herdr)

	files := hkInstall(t, e, hkBin)
	if !hkHasSuffix(files, ".config/opencode/plugins/lodan.js") {
		t.Errorf("files = %v, falta lodan.js", files)
	}
	js := hkRead(t, e.path(".config/opencode/plugins/lodan.js"))
	if !strings.HasPrefix(js, hkHeader) {
		t.Errorf("el plugin no empieza por %q", hkHeader)
	}
	for _, want := range []string{"lodan.opencode", "session.compaction.ended", "execute.after", "subagent", "session.synthetic", "--client", "opencode"} {
		if !strings.Contains(js, want) {
			t.Errorf("el plugin no contiene %q", want)
		}
	}
	if !strings.Contains(js, hkBinFwd) && !strings.Contains(js, `C:\\x\\lodan.exe`) {
		t.Errorf("el plugin no contiene la ruta del bin")
	}
	if len(hkInstall(t, e, hkBin)) != 0 {
		t.Errorf("la segunda instalación no debía cambiar archivos")
	}
	if hkRead(t, e.path(".config/opencode/plugins/lodan.js")) != js {
		t.Errorf("el plugin cambió en la segunda instalación")
	}
	if hkRead(t, e.path(".config/opencode/plugins/herdr-agent-state.js")) != herdr {
		t.Errorf("herdr-agent-state.js fue modificado")
	}
}

const hkHooksJSON = `{"herdr":{"SessionStart":[{"command":"herdr report","timeout":5,"type":"command"}],"Stop":[{"command":"herdr stop","type":"command"}]}}`

// S13 details: Antigravity gets the exact lodan group; herdr is kept; nothing else.
func TestHooksAntigravityGrupo(t *testing.T) {
	e := hkNew(t, ".gemini/config")
	hkWrite(t, e.path(".gemini/config/hooks.json"), hkHooksJSON)
	var orig map[string]any
	_ = json.Unmarshal([]byte(hkHooksJSON), &orig)

	files := hkInstall(t, e, hkBin)
	if !hkHasSuffix(files, ".gemini/config/hooks.json") {
		t.Errorf("files = %v, falta hooks.json", files)
	}
	got := hkJSON(t, e.path(".gemini/config/hooks.json"))
	wantLodan := map[string]any{"SessionStart": []any{
		map[string]any{
			"command": `"C:/x/lodan.exe" hook compact --client antigravity-client --format antigravity`,
			"timeout": float64(15),
			"type":    "command",
		},
		map[string]any{
			"command": `"C:/x/lodan.exe" hook session-start --client antigravity-client --format antigravity`,
			"timeout": float64(15),
			"type":    "command",
		},
	}}
	if !reflect.DeepEqual(got["lodan"], wantLodan) {
		t.Errorf("grupo lodan = %v, quería %v", got["lodan"], wantLodan)
	}
	if !reflect.DeepEqual(got["herdr"], orig["herdr"]) {
		t.Errorf("herdr cambiado: %v", got["herdr"])
	}
	if len(got) != 2 {
		t.Errorf("claves de primer nivel = %v, quería herdr y lodan", got)
	}
	if len(hkInstall(t, e, hkBin)) != 0 {
		t.Errorf("la segunda instalación no debía cambiar archivos")
	}
}

// S13 details: dry run reports files and writes nothing.
func TestHooksDryRunNoEscribe(t *testing.T) {
	e := hkNew(t, ".claude", ".config/opencode", ".gemini/config")
	hkWrite(t, e.path(".claude/settings.json"), hkSettingsExisting)
	hkWrite(t, e.path(".gemini/config/hooks.json"), hkHooksJSON)
	files, err := InstallHooks(e.env, hkBin, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, suf := range []string{".claude/settings.json", ".config/opencode/plugins/lodan.js", ".gemini/config/hooks.json"} {
		if !hkHasSuffix(files, suf) {
			t.Errorf("dry run: files = %v, falta %s", files, suf)
		}
	}
	if hkRead(t, e.path(".claude/settings.json")) != hkSettingsExisting {
		t.Errorf("dry run modificó settings.json")
	}
	if hkRead(t, e.path(".gemini/config/hooks.json")) != hkHooksJSON {
		t.Errorf("dry run modificó hooks.json")
	}
	if hkExists(e.path(".config/opencode/plugins/lodan.js")) || hkExists(e.path(".claude/settings.json.bak-lodan")) {
		t.Errorf("dry run escribió archivos")
	}
}

// S13 details: uninstall removes only lodan's handlers and empty groups.
func TestHooksDesinstalarClaude(t *testing.T) {
	e := hkNew(t, ".claude")
	hkWrite(t, e.path(".claude/settings.json"), hkSettingsExisting)
	hkInstall(t, e, hkBin)

	files, err := UninstallHooks(e.env, false)
	if err != nil {
		t.Fatal(err)
	}
	if !hkHasSuffix(files, ".claude/settings.json") {
		t.Errorf("files = %v, falta settings.json", files)
	}
	s := hkJSON(t, e.path(".claude/settings.json"))
	if n := hkCountLodan(s); n != 0 {
		t.Errorf("quedan %d handlers de lodan", n)
	}
	hkAssertForeignKept(t, s, hkOrig(t))
	for _, ev := range []string{"PostCompact", "PostToolUse"} {
		if gs := hkGroups(t, s, ev); len(gs) != 0 {
			t.Errorf("%s: grupos vacíos sin eliminar: %v", ev, gs)
		}
	}
	for _, ev := range []string{"SessionStart", "SubagentStop"} {
		for _, g := range hkGroups(t, s, ev) {
			if len(hkHandlers(g)) == 0 {
				t.Errorf("%s: grupo vacío sin eliminar: %v", ev, g)
			}
		}
	}
	again, err := UninstallHooks(e.env, false)
	if err != nil || len(again) != 0 {
		t.Errorf("segunda desinstalación: files = %v, err = %v", again, err)
	}
}

// S13 details: uninstall removes lodan.js (with header) and lodan's group from hooks.json.
func TestHooksDesinstalarOpencodeYAntigravity(t *testing.T) {
	e := hkNew(t, ".config/opencode", ".gemini/config")
	hkWrite(t, e.path(".gemini/config/hooks.json"), hkHooksJSON)
	var orig map[string]any
	_ = json.Unmarshal([]byte(hkHooksJSON), &orig)
	hkInstall(t, e, hkBin)

	files, err := UninstallHooks(e.env, false)
	if err != nil {
		t.Fatal(err)
	}
	if !hkHasSuffix(files, ".config/opencode/plugins/lodan.js") || !hkHasSuffix(files, ".gemini/config/hooks.json") {
		t.Errorf("files = %v", files)
	}
	if hkExists(e.path(".config/opencode/plugins/lodan.js")) {
		t.Errorf("lodan.js no se eliminó")
	}
	got := hkJSON(t, e.path(".gemini/config/hooks.json"))
	if _, ok := got["lodan"]; ok {
		t.Errorf("queda el grupo lodan")
	}
	if !reflect.DeepEqual(got["herdr"], orig["herdr"]) {
		t.Errorf("herdr cambiado: %v", got["herdr"])
	}
	again, err := UninstallHooks(e.env, false)
	if err != nil || len(again) != 0 {
		t.Errorf("segunda desinstalación: files = %v, err = %v", again, err)
	}
}

// S13 details: a user file at plugins/lodan.js without the header is kept.
func TestHooksDesinstalarConservaPluginAjeno(t *testing.T) {
	e := hkNew(t, ".config/opencode")
	own := "export default { id: 'mine' }\n"
	hkWrite(t, e.path(".config/opencode/plugins/lodan.js"), own)
	if _, err := UninstallHooks(e.env, false); err != nil {
		t.Fatal(err)
	}
	if hkRead(t, e.path(".config/opencode/plugins/lodan.js")) != own {
		t.Errorf("el archivo del usuario fue modificado o eliminado")
	}
}

// S13 details: uninstall dry run reports files and writes nothing.
func TestHooksDesinstalarDryRun(t *testing.T) {
	e := hkNew(t, ".claude", ".config/opencode", ".gemini/config")
	hkInstall(t, e, hkBin)
	before := hkRead(t, e.path(".claude/settings.json"))
	files, err := UninstallHooks(e.env, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Errorf("dry run no informó archivos")
	}
	if hkRead(t, e.path(".claude/settings.json")) != before || !hkExists(e.path(".config/opencode/plugins/lodan.js")) {
		t.Errorf("dry run modificó archivos")
	}
	if _, ok := hkJSON(t, e.path(".gemini/config/hooks.json"))["lodan"]; !ok {
		t.Errorf("dry run eliminó el grupo lodan")
	}
}

// S13 details: CheckHooks detects missing hooks and a different bin.
func TestHooksCheck(t *testing.T) {
	e := hkNew(t, ".claude", ".config/opencode", ".gemini/config")
	ok, detail := CheckHooks(e.env, hkBin)
	if ok || !strings.Contains(detail, "lodan install") {
		t.Errorf("sin instalar: ok = %v, detail = %q", ok, detail)
	}
	hkInstall(t, e, hkBin)
	if ok, detail := CheckHooks(e.env, hkBin); !ok {
		t.Errorf("tras instalar: ok = false, detail = %q", detail)
	}
	ok, detail = CheckHooks(e.env, hkBin2)
	if ok || !strings.Contains(detail, "lodan install") {
		t.Errorf("otro bin: ok = %v, detail = %q", ok, detail)
	}
	if err := os.Remove(e.path(".config/opencode/plugins/lodan.js")); err != nil {
		t.Fatal(err)
	}
	ok, detail = CheckHooks(e.env, hkBin)
	if ok || !strings.Contains(detail, "lodan install") {
		t.Errorf("sin plugin: ok = %v, detail = %q", ok, detail)
	}
}
