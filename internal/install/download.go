package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// dialTimeout bounds the connection phase only; downloads are large and
	// have no overall timeout.
	dialTimeout = 30 * time.Second
	// progressInterval throttles the progress callback.
	progressInterval = 150 * time.Millisecond
	// maxSmallBody caps small text resources (.sha256, sha256sum.txt).
	maxSmallBody = 1 << 20
)

// httpClient is used for downloads: timeouts for connecting and for the
// response headers, but none for the body.
var httpClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	},
}

// download fetches url into dest and verifies its SHA-256 (hex, case
// insensitive). It writes to dest+".part" and renames on success, so dest is
// never left half written. If dest already exists with the expected hash, no
// download happens. progress may be nil; total is < 0 when the size is unknown.
func download(ctx context.Context, url, dest, wantSHA256 string, progress func(done, total int64)) error {
	want := strings.ToLower(strings.TrimSpace(wantSHA256))
	if !isHexSHA256(want) {
		return fmt.Errorf("la suma SHA-256 esperada no es válida: %q", wantSHA256)
	}

	// Already downloaded and intact: nothing to do.
	if got, err := fileSHA256(dest); err == nil && got == want {
		if progress != nil {
			if st, err := os.Stat(dest); err == nil {
				progress(st.Size(), st.Size())
			}
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("no se pudo crear el directorio de descarga: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("URL de descarga no válida %q: %w", url, err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("no se pudo descargar %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("no se pudo descargar %s: respuesta HTTP %s", url, resp.Status)
	}

	part := dest + ".part"
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(part)
		}
	}()

	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("no se pudo crear %s: %w", part, err)
	}
	h := sha256.New()
	pw := &progressWriter{total: resp.ContentLength, fn: progress}
	if _, err := io.Copy(io.MultiWriter(f, h, pw), resp.Body); err != nil {
		_ = f.Close()
		return fmt.Errorf("descarga interrumpida de %s: %w", url, err)
	}
	pw.finish()
	if err := f.Close(); err != nil {
		return fmt.Errorf("no se pudo escribir %s: %w", part, err)
	}

	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("la suma SHA-256 de %s no coincide (esperada %s, obtenida %s)", url, want, got)
	}
	if err := os.Rename(part, dest); err != nil {
		return fmt.Errorf("no se pudo mover la descarga a %s: %w", dest, err)
	}
	ok = true
	return nil
}

// progressWriter counts written bytes and reports them, throttled.
type progressWriter struct {
	total int64
	done  int64
	fn    func(done, total int64)
	last  time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if p.fn != nil && time.Since(p.last) >= progressInterval {
		p.last = time.Now()
		p.fn(p.done, p.total)
	}
	return len(b), nil
}

// finish always reports the final state.
func (p *progressWriter) finish() {
	if p.fn != nil {
		p.fn(p.done, p.total)
	}
}

// fileSHA256 returns the hex SHA-256 of a file.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fetchText downloads a small text resource.
func fetchText(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("URL no válida %q: %w", url, err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("no se pudo descargar %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("no se pudo descargar %s: respuesta HTTP %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSmallBody))
	if err != nil {
		return "", fmt.Errorf("no se pudo leer %s: %w", url, err)
	}
	return string(body), nil
}

// fetchSHA256 reads a .sha256 file. It accepts both "<hash>" and
// "<hash>  <name>" (also the "*name" binary marker).
func fetchSHA256(ctx context.Context, url string) (string, error) {
	body, err := fetchText(ctx, url)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		h := strings.ToLower(fields[0])
		if !isHexSHA256(h) {
			return "", fmt.Errorf("el archivo %s no contiene una suma SHA-256 válida", url)
		}
		return h, nil
	}
	return "", fmt.Errorf("el archivo %s está vacío", url)
}

// shaFromSumFile finds the hash of asset in the contents of a sha256sum.txt
// ("<hash>  <name>" per line, name optionally prefixed by "*" or "./").
func shaFromSumFile(body, asset string) (string, error) {
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(fields[1], "*"), "./")
		if name != asset {
			continue
		}
		h := strings.ToLower(fields[0])
		if !isHexSHA256(h) {
			return "", fmt.Errorf("la suma de %s en sha256sum.txt no es válida", asset)
		}
		return h, nil
	}
	return "", fmt.Errorf("sha256sum.txt no incluye %s", asset)
}

// isHexSHA256 reports whether s is a 64-character lowercase or uppercase hex string.
func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// exeSuffix returns ".exe" on Windows and "" elsewhere.
func exeSuffix(goos string) string {
	if goos == "windows" {
		return ".exe"
	}
	return ""
}

// errNotFound is a sentinel for "file not found" results of local searches.
var errNotFound = errors.New("no encontrado")
