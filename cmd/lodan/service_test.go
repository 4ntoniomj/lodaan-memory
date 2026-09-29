package main

import (
	"bytes"
	"errors"
	"io/fs"
	"runtime"
	"strings"
	"testing"

	"lodan/internal/service"
)

func TestPermissionHint(t *testing.T) {
	want := "sudo"
	if runtime.GOOS == "windows" {
		want = "administrador"
	}
	for _, err := range []error{
		errors.New("permiso denegado"),
		errors.New("Access is denied"),
		errors.New("open /etc/systemd/system/lodan.service: access refused"),
		fs.ErrPermission,
	} {
		if hint := permissionHint(err); !strings.Contains(hint, want) {
			t.Errorf("permissionHint(%q) = %q, se esperaba una pista con %q", err, hint, want)
		}
	}
	if hint := permissionHint(errors.New("unit lodan.service not found")); hint != "" {
		t.Errorf("no se esperaba pista para un error ajeno a los permisos, y es %q", hint)
	}
}

func TestStatusLine(t *testing.T) {
	got := statusLine("lodan", service.Status{State: "running", Enabled: true, Detail: "pid 42"})
	for _, want := range []string{"lodan", "en marcha", "sí", "pid 42"} {
		if !strings.Contains(got, want) {
			t.Errorf("statusLine = %q, falta %q", got, want)
		}
	}
	got = statusLine("lodan", service.Status{State: "not-installed"})
	if !strings.Contains(got, "no instalado") || !strings.Contains(got, "no") {
		t.Errorf("statusLine = %q", got)
	}
}

func TestServiceArgumentosInvalidos(t *testing.T) {
	casos := [][]string{
		{"service"},
		{"service", "explotar"},
		{"service", "run", "sobra"},
		{"service", "status", "sobra"},
	}
	for _, args := range casos {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 {
			t.Errorf("run(%v) = %d, se esperaba 2 (stderr: %s)", args, code, errOut.String())
		}
	}
}
