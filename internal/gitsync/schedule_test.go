package gitsync_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lodan/internal/gitsync"
)

const intervaloInvalido = "el intervalo debe estar entre 5 y 1440 minutos"

type fakeRun struct {
	calls  []string
	failOn map[string]error
}

func (f *fakeRun) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	for prefix, err := range f.failOn {
		if strings.HasPrefix(line, prefix) {
			return nil, err
		}
	}
	return nil, nil
}

func newSched(t *testing.T, goos, bin string, f *fakeRun) (gitsync.Scheduler, string) {
	t.Helper()
	home := t.TempDir()
	return gitsync.Scheduler{GOOS: goos, Home: home, Bin: bin, Run: f.run}, home
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSchedulerTaskName(t *testing.T) {
	if gitsync.TaskName != "lodan-sync" {
		t.Errorf("TaskName = %q", gitsync.TaskName)
	}
}

// S8: Windows registers the task with schtasks, exact arguments, /tr as one argument.
func TestSchedulerWindowsSchedule(t *testing.T) {
	var got []string
	n := 0
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		n++
		got = append([]string{name}, args...)
		return nil, nil
	}
	s := gitsync.Scheduler{GOOS: "windows", Home: t.TempDir(), Bin: `C:\x\lodan.exe`, Run: run}
	if err := s.Schedule(context.Background(), 15); err != nil {
		t.Fatal(err)
	}
	want := []string{"schtasks", "/create", "/tn", "lodan-sync", "/tr", `conhost.exe --headless "C:\x\lodan.exe" sync run`, "/sc", "minute", "/mo", "15", "/it", "/rl", "LIMITED", "/f"}
	if n != 1 || strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("llamadas = %d, argv = %q, quería %q", n, got, want)
	}
}

// S8: Windows unschedule queries first, then deletes.
func TestSchedulerWindowsUnschedule(t *testing.T) {
	f := &fakeRun{}
	s, _ := newSched(t, "windows", `C:\x\lodan.exe`, f)
	if err := s.Unschedule(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"schtasks /query /tn lodan-sync", "schtasks /delete /tn lodan-sync /f"}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %q, quería %q", f.calls, want)
	}
}

// S8: a missing task is not an error and nothing is deleted.
func TestSchedulerWindowsUnscheduleSinTarea(t *testing.T) {
	f := &fakeRun{failOn: map[string]error{"schtasks /query": errors.New("no existe")}}
	s, _ := newSched(t, "windows", `C:\x\lodan.exe`, f)
	if err := s.Unschedule(context.Background()); err != nil {
		t.Fatalf("err = %v, quería nil", err)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "/delete") {
			t.Errorf("no debía borrar: %q", f.calls)
		}
	}
}

// S8: an error of the essential command is returned.
func TestSchedulerWindowsErrorDeCreate(t *testing.T) {
	f := &fakeRun{failOn: map[string]error{"schtasks /create": errors.New("acceso denegado")}}
	s, _ := newSched(t, "windows", `C:\x\lodan.exe`, f)
	if err := s.Schedule(context.Background(), 15); err == nil {
		t.Fatal("quería error")
	}
}

const linuxBin = "/opt/lodan/lodan"

func unitDir(home string) string { return filepath.Join(home, ".config", "systemd", "user") }

// S8: Linux writes units and enables the timer.
func TestSchedulerLinuxSchedule(t *testing.T) {
	f := &fakeRun{}
	s, home := newSched(t, "linux", linuxBin, f)
	if err := s.Schedule(context.Background(), 30); err != nil {
		t.Fatal(err)
	}
	svc := readFile(t, filepath.Join(unitDir(home), "lodan-sync.service"))
	if !strings.Contains(svc, `ExecStart="`+linuxBin+`" sync run`) {
		t.Errorf("service sin ExecStart esperado:\n%s", svc)
	}
	tm := readFile(t, filepath.Join(unitDir(home), "lodan-sync.timer"))
	for _, w := range []string{"OnUnitActiveSec=30min", "OnBootSec=2min", "[Install]", "WantedBy=timers.target"} {
		if !strings.Contains(tm, w) {
			t.Errorf("timer sin %q:\n%s", w, tm)
		}
	}
	want := []string{"systemctl --user daemon-reload", "systemctl --user enable --now lodan-sync.timer"}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %q, quería %q", f.calls, want)
	}
}

