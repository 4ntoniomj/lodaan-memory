//go:build linux

package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// defaultUnitDir is where systemd loads administrator-provided system units.
const defaultUnitDir = "/etc/systemd/system"

// linuxManager manages a system-level systemd unit.
type linuxManager struct {
	// unitDir is the base directory of the unit files (a field so tests can
	// redirect it).
	unitDir string

	// run executes systemctl.
	run runner
}

// NewManager returns the systemd manager for this platform.
func NewManager() Manager {
	return &linuxManager{unitDir: defaultUnitDir, run: execRunner{}}
}

func (m *linuxManager) unitPath(name string) string {
	return filepath.Join(m.unitDir, name+".service")
}

func unitName(name string) string { return name + ".service" }

// systemctl runs "systemctl args..." and fails on a non-zero exit code.
func (m *linuxManager) systemctl(args ...string) error {
	return mustRun(m.run, "systemctl", args...)
}

func (m *linuxManager) Install(s Spec) error {
	if err := s.validate(); err != nil {
		return err
	}
	if err := writeFileAtomic(m.unitPath(s.Name), []byte(renderUnit(s)), 0o644); err != nil {
		return err
	}
	if err := m.systemctl("daemon-reload"); err != nil {
		return err
	}
	// No --now: whoever calls Install decides when to start the service.
	return m.systemctl("enable", unitName(s.Name))
}

func (m *linuxManager) Uninstall(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	path := m.unitPath(name)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil // nothing to remove
	}
	if err := m.systemctl("disable", "--now", unitName(name)); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no se pudo borrar %s: %w", path, permissionHint(err))
	}
	return m.systemctl("daemon-reload")
}

func (m *linuxManager) Start(name string) error   { return m.control("start", name) }
func (m *linuxManager) Stop(name string) error    { return m.control("stop", name) }
func (m *linuxManager) Restart(name string) error { return m.control("restart", name) }
func (m *linuxManager) Enable(name string) error  { return m.control("enable", name) }
func (m *linuxManager) Disable(name string) error { return m.control("disable", name) }

func (m *linuxManager) control(verb, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	return m.systemctl(verb, unitName(name))
}

// Status combines "systemctl is-enabled" and "systemctl is-active". Both
// return non-zero for the negative answers, so the exit code alone is not an
// error: the state is read from the standard output.
func (m *linuxManager) Status(name string) (Status, error) {
	if err := validateName(name); err != nil {
		return Status{}, err
	}
	unit := unitName(name)
	enabledRes, err := m.run.Run("systemctl", "is-enabled", unit)
	if err != nil {
		return Status{}, fmt.Errorf("no se pudo ejecutar «systemctl is-enabled %s»: %w", unit, err)
	}
	activeRes, err := m.run.Run("systemctl", "is-active", unit)
	if err != nil {
		return Status{}, fmt.Errorf("no se pudo ejecutar «systemctl is-active %s»: %w", unit, err)
	}
	enabledOut := strings.TrimSpace(enabledRes.Stdout)
	activeOut := strings.TrimSpace(activeRes.Stdout)

	// A unit that does not exist: is-enabled answers "not-found" or prints
	// "No such file or directory" on stderr with an empty stdout.
	if enabledOut == "not-found" || strings.Contains(enabledRes.Stderr, "No such file or directory") {
		return Status{State: StateNotInstalled, Detail: "la unidad de systemd no existe"}, nil
	}
	if enabledOut == "" && enabledRes.ExitCode != 0 {
		if _, statErr := os.Stat(m.unitPath(name)); errors.Is(statErr, os.ErrNotExist) {
			return Status{State: StateNotInstalled, Detail: "la unidad de systemd no existe"}, nil
		}
		return Status{}, fmt.Errorf("«systemctl is-enabled %s» falló (código %d): %s", unit, enabledRes.ExitCode, enabledRes.output())
	}

	st := Status{
		Enabled: enabledOut == "enabled" || enabledOut == "enabled-runtime",
		Detail:  fmt.Sprintf("systemd: is-active=%s, is-enabled=%s", activeOut, enabledOut),
	}
	switch activeOut {
	case "active", "reloading":
		st.State = StateRunning
	case "inactive", "failed", "deactivating":
		st.State = StateStopped
	default: // "activating" (including auto-restart), or an unexpected answer
		st.State = StateUnknown
	}
	return st, nil
}

// renderUnit generates the content of the systemd unit for s.
func renderUnit(s Spec) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=%s\n", systemdText(firstNonEmpty(s.Description, s.DisplayName, s.Name)))
	b.WriteString("After=network.target\n")
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")
	if s.User != "" {
		fmt.Fprintf(&b, "User=%s\n", systemdText(s.User))
	}
	execStart := []string{systemdArg(s.Exec)}
	for _, a := range s.Args {
		execStart = append(execStart, systemdArg(a))
	}
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(execStart, " "))
	for _, kv := range sortedEnv(s.Env) {
		fmt.Fprintf(&b, "Environment=%s\n", systemdEnvAssignment(kv))
	}
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=5\n")
	// mixed: SIGTERM goes only to the main process (the supervisor, which stops
	// PostgreSQL cleanly); when the timeout expires SIGKILL goes to all of them.
	b.WriteString("KillMode=mixed\n")
	b.WriteString("TimeoutStopSec=45\n")
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String()
}

// systemdText escapes free text for a single-line unit setting: it removes
// line breaks and doubles "%", which systemd treats as a specifier.
func systemdText(v string) string {
	v = strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
	return strings.ReplaceAll(v, "%", "%%")
}

// systemdArg quotes one word of an ExecStart= line following systemd.service(5):
// double quotes with C-style escapes, "%%" for a literal "%" (specifiers) and
// "$$" for a literal "$" (variable substitution). Words without special
// characters are left as they are.
func systemdArg(a string) string {
	if a != "" && !strings.ContainsAny(a, " \t\r\n\"'\\;$%") {
		return a
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range a {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '%':
			b.WriteString("%%")
		case '$':
			b.WriteString("$$")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// systemdEnvAssignment quotes a whole "K=V" assignment for Environment=. The
// specifier "%" is doubled; "$" needs no escape because Environment= values
// are not subject to variable substitution.
func systemdEnvAssignment(kv string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range kv {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '%':
			b.WriteString("%%")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
