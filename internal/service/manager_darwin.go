//go:build darwin

package service

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// defaultPlistDir is where launchd loads system-wide daemons from.
	defaultPlistDir = "/Library/LaunchDaemons"

	// labelPrefix namespaces the launchd labels of every lodan service.
	labelPrefix = "com.lodan."

	// domain is the launchd domain of system daemons.
	domain = "system"

	// exitTimeOut is the seconds launchd waits between SIGTERM and SIGKILL.
	exitTimeOut = 45
)

// darwinManager manages a LaunchDaemon in the system domain.
//
// Stop and Restart deliberately do not use "launchctl kill" or
// "launchctl kickstart -k": with KeepAlive set, launchd relaunches a job that
// was killed, and kickstart -k restarts it abruptly (the documentation does not
// say which signal it sends, and PostgreSQL needs a clean SIGTERM shutdown).
// Instead Stop unloads the job with "launchctl bootout", which stops it
// (SIGTERM, then SIGKILL after ExitTimeOut) and removes it from launchd so
// KeepAlive cannot relaunch it; Start loads it again with "launchctl
// bootstrap" (RunAtLoad starts it) or, if it is already loaded, kickstarts it.
// The plist stays in /Library/LaunchDaemons, so a stopped service still starts
// at the next boot unless it was disabled, like "systemctl stop".
type darwinManager struct {
	// plistDir is the base directory of the plist files (a field so tests can
	// redirect it).
	plistDir string

	// run executes launchctl.
	run runner

	// pollEvery is the interval between checks while waiting for a job to unload.
	pollEvery time.Duration

	// stopTimeout is the maximum wait for a job to unload.
	stopTimeout time.Duration
}

// NewManager returns the launchd manager for this platform.
func NewManager() Manager {
	return &darwinManager{
		plistDir:    defaultPlistDir,
		run:         execRunner{},
		pollEvery:   500 * time.Millisecond,
		stopTimeout: (exitTimeOut + 5) * time.Second,
	}
}

func label(name string) string { return labelPrefix + name }

// target is the launchctl service-target of the job: "system/<label>".
func target(name string) string { return domain + "/" + label(name) }

func (m *darwinManager) plistPath(name string) string {
	return filepath.Join(m.plistDir, label(name)+".plist")
}

func (m *darwinManager) launchctl(args ...string) error {
	return mustRun(m.run, "launchctl", args...)
}

// print queries launchd for the job. loaded is false when launchd does not know
// the job; any other failure is returned as an error.
func (m *darwinManager) print(name string) (loaded bool, out string, err error) {
	args := []string{"print", target(name)}
	res, err := m.run.Run("launchctl", args...)
	if err != nil {
		return false, "", fmt.Errorf("no se pudo ejecutar «%s»: %w", commandLine("launchctl", args), err)
	}
	if res.ExitCode == 0 {
		return true, res.Stdout, nil
	}
	// launchd answers 113 / "Could not find service" for an unknown job.
	if res.ExitCode == 113 || strings.Contains(res.output(), "Could not find service") {
		return false, "", nil
	}
	return false, "", fmt.Errorf("«%s» falló (código %d): %s", commandLine("launchctl", args), res.ExitCode, res.output())
}

func (m *darwinManager) Install(s Spec) error {
	if err := s.validate(); err != nil {
		return err
	}
	if s.LogDir != "" {
		if err := os.MkdirAll(s.LogDir, 0o755); err != nil {
			return fmt.Errorf("no se pudo crear el directorio de registros %s: %w", s.LogDir, permissionHint(err))
		}
	}
	// A job that is already loaded (reinstall) must be unloaded first: bootstrap
	// fails for a job launchd already knows.
	if loaded, _, err := m.print(s.Name); err != nil {
		return err
	} else if loaded {
		if err := m.unload(s.Name); err != nil {
			return err
		}
	}
	path := m.plistPath(s.Name)
	if err := writeFileAtomic(path, []byte(renderPlist(s)), 0o644); err != nil {
		return err
	}
	// bootstrap refuses a disabled job, so clear a possible "disabled" override
	// left by a previous install: installing means enabling, as with systemd.
	if err := m.launchctl("enable", target(s.Name)); err != nil {
		return err
	}
	// RunAtLoad makes launchd start the job as soon as it is bootstrapped.
	return m.launchctl("bootstrap", domain, path)
}

func (m *darwinManager) Uninstall(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := m.unload(name); err != nil {
		return err
	}
	path := m.plistPath(name)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no se pudo borrar %s: %w", path, permissionHint(err))
	}
	return nil
}

func (m *darwinManager) Start(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	path := m.plistPath(name)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("el servicio %q no está instalado (falta %s)", name, path)
	}
	loaded, _, err := m.print(name)
	if err != nil {
		return err
	}
	if loaded {
		return m.launchctl("kickstart", target(name))
	}
	if err := m.launchctl("bootstrap", domain, path); err != nil {
		return fmt.Errorf("%w (si el servicio está deshabilitado, habilítelo antes con «lodan service enable»)", err)
	}
	return nil
}

