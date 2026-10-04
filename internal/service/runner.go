//go:build linux || darwin

package service

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// cmdResult is the outcome of a command that ran to completion.
type cmdResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// output returns stdout and stderr joined, trimmed, for error messages.
func (r cmdResult) output() string {
	return strings.TrimSpace(strings.TrimSpace(r.Stdout) + "\n" + strings.TrimSpace(r.Stderr))
}

// runner executes external commands. It is an interface so the tests can
// replace systemctl and launchctl with a fake.
//
// Run returns an error only when the command could not be started; a command
// that ends with a non-zero exit code is reported through cmdResult.ExitCode.
type runner interface {
	Run(name string, args ...string) (cmdResult, error)
}

// execRunner runs real commands with os/exec.
type execRunner struct{}

func (execRunner) Run(name string, args ...string) (cmdResult, error) {
	cmd := exec.Command(name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := cmdResult{Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	return res, err
}

// commandLine formats a command for error messages.
func commandLine(name string, args []string) string {
	return strings.TrimSpace(name + " " + strings.Join(args, " "))
}

// mustRun runs a command and turns a start failure or a non-zero exit code
// into an error that includes the command output.
func mustRun(r runner, name string, args ...string) error {
	res, err := r.Run(name, args...)
	if err != nil {
		return fmt.Errorf("no se pudo ejecutar «%s»: %w", commandLine(name, args), err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("«%s» falló (código %d): %s", commandLine(name, args), res.ExitCode, res.output())
	}
	return nil
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory followed by a rename, so a reader never sees a partial file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("no se pudo crear %s: %w", dir, permissionHint(err))
	}
	tmp, err := os.CreateTemp(dir, ".lodan-*.tmp")
	if err != nil {
		return fmt.Errorf("no se pudo escribir en %s: %w", dir, permissionHint(err))
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("no se pudo escribir %s: %w", path, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("no se pudieron fijar los permisos de %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("no se pudo escribir %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("no se pudo colocar %s: %w", path, permissionHint(err))
	}
	return nil
}

// permissionHint adds a hint about administrator privileges to permission errors.
func permissionHint(err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("%w (¿faltan permisos de administrador?)", err)
	}
	return err
}
