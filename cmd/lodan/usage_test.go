package main

import (
	"bytes"
	"strings"
	"testing"
)

// The top-level help lists every db subcommand, fix-encoding included.
func TestUsageListsFixEncoding(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--help"}, &out, &errOut); code != 0 {
		t.Fatalf("lodan --help: exit %d, stderr %q", code, errOut.String())
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "db ") {
			if !strings.Contains(line, "fix-encoding") {
				t.Errorf("la línea de db no lista fix-encoding: %q", line)
			}
			return
		}
	}
	t.Fatalf("lodan --help no tiene línea de db:\n%s", out.String())
}
