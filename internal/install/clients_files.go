package install

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// backupSuffix is appended to the original file the first time lodan modifies it.
const backupSuffix = ".bak-lodan"

// WarningError reports a situation that is not a failure but that the caller
// must show to the user as a warning (for example, a file that lodan refuses
// to touch because it cannot edit it safely).
type WarningError struct {
	Msg string
}

func (e *WarningError) Error() string { return e.Msg }

// IsWarning reports whether err is (or wraps) a *WarningError.
func IsWarning(err error) bool {
	var w *WarningError
	return errors.As(err, &w)
}

// backupFirstTime copies path to path+".bak-lodan" unless the copy already
// exists (so the oldest original is kept). It does nothing if path does not exist.
func backupFirstTime(path string) error {
	bak := path + backupSuffix
	if _, err := os.Lstat(bak); err == nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	return os.WriteFile(bak, data, mode)
}

// writeAtomicPreserving writes data to a temporary file in the same folder and
// renames it over path. It keeps the permissions of the existing file (newPerm
// is used when it does not exist) and follows a symlink to write to its target.
func writeAtomicPreserving(path string, data []byte, newPerm os.FileMode) error {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	}
	perm := newPerm
	if st, err := os.Stat(target); err == nil {
		perm = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".lodan-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// editTextFile reads path (a missing file counts as empty), applies transform
// and, if the result differs, writes it atomically after keeping the first-time
// backup. With dryRun nothing is written. With removeIfEmpty a result that is
// only whitespace deletes the file instead (used for files lodan created).
func editTextFile(path string, newPerm os.FileMode, removeIfEmpty, dryRun bool, transform func(old string) (string, error)) (bool, error) {
	existed := true
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		existed = false
	}
	old := string(raw)
	updated, err := transform(old)
	if err != nil {
		return false, err
	}
	if updated == old {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	if removeIfEmpty && existed && strings.TrimSpace(updated) == "" {
		return true, os.Remove(path)
	}
	if existed {
		if err := backupFirstTime(path); err != nil {
			return false, err
		}
	}
	if err := writeAtomicPreserving(path, []byte(updated), newPerm); err != nil {
		return false, err
	}
	return true, nil
}

// splitLinesKeep splits text into lines keeping their line terminators.
func splitLinesKeep(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// detectEOL returns "\r\n" when the text uses Windows line endings.
func detectEOL(text string) string {
	if strings.Contains(text, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

func lineIsBlank(line string) bool {
	return strings.TrimSpace(line) == ""
}

func endsWithBlankLine(text string) bool {
	lines := splitLinesKeep(text)
	return len(lines) > 0 && lineIsBlank(lines[len(lines)-1])
}

func startsWithBlankLine(text string) bool {
	lines := splitLinesKeep(text)
	return len(lines) > 0 && lineIsBlank(lines[0])
}

// spliceLines returns lines with [start, end) replaced by ins.
func spliceLines(lines []string, start, end int, ins []string) []string {
	out := make([]string, 0, len(lines)-(end-start)+len(ins))
	out = append(out, lines[:start]...)
	out = append(out, ins...)
	return append(out, lines[end:]...)
}

// removeLines removes [start, end). If that leaves a blank line before the
// removed range followed by another blank line (or the end of the file), the
// blank line before is dropped too, so that removing a block that was appended
// with a blank separator restores the original text.
func removeLines(lines []string, start, end int) []string {
	out := spliceLines(lines, start, end, nil)
	prev := start - 1
	if prev >= 0 && lineIsBlank(out[prev]) && (start >= len(out) || lineIsBlank(out[start])) {
		out = append(out[:prev], out[prev+1:]...)
	}
	return out
}
