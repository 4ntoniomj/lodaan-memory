package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Environment variables of the fake Ollama. The test binary plays that role itself (see
// TestMain), so the tests never depend on a real Ollama. They reach it only through the "--env"
// mechanism under test, never through the environment of the test process.
const (
	fakeOllamaEnv       = "LODAN_FAKE_OLLAMA"        // "1": wait; "exit": end with status 3
	fakeOllamaReportEnv = "LODAN_FAKE_OLLAMA_REPORT" // file where it leaves what it received
)

func TestMain(m *testing.M) {
	switch os.Getenv(fakeOllamaEnv) {
	case "":
		os.Exit(m.Run())
	case "exit":
		os.Exit(3)
	default:
		os.Exit(fakeOllamaMain())
	}
}

// fakeOllamaMain is the body of the fake `ollama serve`: it prints, leaves a report (written
// atomically) with its arguments and OLLAMA_HOST, and waits until the parent ends it.
func fakeOllamaMain() int {
	os.Stdout.WriteString("ollama falso: salida estándar\n")
	os.Stderr.WriteString("ollama falso: salida de error\n")
	report := "args=" + strings.Join(os.Args[1:], " ") + "\nOLLAMA_HOST=" + os.Getenv("OLLAMA_HOST") + "\nlisto\n"
	path := os.Getenv(fakeOllamaReportEnv)
	if err := os.WriteFile(path+".tmp", []byte(report), 0o600); err != nil {
		return 4
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return 4
	}
	time.Sleep(time.Minute) // the parent ends this process long before
	return 0
}

// lockedBuffer is a bytes.Buffer safe for concurrent writers (the child and the logger).
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			return string(data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("el Ollama falso no escribió %s a tiempo", path)
	return ""
}

func TestParseOllamaArgs(t *testing.T) {
	var errOut bytes.Buffer
	o, code, done := parseOllamaArgs([]string{"--ollama-exe", "/x/ollama", "--log", "/x/ollama.log",
		"--env", "OLLAMA_HOST=127.0.0.1:11434", "--env", "OLLAMA_MODELS=/x/models"}, &errOut)
	if done || code != 0 {
		t.Fatalf("args válidos: done=%v code=%d stderr=%s", done, code, errOut.String())
	}
	want := ollamaOptions{Exe: "/x/ollama", LogPath: "/x/ollama.log",
		Env: []string{"OLLAMA_HOST=127.0.0.1:11434", "OLLAMA_MODELS=/x/models"}}
	if !reflect.DeepEqual(o, want) {
		t.Errorf("opciones = %+v, se esperaba %+v", o, want)
	}

	for name, args := range map[string][]string{
		"sin --ollama-exe": {"--log", "/x/ollama.log"},
		"env sin igual":    {"--ollama-exe", "/x/ollama", "--env", "OLLAMA_HOST"},
		"env sin nombre":   {"--ollama-exe", "/x/ollama", "--env", "=valor"},
		"sobran args":      {"--ollama-exe", "/x/ollama", "sobra"},
	} {
		var e bytes.Buffer
		if _, code, done := parseOllamaArgs(args, &e); !done || code != 2 {
			t.Errorf("%s: done=%v code=%d, se esperaba done y 2 (stderr: %s)", name, done, code, e.String())
		}
	}

	var out, e bytes.Buffer
	if code := run([]string{"service", "ollama"}, &out, &e); code != 2 {
		t.Errorf("run(service ollama) = %d, se esperaba 2 (stderr: %s)", code, e.String())
	}
}

func TestRunOllamaChildLanzaServeConElEntornoYSeParaAlCancelar(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	report := filepath.Join(dir, "informe.txt")
	logPath := filepath.Join(dir, "logs", "ollama.log") // the folder does not exist yet
	o := ollamaOptions{
		Exe:     exe,
		LogPath: logPath,
		Env: []string{
			fakeOllamaEnv + "=1",
			fakeOllamaReportEnv + "=" + report,
			"OLLAMA_HOST=127.0.0.1:11434",
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := time.Now()
	res := make(chan error, 1)
	go func() { res <- runOllamaChild(ctx, o, nil) }()

	got := waitFile(t, report)
	for _, want := range []string{"args=serve\n", "OLLAMA_HOST=127.0.0.1:11434"} {
		if !strings.Contains(got, want) {
			t.Errorf("el Ollama falso debe recibir %q:\n%s", want, got)
		}
	}
	if v := os.Getenv(fakeOllamaEnv); v != "" {
		t.Fatalf("el entorno del proceso de test no debe cambiar: %s=%q", fakeOllamaEnv, v)
	}

	cancel()
	select {
	case err := <-res:
		if err != nil {
			t.Fatalf("una parada pedida no es un error: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("runOllamaChild no terminó tras cancelar")
	}
	// The fake waits a minute: finishing earlier means the child was ended.
	if time.Since(started) > 40*time.Second {
		t.Errorf("el hijo no se paró al cancelar: tardó %s", time.Since(started))
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("debe crearse el log %s: %v", logPath, err)
	}
	for _, want := range []string{"ollama falso: salida estándar", "ollama falso: salida de error", "lodan-ollama:"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("falta %q en el log:\n%s", want, data)
		}
	}
}

func TestRunOllamaChildSinLogUsaElFallback(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(t.TempDir(), "informe.txt")
	o := ollamaOptions{Exe: exe, Env: []string{fakeOllamaEnv + "=1", fakeOllamaReportEnv + "=" + report}}

	out := &lockedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := make(chan error, 1)
	go func() { res <- runOllamaChild(ctx, o, out) }()
	waitFile(t, report)
	cancel()
	select {
	case err := <-res:
		if err != nil {
			t.Fatalf("runOllamaChild: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("runOllamaChild no terminó tras cancelar")
	}
	if !strings.Contains(out.String(), "ollama falso: salida estándar") {
		t.Errorf("sin --log la salida va al writer de respaldo: %q", out.String())
	}
}

func TestRunOllamaChildFallaSiOllamaMuereSolo(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	o := ollamaOptions{Exe: exe, Env: []string{fakeOllamaEnv + "=exit"}}
	err = runOllamaChild(context.Background(), o, nil)
	if err == nil || !strings.Contains(err.Error(), "terminó por su cuenta") {
		t.Fatalf("si Ollama muere solo debe devolver error (el SCM lo reiniciará): %v", err)
	}
}

func TestTeeLogEscribeEnLosDosSitios(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "service-elevated.log") // the folder does not exist yet
	var out, errOut bytes.Buffer

	stdout, stderr, closeLog := teeLog(path, &out, &errOut)
	stdout.Write([]byte("salida\n"))
	stderr.Write([]byte("error\n"))
	closeLog()

	if out.String() != "salida\n" || errOut.String() != "error\n" {
		t.Errorf("la salida original debe conservarse: stdout=%q stderr=%q", out.String(), errOut.String())
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "salida\nerror\n" {
		t.Errorf("el log = %q (%v), se esperaba la salida y el error en orden", data, err)
	}

	// A second run appends.
	stdout, _, closeLog = teeLog(path, &out, &errOut)
	stdout.Write([]byte("otra\n"))
	closeLog()
	if data, _ := os.ReadFile(path); string(data) != "salida\nerror\notra\n" {
		t.Errorf("el log debe acumular: %q", data)
	}
}

func TestTeeLogSinRutaNoCambiaNada(t *testing.T) {
	var out, errOut bytes.Buffer
	stdout, stderr, closeLog := teeLog("", &out, &errOut)
	defer closeLog()
	if stdout != &out || stderr != &errOut {
		t.Error("sin ruta los writers deben ser los originales")
	}
}

func TestTeeLogAvisaSiNoPuedeAbrirElLog(t *testing.T) {
	// The parent of the log is a file, so the folder cannot be created.
	blocker := filepath.Join(t.TempDir(), "archivo")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	stdout, _, closeLog := teeLog(filepath.Join(blocker, "log.txt"), &out, &errOut)
	defer closeLog()
	if !strings.Contains(errOut.String(), "no se pudo abrir el log") {
		t.Errorf("debe avisar por stderr: %q", errOut.String())
	}
	stdout.Write([]byte("sigue\n"))
	if out.String() != "sigue\n" {
		t.Errorf("sin log, la salida debe seguir saliendo: %q", out.String())
	}
}

func TestServiceInstallEscribeSusErroresEnElLog(t *testing.T) {
	// Without --data-dir it fails before touching any service, so nothing real is installed.
	logPath := filepath.Join(t.TempDir(), "service-elevated.log")
	var out, errOut bytes.Buffer
	if code := run([]string{"service", "install", "--log", logPath}, &out, &errOut); code != 2 {
		t.Fatalf("code = %d, se esperaba 2 (stderr: %s)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "falta --data-dir") {
		t.Errorf("el error también debe salir por stderr: %q", errOut.String())
	}
	data, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(data), "falta --data-dir") {
		t.Errorf("el error debe quedar en el log: %q (%v)", data, err)
	}
}
