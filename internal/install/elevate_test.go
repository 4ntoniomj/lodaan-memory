package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"lodan/internal/service"
)

// argAfter returns the argument that follows flag in args, or "".
func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func hasArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func testServiceParams() ServiceParams {
	sep := string(filepath.Separator)
	dataDir := filepath.Join(sep, "datos", "lodan")
	return ServiceParams{
		DataDir:   dataDir,
		OllamaExe: filepath.Join(dataDir, "runtime", "ollama", "ollama.exe"),
		OllamaDir: filepath.Join(dataDir, "runtime", "ollama"),
	}
}

func TestServiceSpecsWindowsEnvuelveOllama(t *testing.T) {
	p := testServiceParams()
	specs := ServiceSpecs(p, "windows")
	if len(specs) != 2 {
		t.Fatalf("se esperaban 2 servicios, hay %d", len(specs))
	}

	ol := specs[0]
	if ol.Name != OllamaServiceName {
		t.Fatalf("el primero debe ser %s y es %s", OllamaServiceName, ol.Name)
	}
	if want := StableBinary(p.DataDir, "windows"); ol.Exec != want {
		t.Errorf("Exec = %q, se esperaba el binario estable %q (ollama.exe no habla con el SCM)", ol.Exec, want)
	}
	wantArgs := []string{"service", "ollama", "--ollama-exe", p.OllamaExe, "--log", filepath.Join(p.DataDir, "logs", OllamaLogName)}
	if !reflect.DeepEqual(ol.Args, wantArgs) {
		t.Errorf("Args = %v, se esperaba %v", ol.Args, wantArgs)
	}
	// The environment keeps travelling in Spec.Env: the Windows manager turns it into "--env K=V".
	if ol.Env["OLLAMA_HOST"] != "127.0.0.1:11434" || ol.Env["OLLAMA_MODELS"] != filepath.Join(p.OllamaDir, "models") {
		t.Errorf("Env = %v, faltan OLLAMA_HOST y OLLAMA_MODELS", ol.Env)
	}

	lo := specs[1]
	if lo.Name != service.DefaultName || lo.Exec != StableBinary(p.DataDir, "windows") ||
		!reflect.DeepEqual(lo.Args, []string{"service", "run"}) {
		t.Errorf("el servicio lodan no debe cambiar: %+v", lo)
	}
}

func TestServiceSpecsUnixEjecutaOllamaDirectamente(t *testing.T) {
	p := testServiceParams()
	for _, goos := range []string{"linux", "darwin"} {
		specs := ServiceSpecs(p, goos)
		if len(specs) != 2 {
			t.Fatalf("%s: se esperaban 2 servicios, hay %d", goos, len(specs))
		}
		ol := specs[0]
		if ol.Exec != p.OllamaExe || !reflect.DeepEqual(ol.Args, []string{"serve"}) {
			t.Errorf("%s: ollama debe ejecutarse directamente con «serve»: Exec=%q Args=%v", goos, ol.Exec, ol.Args)
		}
	}
}

func TestServiceSpecsSinOllama(t *testing.T) {
	p := testServiceParams()
	p.OllamaExe, p.OllamaDir = "", ""
	for _, goos := range []string{"windows", "linux", "darwin"} {
		specs := ServiceSpecs(p, goos)
		if len(specs) != 1 || specs[0].Name != service.DefaultName {
			t.Errorf("%s: sin Ollama solo se registra lodan: %+v", goos, specs)
		}
	}
}

func TestServiceArgsConLog(t *testing.T) {
	p := testServiceParams()
	p.User = "tester"
	if hasArg(ServiceInstallArgs(p), "--log") {
		t.Error("sin LogFile no debe haber --log")
	}
	p.LogFile = filepath.Join(p.DataDir, "logs", "service-elevated.log")
	args := ServiceInstallArgs(p)
	if got := argAfter(args, "--log"); got != p.LogFile {
		t.Errorf("--log = %q, se esperaba %q (args: %v)", got, p.LogFile, args)
	}
	if got := argAfter(args, "--ollama-exe"); got != p.OllamaExe {
		t.Errorf("--ollama-exe = %q, se esperaba %q", got, p.OllamaExe)
	}

	if got := ServiceUninstallArgs(true, ""); !reflect.DeepEqual(got, []string{"service", "uninstall", "--ollama"}) {
		t.Errorf("ServiceUninstallArgs(true, \"\") = %v", got)
	}
	got := ServiceUninstallArgs(true, "/x/log.txt")
	if want := []string{"service", "uninstall", "--ollama", "--log", "/x/log.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ServiceUninstallArgs con log = %v, se esperaba %v", got, want)
	}
}