// S8: re-running replaces the timer.
func TestSchedulerLinuxIdempotente(t *testing.T) {
	f := &fakeRun{}
	s, home := newSched(t, "linux", linuxBin, f)
	for _, n := range []int{30, 60} {
		if err := s.Schedule(context.Background(), n); err != nil {
			t.Fatal(err)
		}
	}
	tm := readFile(t, filepath.Join(unitDir(home), "lodan-sync.timer"))
	if !strings.Contains(tm, "OnUnitActiveSec=60min") || strings.Contains(tm, "30min") {
		t.Errorf("timer no reemplazado:\n%s", tm)
	}
}

// S8: an error of systemctl enable is returned.
func TestSchedulerLinuxErrorDeEnable(t *testing.T) {
	f := &fakeRun{failOn: map[string]error{"systemctl --user enable": errors.New("falla")}}
	s, _ := newSched(t, "linux", linuxBin, f)
	if err := s.Schedule(context.Background(), 30); err == nil {
		t.Fatal("quería error")
	}
}

// S8: Linux unschedule disables, deletes files and reloads.
func TestSchedulerLinuxUnschedule(t *testing.T) {
	f := &fakeRun{}
	s, home := newSched(t, "linux", linuxBin, f)
	if err := s.Schedule(context.Background(), 30); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	if err := s.Unschedule(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"systemctl --user disable --now lodan-sync.timer", "systemctl --user daemon-reload"}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %q, quería %q", f.calls, want)
	}
	for _, n := range []string{"lodan-sync.service", "lodan-sync.timer"} {
		if exists(filepath.Join(unitDir(home), n)) {
			t.Errorf("%s sigue existiendo", n)
		}
	}
}

// S8: nothing installed is not an error.
func TestSchedulerLinuxUnscheduleSinNada(t *testing.T) {
	f := &fakeRun{}
	s, _ := newSched(t, "linux", linuxBin, f)
	if err := s.Unschedule(context.Background()); err != nil {
		t.Fatalf("err = %v", err)
	}
}

// S8: macOS writes the LaunchAgent and loads it; an unload error is ignored.
func TestSchedulerDarwinSchedule(t *testing.T) {
	f := &fakeRun{failOn: map[string]error{"launchctl unload": errors.New("no cargado")}}
	s, home := newSched(t, "darwin", "/usr/local/bin/lodan", f)
	if err := s.Schedule(context.Background(), 15); err != nil {
		t.Fatalf("el error de unload debe ignorarse: %v", err)
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", "com.lodan.sync.plist")
	p := readFile(t, plist)
	for _, w := range []string{"/usr/local/bin/lodan", "<string>sync</string>", "<string>run</string>", "StartInterval", "<integer>900</integer>"} {
		if !strings.Contains(p, w) {
			t.Errorf("plist sin %q:\n%s", w, p)
		}
	}
	want := []string{"launchctl unload " + plist, "launchctl load -w " + plist}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %q, quería %q", f.calls, want)
	}
}

// S8: macOS unschedule unloads and deletes the plist.
func TestSchedulerDarwinUnschedule(t *testing.T) {
	f := &fakeRun{}
	s, home := newSched(t, "darwin", "/usr/local/bin/lodan", f)
	if err := s.Schedule(context.Background(), 15); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", "com.lodan.sync.plist")
	f.calls = nil
	if err := s.Unschedule(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) == 0 || f.calls[0] != "launchctl unload "+plist {
		t.Errorf("calls = %q, quería unload primero", f.calls)
	}
	if exists(plist) {
		t.Error("el plist sigue existiendo")
	}
}

// S8: interval bounds on every OS; nothing run, nothing written.
func TestSchedulerIntervaloFueraDeRango(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin"} {
		for _, n := range []int{4, 1441} {
			f := &fakeRun{}
			s, home := newSched(t, goos, "/b/lodan", f)
			err := s.Schedule(context.Background(), n)
			if err == nil || err.Error() != intervaloInvalido {
				t.Errorf("%s/%d: err = %v", goos, n, err)
			}
			if len(f.calls) != 0 {
				t.Errorf("%s/%d: ejecutó %q", goos, n, f.calls)
			}
			if entries, _ := os.ReadDir(home); len(entries) != 0 {
				t.Errorf("%s/%d: escribió archivos", goos, n)
			}
		}
	}
}

// S8: the bounds 5 and 1440 are accepted.
func TestSchedulerIntervaloLimitesAceptados(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin"} {
		for _, n := range []int{5, 1440} {
			f := &fakeRun{}
			s, _ := newSched(t, goos, "/b/lodan", f)
			if err := s.Schedule(context.Background(), n); err != nil {
				t.Errorf("%s/%d: %v", goos, n, err)
			}
		}
	}
}
