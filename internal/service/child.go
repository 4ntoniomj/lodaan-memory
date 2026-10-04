package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// childStopTimeout is how long RunChild waits for the child to exit after asking it to stop,
// before killing it. It matches pgStopTimeout so all the stop times of the service add up the same.
// A variable so the tests can shorten it; it is read before anything else runs, never from a
// goroutine that could outlive the call.
var childStopTimeout = 30 * time.Second

// ChildSpec describes a process that RunChild keeps alive in the foreground.
type ChildSpec struct {
	// Exe is the executable and Args its arguments.
	Exe  string
	Args []string

	// Env holds extra "K=V" variables. They are added to the environment of the current process,
	// and win over it when a name is repeated.
	Env []string

	// Out receives the stdout and stderr of the child. Nil discards them.
	Out io.Writer
}

// RunChild runs spec as a child process and blocks until it is over. It is the body of services
// that wrap a program that does not speak the service protocol of the OS (on Windows,
// `lodan service ollama`: ollama.exe does not talk to the SCM).
//
// While ctx is alive, the child is expected to stay running: if it exits on its own, even with
// status 0, RunChild returns an error so the service manager applies its recovery policy.
//
// When ctx is cancelled, the child is stopped and RunChild returns nil once it is gone. On Unix
// the child gets SIGINT first (what a Ctrl-C does) and is killed if it is still alive after
// childStopTimeout, which is reported as an error. Windows has no soft signal: the child and the
// processes it started are killed right away.
func RunChild(ctx context.Context, spec ChildSpec, logger *log.Logger) error {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	if spec.Exe == "" {
		return errors.New("falta el ejecutable del proceso hijo")
	}
	stopTimeout := childStopTimeout
	name := filepath.Base(spec.Exe)

	// Not CommandContext: cancelling ctx must trigger a clean stop, not an immediate kill.
	cmd := exec.Command(spec.Exe, spec.Args...)
	cmd.Env = append(os.Environ(), spec.Env...)
	cmd.Stdout = spec.Out
	cmd.Stderr = spec.Out
	// With a writer that is not a file, Wait would block while a grandchild keeps the pipe open.
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("no se pudo lanzar %s: %w", spec.Exe, err)
	}
	logger.Printf("%s arrancado (pid %d)", name, cmd.Process.Pid)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if ctx.Err() != nil {
			return nil // the shutdown and the exit crossed paths: it is not a failure
		}
		logger.Printf("%s terminó por su cuenta (%v)", name, err)
		return fmt.Errorf("%s terminó por su cuenta (%v)", name, err)
	case <-ctx.Done():
	}

	logger.Printf("parando %s", name)
	terminateChild(cmd.Process)
	timer := time.NewTimer(stopTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
	}
	_ = killProcessTree(cmd.Process)
	<-done
	logger.Printf("%s no paró en %s: se forzó su cierre", name, stopTimeout)
	return fmt.Errorf("%s no paró en %s: se forzó su cierre", name, stopTimeout)
}

// terminateChild asks the child to stop: SIGINT on Unix, a kill of the whole tree on Windows.
func terminateChild(p *os.Process) {
	if runtime.GOOS == "windows" {
		_ = killProcessTree(p)
		return
	}
	_ = p.Signal(os.Interrupt)
}

// killProcessTree kills p. On Windows it also kills the processes p started (Ollama launches
// runner processes that would otherwise stay orphaned and keep the model in memory), with
// taskkill; if that fails it falls back to killing only p.
func killProcessTree(p *os.Process) error {
	if runtime.GOOS == "windows" {
		if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid)).Run(); err == nil {
			return nil
		}
	}
	return p.Kill()
}
