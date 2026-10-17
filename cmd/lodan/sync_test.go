package main

import (
	"bytes"
	"strings"
	"testing"
)

const syncNoConfigurada = "la sincronización no está configurada: ejecuta lodan sync init --repo <url>"

// S7: push/pull without init fail before touching the database.
func TestSyncPushPullSinConfigurar(t *testing.T) {
	for _, sub := range []string{"push", "pull"} {
		t.Run(sub, func(t *testing.T) {
			t.Setenv("LODAN_DATA_DIR", t.TempDir())
			t.Setenv("LODAN_SYNC_INTERVAL_MINUTES", "")
			var out, errOut bytes.Buffer
			if code := run([]string{"sync", sub}, &out, &errOut); code != 1 {
				t.Fatalf("código = %d, se esperaba 1 (stderr: %s)", code, errOut.String())
			}
			if !strings.Contains(errOut.String(), syncNoConfigurada) {
				t.Errorf("stderr = %q, falta %q", errOut.String(), syncNoConfigurada)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, se esperaba vacío", out.String())
			}
		})
	}
}

// S7: init requires --repo.
func TestSyncInitSinRepo(t *testing.T) {
	t.Setenv("LODAN_DATA_DIR", t.TempDir())
	var out, errOut bytes.Buffer
	if code := run([]string{"sync", "init"}, &out, &errOut); code != 2 {
		t.Errorf("código = %d, se esperaba 2 (stderr: %s)", code, errOut.String())
	}
}

// S7: the help mentions the sync subcommand.
func TestAyudaMencionaSync(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"help"}, &out, &errOut); code != 0 {
		t.Fatalf("código = %d", code)
	}
	if !strings.Contains(out.String(), "sync") {
		t.Errorf("la ayuda no menciona sync:\n%s", out.String())
	}
}

// Previous behaviour kept: help still lists service and unknown subcommands still exit 2.
func TestSyncNoRompeAyudaNiDesconocidos(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"help"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "service") {
		t.Errorf("help: código %d, sin 'service'", code)
	}
	out.Reset()
	errOut.Reset()
	if code := run([]string{"no-existe"}, &out, &errOut); code != 2 {
		t.Errorf("subcomando desconocido: código %d, se esperaba 2", code)
	}
}
