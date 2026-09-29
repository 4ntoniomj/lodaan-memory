package install

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"lodan/internal/service"
)

// OllamaServiceName is the name of the system service that runs the Ollama
// installed by lodan.
const OllamaServiceName = "lodan-ollama"

// StableBinary returns the path where install keeps its own copy of the
// executable: <dataDir>/bin/lodan[.exe]. Services and clients always point here.
func StableBinary(dataDir, goos string) string {
	return filepath.Join(dataDir, "bin", "lodan"+exeSuffix(goos))
}

// ServiceParams holds what `lodan service install` needs to build the specs of
// the system services. Install passes it as command line arguments to the
// elevated process (see ServiceInstallArgs) and the command turns it back into
// a value (see cmd/lodan).
type ServiceParams struct {
	// User is the account the services run as. Empty on Windows (LocalSystem).
	User string
	// Home is the home directory of User; empty means "do not set HOME".
	Home string
	// DataDir is the lodan data directory.
	DataDir string
	// OllamaExe and OllamaDir describe the Ollama installed by lodan; both empty
	// when no Ollama service has to be registered.
	OllamaExe string
	OllamaDir string
}

// ServiceSpecs builds the specs of the services to register, in the order in
// which they should be installed and started: lodan-ollama first (when there is
// one) and lodan last, because the supervisor warms up the model through Ollama.
func ServiceSpecs(p ServiceParams, goos string) []service.Spec {
	logDir := filepath.Join(p.DataDir, "logs")
	var specs []service.Spec

	if p.OllamaExe != "" {
		env := OllamaEnv(p.OllamaDir)
		if p.Home != "" {
			env["HOME"] = p.Home
		}
		specs = append(specs, service.Spec{
			Name:        OllamaServiceName,
			DisplayName: "lodan Ollama",
			Description: "Ollama local de lodan (embeddings), solo en 127.0.0.1",
			Exec:        p.OllamaExe,
			Args:        []string{"serve"},
			User:        p.User,
			Env:         env,
			LogDir:      logDir,
		})
	}

	env := map[string]string{"LODAN_DATA_DIR": p.DataDir}
	if p.Home != "" {
		env["HOME"] = p.Home
	}
	specs = append(specs, service.Spec{
		Name:        service.DefaultName,
		DisplayName: "lodan",
		Description: "lodan: memoria persistente para IA (PostgreSQL y tareas de fondo)",
		Exec:        StableBinary(p.DataDir, goos),
		Args:        []string{"service", "run"},
		User:        p.User,
		Env:         env,
		LogDir:      logDir,
	})
	return specs
}

// ServiceInstallArgs returns the arguments of `lodan service install` for p.
func ServiceInstallArgs(p ServiceParams) []string {
	args := []string{"service", "install"}
	if p.User != "" {
		args = append(args, "--user", p.User)
	}
	if p.Home != "" {
		args = append(args, "--home", p.Home)
	}
	args = append(args, "--data-dir", p.DataDir)
	if p.OllamaExe != "" {
		args = append(args, "--ollama-exe", p.OllamaExe, "--ollama-dir", p.OllamaDir)
	}
	return args
}

// ServiceUninstallArgs returns the arguments of `lodan service uninstall`.
func ServiceUninstallArgs(withOllama bool) []string {
	args := []string{"service", "uninstall"}
	if withOllama {
		args = append(args, "--ollama")
	}
	return args
}

// elevationCommand returns the command that runs exe with args with
// administrator privileges:
//   - already root (Unix): the command itself;
//   - Unix: sudo <exe> <args...>;
//   - Windows: powershell Start-Process ... -Verb RunAs -Wait (UAC prompt).
func elevationCommand(goos string, isRoot bool, exe string, args []string) (name string, cmdArgs []string) {
	if goos == "windows" {
		script := "Start-Process -FilePath " + powershellQuote(exe)
		if len(args) > 0 {
			quoted := make([]string, len(args))
			for i, a := range args {
				quoted[i] = windowsQuoteArg(a)
			}
			script += " -ArgumentList " + powershellQuote(strings.Join(quoted, " "))
		}
		script += " -Verb RunAs -Wait"
		return "powershell", []string{"-NoProfile", "-Command", script}
	}
	if isRoot {
		return exe, args
	}
	return "sudo", append([]string{exe}, args...)
}

// powershellQuote wraps s in a PowerShell single-quoted string: a single quote
// inside is written twice.
func powershellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// windowsQuoteArg quotes one argument of a Windows command line following the
// rules of CommandLineToArgvW (the same ones as syscall.EscapeArg, which only
// exists on Windows).
func windowsQuoteArg(s string) string {
	if s == "" {
		return `""`
	}
	needsBackslash := false
	hasSpace := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"', '\\':
			needsBackslash = true
		case ' ', '\t':
			hasSpace = true
		}
	}
	if !needsBackslash && !hasSpace {
		return s
	}
	if !needsBackslash {
		return `"` + s + `"`
	}

	var b strings.Builder
	if hasSpace {
		b.WriteByte('"')
	}
	slashes := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			slashes++
		case '"':
			// Backslashes before a quote are doubled, and the quote is escaped.
			b.WriteString(strings.Repeat(`\`, slashes))
			b.WriteByte('\\')
			slashes = 0
		default:
			slashes = 0
		}
		b.WriteByte(c)
	}
	if hasSpace {
		b.WriteString(strings.Repeat(`\`, slashes))
		b.WriteByte('"')
	}
	return b.String()
}

// DefaultElevate returns the function that runs exe with args elevated on goos,
// inheriting stdin, stdout and stderr so that sudo can ask for the password.
func DefaultElevate(goos string) func(ctx context.Context, exe string, args []string) error {
	return func(ctx context.Context, exe string, args []string) error {
		isRoot := goos != "windows" && os.Geteuid() == 0
		name, cmdArgs := elevationCommand(goos, isRoot, exe, args)
		if name == "sudo" {
			if _, err := exec.LookPath("sudo"); err != nil {
				return fmt.Errorf("no se encontró sudo: ejecuta como administrador «%s»", strings.TrimSpace(exe+" "+strings.Join(args, " ")))
			}
		}
		cmd := exec.CommandContext(ctx, name, cmdArgs...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			if goos == "windows" {
				return fmt.Errorf("la ejecución como administrador falló o se canceló en la ventana de UAC: %w", err)
			}
			return fmt.Errorf("la ejecución con permisos de administrador falló: %w", err)
		}
		return nil
	}
}
