package install

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// entry describes one archive member for the test builders.
type entry struct {
	name string
	body string
	mode int64
	typ  byte // tar type flag; 0 means regular file
	link string
}

func buildTar(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.name, Typeflag: typ, Mode: e.mode, Linkname: e.link}
		if typ == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildTgz(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(buildTar(t, entries)); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildTarZst(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(buildTar(t, entries)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// buildZip ignores symlink/link entries (zip archives here only hold files and dirs).
func buildZip(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		mode := fs.FileMode(e.mode)
		if e.typ == tar.TypeDir {
			mode |= fs.ModeDir
		}
		hdr.SetMode(mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if e.typ != tar.TypeDir {
			if _, err := w.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// archiveKinds are the formats under test; each builds an archive from entries.
var archiveKinds = []struct {
	ext   string
	build func(*testing.T, []entry) []byte
}{
	{".tar.zst", buildTarZst},
	{".tgz", buildTgz},
	{".zip", buildZip},
}

func writeArchive(t *testing.T, ext string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "paquete"+ext)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractArchive(t *testing.T) {
	entries := []entry{
		{name: "bin/", typ: tar.TypeDir, mode: 0o755},
		{name: "bin/ollama", body: "ejecutable", mode: 0o755},
		{name: "./lib/ollama/libx.so", body: "libreria", mode: 0o644},
		{name: "LICENSE", body: "texto", mode: 0o644},
	}
	for _, k := range archiveKinds {
		t.Run(k.ext, func(t *testing.T) {
			archive := writeArchive(t, k.ext, k.build(t, entries))
			dest := filepath.Join(t.TempDir(), "destino")
			if err := extractArchive(archive, dest); err != nil {
				t.Fatalf("extractArchive: %v", err)
			}
			want := [][2]string{
				{"bin/ollama", "ejecutable"},
				{"lib/ollama/libx.so", "libreria"},
				{"LICENSE", "texto"},
			}
			for _, w := range want {
				got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(w[0])))
				if err != nil || string(got) != w[1] {
					t.Errorf("%s = %q (err=%v), quería %q", w[0], got, err, w[1])
				}
			}
			if runtime.GOOS != "windows" {
				st, err := os.Stat(filepath.Join(dest, "bin", "ollama"))
				if err != nil {
					t.Fatal(err)
				}
				if st.Mode().Perm()&0o111 == 0 {
					t.Errorf("bin/ollama perdió el permiso de ejecución: %v", st.Mode())
				}
			}
			// Extracting twice over the same destination must work (overwrite).
			if err := extractArchive(archive, dest); err != nil {
				t.Errorf("segunda extracción: %v", err)
			}
		})
	}
}

func TestExtractArchivePathTraversal(t *testing.T) {
	for _, name := range []string{"../evil.txt", "a/../../evil.txt", "/tmp/evil-abs.txt"} {
		for _, k := range archiveKinds {
			t.Run(k.ext+"/"+name, func(t *testing.T) {
				root := t.TempDir()
				dest := filepath.Join(root, "destino")
				archive := writeArchive(t, k.ext, k.build(t, []entry{
					{name: "ok.txt", body: "bien", mode: 0o644},
					{name: name, body: "mal", mode: 0o644},
				}))
				err := extractArchive(archive, dest)
				if err == nil || !strings.Contains(err.Error(), "insegura") {
					t.Fatalf("quería error de entrada insegura, obtuve %v", err)
				}
				if _, err := os.Stat(filepath.Join(root, "evil.txt")); !os.IsNotExist(err) {
					t.Errorf("se escribió fuera del destino")
				}
			})
		}
	}
}

func TestExtractTarSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("los enlaces simbólicos requieren privilegios en Windows")
	}
	t.Run("relativo válido", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "d")
		archive := writeArchive(t, ".tgz", buildTgz(t, []entry{
			{name: "lib/libx.so.1", body: "datos", mode: 0o644},
			{name: "lib/libx.so", typ: tar.TypeSymlink, link: "libx.so.1"},
			{name: "lib/hard", typ: tar.TypeLink, link: "lib/libx.so.1"},
		}))
		if err := extractArchive(archive, dest); err != nil {
			t.Fatal(err)
		}
		if got, err := os.Readlink(filepath.Join(dest, "lib", "libx.so")); err != nil || got != "libx.so.1" {
			t.Errorf("Readlink = %q, %v", got, err)
		}
		if got, err := os.ReadFile(filepath.Join(dest, "lib", "hard")); err != nil || string(got) != "datos" {
			t.Errorf("enlace duro = %q, %v", got, err)
		}
	})

	bad := []struct {
		name    string
		entries []entry
	}{
		{"destino fuera", []entry{{name: "l", typ: tar.TypeSymlink, link: "../../etc/passwd"}}},
		{"destino absoluto", []entry{{name: "l", typ: tar.TypeSymlink, link: "/etc/passwd"}}},
		{"escritura a través de enlace", []entry{
			{name: "sub/", typ: tar.TypeDir, mode: 0o755},
			{name: "l", typ: tar.TypeSymlink, link: "sub"},
			{name: "l/x", body: "mal", mode: 0o644},
		}},
		{"enlace duro fuera", []entry{{name: "h", typ: tar.TypeLink, link: "../fuera"}}},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "d")
			archive := writeArchive(t, ".tgz", buildTgz(t, tt.entries))
			if err := extractArchive(archive, dest); err == nil {
				t.Fatal("quería error")
			}
		})
	}
}

func TestExtractArchiveUnsupportedFormat(t *testing.T) {
	if err := extractArchive(filepath.Join(t.TempDir(), "x.rar"), t.TempDir()); err == nil {
		t.Fatal("quería error de formato no soportado")
	}
}

func TestSafeJoin(t *testing.T) {
	dest := filepath.Clean("/base/dest")
	tests := []struct {
		name    string
		want    string
		wantErr bool
	}{
		{"a/b", filepath.Join(dest, "a", "b"), false},
		{"./a", filepath.Join(dest, "a"), false},
		{"a/../b", filepath.Join(dest, "b"), false},
		{"./", dest, false},
		{"..", "", true},
		{"../x", "", true},
		{"a/../../x", "", true},
		{"/etc/passwd", "", true},
		{`\windows\x`, "", true},
	}
	for _, tt := range tests {
		got, err := safeJoin(dest, tt.name)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("safeJoin(%q) = (%q, %v), quería (%q, err=%v)", tt.name, got, err, tt.want, tt.wantErr)
		}
	}
}
