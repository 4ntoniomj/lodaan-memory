package service

import (
	"context"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Environment variables of the helper process pattern: the test binary runs itself as the fake
// child (see TestChildHelperProcess), so the tests never depend on an external program.
const (
	helperModeEnv   = "LODAN_CHILD_HELPER"
	helperReportEnv = "LODAN_CHILD_REPORT"
	helperProbeEnv  = "LODAN_CHILD_PROBE"
)

// TestChildHelperProcess is not a test: it is the body of the fake child. It does nothing unless
// the helper variable is set, so a normal `go test` run skips it.
//
// Modes: "block" waits (a minute at most), "ignore" waits and ignores SIGINT, "exit" ends with
// status 3 right away. Before waiting it prints on stdout and stderr and leaves a report file
// (written atomically) with its arguments after "--" and the probe variable.
func TestChildHelperProcess(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		return
	}
	if mode == "exit" {
		os.Exit(3)
	}
	if mode == "ignore" {
		signal.Ignore(os.Interrupt)
	}

	var args []string
	for i, a := range os.Args {
		if a == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	os.Stdout.WriteString("hijo: salida estándar\n")
	os.Stderr.WriteString("hijo: salida de error\n")
	report := "args=" + strings.Join(args, " ") + "\nprobe=" + os.Getenv(helperProbeEnv) + "\nlisto\n"
	tmp := os.Getenv(helperReportEnv) + ".tmp"
	if err := os.WriteFile(tmp, []byte(report), 0o600); err != nil {
		os.Exit(4)
	}
	if err := os.Rename(tmp, os.Getenv(helperReportEnv)); err != nil {
		os.Exit(4)
	}
	time.Sleep(time.Minute) // the parent ends this process long before
	os.Exit(0)
}

// helperSpec returns the ChildSpec that runs the helper process in mode.
func helperSpec(t *testing.T, mode, report string, out io.Writer) ChildSpec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("no se pudo localizar el ejecutable de test: %v", err)
	}
	return ChildSpec{
		Exe:  exe,
		Args: []string{"-test.run=^TestChildHelperProcess$", "--", "serve"},
		Env:  []string{helperModeEnv + "=" + mode, helperReportEnv + "=" + report, helperProbeEnv + "=valor-de-prueba"},
		Out:  out,
	}
}

// waitReport waits until the helper has left its report and returns it.
func waitReport(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			return string(data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("el proceso hijo no escribió %s a tiempo", path)
	return ""
}

// runChildAsync runs RunChild in a goroutine and returns the channel with its result.
func runChildAsync(ctx context.Context, spec ChildSpec, logger *log.Logger) <-chan error {
	res := make(chan error, 1)
	go func() { res <- RunChild(ctx, spec, logger) }()
	return res
}

func awaitChild(t *testing.T, res <-chan error, within time.Duration) error {
	t.Helper()
	select {
	case err := <-res:
		return err
	case <-time.After(within):
		t.Fatalf("RunChild no terminó en %s", within)
		return nil
	}
}

func TestRunChildPasaArgumentosEntornoYSalida(t *testing.T) {
	report := filepath.Join(t.TempDir(), "informe.txt")
	out := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := time.Now()
	res := runChildAsync(ctx, helperSpec(t, "block", report, out), nil)
	got := waitReport(t, report)
	for _, want := range []string{"args=serve", "probe=valor-de-prueba"} {
		if !strings.Contains(got, want) {
			t.Errorf("el informe del hijo no contiene %q:\n%s", want, got)
		}
	}

	cancel()
	if err := awaitChild(t, res, 20*time.Second); err != nil {
		t.Fatalf("una parada pedida no es un error: %v", err)
	}
	// The helper waits a minute: finishing earlier means RunChild stopped it.
	if time.Since(started) > 40*time.Second {
		t.Errorf("el hijo no se paró al cancelar: tardó %s", time.Since(started))
	}
	for _, want := range []string{"hijo: salida estándar", "hijo: salida de error"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("la salida del hijo debe ir a Out, falta %q en %q", want, out.String())
		}
	}
}

func TestRunChildFalloSiElHijoTerminaSolo(t *testing.T) {
	logs := &syncBuffer{}
	logger := log.New(logs, "", 0)
	res := runChildAsync(context.Background(), helperSpec(t, "exit", "", nil), logger)
	err := awaitChild(t, res, 20*time.Second)
	if err == nil || !strings.Contains(err.Error(), "terminó por su cuenta") {
		t.Fatalf("si el hijo muere solo se espera un error, y es %v", err)
	}
	if !strings.Contains(logs.String(), "terminó por su cuenta") {
		t.Errorf("el motivo debe quedar en el log: %q", logs.String())
	}
}

func TestRunChildForzadoSiElHijoIgnoraLaSenal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows no tiene señal suave: el hijo se mata directamente")
	}
	old := childStopTimeout
	childStopTimeout = 500 * time.Millisecond
	t.Cleanup(func() { childStopTimeout = old })

	report := filepath.Join(t.TempDir(), "informe.txt")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := runChildAsync(ctx, helperSpec(t, "ignore", report, nil), nil)
	waitReport(t, report) // SIGINT is ignored from here on

	cancel()
	err := awaitChild(t, res, 20*time.Second)
	if err == nil || !strings.Contains(err.Error(), "se forzó su cierre") {
		t.Fatalf("un hijo que ignora SIGINT debe acabar forzado y con error, y es %v", err)
	}
}

func TestRunChildEjecutableInexistente(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-existe")
	err := RunChild(context.Background(), ChildSpec{Exe: missing}, nil)
	if err == nil || !strings.Contains(err.Error(), "no se pudo lanzar") {
		t.Fatalf("se esperaba un error de lanzamiento: %v", err)
	}
	if err := RunChild(context.Background(), ChildSpec{}, nil); err == nil {
		t.Fatal("sin ejecutable debe fallar")
	}
}
