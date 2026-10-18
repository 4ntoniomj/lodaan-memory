package hook_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"lodan/internal/hook"
)

// S16: text format prints nothing and pulls once.
func TestSessionStartTextoPullUnaVez(t *testing.T) {
	n := 0
	var out bytes.Buffer
	err := hook.SessionStart(context.Background(), func(context.Context) error { n++; return nil }, "text", &out)
	if err != nil || n != 1 || out.Len() != 0 {
		t.Errorf("err=%v pulls=%d out=%q", err, n, out.String())
	}
}

// S16: antigravity prints {} and pulls once.
func TestSessionStartAntigravity(t *testing.T) {
	n := 0
	var out bytes.Buffer
	err := hook.SessionStart(context.Background(), func(context.Context) error { n++; return nil }, "antigravity", &out)
	if err != nil || n != 1 || strings.TrimSpace(out.String()) != "{}" {
		t.Errorf("err=%v pulls=%d out=%q", err, n, out.String())
	}
}

// S16: pull nil means sync not configured.
func TestSessionStartSinPull(t *testing.T) {
	var out bytes.Buffer
	if err := hook.SessionStart(context.Background(), nil, "antigravity", &out); err != nil || strings.TrimSpace(out.String()) != "{}" {
		t.Errorf("err=%v out=%q", err, out.String())
	}
	out.Reset()
	if err := hook.SessionStart(context.Background(), nil, "text", &out); err != nil || out.Len() != 0 {
		t.Errorf("err=%v out=%q", err, out.String())
	}
}

// S16: a pull error is returned but the output is still written.
func TestSessionStartErrorDePull(t *testing.T) {
	boom := errors.New("boom")
	var out bytes.Buffer
	err := hook.SessionStart(context.Background(), func(context.Context) error { return boom }, "antigravity", &out)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, quería boom", err)
	}
	if strings.TrimSpace(out.String()) != "{}" {
		t.Errorf("out = %q", out.String())
	}
}
