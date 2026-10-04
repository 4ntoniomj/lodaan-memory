package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"lodan/internal/install"
	"lodan/internal/service"
)

// ollamaOptions are the parsed flags of the internal `lodan service ollama`.
type ollamaOptions struct {
	// Exe is the ollama executable that runs as `<Exe> serve`.
	Exe string
	// Env holds "K=V" variables for the Ollama process.
	Env []string
	// LogPath is the file that receives the output of Ollama and the messages of this command;
	// empty means stderr (foreground) or nothing (Windows service, which has no console).
	LogPath string
}

// parseOllamaArgs parses the flags of `lodan service ollama`. When done is true, the caller must
// return code.
func parseOllamaArgs(args []string, stderr io.Writer) (o ollamaOptions, code int, done bool) {
	flags := newFlagSet("service ollama", "lodan service ollama --ollama-exe <ruta> [--log <archivo>] [--env K=V]...", stderr)
	var envs stringList
	flags.StringVar(&o.Exe, "ollama-exe", "", "ejecutable de Ollama")
	flags.StringVar(&o.LogPath, "log", "", "archivo donde se escribe la salida de Ollama")
	flags.Var(&envs, "env", "variable de entorno K=V para Ollama (repetible)")
	if code, done := parseFlags(flags, args); done {
		return o, code, true
	}
	if o.Exe == "" {
		fmt.Fprintln(stderr, "lodan service ollama: falta --ollama-exe")
		return o, 2, true
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "lodan service ollama: argumentos inesperados: %s\n", strings.Join(flags.Args(), " "))
		return o, 2, true
	}
	for _, kv := range envs {
		if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
			fmt.Fprintf(stderr, "lodan service ollama: --env mal formado %q (se espera K=V)\n", kv)
			return o, 2, true
		}
	}
	o.Env = envs
	return o, 0, false
}

// runServiceOllama implements the internal `lodan service ollama`: the process the Windows SCM
// keeps alive for lodan-ollama. ollama.exe does not speak the service protocol, so this command
// answers the SCM and runs `ollama serve` as its child. Outside a Windows service it does the same
// in the foreground and stops Ollama on Ctrl-C.
func runServiceOllama(args []string, stderr io.Writer) int {
	o, code, done := parseOllamaArgs(args, stderr)
	if done {
		return code
	}
	isWinSvc, err := service.IsWindowsService()
	if err != nil {
		fmt.Fprintf(stderr, "lodan service ollama: %v\n", err)
		return 1
	}

	if isWinSvc {
		// A Windows service has no console: everything goes to the log file, if there is one.
		err = service.RunAsWindowsService(install.OllamaServiceName, func(ctx context.Context) error {
			return runOllamaChild(ctx, o, nil)
		})
	} else {
		ctx, stop := signalContext()
		defer stop()
		err = runOllamaChild(ctx, o, stderr)
	}
	if err != nil {
		fmt.Fprintf(stderr, "lodan service ollama: %v\n", err)
		return 1
	}
	return 0
}

// runOllamaChild runs `<o.Exe> serve` until ctx is cancelled (nil) or Ollama dies (error). Its
// output, and the messages of this command, go to o.LogPath or, if it is empty, to fallback
// (nil discards them).
func runOllamaChild(ctx context.Context, o ollamaOptions, fallback io.Writer) error {
	out := fallback
	if o.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(o.LogPath), 0o700); err != nil {
			return fmt.Errorf("no se pudo crear el directorio de logs de %s: %w", o.LogPath, err)
		}
		f, err := os.OpenFile(o.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("no se pudo abrir %s: %w", o.LogPath, err)
		}
		defer f.Close()
		out = f
	}
	logOut := out
	if logOut == nil {
		logOut = io.Discard
	}
	logger := log.New(logOut, "lodan-ollama: ", log.LstdFlags)
	return service.RunChild(ctx, service.ChildSpec{
		Exe:  o.Exe,
		Args: []string{"serve"},
		Env:  o.Env,
		Out:  out,
	}, logger)
}
