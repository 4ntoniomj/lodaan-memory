package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ollamaReleaseBase is the release download base; a variable so tests can
// point it to a local server.
var ollamaReleaseBase = "https://github.com/ollama/ollama/releases/latest/download"

// apiClient is used for quick Ollama API calls (with an overall timeout).
var apiClient = &http.Client{Timeout: 5 * time.Second}

// maxAPIBody caps the size of a JSON API response.
const maxAPIBody = 16 << 20

// apiGet performs a GET on the Ollama API and returns the body.
func apiGet(ctx context.Context, baseURL, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := apiClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("respuesta HTTP %s de %s", resp.Status, path)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxAPIBody))
}

// Detect reports whether an Ollama server answers at baseURL and its version.
func Detect(ctx context.Context, baseURL string) (version string, ok bool) {
	body, err := apiGet(ctx, baseURL, "/api/version")
	if err != nil {
		return "", false
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &v); err != nil || v.Version == "" {
		return "", false
	}
	return v.Version, true
}

// HasModel reports whether the server already has model. The name is compared
// exactly and, when it carries no tag, also as "<name>:latest".
func HasModel(ctx context.Context, baseURL, model string) (bool, error) {
	body, err := apiGet(ctx, baseURL, "/api/tags")
	if err != nil {
		return false, fmt.Errorf("no se pudo consultar los modelos de Ollama: %w", err)
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &tags); err != nil {
		return false, fmt.Errorf("respuesta de Ollama ilegible: %w", err)
	}
	withLatest := model
	if !strings.Contains(model, ":") {
		withLatest = model + ":latest"
	}
	for _, m := range tags.Models {
		if m.Name == model || m.Name == withLatest {
			return true, nil
		}
	}
	return false, nil
}

// pullLine is one JSON line of the /api/pull stream.
type pullLine struct {
	Status    string `json:"status"`
	Digest    string `json:"digest"`
	Total     int64  `json:"total"`
	Completed int64  `json:"completed"`
	Error     string `json:"error"`
}

// Pull downloads model through /api/pull, reporting progress. It fails if any
// line carries an error or if the stream ends without a "success" status.
func Pull(ctx context.Context, baseURL, model string, progress func(status string, done, total int64)) error {
	payload, err := json.Marshal(map[string]any{"model": model, "stream": true})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/api/pull", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// The stream lasts as long as the download: no overall timeout.
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("no se pudo contactar con Ollama: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("Ollama rechazó la descarga de %s (HTTP %d): %s", model, resp.StatusCode, apiErrorMessage(b))
	}

	dec := json.NewDecoder(resp.Body)
	success := false
	for {
		var line pullLine
		if err := dec.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("se interrumpió la descarga de %s: %w", model, err)
		}
		if line.Error != "" {
			return fmt.Errorf("Ollama no pudo descargar %s: %s", model, line.Error)
		}
		if progress != nil {
			progress(line.Status, line.Completed, line.Total)
		}
		if line.Status == "success" {
			success = true
		}
	}
	if !success {
		return fmt.Errorf("la descarga de %s terminó sin confirmación de Ollama (falta el estado success)", model)
	}
	return nil
}

// apiErrorMessage extracts {"error": "..."} or falls back to the raw text.
func apiErrorMessage(body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return e.Error
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return "sin detalle"
	}
	return msg
}

// AssetFor returns the official Ollama release asset for a platform.
func AssetFor(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "linux/amd64":
		return "ollama-linux-amd64.tar.zst", nil
	case "linux/arm64":
		return "ollama-linux-arm64.tar.zst", nil
	case "darwin/amd64", "darwin/arm64":
		return "ollama-darwin.tgz", nil
	case "windows/amd64":
		return "ollama-windows-amd64.zip", nil
	}
	return "", fmt.Errorf("plataforma no soportada: %s/%s", goos, goarch)
}

// InstallOllama downloads the official archive (verified against the
// release's sha256sum.txt), extracts it into dir and returns the path of the
// ollama executable. progress may be nil.
func InstallOllama(ctx context.Context, dir string, progress func(done, total int64)) (exe string, err error) {
	asset, err := AssetFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	sums, err := fetchText(ctx, ollamaReleaseBase+"/sha256sum.txt")
	if err != nil {
		return "", fmt.Errorf("no se pudo obtener las sumas de verificación de Ollama: %w", err)
	}
	want, err := shaFromSumFile(sums, asset)
	if err != nil {
		return "", err
	}

	dlDir := filepath.Join(dir, "download")
	archive := filepath.Join(dlDir, asset)
	if err := download(ctx, ollamaReleaseBase+"/"+asset, archive, want, progress); err != nil {
		return "", fmt.Errorf("no se pudo descargar Ollama: %w", err)
	}
	if err := extractArchive(archive, dir); err != nil {
		return "", fmt.Errorf("no se pudo extraer Ollama: %w", err)
	}
	// The archive is large: drop it once extracted (best effort).
	_ = os.Remove(archive)
	_ = os.Remove(dlDir)

	return findOllama(dir, runtime.GOOS)
}

// FindOllama locates the ollama executable extracted into dir.
func FindOllama(dir string) (string, error) {
	return findOllama(dir, runtime.GOOS)
}

// findOllama looks for bin/ollama and ollama at the root first and then walks
// the tree by name (some macOS archives have a different layout, e.g.
// "GNUSparseFile.0/ollama").
func findOllama(dir, goos string) (string, error) {
	name := "ollama" + exeSuffix(goos)
	for _, p := range []string{
		filepath.Join(dir, "bin", name),
		filepath.Join(dir, name),
	} {
		if isRegularFile(p) {
			return p, nil
		}
	}
	var found string
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			// Downloaded models can be huge and never hold the executable.
			if p != dir && (d.Name() == "models" || d.Name() == "download") {
				return filepath.SkipDir
			}
			return nil
		}
		// "lib/ollama" is a directory: only regular files count.
		if d.Name() == name && d.Type().IsRegular() {
			found = p
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return "", fmt.Errorf("no se pudo buscar %s en %s: %w", name, dir, walkErr)
	}
	if found == "" {
		return "", fmt.Errorf("%s en %s: %w", name, dir, errNotFound)
	}
	return found, nil
}

// OllamaEnv returns the environment variables to run the Ollama installed in
// dir: local-only address, models under dir and, on Linux, its bundled libraries.
func OllamaEnv(dir string) map[string]string {
	return ollamaEnv(runtime.GOOS, dir)
}

func ollamaEnv(goos, dir string) map[string]string {
	env := map[string]string{
		"OLLAMA_HOST":   "127.0.0.1:11434",
		"OLLAMA_MODELS": filepath.Join(dir, "models"),
	}
	if goos == "linux" {
		env["LD_LIBRARY_PATH"] = filepath.Join(dir, "lib", "ollama")
	}
	return env
}
