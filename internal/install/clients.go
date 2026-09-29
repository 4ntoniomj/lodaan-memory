package install

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
)

// Env abstracts the operating system and the per-user directories so that the
// client registry can be tested with fake environments (Windows, macOS, Linux)
// from any host.
type Env struct {
	GOOS         string
	Home         string
	AppData      string
	LocalAppData string
}

// CurrentEnv returns the environment of the running process.
func CurrentEnv() Env {
	home, _ := os.UserHomeDir()
	env := Env{GOOS: runtime.GOOS, Home: home, AppData: os.Getenv("APPDATA"), LocalAppData: os.Getenv("LOCALAPPDATA")}
	if env.GOOS == "windows" && home != "" {
		if env.AppData == "" {
			env.AppData = envJoin(env, home, "AppData", "Roaming")
		}
		if env.LocalAppData == "" {
			env.LocalAppData = envJoin(env, home, "AppData", "Local")
		}
	}
	return env
}

// envJoin joins path elements with the separator of env.GOOS. It does not use
// filepath so that paths of a fake Windows environment can be built on Linux.
func envJoin(env Env, elems ...string) string {
	if len(elems) == 0 {
		return ""
	}
	sep := "/"
	if env.GOOS == "windows" {
		sep = `\`
	}
	out := elems[0]
	for _, e := range elems[1:] {
		out = strings.TrimRight(out, `/\`) + sep + strings.TrimLeft(e, `/\`)
	}
	return out
}

// homePath returns Home/elems..., or "" when the home directory is unknown.
func homePath(env Env, elems ...string) string {
	if env.Home == "" {
		return ""
	}
	return envJoin(env, append([]string{env.Home}, elems...)...)
}

// appDataPath returns AppData/elems..., or "" when %APPDATA% is unknown.
func appDataPath(env Env, elems ...string) string {
	if env.AppData == "" {
		return ""
	}
	return envJoin(env, append([]string{env.AppData}, elems...)...)
}

func pathExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

func dirExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// cliRunner runs external commands; it is an interface so that tests can
// replace it.
type cliRunner interface {
	Run(name string, args ...string) ([]byte, error)
}

type execCLIRunner struct{}

// Run executes the command and returns its combined stdout and stderr.
func (execCLIRunner) Run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// cliLookPath and cliExec are package variables so that tests can inject
// them; the API functions (Configure, Unconfigure) do not receive them.
var cliLookPath = exec.LookPath

var cliExec cliRunner = execCLIRunner{}

// cliSpec describes the official CLI of a client, used instead of editing the
// file when the binary is on the PATH.
type cliSpec struct {
	Binary string
	Add    func(bin string, env map[string]string) []string
	Remove func() []string
}

func claudeCodeCLI() *cliSpec {
	return &cliSpec{Binary: "claude", Add: claudeCodeAddArgs, Remove: claudeCodeRemoveArgs}
}

// claudeCodeAddArgs builds `claude mcp add --scope user lodan -e K=V -- <bin> serve`.
// The server name goes before -e because -e accepts several values.
func claudeCodeAddArgs(bin string, env map[string]string) []string {
	args := []string{"mcp", "add", "--scope", "user", "lodan"}
	for _, k := range sortedEnvKeys(env) {
		args = append(args, "-e", k+"="+env[k])
	}
	return append(args, "--", bin, "serve")
}

func claudeCodeRemoveArgs() []string {
	return []string{"mcp", "remove", "--scope", "user", "lodan"}
}

func sortedEnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func cliAvailable(spec *cliSpec) bool {
	_, err := cliLookPath(spec.Binary)
	return err == nil
}

// format identifies how the lodan entry is stored in a client configuration.
type format int

const (
	// formatMCPServers is JSON {"mcpServers": {"lodan": {command, args, env}}}.
	formatMCPServers format = iota
	// formatClaudeCode is like formatMCPServers plus "type": "stdio".
	formatClaudeCode
	// formatVSCode is JSON {"servers": {"lodan": {type, command, args, env}}}.
	formatVSCode
	// formatOpencode is JSON {"mcp": {"lodan": {type: "local", command: [...]}}}.
	formatOpencode
	// formatZed is JSON {"context_servers": {"lodan": {command, args, env}}}.
	formatZed
	// formatCodexTOML is the [mcp_servers.lodan] table of ~/.codex/config.toml.
	formatCodexTOML
	// formatHermesYAML is the mcp_servers: lodan: block of ~/.hermes/config.yaml.
	formatHermesYAML
)

// Client describes an MCP client: where its configuration lives, how to detect
// it and in which format the lodan entry is written. Clients returned by
// DetectClients and AllClients are bound to an Env, so Configure, Unconfigure
// and Check do not need it.
type Client struct {
	ID     string
	Name   string
	Path   func(env Env) string
	Detect func(env Env) bool
	Format format
	CLI    *cliSpec
	env    Env
}

// File returns the configuration file of the client, or "" when the client
// does not exist on the current operating system.
func (c Client) File() string {
	if c.Path == nil {
		return ""
	}
	return c.Path(c.env)
}

func newClient(id, name string, f format, path func(Env) string, detect func(Env) bool, cli *cliSpec) Client {
	return Client{ID: id, Name: name, Path: path, Detect: detect, Format: f, CLI: cli}
}

// homeDirFn returns a function resolving Home/elems....
func homeDirFn(elems ...string) func(Env) string {
	return func(env Env) string { return homePath(env, elems...) }
}

// joinIn returns a function resolving dir(env)/name.
func joinIn(dir func(Env) string, name string) func(Env) string {
	return func(env Env) string {
		d := dir(env)
		if d == "" {
			return ""
		}
		return envJoin(env, d, name)
	}
}

// existsAt returns a detector that checks that fn(env) exists (file or folder).
func existsAt(fn func(Env) string) func(Env) bool {
	return func(env Env) bool { return pathExists(fn(env)) }
}

func claudeDesktopDir(env Env) string {
	switch env.GOOS {
	case "darwin":
		return homePath(env, "Library", "Application Support", "Claude")
	case "windows":
		return appDataPath(env, "Claude")
	default:
		// Claude Desktop does not exist on Linux.
		return ""
	}
}

func vscodeUserDir(env Env) string {
	switch env.GOOS {
	case "windows":
		return appDataPath(env, "Code", "User")
	case "darwin":
		return homePath(env, "Library", "Application Support", "Code", "User")
	default:
		return homePath(env, ".config", "Code", "User")
	}
}

func zedDir(env Env) string {
	if env.GOOS == "windows" {
		return appDataPath(env, "Zed")
	}
	return homePath(env, ".config", "zed")
}

func detectClaudeCode(env Env) bool {
	if pathExists(homePath(env, ".claude.json")) || pathExists(homePath(env, ".claude")) {
		return true
	}
	_, err := cliLookPath("claude")
	return err == nil
}

// buildClients builds the registry bound to env.
func buildClients(env Env) []Client {
	windsurf := joinIn(homeDirFn(".codeium", "windsurf"), "mcp_config.json")
	lmstudio := joinIn(homeDirFn(".lmstudio"), "mcp.json")
	list := []Client{
		newClient("claude-code", "Claude Code", formatClaudeCode, homeDirFn(".claude.json"), detectClaudeCode, claudeCodeCLI()),
		newClient("claude-desktop", "Claude Desktop", formatMCPServers, joinIn(claudeDesktopDir, "claude_desktop_config.json"), existsAt(claudeDesktopDir), nil),
		newClient("cursor", "Cursor", formatMCPServers, joinIn(homeDirFn(".cursor"), "mcp.json"), existsAt(homeDirFn(".cursor")), nil),
		newClient("codex", "Codex", formatCodexTOML, joinIn(homeDirFn(".codex"), "config.toml"), existsAt(homeDirFn(".codex")), nil),
		newClient("gemini", "Gemini CLI", formatMCPServers, joinIn(homeDirFn(".gemini"), "settings.json"), existsAt(homeDirFn(".gemini")), nil),
		// Windsurf and LM Studio: only when the file already exists (paths not verified).
		newClient("windsurf", "Windsurf", formatMCPServers, windsurf, existsAt(windsurf), nil),
		newClient("vscode", "VS Code", formatVSCode, joinIn(vscodeUserDir, "mcp.json"), existsAt(vscodeUserDir), nil),
		newClient("hermes", "Hermes", formatHermesYAML, joinIn(homeDirFn(".hermes"), "config.yaml"), existsAt(homeDirFn(".hermes")), nil),
		newClient("opencode", "opencode", formatOpencode, joinIn(homeDirFn(".config", "opencode"), "opencode.json"), existsAt(homeDirFn(".config", "opencode")), nil),
		newClient("zed", "Zed", formatZed, joinIn(zedDir, "settings.json"), existsAt(zedDir), nil),
		newClient("lmstudio", "LM Studio", formatMCPServers, lmstudio, existsAt(lmstudio), nil),
	}
	for i := range list {
		list[i].env = env
	}
	return list
}

// AllClients returns the whole registry bound to the current environment.
func AllClients() []Client {
	return buildClients(CurrentEnv())
}

// DetectClients returns the clients whose configuration file or app folder
// exists in env.
func DetectClients(env Env) []Client {
	var found []Client
	for _, c := range buildClients(env) {
		if c.Path(env) == "" {
			continue
		}
		if c.Detect != nil && c.Detect(env) {
			found = append(found, c)
		}
	}
	return found
}

// Configure writes the lodan entry into the client configuration. It returns
// changed=false when the entry was already identical (nothing is rewritten).
// With dryRun it only reports whether it would change something. If the file
// cannot be parsed safely it returns a *WarningError and does not touch it.
// Before the first modification a copy <file>.bak-lodan is kept.
func Configure(c Client, bin string, entryEnv map[string]string, dryRun bool) (bool, error) {
	path := c.File()
	if path == "" {
		return false, fmt.Errorf("%s no está soportado en este sistema", c.Name)
	}
	if c.Detect != nil && !c.Detect(c.env) {
		return false, fmt.Errorf("%s no está instalado (no existe su carpeta de configuración); no se crea nada", c.Name)
	}
	if entryEnv == nil {
		entryEnv = map[string]string{}
	}
	if c.CLI != nil && cliAvailable(c.CLI) {
		return configureViaCLI(c, path, bin, entryEnv, dryRun)
	}
	return c.edit(path, bin, entryEnv, false, dryRun)
}

// Unconfigure removes the lodan entry from the client configuration.
func Unconfigure(c Client, dryRun bool) (bool, error) {
	path := c.File()
	if path == "" || !pathExists(path) {
		return false, nil
	}
	if c.CLI != nil && cliAvailable(c.CLI) {
		data, err := os.ReadFile(path)
		if err != nil {
			return false, err
		}
		_, present, err := jsonReadEntry(string(data), c.Format)
		if err != nil || !present {
			return false, nil
		}
		if dryRun {
			return true, nil
		}
		if err := backupFirstTime(path); err != nil {
			return false, err
		}
		if out, err := cliExec.Run(c.CLI.Binary, c.CLI.Remove()...); err != nil {
			return false, fmt.Errorf("%s mcp remove falló: %v: %s", c.CLI.Binary, err, strings.TrimSpace(string(out)))
		}
		return true, nil
	}
	return c.edit(path, "", nil, true, dryRun)
}

// Check verifies that the client has a lodan entry pointing to bin.
func Check(c Client, bin string) (ok bool, detail string) {
	path := c.File()
	if path == "" {
		return false, "no aplicable en este sistema"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, fmt.Sprintf("no existe %s: ejecuta lodan install", path)
		}
		return false, fmt.Sprintf("no se puede leer %s: %v", path, err)
	}
	var cmd string
	var found bool
	switch c.Format {
	case formatCodexTOML:
		cmd, found = tomlEntryCommand(string(data))
	case formatHermesYAML:
		cmd, found = yamlEntryCommand(string(data))
	default:
		entry, present, err := jsonReadEntry(string(data), c.Format)
		if err != nil {
			return false, fmt.Sprintf("no se puede leer %s como JSON: %v", path, err)
		}
		found = present
		cmd = jsonEntryCommand(c.Format, entry)
	}
	if !found {
		return false, fmt.Sprintf("sin entrada lodan en %s: ejecuta lodan install", path)
	}
	if cmd != bin {
		return false, fmt.Sprintf("la entrada de %s apunta a %q en lugar de %q: ejecuta lodan install", path, cmd, bin)
	}
	return true, path
}

// edit applies the format-specific transformation to the file.
func (c Client) edit(path, bin string, env map[string]string, remove, dryRun bool) (bool, error) {
	transform := func(old string) (string, error) {
		switch c.Format {
		case formatCodexTOML:
			return tomlTransform(old, bin, env, remove)
		case formatHermesYAML:
			return yamlTransform(old, bin, env, remove)
		default:
			return jsonTransform(old, c.Format, bin, env, remove)
		}
	}
	changed, err := editTextFile(path, 0o600, false, dryRun, transform)
	var w *WarningError
	if errors.As(err, &w) {
		return false, &WarningError{Msg: fmt.Sprintf("%s (%s): %s", c.Name, path, w.Msg)}
	}
	return changed, err
}

// configureViaCLI uses the official CLI of the client. The state is read from
// the file first so that the operation stays idempotent.
func configureViaCLI(c Client, path, bin string, env map[string]string, dryRun bool) (bool, error) {
	if data, err := os.ReadFile(path); err == nil {
		entry, present, err := jsonReadEntry(string(data), c.Format)
		if err == nil && present && jsonEntryEqual(entry, c.Format, bin, env) {
			return false, nil
		}
	}
	if dryRun {
		return true, nil
	}
	if err := backupFirstTime(path); err != nil {
		return false, err
	}
	// Removing first makes the operation idempotent; its error (the entry may
	// not exist) is ignored on purpose.
	_, _ = cliExec.Run(c.CLI.Binary, c.CLI.Remove()...)
	if out, err := cliExec.Run(c.CLI.Binary, c.CLI.Add(bin, env)...); err != nil {
		return false, fmt.Errorf("%s mcp add falló: %v: %s", c.CLI.Binary, err, strings.TrimSpace(string(out)))
	}
	return true, nil
}
