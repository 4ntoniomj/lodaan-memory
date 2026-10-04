package service

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func darwinTestSpec(logDir string) Spec {
	return Spec{
		Name:   "lodan",
		Exec:   "/usr/local/bin/lodan",
		Args:   []string{"service", "run"},
		User:   "antonio",
		Env:    map[string]string{"LODAN_DATA_DIR": "/Users/antonio/Library/Application Support/lodan"},
		LogDir: logDir,
	}
}

func newTestDarwinManager(t *testing.T, fr *fakeRunner) *darwinManager {
	t.Helper()
	return &darwinManager{
		plistDir:    t.TempDir(),
		run:         fr,
		pollEvery:   time.Millisecond,
		stopTimeout: time.Second,
	}
}

const (
	printCmd     = "launchctl print system/com.lodan.lodan"
	notFoundExit = 113
)

func TestRenderPlist(t *testing.T) {
	want := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.lodan.lodan</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/bin/lodan</string>
		<string>service</string>
		<string>run</string>
	</array>
	<key>UserName</key>
	<string>antonio</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>LODAN_DATA_DIR</key>
		<string>/Users/antonio/Library/Application Support/lodan</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>StandardOutPath</key>
	<string>/var/log/lodan/lodan.out.log</string>
	<key>StandardErrorPath</key>
	<string>/var/log/lodan/lodan.err.log</string>
	<key>ExitTimeOut</key>
	<integer>45</integer>
</dict>
</plist>
`
	if got := renderPlist(darwinTestSpec("/var/log/lodan")); got != want {
		t.Errorf("plist generado distinto.\n--- obtenido ---\n%s\n--- esperado ---\n%s", got, want)
	}
}

func TestRenderPlistEscapaXML(t *testing.T) {
	s := darwinTestSpec("")
	s.Args = []string{"--nombre", `a&b <c> "d"`}
	s.Env = map[string]string{"K": "x<y&z"}
	s.User = ""
	got := renderPlist(s)
	for _, want := range []string{
		"<string>a&amp;b &lt;c&gt; &#34;d&#34;</string>",
		"<string>x&lt;y&amp;z</string>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("falta %q en:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"UserName", "StandardOutPath", "StandardErrorPath"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("no debería aparecer %q sin usuario ni LogDir", unwanted)
		}
	}
}

func TestInstallEscribePlistYCargaElServicio(t *testing.T) {
	fr := newFakeRunner()
	fr.results[printCmd] = cmdResult{ExitCode: notFoundExit, Stderr: "Could not find service"}
	m := newTestDarwinManager(t, fr)
	s := darwinTestSpec(filepath.Join(t.TempDir(), "logs"))

	if err := m.Install(s); err != nil {
		t.Fatalf("Install: %v", err)
	}
	path := filepath.Join(m.plistDir, "com.lodan.lodan.plist")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no se escribió el plist: %v", err)
	}
	if string(data) != renderPlist(s) {
		t.Errorf("contenido del plist inesperado:\n%s", data)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
		t.Errorf("permisos del plist: %v, se esperaba 0644", info.Mode().Perm())
	}
	want := []string{
		printCmd,
		"launchctl enable system/com.lodan.lodan",
		"launchctl bootstrap system " + path,
	}
	if !slices.Equal(fr.calls, want) {
		t.Errorf("comandos: %q, se esperaba %q", fr.calls, want)
	}
}

func TestStopHaceBootout(t *testing.T) {
	fr := newFakeRunner()
	// First print: loaded. After bootout: gone.
	fr.queue[printCmd] = []cmdResult{{ExitCode: 0, Stdout: "state = running\n"}, {ExitCode: notFoundExit}}
	m := newTestDarwinManager(t, fr)

	if err := m.Stop("lodan"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	want := []string{printCmd, "launchctl bootout system/com.lodan.lodan", printCmd}
	if !slices.Equal(fr.calls, want) {
		t.Errorf("comandos: %q, se esperaba %q", fr.calls, want)
	}
}

func TestStopSinCargarNoHaceNada(t *testing.T) {
	fr := newFakeRunner()
	fr.results[printCmd] = cmdResult{ExitCode: notFoundExit}
	m := newTestDarwinManager(t, fr)
	if err := m.Stop("lodan"); err != nil {
		t.Fatal(err)
	}
	if want := []string{printCmd}; !slices.Equal(fr.calls, want) {
		t.Errorf("comandos: %q, se esperaba %q", fr.calls, want)
	}
}

func TestStartCargaOReinicia(t *testing.T) {
	fr := newFakeRunner()
	fr.results[printCmd] = cmdResult{ExitCode: notFoundExit}
	m := newTestDarwinManager(t, fr)
	path := filepath.Join(m.plistDir, "com.lodan.lodan.plist")

	if err := m.Start("lodan"); err == nil {
		t.Error("sin plist, Start debería fallar")
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fr.calls = nil
	if err := m.Start("lodan"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if want := []string{printCmd, "launchctl bootstrap system " + path}; !slices.Equal(fr.calls, want) {
		t.Errorf("comandos: %q, se esperaba %q", fr.calls, want)
	}

	// Already loaded: kickstart, no bootstrap.
	fr.results[printCmd] = cmdResult{ExitCode: 0, Stdout: "state = running\n"}
	fr.calls = nil
	if err := m.Start("lodan"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if want := []string{printCmd, "launchctl kickstart system/com.lodan.lodan"}; !slices.Equal(fr.calls, want) {
		t.Errorf("comandos: %q, se esperaba %q", fr.calls, want)
	}
}

func TestUninstallHaceBootoutYBorraElPlist(t *testing.T) {
	fr := newFakeRunner()
	fr.queue[printCmd] = []cmdResult{{ExitCode: 0, Stdout: "state = running\n"}, {ExitCode: notFoundExit}}
	m := newTestDarwinManager(t, fr)
	path := filepath.Join(m.plistDir, "com.lodan.lodan.plist")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall("lodan"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("el plist debería haberse borrado (err=%v)", err)
	}
	want := []string{printCmd, "launchctl bootout system/com.lodan.lodan", printCmd}
	if !slices.Equal(fr.calls, want) {
		t.Errorf("comandos: %q, se esperaba %q", fr.calls, want)
	}
}

func TestEnableDisable(t *testing.T) {
	fr := newFakeRunner()
	m := newTestDarwinManager(t, fr)
	if err := m.Enable("lodan"); err != nil {
		t.Fatal(err)
	}
	if err := m.Disable("lodan"); err != nil {
		t.Fatal(err)
	}
	want := []string{"launchctl enable system/com.lodan.lodan", "launchctl disable system/com.lodan.lodan"}
	if !slices.Equal(fr.calls, want) {
		t.Errorf("comandos: %q, se esperaba %q", fr.calls, want)
	}
}

func TestStatus(t *testing.T) {
	const printDisabled = "launchctl print-disabled system"
	tests := []struct {
		name        string
		print       cmdResult
		disabled    string
		withPlist   bool
		wantState   State
		wantEnabled bool
	}{
		{"en marcha", cmdResult{Stdout: "com.lodan.lodan = {\n\tstate = running\n\tpid = 123\n}\n"},
			"disabled services = {\n}\n", true, StateRunning, true},
		{"cargado sin ejecutar", cmdResult{Stdout: "com.lodan.lodan = {\n\tstate = not running\n}\n"},
			"disabled services = {\n}\n", true, StateStopped, true},
		{"no cargado con plist", cmdResult{ExitCode: notFoundExit, Stderr: "Could not find service"},
			"disabled services = {\n\t\"com.lodan.lodan\" => disabled\n}\n", true, StateStopped, false},
		{"no instalado", cmdResult{ExitCode: notFoundExit, Stderr: "Could not find service"},
			"disabled services = {\n}\n", false, StateNotInstalled, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := newFakeRunner()
			fr.results[printCmd] = tt.print
			fr.results[printDisabled] = cmdResult{Stdout: tt.disabled}
			m := newTestDarwinManager(t, fr)
			if tt.withPlist {
				if err := os.WriteFile(filepath.Join(m.plistDir, "com.lodan.lodan.plist"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			st, err := m.Status("lodan")
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if st.State != tt.wantState || st.Enabled != tt.wantEnabled {
				t.Errorf("Status = {%s, enabled=%v}, se esperaba {%s, enabled=%v}", st.State, st.Enabled, tt.wantState, tt.wantEnabled)
			}
		})
	}
}
