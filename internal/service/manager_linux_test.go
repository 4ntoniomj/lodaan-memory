package service

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func testSpec() Spec {
	return Spec{
		Name:        "lodan",
		DisplayName: "lodan",
		Description: "Memoria persistente para IAs",
		Exec:        "/usr/local/bin/lodan",
		Args:        []string{"service", "run"},
		User:        "antonio",
		Env:         map[string]string{"LODAN_DATA_DIR": "/home/antonio/.local/share/lodan"},
	}
}

func TestRenderUnit(t *testing.T) {
	want := `[Unit]
Description=Memoria persistente para IAs
After=network.target

[Service]
Type=simple
User=antonio
ExecStart=/usr/local/bin/lodan service run
Environment="LODAN_DATA_DIR=/home/antonio/.local/share/lodan"
Restart=on-failure
RestartSec=5
KillMode=mixed
TimeoutStopSec=45

[Install]
WantedBy=multi-user.target
`
	if got := renderUnit(testSpec()); got != want {
		t.Errorf("unidad generada distinta.\n--- obtenida ---\n%s\n--- esperada ---\n%s", got, want)
	}
}

func TestRenderUnitCitadoYEntorno(t *testing.T) {
	s := testSpec()
	s.Exec = "/opt/mi programa/lodan"
	s.Args = []string{"service", "run", "--nombre", "con espacios", `comilla"doble`, "50%", "coste$5", ""}
	s.Env = map[string]string{
		"B": `valor "citado" y \barra`,
		"A": "100%",
	}
	s.User = ""
	got := renderUnit(s)

	wantExec := `ExecStart="/opt/mi programa/lodan" service run --nombre "con espacios" "comilla\"doble" "50%%" "coste$$5" ""`
	if !strings.Contains(got, wantExec+"\n") {
		t.Errorf("ExecStart incorrecto.\nobtenida:\n%s\nesperada la línea:\n%s", got, wantExec)
	}
	// Environment entries are sorted by key, one line per variable.
	wantEnvA := `Environment="A=100%%"`
	wantEnvB := `Environment="B=valor \"citado\" y \\barra"`
	iA, iB := strings.Index(got, wantEnvA), strings.Index(got, wantEnvB)
	if iA < 0 || iB < 0 || iA > iB {
		t.Errorf("Environment incorrecto o desordenado:\n%s", got)
	}
	if strings.Contains(got, "User=") {
		t.Errorf("sin usuario no debe haber User=:\n%s", got)
	}
}

func TestInstallEscribeUnidadYActiva(t *testing.T) {
	dir := t.TempDir()
	fr := newFakeRunner()
	m := &linuxManager{unitDir: dir, run: fr}
	s := testSpec()

	if err := m.Install(s); err != nil {
		t.Fatalf("Install: %v", err)
	}
	path := filepath.Join(dir, "lodan.service")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no se escribió la unidad: %v", err)
	}
	if string(data) != renderUnit(s) {
		t.Errorf("contenido de la unidad inesperado:\n%s", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("permisos de la unidad: %v, se esperaba 0644", info.Mode().Perm())
	}
	want := []string{"systemctl daemon-reload", "systemctl enable lodan.service"}
	if !slices.Equal(fr.calls, want) {
		t.Errorf("comandos: %q, se esperaba %q", fr.calls, want)
	}
}

func TestInstallRechazaSpecInvalida(t *testing.T) {
	m := &linuxManager{unitDir: t.TempDir(), run: newFakeRunner()}
	for name, mod := range map[string]func(*Spec){
		"nombre con ruta":   func(s *Spec) { s.Name = "../lodan" },
		"sin ejecutable":    func(s *Spec) { s.Exec = "" },
		"ejecutable no abs": func(s *Spec) { s.Exec = "lodan" },
		"clave de entorno":  func(s *Spec) { s.Env = map[string]string{"A=B": "x"} },
	} {
		s := testSpec()
		mod(&s)
		if err := m.Install(s); err == nil {
			t.Errorf("%s: se esperaba un error", name)
		}
	}
	entries, _ := os.ReadDir(m.unitDir)
	if len(entries) != 0 {
		t.Errorf("no debería haberse escrito nada, hay %d archivos", len(entries))
	}
}

func TestUninstallQuitaUnidad(t *testing.T) {
	dir := t.TempDir()
	fr := newFakeRunner()
	m := &linuxManager{unitDir: dir, run: fr}
	if err := m.Install(testSpec()); err != nil {
		t.Fatal(err)
	}
	fr.calls = nil

	if err := m.Uninstall("lodan"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "lodan.service")); !os.IsNotExist(err) {
		t.Errorf("la unidad debería haberse borrado (err=%v)", err)
	}
	want := []string{"systemctl disable --now lodan.service", "systemctl daemon-reload"}
	if !slices.Equal(fr.calls, want) {
		t.Errorf("comandos: %q, se esperaba %q", fr.calls, want)
	}
}

