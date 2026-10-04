package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const clBin = "/usr/local/bin/lodan"

// clFakeCLI records the commands of the official CLI instead of running them.
type clFakeCLI struct {
	calls     [][]string
	removeErr bool
}

func (f *clFakeCLI) Run(name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.removeErr && len(args) > 1 && args[1] == "remove" {
		return []byte("No MCP server found"), errors.New("exit status 1")
	}
	return nil, nil
}

// clStubCLI replaces cliLookPath and cliExec. found == "" simulates a missing
// `claude` binary.
func clStubCLI(t *testing.T, found string, r cliRunner) {
	t.Helper()
	oldLook, oldExec := cliLookPath, cliExec
	cliLookPath = func(name string) (string, error) {
		if found != "" && name == "claude" {
			return found, nil
		}
		return "", errors.New("no encontrado")
	}
	if r != nil {
		cliExec = r
	}
	t.Cleanup(func() {
		cliLookPath, cliExec = oldLook, oldExec
	})
}

// clEnv returns a Linux environment with a temporary HOME and no `claude` CLI.
func clEnv(t *testing.T) Env {
	t.Helper()
	clStubCLI(t, "", nil)
	return Env{GOOS: "linux", Home: t.TempDir()}
}

func clWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func clRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func clFind(t *testing.T, list []Client, id string) Client {
	t.Helper()
	for _, c := range list {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("cliente %q no encontrado en el registro", id)
	return Client{}
}

func clConfigure(t *testing.T, c Client, env map[string]string) bool {
	t.Helper()
	changed, err := Configure(c, clBin, env, false)
	if err != nil {
		t.Fatalf("Configure(%s): %v", c.ID, err)
	}
	return changed
}

func clUnconfigure(t *testing.T, c Client) bool {
	t.Helper()
	changed, err := Unconfigure(c, false)
	if err != nil {
		t.Fatalf("Unconfigure(%s): %v", c.ID, err)
	}
	return changed
}

func clJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(clRead(t, path)), &doc); err != nil {
		t.Fatalf("JSON inválido en %s: %v\n%s", path, err, clRead(t, path))
	}
	return doc
}

type clientPathCase struct {
	env  Env
	id   string
	want string
}

func TestClientesRutasPorSO(t *testing.T) {
	linux := Env{GOOS: "linux", Home: "/home/u"}
	darwin := Env{GOOS: "darwin", Home: "/Users/u"}
	windows := Env{GOOS: "windows", Home: `C:\Users\u`, AppData: `C:\Users\u\AppData\Roaming`, LocalAppData: `C:\Users\u\AppData\Local`}
	cases := []clientPathCase{
		{linux, "claude-code", "/home/u/.claude.json"},
		{linux, "claude-desktop", ""},
		{linux, "cursor", "/home/u/.cursor/mcp.json"},
		{linux, "codex", "/home/u/.codex/config.toml"},
		{linux, "gemini", "/home/u/.gemini/settings.json"},
		{linux, "windsurf", "/home/u/.codeium/windsurf/mcp_config.json"},
		{linux, "vscode", "/home/u/.config/Code/User/mcp.json"},
		{linux, "hermes", "/home/u/.hermes/config.yaml"},
		{linux, "opencode", "/home/u/.config/opencode/opencode.json"},
		{linux, "zed", "/home/u/.config/zed/settings.json"},
		{linux, "lmstudio", "/home/u/.lmstudio/mcp.json"},
		{darwin, "claude-desktop", "/Users/u/Library/Application Support/Claude/claude_desktop_config.json"},
		{darwin, "vscode", "/Users/u/Library/Application Support/Code/User/mcp.json"},
		{darwin, "zed", "/Users/u/.config/zed/settings.json"},
		{windows, "claude-desktop", `C:\Users\u\AppData\Roaming\Claude\claude_desktop_config.json`},
		{windows, "vscode", `C:\Users\u\AppData\Roaming\Code\User\mcp.json`},
		{windows, "zed", `C:\Users\u\AppData\Roaming\Zed\settings.json`},
		{windows, "cursor", `C:\Users\u\.cursor\mcp.json`},
		{windows, "codex", `C:\Users\u\.codex\config.toml`},
	}
	for _, tc := range cases {
		t.Run(tc.env.GOOS+"/"+tc.id, func(t *testing.T) {
			got := clFind(t, buildClients(tc.env), tc.id).File()
			if got != tc.want {
				t.Fatalf("ruta = %q, quiero %q", got, tc.want)
			}
		})
	}
}

