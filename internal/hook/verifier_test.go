package hook

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbeKeyLearningsFilter(t *testing.T) {
	cases := map[string]bool{ // item -> want saved
		"The migration needs a pg_dump before running on the real database":   true,
		"password: hunter2 is used for the staging database server":           false,
		"API_KEY=abcd1234efgh5678 lets the worker reach the provider":         false,
		"Use AGE-SECRET-KEY-1QQQ to decrypt the repository files today":       false,
		"The user DNI is 12345678Z and it was stored in the form":             false,
		"The NIE X1234567L belongs to the tenant of the flat":                 false,
		"The GitHub token ghp_ABCDEFGHIJKLMNOPQRSTUVWX123 grants push access": false,
		// realistic prose forms (advisory if saved)
		"La contraseña del wifi de casa es Hunter2024 para invitados":    false,
		"The database password is s3cr3tP4ss for the staging server":     false,
		"The DNI 12345678-Z belongs to the user and goes in the form":    false,
		"The AWS key AKIAIOSFODNN7EXAMPLE is used by the backup job":     false,
		"Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc is the session": false,
	}
	for item, want := range cases {
		got := ExtractKeyLearnings("text\n## Key Learnings\n1. " + item + "\n")
		if (len(got) == 1) != want {
			t.Errorf("FILTER MISMATCH saved=%v want=%v: %q", len(got) == 1, want, item)
		}
	}
	// at most 5, last block, stops at next heading
	msg := "## Key Learnings\n1. first old block item with enough words here\n\n## Key Learnings\n" +
		"1. one item that is long enough to count\n2) two item that is long enough to count\n" +
		"3. three item that is long enough to count\n4. four item that is long enough to count\n" +
		"5. five item that is long enough to count\n6. six item that is long enough to count\n## Other\n7. seven no"
	got := ExtractKeyLearnings(msg)
	if len(got) != 5 || !strings.HasPrefix(got[0], "one") || !strings.HasPrefix(got[4], "five") {
		t.Errorf("got %q", got)
	}
	if got := ExtractKeyLearnings("1. short\n2. no heading at all in this message here"); len(got) != 0 {
		t.Errorf("no block -> %q", got)
	}
	if got := ExtractKeyLearnings("## Key Learnings\r\n1. windows line endings item with words\r\n"); len(got) != 1 {
		t.Errorf("CRLF -> %q", got)
	}
}

func TestProbeTitleAndTopic(t *testing.T) {
	long := strings.Repeat("palabra ", 20) + "final"
	ti := title(long)
	if r := []rune(ti); len(r) > 81 || !strings.HasSuffix(ti, "…") {
		t.Errorf("title %q (%d runes)", ti, len(r))
	}
	if ti := title(strings.Repeat("ñ", 100)); len([]rune(ti)) != 81 {
		t.Errorf("no-space title %d runes", len([]rune(ti)))
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "mi-proyecto")
	sub := filepath.Join(repo, "a", "b")
	_ = os.MkdirAll(sub, 0o755)
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if got := ProjectTopic(sub); got != "mi-proyecto" {
		t.Errorf("ProjectTopic(subdir of repo) = %q", got)
	}
	plain := filepath.Join(dir, "carpeta suelta")
	_ = os.MkdirAll(plain, 0o755)
	if got := ProjectTopic(plain); got != "carpeta suelta" {
		t.Errorf("ProjectTopic(plain) = %q", got)
	}
	if got := ProjectTopic(filepath.Join(dir, "no-existe")); got != "no-existe" {
		t.Errorf("ProjectTopic(missing) = %q", got)
	}
}
