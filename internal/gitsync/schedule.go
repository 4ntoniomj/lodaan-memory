package gitsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// TaskName names the scheduled task (Windows), the systemd user units (Linux) and, as
// com.lodan.sync, the LaunchAgent (macOS) that run `lodan sync run` periodically.
const TaskName = "lodan-sync"

// Limits of the sync interval, in minutes.
const (
	minIntervalMinutes = 5
	maxIntervalMinutes = 1440
)

// CommandRunner runs an external command and returns its combined output.
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Scheduler registers the periodic sync as a task of the current user, so it runs with the
// user's git credentials (the lodan service runs as another account and cannot use them).
type Scheduler struct {
	GOOS string
	Home string
	Bin  string
	Run  CommandRunner // nil runs the real command
}

func (s Scheduler) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if s.Run != nil {
		return s.Run(ctx, name, args...)
	}
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// Schedule registers (or replaces) the task that runs `lodan sync run` every everyMinutes.
func (s Scheduler) Schedule(ctx context.Context, everyMinutes int) error {
	if everyMinutes < minIntervalMinutes || everyMinutes > maxIntervalMinutes {
		return fmt.Errorf("el intervalo debe estar entre %d y %d minutos", minIntervalMinutes, maxIntervalMinutes)
	}
	switch s.GOOS {
	case "windows":
		tr := `conhost.exe --headless "` + s.Bin + `" sync run`
		_, err := s.run(ctx, "schtasks", "/create", "/tn", TaskName, "/tr", tr, "/sc", "minute",
			"/mo", strconv.Itoa(everyMinutes), "/it", "/rl", "LIMITED", "/f")
		return err
	case "darwin":
		plist := s.launchAgentPath()
		if err := writeFile(plist, launchAgent(s.Bin, everyMinutes*60)); err != nil {
			return err
		}
		_, _ = s.run(ctx, "launchctl", "unload", plist) // not loaded yet: nothing to unload
		_, err := s.run(ctx, "launchctl", "load", "-w", plist)
		return err
	default:
		dir := s.systemdDir()
		if err := writeFile(filepath.Join(dir, TaskName+".service"), systemdService(s.Bin)); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(dir, TaskName+".timer"), systemdTimer(everyMinutes)); err != nil {
			return err
		}
		if _, err := s.run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		_, err := s.run(ctx, "systemctl", "--user", "enable", "--now", TaskName+".timer")
		return err
	}
}

// Unschedule removes the task; it is not an error if there is none.
func (s Scheduler) Unschedule(ctx context.Context) error {
	switch s.GOOS {
	case "windows":
		if _, err := s.run(ctx, "schtasks", "/query", "/tn", TaskName); err != nil {
			return nil // no such task
		}
		_, err := s.run(ctx, "schtasks", "/delete", "/tn", TaskName, "/f")
		return err
	case "darwin":
		plist := s.launchAgentPath()
		if !fileExists(plist) {
			return nil
		}
		_, _ = s.run(ctx, "launchctl", "unload", plist)
		return removeFile(plist)
	default:
		dir := s.systemdDir()
		service, timer := filepath.Join(dir, TaskName+".service"), filepath.Join(dir, TaskName+".timer")
		if !fileExists(service) && !fileExists(timer) {
			return nil
		}
		_, _ = s.run(ctx, "systemctl", "--user", "disable", "--now", TaskName+".timer")
		if err := removeFile(timer); err != nil {
			return err
		}
		if err := removeFile(service); err != nil {
			return err
		}
		_, err := s.run(ctx, "systemctl", "--user", "daemon-reload")
		return err
	}
}

func (s Scheduler) systemdDir() string {
	return filepath.Join(s.Home, ".config", "systemd", "user")
}

func (s Scheduler) launchAgentPath() string {
	return filepath.Join(s.Home, "Library", "LaunchAgents", "com.lodan.sync.plist")
}

func systemdService(bin string) string {
	return "[Unit]\nDescription=lodan: sincroniza la memoria con el repositorio git\n\n" +
		"[Service]\nType=oneshot\nExecStart=\"" + bin + "\" sync run\n"
}

func systemdTimer(every int) string {
	return "[Unit]\nDescription=lodan: sincronización periódica\n\n" +
		"[Timer]\nOnBootSec=2min\nOnUnitActiveSec=" + strconv.Itoa(every) + "min\n\n" +
		"[Install]\nWantedBy=timers.target\n"
}

func launchAgent(bin string, seconds int) string {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.lodan.sync</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + esc(bin) + `</string>
    <string>sync</string>
    <string>run</string>
  </array>
  <key>StartInterval</key>
  <integer>` + strconv.Itoa(seconds) + `</integer>
</dict>
</plist>
`
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("no se pudo crear %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("no se pudo escribir %s: %w", path, err)
	}
	return nil
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no se pudo borrar %s: %w", path, err)
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
