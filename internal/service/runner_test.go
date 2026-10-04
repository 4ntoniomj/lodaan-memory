//go:build linux || darwin

package service

import "strings"

// fakeRunner records the commands it receives and answers with canned results.
// Commands without a canned result succeed with empty output.
type fakeRunner struct {
	calls   []string
	results map[string]cmdResult   // fixed answer per command line
	queue   map[string][]cmdResult // successive answers per command line (take precedence)
	startEr map[string]error       // commands that fail to start
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		results: map[string]cmdResult{},
		queue:   map[string][]cmdResult{},
		startEr: map[string]error{},
	}
}

func (f *fakeRunner) Run(name string, args ...string) (cmdResult, error) {
	key := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, key)
	if err, ok := f.startEr[key]; ok {
		return cmdResult{}, err
	}
	if q := f.queue[key]; len(q) > 0 {
		f.queue[key] = q[1:]
		return q[0], nil
	}
	return f.results[key], nil
}
