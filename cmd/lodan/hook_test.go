package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const hookInstrucciones = "lodan: el contexto se acaba de compactar. Antes de seguir, llama una vez a recall de lodan en lote (queries) con los temas en curso de esta conversación y continúa con lo que devuelva."

// runHook runs the CLI with the given stdin against an empty data dir, limited to 10 s.
func runHook(t *testing.T, dir, input string, args ...string) (code int, out, errOut string) {
	t.Helper()
	t.Setenv("LODAN_DATA_DIR", dir)
	old := stdin
	stdin = strings.NewReader(input)
	t.Cleanup(func() { stdin = old })

	type result struct {
		code     int
		out, err string
	}
	done := make(chan result, 1)
	go func() {
		var o, e bytes.Buffer
		c := run(args, &o, &e)
		done <- result{c, o.String(), e.String()}
	}()
	select {
	case r := <-done:
		return r.code, r.out, r.err
	case <-time.After(10 * time.Second):
		t.Fatal("el hook no terminó en 10 s")
		return 0, "", ""
	}
}

func assertNoPG(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "pg")); !os.IsNotExist(err) {
		t.Errorf("el hook no debe crear un clúster PostgreSQL en %s (err: %v)", dir, err)
	}
}

// S10, S12: hook compact without a database prints the instructions, exits 0 and starts no PostgreSQL.
func TestHookCompactSinBaseDeDatos(t *testing.T) {
	dir := t.TempDir()
	code, out, errOut := runHook(t, dir, `{"hook_event_name":"SessionStart","source":"compact"}`, "hook", "compact", "--client", "claude-code")
	if code != 0 {
		t.Fatalf("código = %d (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, hookInstrucciones) {
		t.Errorf("stdout = %q, falta la instrucción de recuperación", out)
	}
	assertNoPG(t, dir)
}

// S12: malformed or empty input never fails.
func TestHookEntradaMalformadaSalida0(t *testing.T) {
	for _, sub := range []string{"compact", "subagent-stop"} {
		for name, in := range map[string]string{"invalida": "{{ esto no es json", "vacia": ""} {
			t.Run(sub+"/"+name, func(t *testing.T) {
				dir := t.TempDir()
				code, _, errOut := runHook(t, dir, in, "hook", sub, "--client", "claude-code")
				if code != 0 {
					t.Errorf("código = %d (stderr: %s)", code, errOut)
				}
				assertNoPG(t, dir)
			})
		}
	}
}

// S11, S12: subagent-stop without a database exits 0, prints nothing on stdout and starts no PostgreSQL.
func TestHookSubagentStopSinBaseDeDatos(t *testing.T) {
	dir := t.TempDir()
	in := `{"hook_event_name":"SubagentStop","cwd":"/x","last_assistant_message":"## Key Learnings\n1. Este aprendizaje no se puede guardar ahora mismo\n"}`
	code, out, errOut := runHook(t, dir, in, "hook", "subagent-stop", "--client", "claude-code")
	if code != 0 {
		t.Fatalf("código = %d (stderr: %s)", code, errOut)
	}
	if out != "" {
		t.Errorf("stdout = %q, se esperaba vacío", out)
	}
	assertNoPG(t, dir)
}

// S11, S12: the PostToolUse form is also accepted without failing.
func TestHookSubagentStopPostToolUseSinBaseDeDatos(t *testing.T) {
	dir := t.TempDir()
	in := `{"hook_event_name":"PostToolUse","tool_name":"SubagentHandback","cwd":"/x","tool_input":{"message":"## Key Learnings\n1. Este aprendizaje no se puede guardar ahora mismo\n"}}`
	code, out, _ := runHook(t, dir, in, "hook", "subagent-stop", "--client", "claude-code")
	if code != 0 || out != "" {
		t.Errorf("código = %d, stdout = %q", code, out)
	}
	assertNoPG(t, dir)
}

// S10: unknown hook action exits 2.
func TestHookAccionDesconocida(t *testing.T) {
	dir := t.TempDir()
	var o, e bytes.Buffer
	t.Setenv("LODAN_DATA_DIR", dir)
	if code := run([]string{"hook", "nada"}, &o, &e); code != 2 {
		t.Errorf("código = %d, se esperaba 2 (stderr: %s)", code, e.String())
	}
}

// Help mentions hook; previous behaviour of help and unknown subcommands is kept.
func TestAyudaMencionaHook(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"help"}, &out, &errOut); code != 0 {
		t.Fatalf("código = %d", code)
	}
	if !strings.Contains(out.String(), "hook") {
		t.Errorf("la ayuda no menciona hook:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "service") || !strings.Contains(out.String(), "sync") {
		t.Errorf("la ayuda perdió secciones previas")
	}
	out.Reset()
	errOut.Reset()
	if code := run([]string{"no-existe"}, &out, &errOut); code != 2 {
		t.Errorf("subcomando desconocido: código %d, se esperaba 2", code)
	}
}
