package install

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// elevatedLogName is the file, inside the logs directory, where `lodan service install` and
	// `lodan service uninstall` write their output when they run elevated on Windows.
	elevatedLogName = "service-elevated.log"
	// elevatedLogLines is how many trailing lines of that file go into an error message.
	elevatedLogLines = 20
	// elevatedLogMaxBytes bounds how much of the end of the file is read.
	elevatedLogMaxBytes = 64 << 10
)

// elevatedLogFile returns the log file of the elevated step, or "" when it is not used. Only
// Windows needs it: the elevated process runs in its own window, which closes when it ends and
// takes its output along. With sudo the output already reaches the terminal, and a file created
// by root inside the data directory of the user would be a nuisance.
func (in *Installer) elevatedLogFile() string {
	if in.Env.GOOS != "windows" {
		return ""
	}
	return filepath.Join(in.Cfg.LogsDir(), elevatedLogName)
}

// resetElevatedLog deletes the log of a previous run, so that what is read later belongs to the
// run that is about to start. Does nothing for an empty path.
func resetElevatedLog(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

// tailLines returns the last n non-empty lines of the file at path, or "" if it cannot be read or
// has none. Only the last elevatedLogMaxBytes bytes are read.
func tailLines(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	truncated := false
	if st, err := f.Stat(); err == nil && st.Size() > elevatedLogMaxBytes {
		if _, err := f.Seek(-elevatedLogMaxBytes, io.SeekEnd); err == nil {
			truncated = true
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	raw := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if truncated && len(raw) > 0 {
		raw = raw[1:] // the first line was cut in the middle
	}
	var lines []string
	for _, l := range raw {
		if l = strings.TrimRight(l, " \t\r"); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// elevatedLogText returns the end of the log of the elevated step, indented and headed by its
// path, ready to be appended to a message. It is "" when path is empty or the file has no
// text, which is also the case if the elevated process never got to write.
func elevatedLogText(path string) string {
	if path == "" {
		return ""
	}
	tail := tailLines(path, elevatedLogLines)
	if tail == "" {
		return ""
	}
	return fmt.Sprintf("salida del paso con administrador (%s):\n    %s", path, strings.ReplaceAll(tail, "\n", "\n    "))
}

// withElevatedLog adds the log of the elevated step to err, if there is one.
func withElevatedLog(err error, path string) error {
	text := elevatedLogText(path)
	if text == "" {
		return err
	}
	return fmt.Errorf("%w\n  %s", err, text)
}
