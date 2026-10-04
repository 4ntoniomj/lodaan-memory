package install

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// extractArchive unpacks a .tar.zst, .tgz/.tar.gz or .zip archive into dest,
// rejecting any entry that would land outside dest.
func extractArchive(archive, dest string) error {
	dest = filepath.Clean(dest)
	lower := strings.ToLower(archive)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractZip(archive, dest)
	case strings.HasSuffix(lower, ".tar.zst"):
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		dec, err := zstd.NewReader(f)
		if err != nil {
			return fmt.Errorf("no se pudo abrir el archivo zstd: %w", err)
		}
		defer dec.Close()
		return extractTar(dec, dest)
	case strings.HasSuffix(lower, ".tgz"), strings.HasSuffix(lower, ".tar.gz"):
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("no se pudo abrir el archivo gzip: %w", err)
		}
		defer gz.Close()
		return extractTar(gz, dest)
	}
	return fmt.Errorf("formato de archivo no soportado: %s", filepath.Base(archive))
}

func extractTar(r io.Reader, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if errors.Is(err, tar.ErrInsecurePath) {
			// Only raised when GODEBUG=tarinsecurepath=0; same outcome as safeJoin.
			return fmt.Errorf("entrada insegura en el archivo: %w", err)
		}
		if err != nil {
			return fmt.Errorf("archivo tar dañado: %w", err)
		}
		target, err := safeJoin(dest, hdr.Name)
		if err != nil {
			return err
		}
		if target == dest {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = makeDir(dest, target)
		case tar.TypeReg, tar.TypeGNUSparse:
			err = writeFile(dest, target, tr, hdr.FileInfo().Mode().Perm())
		case tar.TypeSymlink:
			err = makeSymlink(dest, target, hdr.Linkname)
		case tar.TypeLink:
			err = makeHardlink(dest, target, hdr.Linkname)
		default:
			// Devices, FIFOs and global PAX headers are ignored.
		}
		if err != nil {
			return err
		}
	}
}

func extractZip(archive, dest string) error {
	zr, err := zip.OpenReader(archive)
	if errors.Is(err, zip.ErrInsecurePath) {
		// Only raised when GODEBUG=zipinsecurepath=0; same outcome as safeJoin.
		if zr != nil {
			_ = zr.Close()
		}
		return fmt.Errorf("entrada insegura en el archivo: %w", err)
	}
	if err != nil {
		return fmt.Errorf("archivo zip dañado: %w", err)
	}
	defer zr.Close()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for _, f := range zr.File {
		// Zip names use "/", but some writers emit "\": normalize before validating.
		name := strings.ReplaceAll(f.Name, `\`, "/")
		target, err := safeJoin(dest, name)
		if err != nil {
			return err
		}
		if target == dest {
			continue
		}
		mode := f.Mode()
		switch {
		case f.FileInfo().IsDir():
			err = makeDir(dest, target)
		case mode&fs.ModeSymlink != 0 || !mode.IsRegular():
			// Symlinks and special files are not expected in these archives.
			continue
		default:
			err = func() error {
				rc, err := f.Open()
				if err != nil {
					return err
				}
				defer rc.Close()
				return writeFile(dest, target, rc, mode.Perm())
			}()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// safeJoin joins an archive entry name to dest, failing if the result would
// escape dest (absolute paths, volume names, ".." components).
func safeJoin(dest, name string) (string, error) {
	unsafe := fmt.Errorf("entrada insegura en el archivo (sale del directorio de destino): %q", name)
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return "", unsafe
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" {
		return "", unsafe
	}
	if clean == "." {
		return dest, nil
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", unsafe
	}
	return filepath.Join(dest, clean), nil
}

// within reports whether p is dest or lives under it (lexically).
func within(dest, p string) bool {
	rel, err := filepath.Rel(dest, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ensureNoSymlinkParents fails if any existing parent directory of target
// (below dest) is a symlink, so extraction never writes through a link.
func ensureNoSymlinkParents(dest, target string) error {
	rel, err := filepath.Rel(dest, filepath.Dir(target))
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	cur := dest
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("entrada insegura en el archivo: %s pasa por un enlace simbólico", target)
		}
	}
	return nil
}

func makeDir(dest, target string) error {
	if err := ensureNoSymlinkParents(dest, target); err != nil {
		return err
	}
	return os.MkdirAll(target, 0o755)
}

// removeExisting deletes a pre-existing non-directory at target. Removing
// (instead of truncating) avoids following a symlink and "text file busy"
// errors on a running binary.
func removeExisting(target string) error {
	fi, err := os.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("no se puede sustituir el directorio %s por un archivo", target)
	}
	return os.Remove(target)
}

// writeFile writes a regular file preserving the permission bits (which keeps
// the executable flag). setuid/setgid bits are never applied.
func writeFile(dest, target string, src io.Reader, perm fs.FileMode) error {
	if err := ensureNoSymlinkParents(dest, target); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := removeExisting(target); err != nil {
		return err
	}
	if perm == 0 {
		perm = 0o644
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, src); err != nil {
		_ = f.Close()
		return fmt.Errorf("no se pudo extraer %s: %w", target, err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	// The umask may have stripped bits at creation.
	return os.Chmod(target, perm)
}

func makeSymlink(dest, target, link string) error {
	if link == "" || strings.HasPrefix(link, "/") || strings.HasPrefix(link, `\`) ||
		filepath.IsAbs(link) || filepath.VolumeName(link) != "" {
		return fmt.Errorf("enlace simbólico inseguro en el archivo: %s -> %q", target, link)
	}
	resolved := filepath.Join(filepath.Dir(target), filepath.FromSlash(link))
	if !within(dest, resolved) {
		return fmt.Errorf("enlace simbólico inseguro en el archivo (sale del destino): %s -> %q", target, link)
	}
	if err := ensureNoSymlinkParents(dest, target); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := removeExisting(target); err != nil {
		return err
	}
	return os.Symlink(link, target)
}

func makeHardlink(dest, target, linkname string) error {
	src, err := safeJoin(dest, linkname)
	if err != nil {
		return err
	}
	if err := ensureNoSymlinkParents(dest, src); err != nil {
		return err
	}
	if err := ensureNoSymlinkParents(dest, target); err != nil {
		return err
	}
	fi, err := os.Lstat(src)
	if err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("enlace duro inseguro o roto en el archivo: %s -> %q", target, linkname)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := removeExisting(target); err != nil {
		return err
	}
	if err := os.Link(src, target); err == nil {
		return nil
	}
	// Some filesystems do not support hard links: fall back to a copy.
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeFile(dest, target, in, fi.Mode().Perm())
}
