//go:build windows

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

// runHandler runs Execute in a goroutine with a buffered status channel and
// returns the pieces the tests need.
func runHandler(h *serviceHandler) (requests chan svc.ChangeRequest, status chan svc.Status, exit <-chan [2]uint32) {
	requests = make(chan svc.ChangeRequest)
	status = make(chan svc.Status, 16)
	out := make(chan [2]uint32, 1)
	go func() {
		specific, code := h.Execute(nil, requests, status)
		var flag uint32
		if specific {
			flag = 1
		}
		out <- [2]uint32{flag, code}
	}()
	return requests, status, out
}

func awaitHandlerExit(t *testing.T, exit <-chan [2]uint32) [2]uint32 {
	t.Helper()
	select {
	case r := <-exit:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("el manejador no terminó a tiempo")
		return [2]uint32{}
	}
}

func drainStates(status chan svc.Status) []svc.State {
	close(status)
	var states []svc.State
	for st := range status {
		states = append(states, st.State)
	}
	return states
}

func TestHandlerCancelaElContextoAlParar(t *testing.T) {
	cancelled := make(chan struct{})
	h := &serviceHandler{run: func(ctx context.Context) error {
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}}
	requests, status, exit := runHandler(h)

	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	res := awaitHandlerExit(t, exit)
	if res != [2]uint32{0, 0} {
		t.Errorf("salida = %v, se esperaba {0, 0} (parada limpia)", res)
	}
	select {
	case <-cancelled:
	default:
		t.Error("el contexto de run no se canceló")
	}
	want := []svc.State{svc.StartPending, svc.Running, svc.StopPending}
	got := drainStates(status)
	if len(got) != len(want) {
		t.Fatalf("estados = %v, se esperaba %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("estados = %v, se esperaba %v", got, want)
			break
		}
	}
}

func TestHandlerShutdownTambienCancela(t *testing.T) {
	h := &serviceHandler{run: func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	}}
	requests, _, exit := runHandler(h)
	requests <- svc.ChangeRequest{Cmd: svc.Shutdown}
	if res := awaitHandlerExit(t, exit); res != [2]uint32{0, 0} {
		t.Errorf("salida = %v, se esperaba {0, 0}", res)
	}
}

func TestHandlerInformaDelFalloDeRun(t *testing.T) {
	h := &serviceHandler{run: func(ctx context.Context) error {
		return errors.New("fallo simulado")
	}}
	_, _, exit := runHandler(h)
	// Non-zero service-specific exit code so the SCM recovery actions apply.
	if res := awaitHandlerExit(t, exit); res != [2]uint32{1, 1} {
		t.Errorf("salida = %v, se esperaba {1, 1}", res)
	}
}