func (m *darwinManager) Stop(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	return m.unload(name)
}

func (m *darwinManager) Restart(name string) error {
	if err := m.Stop(name); err != nil {
		return err
	}
	return m.Start(name)
}

func (m *darwinManager) Enable(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	return m.launchctl("enable", target(name))
}

func (m *darwinManager) Disable(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	return m.launchctl("disable", target(name))
}

// unload stops the job and removes it from launchd. It does nothing if the job
// is not loaded, and waits until launchd no longer knows it.
func (m *darwinManager) unload(name string) error {
	loaded, _, err := m.print(name)
	if err != nil {
		return err
	}
	if !loaded {
		return nil
	}
	if err := m.launchctl("bootout", target(name)); err != nil {
		return err
	}
	deadline := time.Now().Add(m.stopTimeout)
	for {
		// Here any failure of print means the job is gone.
		res, err := m.run.Run("launchctl", "print", target(name))
		if err == nil && res.ExitCode != 0 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("no se pudo comprobar la parada de %s: %w", label(name), err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("el servicio %s no se detuvo en %s", label(name), m.stopTimeout)
		}
		time.Sleep(m.pollEvery)
	}
}

var stateLineRe = regexp.MustCompile(`(?m)^\s*state = (.+?)\s*$`)

// Status uses "launchctl print" (loaded or not, "state = running") and
// "launchctl print-disabled" (the persistent enable/disable override).
func (m *darwinManager) Status(name string) (Status, error) {
	if err := validateName(name); err != nil {
		return Status{}, err
	}
	loaded, out, err := m.print(name)
	if err != nil {
		return Status{}, err
	}
	st := Status{Enabled: m.isEnabled(name)}
	if !loaded {
		if _, statErr := os.Stat(m.plistPath(name)); errors.Is(statErr, os.ErrNotExist) {
			st.State = StateNotInstalled
			st.Detail = "no existe el plist de launchd"
			return st, nil
		}
		st.State = StateStopped
		st.Detail = "launchd: el servicio no está cargado"
		return st, nil
	}
	state := ""
	if match := stateLineRe.FindStringSubmatch(out); match != nil {
		state = match[1]
	}
	st.Detail = "launchd: state = " + state
	switch state {
	case "running":
		st.State = StateRunning
	case "":
		st.State = StateUnknown
	default: // "not running", "waiting", ...
		st.State = StateStopped
	}
	return st, nil
}

// isEnabled reads the disabled overrides of the system domain. A job that is
// not listed is enabled (launchd's default). If the query fails it assumes enabled.
func (m *darwinManager) isEnabled(name string) bool {
	res, err := m.run.Run("launchctl", "print-disabled", domain)
	if err != nil || res.ExitCode != 0 {
		return true
	}
	// Lines look like:  "com.lodan.lodan" => disabled   (older systems: => true)
	re := regexp.MustCompile(`"` + regexp.QuoteMeta(label(name)) + `"\s*=>\s*(\w+)`)
	match := re.FindStringSubmatch(res.Stdout)
	if match == nil {
		return true
	}
	return match[1] != "disabled" && match[1] != "true"
}

// renderPlist generates the LaunchDaemon property list for s.
func renderPlist(s Spec) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString("<dict>\n")

	plistKeyString(&b, "Label", label(s.Name))

	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range append([]string{s.Exec}, s.Args...) {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", xmlEscape(a))
	}
	b.WriteString("\t</array>\n")

	if s.User != "" {
		plistKeyString(&b, "UserName", s.User)
	}
	if len(s.Env) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, kv := range sortedEnv(s.Env) {
			k, v, _ := strings.Cut(kv, "=")
			fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", xmlEscape(k), xmlEscape(v))
		}
		b.WriteString("\t</dict>\n")
	}

	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	// Restart only after an unsuccessful exit, the equivalent of systemd's
	// Restart=on-failure; a clean shutdown must not bring the job back.
	b.WriteString("\t<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")

	if s.LogDir != "" {
		plistKeyString(&b, "StandardOutPath", filepath.Join(s.LogDir, s.Name+".out.log"))
		plistKeyString(&b, "StandardErrorPath", filepath.Join(s.LogDir, s.Name+".err.log"))
	}
	fmt.Fprintf(&b, "\t<key>ExitTimeOut</key>\n\t<integer>%d</integer>\n", exitTimeOut)

	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func plistKeyString(b *strings.Builder, key, value string) {
	fmt.Fprintf(b, "\t<key>%s</key>\n\t<string>%s</string>\n", xmlEscape(key), xmlEscape(value))
}

// xmlEscape escapes text for an XML element (&, <, >, quotes and control
// whitespace).
func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s)) // strings.Builder never returns a write error
	return b.String()
}