func TestUninstallSinUnidadNoHaceNada(t *testing.T) {
	fr := newFakeRunner()
	m := &linuxManager{unitDir: t.TempDir(), run: fr}
	if err := m.Uninstall("lodan"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(fr.calls) != 0 {
		t.Errorf("no debería ejecutar comandos: %q", fr.calls)
	}
}

func TestControlLlamaASystemctl(t *testing.T) {
	fr := newFakeRunner()
	m := &linuxManager{unitDir: t.TempDir(), run: fr}
	for _, op := range []func(string) error{m.Start, m.Stop, m.Restart, m.Enable, m.Disable} {
		if err := op("lodan"); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		"systemctl start lodan.service",
		"systemctl stop lodan.service",
		"systemctl restart lodan.service",
		"systemctl enable lodan.service",
		"systemctl disable lodan.service",
	}
	if !slices.Equal(fr.calls, want) {
		t.Errorf("comandos: %q, se esperaba %q", fr.calls, want)
	}
}

func TestErrorIncluyeLaSalidaDelComando(t *testing.T) {
	fr := newFakeRunner()
	fr.results["systemctl start lodan.service"] = cmdResult{ExitCode: 1, Stderr: "Failed to start lodan.service: Access denied\n"}
	m := &linuxManager{unitDir: t.TempDir(), run: fr}
	err := m.Start("lodan")
	if err == nil || !strings.Contains(err.Error(), "Access denied") {
		t.Errorf("el error debería incluir la salida del comando, obtenido: %v", err)
	}
}

func TestStatus(t *testing.T) {
	tests := []struct {
		name        string
		isEnabled   cmdResult
		isActive    cmdResult
		wantState   State
		wantEnabled bool
	}{
		{"activo y habilitado",
			cmdResult{Stdout: "enabled\n"}, cmdResult{Stdout: "active\n"}, StateRunning, true},
		{"parado y deshabilitado",
			cmdResult{Stdout: "disabled\n", ExitCode: 1}, cmdResult{Stdout: "inactive\n", ExitCode: 3}, StateStopped, false},
		{"fallido",
			cmdResult{Stdout: "enabled\n"}, cmdResult{Stdout: "failed\n", ExitCode: 3}, StateStopped, true},
		{"arrancando",
			cmdResult{Stdout: "enabled\n"}, cmdResult{Stdout: "activating\n", ExitCode: 3}, StateUnknown, true},
		{"unidad inexistente (stderr)",
			cmdResult{Stderr: "Failed to get unit file state for lodan.service: No such file or directory\n", ExitCode: 1},
			cmdResult{Stdout: "inactive\n", ExitCode: 3}, StateNotInstalled, false},
		{"unidad inexistente (not-found)",
			cmdResult{Stdout: "not-found\n", ExitCode: 4}, cmdResult{Stdout: "inactive\n", ExitCode: 3}, StateNotInstalled, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := newFakeRunner()
			fr.results["systemctl is-enabled lodan.service"] = tt.isEnabled
			fr.results["systemctl is-active lodan.service"] = tt.isActive
			m := &linuxManager{unitDir: t.TempDir(), run: fr}
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

func TestStatusSalidaVaciaSinUnidadEsNoInstalado(t *testing.T) {
	fr := newFakeRunner()
	fr.results["systemctl is-enabled lodan.service"] = cmdResult{ExitCode: 1}
	fr.results["systemctl is-active lodan.service"] = cmdResult{Stdout: "inactive\n", ExitCode: 3}
	m := &linuxManager{unitDir: t.TempDir(), run: fr}
	st, err := m.Status("lodan")
	if err != nil || st.State != StateNotInstalled {
		t.Errorf("Status = %+v, %v; se esperaba not-installed", st, err)
	}
}

func TestStatusSalidaVaciaConUnidadEsError(t *testing.T) {
	// Without systemd (e.g. a container) is-enabled fails with empty stdout;
	// the unit exists, so it must not be reported as "not installed".
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lodan.service"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fr := newFakeRunner()
	fr.results["systemctl is-enabled lodan.service"] = cmdResult{ExitCode: 1, Stderr: "System has not been booted with systemd"}
	m := &linuxManager{unitDir: dir, run: fr}
	if _, err := m.Status("lodan"); err == nil {
		t.Error("se esperaba un error")
	}
}
