package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAssetFor(t *testing.T) {
	tests := []struct {
		goos, goarch string
		want         string
		wantErr      bool
	}{
		{"linux", "amd64", "ollama-linux-amd64.tar.zst", false},
		{"linux", "arm64", "ollama-linux-arm64.tar.zst", false},
		{"darwin", "amd64", "ollama-darwin.tgz", false},
		{"darwin", "arm64", "ollama-darwin.tgz", false},
		{"windows", "amd64", "ollama-windows-amd64.zip", false},
		{"windows", "arm64", "", true},
		{"freebsd", "amd64", "", true},
	}
	for _, tt := range tests {
		got, err := AssetFor(tt.goos, tt.goarch)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("AssetFor(%s,%s) = (%q, %v), quería (%q, err=%v)", tt.goos, tt.goarch, got, err, tt.want, tt.wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), "plataforma no soportada") {
			t.Errorf("mensaje inesperado: %v", err)
		}
	}
}

func newAPI(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestDetect(t *testing.T) {
	ok := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"version":"0.34.4"}`))
	})
	if v, found := Detect(context.Background(), ok.URL); !found || v != "0.34.4" {
		t.Errorf("Detect = (%q, %v)", v, found)
	}
	// A trailing slash in the base URL is tolerated.
	if _, found := Detect(context.Background(), ok.URL+"/"); !found {
		t.Errorf("Detect con barra final debería funcionar")
	}

	failing := newAPI(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) })
	if _, found := Detect(context.Background(), failing.URL); found {
		t.Errorf("un 500 no es un Ollama")
	}

	notOllama := newAPI(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) })
	if _, found := Detect(context.Background(), notOllama.URL); found {
		t.Errorf("una respuesta que no es JSON no es un Ollama")
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	if _, found := Detect(context.Background(), url); found {
		t.Errorf("un puerto cerrado no es un Ollama")
	}
}

func TestHasModel(t *testing.T) {
	srv := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"embeddinggemma:300m-qat-q4_0"},{"name":"nomic-embed-text:latest"}]}`))
	})
	tests := []struct {
		model string
		want  bool
	}{
		{"embeddinggemma:300m-qat-q4_0", true},
		{"nomic-embed-text:latest", true},
		{"nomic-embed-text", true},
		{"embeddinggemma", false},
		{"embeddinggemma:300m", false},
		{"otro", false},
	}
	for _, tt := range tests {
		got, err := HasModel(context.Background(), srv.URL, tt.model)
		if err != nil || got != tt.want {
			t.Errorf("HasModel(%q) = (%v, %v), quería %v", tt.model, got, err, tt.want)
		}
	}

	failing := newAPI(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) })
	if _, err := HasModel(context.Background(), failing.URL, "x"); err == nil {
		t.Errorf("un 500 debería ser un error")
	}
}

