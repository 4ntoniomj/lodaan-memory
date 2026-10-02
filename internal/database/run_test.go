//go:build !windows

package database

import (
	"os/exec"
	"testing"
)

// A command that exits successfully but leaves a background child holding its output pipe
// (what pg_ctl start does on Windows) must not be reported as a failure.
func TestCombinedOutputIgnoraTuberiasHeredadas(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sin sh")
	}
	out, err := combinedOutput(exec.Command("sh", "-c", "echo hola; sleep 30 &"))
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if out != "hola" {
		t.Fatalf("salida = %q, se esperaba %q", out, "hola")
	}
}

func TestCombinedOutputMantieneLosFallos(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sin sh")
	}
	if _, err := combinedOutput(exec.Command("sh", "-c", "echo fallo; exit 3")); err == nil {
		t.Fatal("se esperaba un error con estado de salida 3")
	}
}
