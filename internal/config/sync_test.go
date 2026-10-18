package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// S8: default off.
func TestSyncIntervalPorDefecto(t *testing.T) {
	isolate(t)
	t.Setenv("LODAN_SYNC_INTERVAL_MINUTES", "")
	if Default().SyncIntervalMinutes != 0 {
		t.Errorf("Default().SyncIntervalMinutes = %d, se esperaba 0", Default().SyncIntervalMinutes)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SyncIntervalMinutes != 0 {
		t.Errorf("SyncIntervalMinutes = %d, se esperaba 0", cfg.SyncIntervalMinutes)
	}
}

// S8: environment override.
func TestSyncIntervalEntorno(t *testing.T) {
	isolate(t)
	t.Setenv("LODAN_SYNC_INTERVAL_MINUTES", "15")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SyncIntervalMinutes != 15 {
		t.Errorf("SyncIntervalMinutes = %d, se esperaba 15", cfg.SyncIntervalMinutes)
	}
}

// S8: negative is a validation error (from the environment and from Validate).
func TestSyncIntervalNegativoInvalido(t *testing.T) {
	isolate(t)
	t.Setenv("LODAN_SYNC_INTERVAL_MINUTES", "-1")
	if _, err := Load(); err == nil {
		t.Error("Load con LODAN_SYNC_INTERVAL_MINUTES=-1 debería fallar")
	}
	c := Default()
	c.SyncIntervalMinutes = -5
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "sync_interval_minutes") {
		t.Errorf("Validate = %v, se esperaba un error que nombre sync_interval_minutes", err)
	}
	c.SyncIntervalMinutes = 0
	if err := c.Validate(); err != nil {
		t.Errorf("0 debe ser válido: %v", err)
	}
}

// S9: the default key lives outside the data directory.
func TestDefaultSyncKeyFile(t *testing.T) {
	dir := isolate(t)
	got, err := DefaultSyncKeyFile()
	if err != nil {
		t.Fatal(err)
	}
	ucd, err := os.UserConfigDir()
	if err != nil {
		t.Skip("sin UserConfigDir")
	}
	if want := filepath.Join(ucd, "lodan", "sync.key"); got != want {
		t.Errorf("DefaultSyncKeyFile = %q, se esperaba %q", got, want)
	}
	if rel, err := filepath.Rel(dir, got); err == nil && !strings.HasPrefix(rel, "..") {
		t.Errorf("la clave %s está dentro del directorio de datos %s", got, dir)
	}
}

// S7: working copy under the data dir.
func TestSyncDir(t *testing.T) {
	c := Config{DataDir: filepath.Join("datos", "lodan")}
	if got, want := c.SyncDir(), filepath.Join("datos", "lodan", "sync-repo"); got != want {
		t.Errorf("SyncDir = %q, se esperaba %q", got, want)
	}
}

// S7, S8, S9: the sync fields survive Save/Load and use the documented JSON names.
func TestSyncCamposIdaYVuelta(t *testing.T) {
	dir := isolate(t)
	t.Setenv("LODAN_SYNC_INTERVAL_MINUTES", "")
	orig := Default()
	orig.DataDir = dir
	orig.SyncRepo = "git@example.com:yo/lodan-sync.git"
	orig.SyncMachine = "portatil-1"
	orig.SyncKeyFile = filepath.Join(dir, "..", "clave.key")
	orig.SyncIntervalMinutes = 30
	if err := orig.Save(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(orig.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{`"sync_repo"`, `"sync_machine"`, `"sync_key_file"`, `"sync_interval_minutes"`} {
		if !strings.Contains(string(raw), name) {
			t.Errorf("config.json no contiene %s", name)
		}
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got != orig {
		t.Fatalf("ida y vuelta distinta:\n got  %+v\n want %+v", got, orig)
	}
}