// streamLines returns a handler that checks the request and writes the lines
// as an NDJSON stream.
func streamLines(t *testing.T, lines ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/pull" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model != "mi-modelo" || !req.Stream {
			t.Errorf("petición inesperada: %+v (err=%v)", req, err)
		}
		flusher, _ := w.(http.Flusher)
		for _, l := range lines {
			fmt.Fprintln(w, l)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func TestPullSuccess(t *testing.T) {
	srv := newAPI(t, streamLines(t,
		`{"status":"pulling manifest"}`,
		`{"status":"pulling abc","digest":"sha256:abc","total":100,"completed":40}`,
		`{"status":"pulling abc","digest":"sha256:abc","total":100,"completed":100}`,
		`{"status":"success"}`,
	))
	type ev struct {
		status      string
		done, total int64
	}
	var events []ev
	err := Pull(context.Background(), srv.URL, "mi-modelo", func(status string, done, total int64) {
		events = append(events, ev{status, done, total})
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("quería 4 eventos, hubo %d: %+v", len(events), events)
	}
	if events[1] != (ev{"pulling abc", 40, 100}) || events[3].status != "success" {
		t.Errorf("eventos inesperados: %+v", events)
	}
}

func TestPullNilProgress(t *testing.T) {
	srv := newAPI(t, streamLines(t, `{"status":"success"}`))
	if err := Pull(context.Background(), srv.URL, "mi-modelo", nil); err != nil {
		t.Fatalf("Pull: %v", err)
	}
}

func TestPullErrorLine(t *testing.T) {
	srv := newAPI(t, streamLines(t,
		`{"status":"pulling manifest"}`,
		`{"error":"pull model manifest: file does not exist"}`,
		`{"status":"success"}`,
	))
	err := Pull(context.Background(), srv.URL, "mi-modelo", nil)
	if err == nil || !strings.Contains(err.Error(), "file does not exist") {
		t.Fatalf("quería el error de Ollama, obtuve %v", err)
	}
}

func TestPullWithoutSuccess(t *testing.T) {
	srv := newAPI(t, streamLines(t,
		`{"status":"pulling manifest"}`,
		`{"status":"pulling abc","total":100,"completed":40}`,
	))
	err := Pull(context.Background(), srv.URL, "mi-modelo", nil)
	if err == nil || !strings.Contains(err.Error(), "success") {
		t.Fatalf("quería error por falta de success, obtuve %v", err)
	}
}

func TestPullTruncatedStream(t *testing.T) {
	srv := newAPI(t, streamLines(t, `{"status":"pulling manifest"}`, `{"status":"pulli`))
	if err := Pull(context.Background(), srv.URL, "mi-modelo", nil); err == nil {
		t.Fatal("un JSON cortado debería ser un error")
	}
}

func TestPullHTTPError(t *testing.T) {
	srv := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"nombre de modelo no válido"}`))
	})
	err := Pull(context.Background(), srv.URL, "mi-modelo", nil)
	if err == nil || !strings.Contains(err.Error(), "nombre de modelo no válido") {
		t.Fatalf("quería el mensaje del servidor, obtuve %v", err)
	}
}

func TestFindOllama(t *testing.T) {
	tests := []struct {
		name  string
		goos  string
		files []string
		want  string // relative to dir; empty means not found
	}{
		{"bin", "linux", []string{"bin/ollama", "lib/ollama/libx.so"}, "bin/ollama"},
		{"raíz", "linux", []string{"ollama"}, "ollama"},
		{"estructura rara de macOS", "darwin", []string{"GNUSparseFile.0/ollama", "otro"}, "GNUSparseFile.0/ollama"},
		{"windows", "windows", []string{"ollama.exe", "lib/ollama/x.dll"}, "ollama.exe"},
		{"solo el directorio lib/ollama", "linux", []string{"lib/ollama/libx.so"}, ""},
		{"ignora models", "linux", []string{"models/blobs/ollama"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tt.files {
				touch(t, filepath.Join(dir, filepath.FromSlash(f)))
			}
			got, err := findOllama(dir, tt.goos)
			if tt.want == "" {
				if !errors.Is(err, errNotFound) {
					t.Fatalf("quería errNotFound, obtuve (%q, %v)", got, err)
				}
				return
			}
			if err != nil || got != filepath.Join(dir, filepath.FromSlash(tt.want)) {
				t.Fatalf("findOllama = (%q, %v), quería %q", got, err, tt.want)
			}
		})
	}
}

func TestOllamaEnv(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "datos", "ollama")
	linux := ollamaEnv("linux", dir)
	if linux["OLLAMA_HOST"] != "127.0.0.1:11434" ||
		linux["OLLAMA_MODELS"] != filepath.Join(dir, "models") ||
		linux["LD_LIBRARY_PATH"] != filepath.Join(dir, "lib", "ollama") {
		t.Errorf("entorno de Linux inesperado: %v", linux)
	}
	for _, goos := range []string{"darwin", "windows"} {
		env := ollamaEnv(goos, dir)
		if _, ok := env["LD_LIBRARY_PATH"]; ok {
			t.Errorf("%s no debería definir LD_LIBRARY_PATH", goos)
		}
		if len(env) != 2 {
			t.Errorf("%s: entorno inesperado: %v", goos, env)
		}
	}
	if got := OllamaEnv(dir); got["OLLAMA_HOST"] != "127.0.0.1:11434" {
		t.Errorf("OllamaEnv = %v", got)
	}
}

// hostArchive builds an archive in the format used for the host platform.
func hostArchive(t *testing.T, asset string, entries []entry) []byte {
	t.Helper()
	switch {
	case strings.HasSuffix(asset, ".zip"):
		return buildZip(t, entries)
	case strings.HasSuffix(asset, ".tar.zst"):
		return buildTarZst(t, entries)
	default:
		return buildTgz(t, entries)
	}
}

func TestInstallOllama(t *testing.T) {
	asset, err := AssetFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("plataforma del host sin asset de Ollama: %v", err)
	}
	exeName := "ollama" + exeSuffix(runtime.GOOS)
	data := string(hostArchive(t, asset, []entry{
		{name: "bin/" + exeName, body: "binario", mode: 0o755},
		{name: "lib/ollama/libx.so", body: "lib", mode: 0o644},
	}))

	setup := func(t *testing.T, sumOf string) string {
		files := map[string]string{}
		files["/"+asset] = data
		files["/sha256sum.txt"] = sumHex(sumOf) + "  " + asset + "\n" + sumHex("x") + "  otro-asset\n"
		srv, _ := serveFiles(t, files)
		old := ollamaReleaseBase
		ollamaReleaseBase = srv.URL
		t.Cleanup(func() { ollamaReleaseBase = old })
		return filepath.Join(t.TempDir(), "ollama")
	}

	t.Run("correcto", func(t *testing.T) {
		dir := setup(t, data)
		var lastDone int64
		exe, err := InstallOllama(context.Background(), dir, func(done, total int64) { lastDone = done })
		if err != nil {
			t.Fatalf("InstallOllama: %v", err)
		}
		if want := filepath.Join(dir, "bin", exeName); exe != want {
			t.Errorf("exe = %q, quería %q", exe, want)
		}
		if got, _ := os.ReadFile(exe); string(got) != "binario" {
			t.Errorf("contenido = %q", got)
		}
		if lastDone != int64(len(data)) {
			t.Errorf("progreso final = %d, quería %d", lastDone, len(data))
		}
		if _, err := os.Stat(filepath.Join(dir, "download")); !os.IsNotExist(err) {
			t.Errorf("el archivo descargado debería haberse borrado")
		}
	})

	t.Run("suma incorrecta", func(t *testing.T) {
		dir := setup(t, "otra cosa")
		if _, err := InstallOllama(context.Background(), dir, nil); err == nil || !strings.Contains(err.Error(), "SHA-256") {
			t.Fatalf("quería error de SHA-256, obtuve %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "bin")); !os.IsNotExist(err) {
			t.Errorf("no se debe extraer nada si la suma no coincide")
		}
	})
}