func TestTailLines(t *testing.T) {
	dir := t.TempDir()

	var b strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&b, "linea %d\r\n\r\n", i) // CRLF and blank lines in between
	}
	path := filepath.Join(dir, "log.txt")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got := tailLines(path, 20)
	lines := strings.Split(got, "\n")
	if len(lines) != 20 || lines[0] != "linea 11" || lines[19] != "linea 30" {
		t.Errorf("tailLines debe devolver las últimas 20 líneas no vacías: %q", got)
	}
	if strings.Contains(got, "\r") {
		t.Errorf("no deben quedar retornos de carro: %q", got)
	}

	if got := tailLines(filepath.Join(dir, "no-existe"), 20); got != "" {
		t.Errorf("un archivo inexistente da cadena vacía, y da %q", got)
	}
	empty := filepath.Join(dir, "vacio.txt")
	if err := os.WriteFile(empty, []byte("\n  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := tailLines(empty, 20); got != "" {
		t.Errorf("un archivo sin texto da cadena vacía, y da %q", got)
	}

	// A file larger than the read window: only complete lines come back.
	big := filepath.Join(dir, "grande.txt")
	line := strings.Repeat("a", 99)
	if err := os.WriteFile(big, []byte(strings.Repeat(line+"\n", 2000)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(tailLines(big, 3), "\n") {
		if l != line {
			t.Fatalf("tailLines de un archivo grande devolvió una línea cortada: %q", l)
		}
	}
}

func TestWithElevatedLog(t *testing.T) {
	orig := errors.New("falló el paso")
	dir := t.TempDir()

	if got := withElevatedLog(orig, ""); got != orig {
		t.Errorf("sin ruta el error no cambia: %v", got)
	}
	if got := withElevatedLog(orig, filepath.Join(dir, "no-existe.log")); got != orig {
		t.Errorf("con un log inexistente el error no cambia: %v", got)
	}

	path := filepath.Join(dir, "service-elevated.log")
	if err := os.WriteFile(path, []byte("lodan service install: lodan-ollama: error 1053\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := withElevatedLog(orig, path)
	if !errors.Is(got, orig) {
		t.Errorf("el error original debe seguir envuelto: %v", got)
	}
	for _, want := range []string{"falló el paso", "error 1053", path} {
		if !strings.Contains(got.Error(), want) {
			t.Errorf("falta %q en %q", want, got.Error())
		}
	}
}

func TestElevatedLogFileSoloEnWindows(t *testing.T) {
	h := newHarness(t, "")
	if got := h.in.elevatedLogFile(); got != "" {
		t.Errorf("fuera de Windows no se usa log del paso elevado, y es %q", got)
	}
	h.in.Env.GOOS = "windows"
	want := filepath.Join(h.dataDir, "logs", "service-elevated.log")
	if got := h.in.elevatedLogFile(); got != want {
		t.Errorf("elevatedLogFile = %q, se esperaba %q", got, want)
	}
}

// windowsStepHarness returns a harness whose Installer believes it runs on Windows, ready to
// call a step directly. The returned path is the log of the elevated step.
func windowsStepHarness(t *testing.T) (*harness, string) {
	t.Helper()
	h := newHarness(t, "")
	h.in.Env.GOOS = "windows"
	h.in.reset(Options{Yes: true})
	logPath := filepath.Join(h.in.Cfg.LogsDir(), "service-elevated.log")
	if err := os.MkdirAll(h.in.Cfg.LogsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	return h, logPath
}

func TestInstallWindowsPasaElLogYLoIncluyeSiElServicioNoAparece(t *testing.T) {
	h, logPath := windowsStepHarness(t)
	if err := os.WriteFile(logPath, []byte("resto de una ejecución anterior\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotArgs []string
	staleAtCall := false
	h.in.Elevate = func(ctx context.Context, exe string, args []string) error {
		gotArgs = args
		_, err := os.Stat(logPath)
		staleAtCall = err == nil
		return os.WriteFile(logPath, []byte("lodan service install: lodan-ollama: arrancar: error 1053\n"), 0o600)
	}

	err := h.in.stepServices(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no aparece registrado") {
		t.Fatalf("se esperaba el error de servicio no registrado: %v", err)
	}
	if !strings.Contains(err.Error(), "error 1053") {
		t.Errorf("el error debe incluir la salida del paso elevado: %v", err)
	}
	if strings.Contains(err.Error(), "anterior") {
		t.Errorf("el log de una ejecución anterior debe borrarse antes de elevar: %v", err)
	}
	if staleAtCall {
		t.Error("el log debe borrarse antes de elevar")
	}
	if got := argAfter(gotArgs, "--log"); got != logPath {
		t.Errorf("--log = %q, se esperaba %q (args: %v)", got, logPath, gotArgs)
	}
}

func TestInstallWindowsIncluyeElLogSiFallaLaElevacion(t *testing.T) {
	h, logPath := windowsStepHarness(t)
	h.in.Elevate = func(ctx context.Context, exe string, args []string) error {
		_ = os.WriteFile(logPath, []byte("lodan service install: acceso denegado\n"), 0o600)
		return errors.New("la ejecución como administrador falló")
	}
	err := h.in.stepServices(context.Background())
	if err == nil || !strings.Contains(err.Error(), "falló") || !strings.Contains(err.Error(), "acceso denegado") {
		t.Fatalf("el error debe llevar la causa y el log: %v", err)
	}
}

func TestInstallWindowsAvisaConElLogSiElServicioNoArranca(t *testing.T) {
	h, logPath := windowsStepHarness(t)
	h.in.Elevate = func(ctx context.Context, exe string, args []string) error {
		h.mgr.status[service.DefaultName] = service.Status{State: service.StateStopped, Enabled: true}
		return os.WriteFile(logPath, []byte("lodan service install: arrancar lodan: se detuvo durante el arranque\n"), 0o600)
	}
	if err := h.in.stepServices(context.Background()); err != nil {
		t.Fatalf("un servicio parado es un aviso, no un error: %v", err)
	}
	out := h.output()
	if !strings.Contains(out, "su estado es «stopped»") || !strings.Contains(out, "se detuvo durante el arranque") {
		t.Errorf("el aviso debe incluir la salida del paso elevado:\n%s", out)
	}
}

func TestInstallEnUnixNoPasaLog(t *testing.T) {
	h := newHarness(t, "")
	if err := h.install(Options{Yes: true}); err != nil {
		t.Fatalf("Install: %v\n%s", err, h.output())
	}
	if len(h.el.calls) != 1 {
		t.Fatalf("debe haber un único paso elevado: %v", h.el.calls)
	}
	if hasArg(h.el.calls[0], "--log") {
		t.Errorf("fuera de Windows no se pasa --log: %v", h.el.calls[0])
	}
	if pathExists(filepath.Join(h.in.Cfg.LogsDir(), "service-elevated.log")) {
		t.Error("fuera de Windows no se crea service-elevated.log")
	}
}

func TestUninstallWindowsPasaElLogYLoMuestraSiSigueRegistrado(t *testing.T) {
	h := installedHarness(t)
	h.in.Env.GOOS = "windows"
	h.in.reset(Options{Yes: true})
	logPath := filepath.Join(h.in.Cfg.LogsDir(), "service-elevated.log")

	var gotArgs []string
	h.in.Elevate = func(ctx context.Context, exe string, args []string) error {
		gotArgs = args
		// The services stay registered in the fake manager, as if the elevated step had failed.
		return os.WriteFile(logPath, []byte("lodan service uninstall: lodan: acceso denegado\n"), 0o600)
	}
	h.out.Reset()
	if err := h.in.removeServices(context.Background()); err != nil {
		t.Fatalf("removeServices: %v", err)
	}
	if !hasArg(gotArgs, "--ollama") || argAfter(gotArgs, "--log") != logPath {
		t.Errorf("args = %v, se esperaba --ollama y --log %s", gotArgs, logPath)
	}
	out := h.output()
	if !strings.Contains(out, "sigue registrado") || !strings.Contains(out, "acceso denegado") {
		t.Errorf("el aviso debe incluir la salida del paso elevado:\n%s", out)
	}
}

func TestUninstallWindowsIncluyeElLogSiFallaLaElevacion(t *testing.T) {
	h := installedHarness(t)
	h.in.Env.GOOS = "windows"
	h.in.reset(Options{Yes: true})
	logPath := filepath.Join(h.in.Cfg.LogsDir(), "service-elevated.log")
	h.in.Elevate = func(ctx context.Context, exe string, args []string) error {
		_ = os.WriteFile(logPath, []byte("lodan service uninstall: error 5\n"), 0o600)
		return errors.New("cancelado en la ventana de UAC")
	}
	err := h.in.removeServices(context.Background())
	if err == nil || !strings.Contains(err.Error(), "UAC") || !strings.Contains(err.Error(), "error 5") {
		t.Fatalf("el error debe llevar la causa y el log: %v", err)
	}
}
