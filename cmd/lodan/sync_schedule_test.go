package main

import (
	"bytes"
	"strings"
	"testing"
)

// S8: run/schedule/unschedule without sync configured fail before touching the system.
func TestSyncScheduleSinConfigurar(t *testing.T) {
	for _, sub := range []string{"run", "schedule", "unschedule"} {
		t.Run(sub, func(t *testing.T) {
			t.Setenv("LODAN_DATA_DIR", t.TempDir())
			var out, errOut bytes.Buffer
			if code := run([]string{"sync", sub}, &out, &errOut); code != 1 {
				t.Fatalf("código = %d (stderr: %s)", code, errOut.String())
			}
			if !strings.Contains(errOut.String(), syncNoConfigurada) {
				t.Errorf("stderr = %q", errOut.String())
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q", out.String())
			}
		})
	}
}

// S8: an out-of-range interval exits 2 even when not configured.
func TestSyncScheduleIntervaloInvalido(t *testing.T) {
	for _, v := range []string{"3", "1441"} {
		t.Setenv("LODAN_DATA_DIR", t.TempDir())
		var out, errOut bytes.Buffer
		if code := run([]string{"sync", "schedule", "--every", v}, &out, &errOut); code != 2 {
			t.Errorf("--every %s: código = %d (stderr: %s)", v, code, errOut.String())
		}
	}
}

// S8: the sync usage mentions schedule and run.
func TestSyncAyudaMencionaScheduleYRun(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"sync", "-h"}, &out, &errOut); code != 0 {
		t.Fatalf("código = %d", code)
	}
	for _, w := range []string{"schedule", "run"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("la ayuda de sync no menciona %q:\n%s", w, out.String())
		}
	}
}

// S8: previous sync behaviour kept (init without --repo and unknown action exit 2).
func TestSyncScheduleNoRompeLoAnterior(t *testing.T) {
	t.Setenv("LODAN_DATA_DIR", t.TempDir())
	var out, errOut bytes.Buffer
	if code := run([]string{"sync", "init"}, &out, &errOut); code != 2 {
		t.Errorf("init sin repo: código = %d", code)
	}
	if code := run([]string{"sync", "nada"}, &out, &errOut); code != 2 {
		t.Errorf("acción desconocida: código = %d", code)
	}
}

// S16: session-start without sync does nothing and starts no PostgreSQL.
func TestHookSessionStartSinSync(t *testing.T) {
	dir := t.TempDir()
	code, out, errOut := runHook(t, dir, "", "hook", "session-start", "--client", "x")
	if code != 0 || out != "" {
		t.Errorf("código=%d stdout=%q stderr=%q", code, out, errOut)
	}
	assertNoPG(t, dir)
}

// S16: antigravity format prints {}.
func TestHookSessionStartAntigravity(t *testing.T) {
	dir := t.TempDir()
	code, out, errOut := runHook(t, dir, "", "hook", "session-start", "--client", "x", "--format", "antigravity")
	if code != 0 || strings.TrimSpace(out) != "{}" {
		t.Errorf("código=%d stdout=%q stderr=%q", code, out, errOut)
	}
	assertNoPG(t, dir)
}

// S16: an invalid format exits 2.
func TestHookSessionStartFormatoInvalido(t *testing.T) {
	code, _, _ := runHook(t, t.TempDir(), "", "hook", "session-start", "--client", "x", "--format", "yaml")
	if code != 2 {
		t.Errorf("código = %d, quería 2", code)
	}
}
