package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func sumHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// serveFiles serves the given path -> body map and counts requests.
func serveFiles(t *testing.T, files map[string]string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestDownloadOK(t *testing.T) {
	body := strings.Repeat("contenido de prueba ", 1000)
	srv, _ := serveFiles(t, map[string]string{"/f": body})
	dest := filepath.Join(t.TempDir(), "sub", "f.bin")

	var lastDone, lastTotal int64
	err := download(context.Background(), srv.URL+"/f", dest, strings.ToUpper(sumHex(body)), func(done, total int64) {
		lastDone, lastTotal = done, total
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != body {
		t.Fatalf("contenido incorrecto (err=%v)", err)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Errorf("el archivo .part debería haberse renombrado")
	}
	if lastDone != int64(len(body)) || lastTotal != int64(len(body)) {
		t.Errorf("progreso final = %d/%d, quería %d/%d", lastDone, lastTotal, len(body), len(body))
	}
}

func TestDownloadBadSHA(t *testing.T) {
	srv, _ := serveFiles(t, map[string]string{"/f": "hola"})
	dest := filepath.Join(t.TempDir(), "f.bin")

	err := download(context.Background(), srv.URL+"/f", dest, sumHex("otra cosa"), nil)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("quería error de SHA-256, obtuve %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest no debería existir")
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part no debería quedar tras un fallo")
	}
}

func TestDownloadInvalidExpectedSHA(t *testing.T) {
	if err := download(context.Background(), "http://127.0.0.1:1/x", filepath.Join(t.TempDir(), "x"), "", nil); err == nil {
		t.Fatal("una suma vacía debería ser un error")
	}
}

func TestDownloadSkipsWhenPresent(t *testing.T) {
	srv, hits := serveFiles(t, map[string]string{"/f": "hola"})
	dest := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(dest, []byte("hola"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := download(context.Background(), srv.URL+"/f", dest, sumHex("hola"), nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("no debería haber peticiones, hubo %d", n)
	}
}

func TestDownloadReplacesCorruptDestAndStalePart(t *testing.T) {
	srv, hits := serveFiles(t, map[string]string{"/f": "hola"})
	dest := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(dest, []byte("corrupto"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest+".part", []byte("basura previa mucho más larga que el contenido"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := download(context.Background(), srv.URL+"/f", dest, sumHex("hola"), nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "hola" {
		t.Errorf("contenido = %q", got)
	}
	if hits.Load() != 1 {
		t.Errorf("quería 1 petición, hubo %d", hits.Load())
	}
}

func TestDownloadHTTPError(t *testing.T) {
	srv, _ := serveFiles(t, nil)
	dest := filepath.Join(t.TempDir(), "f.bin")
	err := download(context.Background(), srv.URL+"/no-existe", dest, sumHex("x"), nil)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("quería error 404, obtuve %v", err)
	}
}

func TestFetchSHA256(t *testing.T) {
	h := sumHex("x")
	tests := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{"solo hash", h + "\n", h, false},
		{"hash y nombre", h + "  micromamba-linux-64\n", h, false},
		{"hash y nombre binario", h + " *micromamba\n", h, false},
		{"mayúsculas", strings.ToUpper(h), h, false},
		{"línea vacía previa", "\n\n" + h + "\n", h, false},
		{"no es un hash", "<html>error</html>\n", "", true},
		{"vacío", "\n", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := serveFiles(t, map[string]string{"/a.sha256": tt.body})
			got, err := fetchSHA256(context.Background(), srv.URL+"/a.sha256")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("hash = %q, quería %q", got, tt.want)
			}
		})
	}
}

func TestShaFromSumFile(t *testing.T) {
	a, b := sumHex("a"), sumHex("b")
	body := a + "  ollama-linux-amd64.tar.zst\n" + b + " *ollama-darwin.tgz\n" + a + "  ./ollama-windows-amd64.zip\nbasura\n"
	tests := []struct {
		asset   string
		want    string
		wantErr bool
	}{
		{"ollama-linux-amd64.tar.zst", a, false},
		{"ollama-darwin.tgz", b, false},
		{"ollama-windows-amd64.zip", a, false},
		{"ollama-linux-arm64.tar.zst", "", true},
	}
	for _, tt := range tests {
		got, err := shaFromSumFile(body, tt.asset)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("%s: got (%q, %v), quería (%q, err=%v)", tt.asset, got, err, tt.want, tt.wantErr)
		}
	}
}