func TestClientesDeteccion(t *testing.T) {
	env := clEnv(t)
	ids := func() []string {
		var out []string
		for _, c := range DetectClients(env) {
			out = append(out, c.ID)
		}
		return out
	}
	if got := ids(); len(got) != 0 {
		t.Fatalf("sin nada instalado no debería detectarse ningún cliente: %v", got)
	}
	for _, d := range []string{".cursor", ".codex", ".hermes", ".config/zed"} {
		if err := os.MkdirAll(filepath.Join(env.Home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := strings.Join(ids(), ",")
	if got != "cursor,codex,hermes,zed" {
		t.Fatalf("detectados = %q", got)
	}
	// Windsurf and LM Studio are only detected when their file exists.
	if err := os.MkdirAll(filepath.Join(env.Home, ".codeium", "windsurf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(ids(), ","), "windsurf") {
		t.Fatal("windsurf no debe detectarse sin su archivo")
	}
	clWrite(t, filepath.Join(env.Home, ".lmstudio", "mcp.json"), "{}")
	if !strings.Contains(strings.Join(ids(), ","), "lmstudio") {
		t.Fatal("lmstudio debe detectarse si existe su archivo")
	}
}

func TestClientesDeteccionClaudeCodePorCLI(t *testing.T) {
	env := clEnv(t)
	if len(DetectClients(env)) != 0 {
		t.Fatal("no debería detectarse nada")
	}
	clStubCLI(t, "/usr/bin/claude", &clFakeCLI{})
	got := DetectClients(env)
	if len(got) != 1 || got[0].ID != "claude-code" {
		t.Fatalf("debería detectarse claude-code por el CLI: %v", got)
	}
}

func TestClientesNoCreanSinCarpeta(t *testing.T) {
	env := clEnv(t)
	c := clFind(t, buildClients(env), "cursor")
	if _, err := Configure(c, clBin, nil, false); err == nil {
		t.Fatal("Configure debería fallar si no existe la carpeta de la app")
	}
	if pathExists(filepath.Join(env.Home, ".cursor")) {
		t.Fatal("no debe crearse la carpeta de la app")
	}
	if changed, err := Unconfigure(c, false); changed || err != nil {
		t.Fatalf("Unconfigure sin archivo: changed=%v err=%v", changed, err)
	}
}

func TestClientesJSON(t *testing.T) {
	cases := []struct{ id, goos, key string }{
		{"cursor", "linux", "mcpServers"},
		{"gemini", "linux", "mcpServers"},
		{"claude-desktop", "darwin", "mcpServers"},
		{"windsurf", "linux", "mcpServers"},
		{"lmstudio", "linux", "mcpServers"},
		{"vscode", "linux", "servers"},
		{"opencode", "linux", "mcp"},
		{"zed", "linux", "context_servers"},
		{"claude-code", "linux", "mcpServers"},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			env := clEnv(t)
			env.GOOS = tc.goos
			c := clFind(t, buildClients(env), tc.id)
			path := c.File()
			original := `{"extra": 1, "` + tc.key + `": {"otro": {"command": "x"}}}`
			clWrite(t, path, original)

			// Dry run: reports the change but does not touch anything.
			if changed, err := Configure(c, clBin, nil, true); err != nil || !changed {
				t.Fatalf("dry run: changed=%v err=%v", changed, err)
			}
			if clRead(t, path) != original || pathExists(path+".bak-lodan") {
				t.Fatal("el dry run no debe modificar nada ni crear copia")
			}

			if !clConfigure(t, c, map[string]string{"K": "V"}) {
				t.Fatal("la primera vez debe cambiar")
			}
			doc := clJSON(t, path)
			if doc["extra"] != float64(1) {
				t.Fatalf("se perdió la clave extra: %v", doc)
			}
			container, ok := doc[tc.key].(map[string]any)
			if !ok {
				t.Fatalf("falta %s: %v", tc.key, doc)
			}
			if _, ok := container["otro"]; !ok {
				t.Fatal("se perdió la entrada ajena")
			}
			if _, ok := container["lodan"]; !ok {
				t.Fatal("falta la entrada lodan")
			}
			if got := clRead(t, path+".bak-lodan"); got != original {
				t.Fatalf("la copia .bak-lodan no es el original: %q", got)
			}
			if ok, detail := Check(c, clBin); !ok {
				t.Fatalf("Check debería pasar: %s", detail)
			}
			if ok, _ := Check(c, "/otra/ruta"); ok {
				t.Fatal("Check debería fallar con otra ruta")
			}

			// Idempotent: the second time nothing changes.
			before := clRead(t, path)
			if clConfigure(t, c, map[string]string{"K": "V"}) {
				t.Fatal("la segunda vez no debe cambiar")
			}
			if clRead(t, path) != before {
				t.Fatal("la segunda vez no debe reescribir el archivo")
			}

			// A different binary updates the entry; the backup keeps the oldest original.
			if changed, err := Configure(c, "/otro/lodan", map[string]string{"K": "V"}, false); err != nil || !changed {
				t.Fatalf("cambio de binario: changed=%v err=%v", changed, err)
			}
			if clRead(t, path+".bak-lodan") != original {
				t.Fatal("la copia .bak-lodan debe conservar el original más antiguo")
			}

			if !clUnconfigure(t, c) {
				t.Fatal("Unconfigure debería cambiar")
			}
			doc = clJSON(t, path)
			container = doc[tc.key].(map[string]any)
			if _, ok := container["lodan"]; ok {
				t.Fatal("la entrada lodan debería haberse quitado")
			}
			if _, ok := container["otro"]; !ok {
				t.Fatal("Unconfigure no debe quitar entradas ajenas")
			}
			if clUnconfigure(t, c) {
				t.Fatal("el segundo Unconfigure no debe cambiar")
			}
		})
	}
}

func TestClientesJSONArchivoNuevoOVacio(t *testing.T) {
	env := clEnv(t)
	c := clFind(t, buildClients(env), "cursor")
	if err := os.MkdirAll(filepath.Join(env.Home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The file does not exist: it is created without backup.
	if !clConfigure(t, c, nil) {
		t.Fatal("debe crear el archivo")
	}
	if pathExists(c.File() + ".bak-lodan") {
		t.Fatal("no hay original que copiar")
	}
	// An empty file counts as {}.
	clWrite(t, c.File(), "")
	if !clConfigure(t, c, nil) {
		t.Fatal("debe escribir sobre el archivo vacío")
	}
	if ok, detail := Check(c, clBin); !ok {
		t.Fatal(detail)
	}
}

func TestClientesJSONOpencodeYVSCode(t *testing.T) {
	env := clEnv(t)
	oc := clFind(t, buildClients(env), "opencode")
	clWrite(t, oc.File(), "{}")
	clConfigure(t, oc, nil)
	entry := clJSON(t, oc.File())["mcp"].(map[string]any)["lodan"].(map[string]any)
	if entry["type"] != "local" || entry["enabled"] != true {
		t.Fatalf("entrada de opencode inesperada: %v", entry)
	}
	cmd, _ := entry["command"].([]any)
	if len(cmd) != 2 || cmd[0] != clBin || cmd[1] != "serve" {
		t.Fatalf("command de opencode = %v", cmd)
	}
	if _, ok := entry["environment"].(map[string]any); !ok {
		t.Fatalf("falta environment: %v", entry)
	}

	vs := clFind(t, buildClients(env), "vscode")
	clWrite(t, vs.File(), "{}")
	clConfigure(t, vs, map[string]string{"A": "1"})
	entry = clJSON(t, vs.File())["servers"].(map[string]any)["lodan"].(map[string]any)
	if entry["type"] != "stdio" || entry["command"] != clBin {
		t.Fatalf("entrada de VS Code inesperada: %v", entry)
	}
	if env, _ := entry["env"].(map[string]any); env["A"] != "1" {
		t.Fatalf("env de VS Code = %v", entry["env"])
	}
}

func TestClientesZedConComentariosNoSeToca(t *testing.T) {
	env := clEnv(t)
	c := clFind(t, buildClients(env), "zed")
	original := "// Zed settings\n{\n  // tema\n  \"theme\": \"One Dark\",\n}\n"
	clWrite(t, c.File(), original)
	changed, err := Configure(c, clBin, nil, false)
	if changed || !IsWarning(err) {
		t.Fatalf("debería ser un aviso sin cambios: changed=%v err=%v", changed, err)
	}
	if clRead(t, c.File()) != original {
		t.Fatal("no debe tocarse el archivo con comentarios")
	}
	if pathExists(c.File() + ".bak-lodan") {
		t.Fatal("no debe crearse copia si no se toca nada")
	}
}

func TestClientesJSONConservaNumerosGrandes(t *testing.T) {
	env := clEnv(t)
	c := clFind(t, buildClients(env), "claude-code")
	clWrite(t, c.File(), `{"big": 12345678901234567890, "html": "<a&b>"}`)
	clConfigure(t, c, nil)
	got := clRead(t, c.File())
	if !strings.Contains(got, "12345678901234567890") || !strings.Contains(got, "<a&b>") {
		t.Fatalf("se alteraron números o caracteres HTML:\n%s", got)
	}
}

func TestClientesClaudeCodeConCLI(t *testing.T) {
	env := clEnv(t)
	fake := &clFakeCLI{removeErr: true}
	clStubCLI(t, "/usr/bin/claude", fake)
	c := clFind(t, buildClients(env), "claude-code")
	original := `{"numStartups": 3}`
	clWrite(t, c.File(), original)

	if changed, err := Configure(c, clBin, map[string]string{"K": "V"}, true); err != nil || !changed || len(fake.calls) != 0 {
		t.Fatalf("dry run: changed=%v err=%v llamadas=%v", changed, err, fake.calls)
	}
	// The error of `remove` is ignored on purpose.
	if !clConfigure(t, c, map[string]string{"K": "V"}) {
		t.Fatal("debe cambiar")
	}
	want := [][]string{
		{"claude", "mcp", "remove", "--scope", "user", "lodan"},
		{"claude", "mcp", "add", "--scope", "user", "lodan", "-e", "K=V", "--", clBin, "serve"},
	}
	if fmt.Sprint(fake.calls) != fmt.Sprint(want) {
		t.Fatalf("llamadas = %v, quiero %v", fake.calls, want)
	}
	if clRead(t, c.File()+".bak-lodan") != original {
		t.Fatal("falta la copia .bak-lodan antes de llamar al CLI")
	}

	// The CLI writes the file; from then on the operation is idempotent.
	entry := formatClaudeCode.jsonEntry(clBin, map[string]string{"K": "V"})
	doc, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"lodan": entry}})
	clWrite(t, c.File(), string(doc))
	fake.calls = nil
	if clConfigure(t, c, map[string]string{"K": "V"}) || len(fake.calls) != 0 {
		t.Fatalf("no debe volver a llamar al CLI si ya está: %v", fake.calls)
	}
	if ok, detail := Check(c, clBin); !ok {
		t.Fatal(detail)
	}

	fake.removeErr = false
	if !clUnconfigure(t, c) {
		t.Fatal("Unconfigure debe cambiar")
	}
	if len(fake.calls) != 1 || strings.Join(fake.calls[0], " ") != "claude mcp remove --scope user lodan" {
		t.Fatalf("llamadas = %v", fake.calls)
	}
	fake.calls = nil
	clWrite(t, c.File(), `{}`)
	if clUnconfigure(t, c) || len(fake.calls) != 0 {
		t.Fatal("sin entrada no se llama al CLI")
	}
}

func TestClientesClaudeCodeSinCLIEditaElArchivo(t *testing.T) {
	env := clEnv(t)
	c := clFind(t, buildClients(env), "claude-code")
	clWrite(t, c.File(), `{"numStartups": 3}`)
	clConfigure(t, c, nil)
	entry := clJSON(t, c.File())["mcpServers"].(map[string]any)["lodan"].(map[string]any)
	if entry["type"] != "stdio" || entry["command"] != clBin {
		t.Fatalf("entrada inesperada: %v", entry)
	}
}

const clCodexOriginal = `# my config
model = "o3"

[mcp_servers.other]
command = "npx"
args = ["-y", "x"]

# comment before lodan
[mcp_servers.lodan]
command = "/old/bin"
args = ["serve"]

[mcp_servers.lodan.env]
OLD = "1"

# comment for next
[projects."/home/u"]
trust = "trusted"
`

func TestClientesCodexSustituyeTablaYSubtabla(t *testing.T) {
	env := clEnv(t)
	c := clFind(t, buildClients(env), "codex")
	clWrite(t, c.File(), clCodexOriginal)
	if !clConfigure(t, c, map[string]string{"K": "V"}) {
		t.Fatal("debe cambiar")
	}
	want := `# my config
model = "o3"

[mcp_servers.other]
command = "npx"
args = ["-y", "x"]

# comment before lodan
[mcp_servers.lodan]
command = "/usr/local/bin/lodan"
args = ["serve"]
env = { K = "V" }

# comment for next
[projects."/home/u"]
trust = "trusted"
`
	if got := clRead(t, c.File()); got != want {
		t.Fatalf("TOML inesperado:\n%s\nquiero:\n%s", got, want)
	}
	if clRead(t, c.File()+".bak-lodan") != clCodexOriginal {
		t.Fatal("falta la copia .bak-lodan")
	}
	if clConfigure(t, c, map[string]string{"K": "V"}) {
		t.Fatal("la segunda vez no debe cambiar")
	}
	if ok, detail := Check(c, clBin); !ok {
		t.Fatal(detail)
	}
	if ok, _ := Check(c, "/otra"); ok {
		t.Fatal("Check debe fallar con otra ruta")
	}
	if !clUnconfigure(t, c) {
		t.Fatal("Unconfigure debe cambiar")
	}
	got := clRead(t, c.File())
	if strings.Contains(got, "mcp_servers.lodan") || !strings.Contains(got, "[mcp_servers.other]") || !strings.Contains(got, `[projects."/home/u"]`) {
		t.Fatalf("Unconfigure dejó un TOML inesperado:\n%s", got)
	}
}

func TestClientesCodexAnadirYQuitarRestauraElOriginal(t *testing.T) {
	env := clEnv(t)
	c := clFind(t, buildClients(env), "codex")
	original := "model = \"o3\"\n\n[mcp_servers.other]\ncommand = \"npx\"\n"
	clWrite(t, c.File(), original)
	clConfigure(t, c, nil)
	want := original + "\n[mcp_servers.lodan]\ncommand = \"/usr/local/bin/lodan\"\nargs = [\"serve\"]\n"
	if got := clRead(t, c.File()); got != want {
		t.Fatalf("TOML inesperado:\n%s", got)
	}
	clUnconfigure(t, c)
	if got := clRead(t, c.File()); got != original {
		t.Fatalf("Unconfigure no restauró el original byte a byte:\n%q", got)
	}
}

func TestClientesCodexRutaWindowsYCadenasMultilinea(t *testing.T) {
	env := clEnv(t)
	c := clFind(t, buildClients(env), "codex")
	// A multi-line string that contains something that looks like a header.
	original := "notes = \"\"\"\n[mcp_servers.lodan]\n\"\"\"\n"
	clWrite(t, c.File(), original)
	bin := `C:\Program Files\lodan\lodan.exe`
	if changed, err := Configure(c, bin, nil, false); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got := clRead(t, c.File())
	if !strings.HasPrefix(got, original) {
		t.Fatalf("se alteró el contenido previo:\n%s", got)
	}
	if !strings.Contains(got, `command = "C:\\Program Files\\lodan\\lodan.exe"`) {
		t.Fatalf("la ruta de Windows no está escapada:\n%s", got)
	}
	if ok, detail := Check(c, bin); !ok {
		t.Fatal(detail)
	}
}

func TestClientesCodexDefinicionInlineEsAviso(t *testing.T) {
	env := clEnv(t)
	c := clFind(t, buildClients(env), "codex")
	original := "[mcp_servers]\nlodan = { command = \"x\" }\n"
	clWrite(t, c.File(), original)
	changed, err := Configure(c, clBin, nil, false)
	if changed || !IsWarning(err) {
		t.Fatalf("debería ser un aviso: changed=%v err=%v", changed, err)
	}
	if clRead(t, c.File()) != original {
		t.Fatal("no debe tocarse")
	}
}

func TestClientesHermesConMcpServersExistente(t *testing.T) {
	env := clEnv(t)
	c := clFind(t, buildClients(env), "hermes")
	original := `model: foo
# mcp_servers: commented
mcp_servers:
  github:
    command: "npx"
    args:
      - "-y"
  # trailing comment

agent:
  x: 1
`
	clWrite(t, c.File(), original)
	if !clConfigure(t, c, map[string]string{"K": "V"}) {
		t.Fatal("debe cambiar")
	}
	want := `model: foo
# mcp_servers: commented
mcp_servers:
  github:
    command: "npx"
    args:
      - "-y"
  lodan:
    command: "/usr/local/bin/lodan"
    args:
      - "serve"
    env:
      K: "V"
  # trailing comment

agent:
  x: 1
`
	if got := clRead(t, c.File()); got != want {
		t.Fatalf("YAML inesperado:\n%s", got)
	}
	if clRead(t, c.File()+".bak-lodan") != original {
		t.Fatal("falta la copia .bak-lodan")
	}
	if clConfigure(t, c, map[string]string{"K": "V"}) {
		t.Fatal("la segunda vez no debe cambiar")
	}
	if ok, detail := Check(c, clBin); !ok {
		t.Fatal(detail)
	}
	// A different binary replaces the block in place.
	if changed, err := Configure(c, "/otro/lodan", nil, false); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got := clRead(t, c.File())
	if strings.Count(got, "lodan:") != 1 || !strings.Contains(got, "env: {}") || !strings.Contains(got, "  github:") {
		t.Fatalf("YAML inesperado tras sustituir:\n%s", got)
	}
	if !clUnconfigure(t, c) {
		t.Fatal("Unconfigure debe cambiar")
	}
	if got := clRead(t, c.File()); got != original {
		t.Fatalf("Unconfigure no restauró el original byte a byte:\n%s", got)
	}
}

func TestClientesHermesSinMcpServersOComentado(t *testing.T) {
	env := clEnv(t)
	c := clFind(t, buildClients(env), "hermes")
	original := "model: foo\n# mcp_servers:\n"
	clWrite(t, c.File(), original)
	clConfigure(t, c, nil)
	want := original + "\nmcp_servers:\n  lodan:\n    command: \"/usr/local/bin/lodan\"\n    args:\n      - \"serve\"\n    env: {}\n"
	if got := clRead(t, c.File()); got != want {
		t.Fatalf("YAML inesperado:\n%s", got)
	}
	if ok, detail := Check(c, clBin); !ok {
		t.Fatal(detail)
	}
	clUnconfigure(t, c)
	if got := clRead(t, c.File()); got != original {
		t.Fatalf("Unconfigure no restauró el original:\n%q", got)
	}
}

func TestClientesHermesMcpServersVacio(t *testing.T) {
	env := clEnv(t)
	c := clFind(t, buildClients(env), "hermes")
	clWrite(t, c.File(), "mcp_servers:\nagent:\n  x: 1\n")
	clConfigure(t, c, nil)
	got := clRead(t, c.File())
	if !strings.HasPrefix(got, "mcp_servers:\n  lodan:\n") || !strings.HasSuffix(got, "agent:\n  x: 1\n") {
		t.Fatalf("YAML inesperado:\n%s", got)
	}
	// {} inline is also supported.
	clWrite(t, c.File(), "mcp_servers: {}\nagent:\n  x: 1\n")
	clConfigure(t, c, nil)
	if got := clRead(t, c.File()); !strings.HasPrefix(got, "mcp_servers:\n  lodan:\n") {
		t.Fatalf("YAML inesperado con {}:\n%s", got)
	}
}

func TestClientesHermesFlowEsAviso(t *testing.T) {
	env := clEnv(t)
	c := clFind(t, buildClients(env), "hermes")
	original := "mcp_servers: { a: { command: x } }\n"
	clWrite(t, c.File(), original)
	changed, err := Configure(c, clBin, nil, false)
	if changed || !IsWarning(err) {
		t.Fatalf("debería ser un aviso: changed=%v err=%v", changed, err)
	}
	if clRead(t, c.File()) != original {
		t.Fatal("no debe tocarse")
	}
}

func TestClientesHermesArchivoGrande(t *testing.T) {
	env := clEnv(t)
	c := clFind(t, buildClients(env), "hermes")
	var b strings.Builder
	for i := 0; i < 1200; i++ {
		fmt.Fprintf(&b, "key_%d: value_%d\n", i, i)
	}
	b.WriteString("mcp_servers:\n  other:\n    command: x\n")
	for i := 0; i < 1200; i++ {
		fmt.Fprintf(&b, "after_%d: value_%d\n", i, i)
	}
	original := b.String()
	clWrite(t, c.File(), original)
	if !clConfigure(t, c, nil) {
		t.Fatal("debe cambiar")
	}
	got := clRead(t, c.File())
	if strings.Count(got, "\n") != strings.Count(original, "\n")+5 {
		t.Fatalf("el bloque debería añadir 5 líneas")
	}
	if !strings.HasPrefix(got, original[:strings.Index(original, "  other:")]) || !strings.HasSuffix(got, "after_1199: value_1199\n") {
		t.Fatal("se alteró el resto del archivo")
	}
	clUnconfigure(t, c)
	if clRead(t, c.File()) != original {
		t.Fatal("Unconfigure no restauró el original")
	}
}
