package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"strings"
)

// Lifecycle hooks registered in the AI clients (lodan hook compact | subagent-stop).

const (
	hookTimeout        = 15 // seconds the client waits for a lodan hook
	hookCompactMark    = " hook compact --client "
	hookSubagentMark   = " hook subagent-stop --client "
	hookSessionMark    = " hook session-start --client "
	opencodePluginHead = "// installed by lodan"
	antigravityGroup   = "lodan"
)

// hookBin is the binary path as written in hook commands: forward slashes, so the same
// command works in cmd, PowerShell and the bash that clients use on Windows.
func hookBin(bin string) string {
	return `"` + strings.ReplaceAll(bin, `\`, "/") + `"`
}

func hookCommand(bin, action, client string, extra ...string) string {
	cmd := hookBin(bin) + " hook " + action + " --client " + client
	for _, e := range extra {
		cmd += " " + e
	}
	return cmd
}

func isLodanHookCommand(cmd string) bool {
	return strings.Contains(cmd, hookCompactMark) || strings.Contains(cmd, hookSubagentMark) ||
		strings.Contains(cmd, hookSessionMark)
}

func claudeSettingsPath(env Env) string { return homePath(env, ".claude", "settings.json") }
func opencodePluginPath(env Env) string {
	return homePath(env, ".config", "opencode", "plugins", "lodan.js")
}
func antigravityHooksPath(env Env) string { return homePath(env, ".gemini", "config", "hooks.json") }

// InstallHooks registers lodan's hooks in every detected client: Claude Code, opencode and
// Antigravity. A client is touched only if its configuration folder exists. It returns the
// files changed (or that would change, with dryRun).
func InstallHooks(env Env, bin string, dryRun bool) ([]string, error) {
	return applyHooks(env, bin, false, dryRun)
}

// UninstallHooks removes lodan's hooks and leaves every other hook untouched.
func UninstallHooks(env Env, dryRun bool) ([]string, error) {
	return applyHooks(env, "", true, dryRun)
}

func applyHooks(env Env, bin string, remove, dryRun bool) ([]string, error) {
	if env.Home == "" {
		return nil, nil
	}
	var changed []string
	var errs []error
	if dirExists(homePath(env, ".claude")) {
		path := claudeSettingsPath(env)
		ok, err := editTextFile(path, 0o600, false, dryRun, func(old string) (string, error) {
			return claudeHooksTransform(old, bin, remove)
		})
		errs = append(errs, wrapHookErr("Claude Code", path, err))
		if ok {
			changed = append(changed, path)
		}
	}
	if dirExists(homePath(env, ".config", "opencode")) {
		path := opencodePluginPath(env)
		ok, err := writeOpencodePlugin(path, bin, remove, dryRun)
		errs = append(errs, wrapHookErr("opencode", path, err))
		if ok {
			changed = append(changed, path)
		}
	}
	if dirExists(homePath(env, ".gemini", "config")) {
		path := antigravityHooksPath(env)
		ok, err := editTextFile(path, 0o600, false, dryRun, func(old string) (string, error) {
			return antigravityHooksTransform(old, bin, remove)
		})
		errs = append(errs, wrapHookErr("Antigravity", path, err))
		if ok {
			changed = append(changed, path)
		}
	}
	return changed, errors.Join(errs...)
}

func wrapHookErr(client, path string, err error) error {
	if err == nil {
		return nil
	}
	var w *WarningError
	if errors.As(err, &w) {
		return &WarningError{Msg: fmt.Sprintf("hooks de %s (%s): %s", client, path, w.Msg)}
	}
	return fmt.Errorf("hooks de %s (%s): %w", client, path, err)
}

// claudeHandler is one hook handler of Claude Code's settings.json.
func claudeHandler(cmd string) map[string]any {
	return map[string]any{"type": "command", "command": cmd, "timeout": json.Number(fmt.Sprint(hookTimeout))}
}

// claudeGroups are lodan's matcher groups, by event.
func claudeGroups(bin string) [][2]any {
	compact := hookCommand(bin, "compact", "claude-code")
	stop := hookCommand(bin, "subagent-stop", "claude-code")
	start := hookCommand(bin, "session-start", "claude-code")
	return [][2]any{
		{"SessionStart", map[string]any{"matcher": "startup|resume|clear", "hooks": []any{claudeHandler(start)}}},
		{"SessionStart", map[string]any{"matcher": "compact", "hooks": []any{claudeHandler(compact)}}},
		{"PostCompact", map[string]any{"matcher": "", "hooks": []any{claudeHandler(compact)}}},
		{"SubagentStop", map[string]any{"matcher": "", "hooks": []any{claudeHandler(stop)}}},
		{"PostToolUse", map[string]any{"matcher": "SubagentHandback", "hooks": []any{claudeHandler(stop)}}},
	}
}

var claudeHookEvents = []string{"SessionStart", "PostCompact", "SubagentStop", "PostToolUse"}

// claudeHooksTransform removes lodan's handlers from settings.json and, unless remove, adds
// them again. Every other setting and hook is kept.
func claudeHooksTransform(text, bin string, remove bool) (string, error) {
	if remove && strings.TrimSpace(text) == "" {
		return text, nil
	}
	doc, err := jsonParseObject(text)
	if err != nil {
		return "", jsonWarning(err)
	}
	original, _ := jsonParseObject(text)

	var hooks map[string]any
	switch v := doc["hooks"].(type) {
	case nil:
		hooks = map[string]any{}
	case map[string]any:
		hooks = v
	default:
		return "", &WarningError{Msg: `la clave "hooks" no es un objeto; no se ha modificado`}
	}
	for _, event := range claudeHookEvents {
		groups, ok := hooks[event].([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, g := range groups {
			group, ok := g.(map[string]any)
			if !ok {
				kept = append(kept, g)
				continue
			}
			handlers, ok := group["hooks"].([]any)
			if !ok {
				kept = append(kept, g)
				continue
			}
			var rest []any
			hadLodan := false
			for _, h := range handlers {
				if hm, ok := h.(map[string]any); ok {
					if cmd, _ := hm["command"].(string); isLodanHookCommand(cmd) {
						hadLodan = true
						continue
					}
				}
				rest = append(rest, h)
			}
			switch {
			case !hadLodan:
				kept = append(kept, g)
			case len(rest) > 0:
				group["hooks"] = rest
				kept = append(kept, group)
			}
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	if !remove {
		for _, eg := range claudeGroups(bin) {
			event := eg[0].(string)
			groups, _ := hooks[event].([]any)
			hooks[event] = append(groups, eg[1])
		}
	}
	if len(hooks) == 0 {
		delete(doc, "hooks")
	} else {
		doc["hooks"] = hooks
	}
	if reflect.DeepEqual(doc, original) {
		return text, nil
	}
	return jsonEncode(doc)
}

// antigravityGroupValue is lodan's group in Antigravity's hooks.json. Antigravity only has
// SessionStart for this: no compaction or subagent events exist there.
func antigravityGroupValue(bin string) map[string]any {
	handler := func(cmd string) map[string]any {
		return map[string]any{"command": cmd, "timeout": json.Number(fmt.Sprint(hookTimeout)), "type": "command"}
	}
	return map[string]any{
		"SessionStart": []any{
			handler(hookCommand(bin, "compact", "antigravity-client", "--format", "antigravity")),
			handler(hookCommand(bin, "session-start", "antigravity-client", "--format", "antigravity")),
		},
	}
}

func antigravityHooksTransform(text, bin string, remove bool) (string, error) {
	if remove && strings.TrimSpace(text) == "" {
		return text, nil
	}
	doc, err := jsonParseObject(text)
	if err != nil {
		return "", jsonWarning(err)
	}
	if remove {
		if _, ok := doc[antigravityGroup]; !ok {
			return text, nil
		}
		delete(doc, antigravityGroup)
		return jsonEncode(doc)
	}
	want := antigravityGroupValue(bin)
	if reflect.DeepEqual(doc[antigravityGroup], want) {
		return text, nil
	}
	doc[antigravityGroup] = want
	return jsonEncode(doc)
}

// writeOpencodePlugin writes (or removes) lodan's opencode plugin. A file at that path
// without lodan's header belongs to the user and is never touched.
func writeOpencodePlugin(path, bin string, remove, dryRun bool) (bool, error) {
	old, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	ours := exists && strings.HasPrefix(strings.TrimPrefix(string(old), "\uFEFF"), opencodePluginHead)
	if exists && !ours {
		if remove {
			return false, nil
		}
		return false, &WarningError{Msg: "el fichero existe y no lo instaló lodan; no se ha modificado"}
	}
	if remove {
		if !exists {
			return false, nil
		}
		if dryRun {
			return true, nil
		}
		return true, os.Remove(path)
	}
	content := opencodePlugin(bin)
	if exists && string(old) == content {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		return false, err
	}
	return true, writeAtomicPreserving(path, []byte(content), 0o644)
}

func dirOf(path string) string {
	i := strings.LastIndexAny(path, `/\`)
	if i < 0 {
		return "."
	}
	return path[:i]
}

// CheckHooks reports whether every detected client has lodan's hooks pointing to bin.
func CheckHooks(env Env, bin string) (ok bool, detail string) {
	var missing []string
	if dirExists(homePath(env, ".claude")) {
		data, _ := os.ReadFile(claudeSettingsPath(env))
		if updated, err := claudeHooksTransform(string(data), bin, false); err != nil || updated != string(data) {
			missing = append(missing, "Claude Code")
		}
	}
	if dirExists(homePath(env, ".config", "opencode")) {
		data, err := os.ReadFile(opencodePluginPath(env))
		if err != nil || string(data) != opencodePlugin(bin) {
			missing = append(missing, "opencode")
		}
	}
	if dirExists(homePath(env, ".gemini", "config")) {
		data, _ := os.ReadFile(antigravityHooksPath(env))
		if updated, err := antigravityHooksTransform(string(data), bin, false); err != nil || updated != string(data) {
			missing = append(missing, "Antigravity")
		}
	}
	if len(missing) > 0 {
		return false, "faltan o están desactualizados los hooks de " + strings.Join(missing, ", ") + ": ejecuta lodan install"
	}
	return true, "hooks registrados"
}

// opencodePlugin is lodan's plugin for opencode 2.x (v2 plugin API: default export with id
// and setup). It runs `lodan hook` when a compaction ends and when a subagent finishes.
func opencodePlugin(bin string) string {
	binJS, _ := json.Marshal(strings.ReplaceAll(bin, `\`, "/"))
	return strings.Replace(opencodePluginTemplate, "__LODAN_BIN__", string(binJS), 1)
}

const opencodePluginTemplate = `// installed by lodan
// managed by lodan: "lodan install" rewrites this file and "lodan uninstall" removes it.
// Runs lodan's lifecycle hooks: recovery instructions after a compaction, and the
// "## Key Learnings" of subagents saved as notes. It never blocks opencode.
import { spawn } from "node:child_process";

const LODAN = __LODAN_BIN__;
const CLIENT = "opencode";
const TIMEOUT_MS = 15000;

function runLodan(action, payload) {
  return new Promise((resolve) => {
    let out = "";
    let child;
    try {
      child = spawn(LODAN, ["hook", action, "--client", CLIENT], { windowsHide: true });
    } catch {
      resolve("");
      return;
    }
    const timer = setTimeout(() => child.kill(), TIMEOUT_MS);
    child.stdout.on("data", (d) => (out += d));
    child.on("error", () => {
      clearTimeout(timer);
      resolve("");
    });
    child.on("close", () => {
      clearTimeout(timer);
      resolve(out.trim());
    });
    child.stdin.on("error", () => {});
    child.stdin.end(JSON.stringify(payload));
  });
}

function eventData(ev) {
  return ev?.data ?? ev?.properties ?? {};
}

export default {
  id: "lodan.opencode",
  async setup(ctx) {
    const cwd = ctx.location?.directory ?? process.cwd();
    const ac = new AbortController();

    const subagentStop = (message) => {
      if (typeof message !== "string" || !message.includes("## Key Learnings")) return;
      runLodan("subagent-stop", { hook_event_name: "SubagentStop", cwd, last_assistant_message: message });
    };

    (async () => {
      for await (const ev of ctx.event.subscribe({ signal: ac.signal })) {
        const d = eventData(ev);
        if (ev?.type === "session.compaction.ended") {
          if (typeof d.text === "string" && d.text.trim() !== "") {
            await runLodan("compact", { hook_event_name: "PostCompact", cwd, compact_summary: d.text });
          }
          const text = await runLodan("compact", { hook_event_name: "SessionStart", source: "compact", cwd });
          if (text && d.sessionID) {
            try {
              await ctx.session.synthetic({ sessionID: d.sessionID, text, description: "lodan", resume: false });
            } catch {}
          }
        } else if (ev?.type === "session.created") {
          // A new session brings the other machines' changes (lodan sync pull) first.
          runLodan("session-start", { hook_event_name: "SessionStart", source: "startup", cwd });
        } else if (ev?.type === "session.synthetic") {
          // A subagent that ran in the background reports to its parent this way.
          if (typeof d.text === "string" && d.text.startsWith("<subagent ")) subagentStop(d.text);
        }
      }
    })().catch(() => {});

    const toolHook = await ctx.tool.hook("execute.after", async (e) => {
      if (e?.tool !== "subagent" || e.status !== "completed") return;
      const o = e.result?.output ?? {};
      if (o.status !== "completed") return;
      subagentStop(o.output);
    });

    return () => {
      ac.abort();
      toolHook?.dispose?.();
    };
  },
};
`
