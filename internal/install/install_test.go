package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"lodan/internal/config"
	"lodan/internal/service"
)

// --- fakes ---

// events records the calls that change the system, in order.
type events struct{ list []string }

func (e *events) add(s string) { e.list = append(e.list, s) }

func (e *events) index(s string) int {
	for i, x := range e.list {
		if x == s {
			return i
		}
	}
	return -1
}

func (e *events) count(s string) int {
	n := 0
	for _, x := range e.list {
		if x == s {
			n++
		}
	}
	return n
}

// since returns the events recorded after position from.
func (e *events) since(from int) []string { return e.list[from:] }

type fakeRuntime struct {
	ev       *events
	binDir   string
	complete bool
}

func (f *fakeRuntime) MissingPostgresFiles() []string {
	if f.complete {
		return nil
	}
	return []string{filepath.Join(f.binDir, "initdb")}
}

func (f *fakeRuntime) PostgresBinDir() string { return f.binDir }

func (f *fakeRuntime) EnsurePostgres(ctx context.Context, log func(string)) (string, error) {
	f.ev.add("runtime.ensure")
	log("Descargando micromamba…")
	if err := os.MkdirAll(f.binDir, 0o755); err != nil {
		return "", err
	}
	f.complete = true
	return f.binDir, nil
}

type fakeCluster struct {
	ev            *events
	initialized   bool
	running       bool
	migrated      bool
	vectorVersion string
	vectorErr     error
}

func (f *fakeCluster) Lock(ctx context.Context) (func(), error) { return func() {}, nil }
func (f *fakeCluster) Initialized() bool                        { return f.initialized }
func (f *fakeCluster) Init(ctx context.Context) error {
	f.ev.add("cluster.init")
	f.initialized = true
	return nil
}
func (f *fakeCluster) Start(ctx context.Context) error {
	f.ev.add("cluster.start")
	f.running = true
	return nil
}
func (f *fakeCluster) Stop(ctx context.Context) error {
	f.ev.add("cluster.stop")
	f.running = false
	return nil
}
func (f *fakeCluster) Status(ctx context.Context) (bool, error) { return f.running, nil }
func (f *fakeCluster) EnsureDatabase(ctx context.Context, name string) error {
	f.ev.add("cluster.ensuredb")
	return nil
}
func (f *fakeCluster) Migrate(ctx context.Context) (bool, error) {
	f.ev.add("cluster.migrate")
	changed := !f.migrated
	f.migrated = true
	return changed, nil
}
func (f *fakeCluster) VectorVersion(ctx context.Context) (string, error) {
	return f.vectorVersion, f.vectorErr
}

type fakeOllama struct {
	ev        *events
	up        bool
	installed bool
	models    map[string]bool
	embedErr  error
}

func (f *fakeOllama) Detect(ctx context.Context, baseURL string) (string, bool) {
	return "0.0.0-prueba", f.up
}

func (f *fakeOllama) HasModel(ctx context.Context, baseURL, model string) (bool, error) {
	return f.models[model], nil
}

func (f *fakeOllama) Pull(ctx context.Context, baseURL, model string, progress func(string, int64, int64)) error {
	f.ev.add("ollama.pull")
	progress("pulling", 50, 100)
	progress("success", 100, 100)
	f.models[model] = true
	return nil
}

func (f *fakeOllama) Install(ctx context.Context, dir string, progress func(int64, int64)) (string, error) {
	f.ev.add("ollama.install")
	exe := filepath.Join(dir, "bin", "ollama")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(exe, []byte("ollama de prueba\n"), 0o755); err != nil {
		return "", err
	}
	progress(50, 100)
	progress(100, 100)
	f.installed = true
	return exe, nil
}

func (f *fakeOllama) Find(dir string) (string, error) {
	if f.installed {
		return filepath.Join(dir, "bin", "ollama"), nil
	}
	return "", errNotFound
}

func (f *fakeOllama) Env(dir string) map[string]string { return map[string]string{} }

func (f *fakeOllama) Embed(ctx context.Context, cfg config.Config) error { return f.embedErr }

type fakeManager struct {
	status map[string]service.Status
}

func (m *fakeManager) Install(service.Spec) error { return nil }
func (m *fakeManager) Uninstall(string) error     { return nil }
func (m *fakeManager) Start(string) error         { return nil }
func (m *fakeManager) Stop(string) error          { return nil }
func (m *fakeManager) Restart(string) error       { return nil }
func (m *fakeManager) Enable(string) error        { return nil }
func (m *fakeManager) Disable(string) error       { return nil }
func (m *fakeManager) Status(name string) (service.Status, error) {
	if st, ok := m.status[name]; ok {
		return st, nil
	}
	return service.Status{State: service.StateNotInstalled}, nil
}

// fakeElevate plays the role of the privileged process: it registers or
// removes the services in the fake manager, as `lodan service install` would.
type fakeElevate struct {
	ev    *events
	mgr   *fakeManager
	ol    *fakeOllama
	cl    *fakeCluster
	err   error
	calls [][]string
}

func (f *fakeElevate) run(ctx context.Context, exe string, args []string) error {
	f.calls = append(f.calls, append([]string{exe}, args...))
	f.ev.add("elevate:" + args[1])
	if f.err != nil {
		return f.err
	}
	running := service.Status{State: service.StateRunning, Enabled: true}
	switch args[1] {
	case "install":
		f.mgr.status[service.DefaultName] = running
		f.cl.running = true
		for _, a := range args {
			if a == "--ollama-exe" {
				f.mgr.status[OllamaServiceName] = running
				f.ol.up = true
			}
		}
	case "uninstall":
		delete(f.mgr.status, service.DefaultName)
		delete(f.mgr.status, OllamaServiceName)
		f.cl.running = false
	}
	return nil
}

// --- harness ---

type harness struct {
	t       *testing.T
	in      *Installer
	home    string
	dataDir string
	exe     string
	out     *bytes.Buffer
	ev      *events
	rt      *fakeRuntime
	cl      *fakeCluster
	ol      *fakeOllama
	mgr     *fakeManager
	el      *fakeElevate
}

// newHarness builds an Installer over a temporary HOME with three clients
// (Claude Code, Cursor and Codex), ~/.local/bin and fakes for everything
// external. stdin is what the user "types".
func newHarness(t *testing.T, stdin string) *harness {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("los tests del flujo usan rutas y enlaces simbólicos de Unix")
	}
	clStubCLI(t, "", nil)

	home := t.TempDir()
	dataDir := filepath.Join(home, ".local", "share", "lodan")
	for _, d := range []string{".claude", ".cursor", ".codex", filepath.Join(".local", "bin")} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	exe := filepath.Join(t.TempDir(), "lodan-origen")
	if err := os.WriteFile(exe, []byte("binario de prueba\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	ev := &events{}
	rt := &fakeRuntime{ev: ev, binDir: filepath.Join(dataDir, "runtime", "pg", "bin")}
	cl := &fakeCluster{ev: ev, vectorVersion: "0.8.6"}
	ol := &fakeOllama{ev: ev, models: map[string]bool{}}
	mgr := &fakeManager{status: map[string]service.Status{}}
	el := &fakeElevate{ev: ev, mgr: mgr, ol: ol, cl: cl}
	out := &bytes.Buffer{}

	cfg := config.Default()
	cfg.DataDir = dataDir
	in := &Installer{
		Cfg:        cfg,
		Env:        Env{GOOS: runtime.GOOS, Home: home},
		In:         strings.NewReader(stdin),
		Out:        out,
		Manager:    mgr,
		SkillFS:    skFS(),
		Username:   "tester",
		Executable: func() (string, error) { return exe, nil },
		Elevate:    el.run,
		NewRuntime: func(string) pgRuntime { return rt },
		NewCluster: func(config.Config) (clusterAPI, error) { return cl, nil },
		Ollama:     ol,
		OllamaWait: 2 * time.Second,
	}
	return &harness{t: t, in: in, home: home, dataDir: dataDir, exe: exe, out: out,
		ev: ev, rt: rt, cl: cl, ol: ol, mgr: mgr, el: el}
}

// installed returns a harness on which a full install (--yes) already ran.
func installedHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, "")
	if err := h.in.Install(context.Background(), Options{Yes: true}); err != nil {
		t.Fatalf("la instalación de preparación falló: %v\n%s", err, h.out.String())
	}
	return h
}

func (h *harness) install(opt Options) error {
	h.out.Reset()
	return h.in.Install(context.Background(), opt)
}

func (h *harness) output() string { return h.out.String() }

func (h *harness) bin() string { return StableBinary(h.dataDir, runtime.GOOS) }

// fileHas reports whether the file exists and contains s.
func fileHas(path, s string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), s)
}

// snapshot describes every entry under root (kind, size, content hash, mode
// and link target), to compare a tree before and after.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			m[rel] = "enlace -> " + target
		case d.IsDir():
			m[rel] = fmt.Sprintf("dir %v", info.Mode().Perm())
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			m[rel] = fmt.Sprintf("archivo %d %x %v", info.Size(), sha256.Sum256(data), info.Mode().Perm())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// --- tests ---

func TestInstallDryRunNoTocaElDisco(t *testing.T) {
	h := newHarness(t, "")
	before := snapshot(t, h.home)

	if err := h.install(Options{DryRun: true}); err != nil {
		t.Fatalf("Install(dry-run): %v\n%s", err, h.output())
	}

	after := snapshot(t, h.home)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("el dry run cambió el disco\nantes:   %v\ndespués: %v", before, after)
	}
	if len(h.ev.list) != 0 {
		t.Fatalf("el dry run no debe ejecutar nada que cambie el sistema: %v", h.ev.list)
	}
	out := h.output()
	for _, want := range []string{"[simulación]", "[1/8]", "[8/8]", "lodan doctor", "Resumen"} {
		if !strings.Contains(out, want) {
			t.Errorf("falta %q en la salida:\n%s", want, out)
		}
	}
	if strings.Contains(out, "✗") {
		t.Errorf("el dry run no debe fallar:\n%s", out)
	}
}

func TestInstallFlujoCompletoEIdempotencia(t *testing.T) {
	h := newHarness(t, "")
	if err := h.install(Options{Yes: true}); err != nil {
		t.Fatalf("Install: %v\n%s", err, h.output())
	}
	out := h.output()
	if strings.Contains(out, "✗") || strings.Contains(out, "  ! ") {
		t.Errorf("la primera instalación no debe dar avisos ni errores:\n%s", out)
	}

	// Order of the steps that change the system.
	order := []string{"runtime.ensure", "cluster.init", "cluster.start", "cluster.migrate", "cluster.stop",
		"ollama.install", "elevate:install", "ollama.pull"}
	prev := -1
	for _, name := range order {
		i := h.ev.index(name)
		if i < 0 {
			t.Fatalf("falta el evento %s: %v", name, h.ev.list)
		}
		if i <= prev {
			t.Fatalf("el evento %s está fuera de orden: %v", name, h.ev.list)
		}
		prev = i
	}

	// Stable binary, symlink, configuration and logs.
	if got, err := os.ReadFile(h.bin()); err != nil || string(got) != "binario de prueba\n" {
		t.Fatalf("binario estable: %q %v", got, err)
	}
	if st, err := os.Stat(h.bin()); err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("el binario debe ser 0755: %v %v", st, err)
	}
	link := filepath.Join(h.home, ".local", "bin", "lodan")
	if target, err := os.Readlink(link); err != nil || target != h.bin() {
		t.Fatalf("enlace ~/.local/bin/lodan: %q %v", target, err)
	}
	if !fileHas(h.in.Cfg.ConfigFile(), h.rt.binDir) {
		t.Errorf("config.json debe guardar pg_bin_dir = %s", h.rt.binDir)
	}
	if !dirExists(h.in.Cfg.LogsDir()) {
		t.Errorf("debe existir %s", h.in.Cfg.LogsDir())
	}

	// The elevated step receives everything it needs.
	if len(h.el.calls) != 1 {
		t.Fatalf("debe haber un único paso elevado: %v", h.el.calls)
	}
	call := strings.Join(h.el.calls[0], " ")
	for _, want := range []string{h.bin(), "service install", "--user tester", "--home " + h.home,
		"--data-dir " + h.dataDir, "--ollama-exe " + filepath.Join(h.in.ollamaInstallDir(), "bin", "ollama"),
		"--ollama-dir " + h.in.ollamaInstallDir()} {
		if !strings.Contains(call, want) {
			t.Errorf("el paso elevado no lleva %q: %s", want, call)
		}
	}

	// Clients, skill and instructions.
	for _, p := range []string{
		filepath.Join(h.home, ".cursor", "mcp.json"),
		filepath.Join(h.home, ".codex", "config.toml"),
		filepath.Join(h.home, ".claude.json"),
	} {
		if !fileHas(p, h.bin()) {
			t.Errorf("%s debe apuntar al binario estable %s", p, h.bin())
		}
	}
	if !pathExists(filepath.Join(h.home, ".claude", "skills", "lodan-memoria", "SKILL.md")) {
		t.Error("falta la skill")
	}
	if !fileHas(filepath.Join(h.home, ".claude", "CLAUDE.md"), instructionsStart) {
		t.Error("falta el bloque de instrucciones")
	}
	if !strings.Contains(out, "lodan doctor") {
		t.Errorf("el resumen debe sugerir lodan doctor:\n%s", out)
	}

	// Second run: only "ya estaba" and nothing that changes the system.
	mark := len(h.ev.list)
	if err := h.install(Options{Yes: true}); err != nil {
		t.Fatalf("segunda instalación: %v\n%s", err, h.output())
	}
	out = h.output()
	if changes := h.ev.since(mark); len(changes) > 0 {
		for _, e := range changes {
			switch e {
			case "cluster.ensuredb", "cluster.migrate": // idempotent, always checked
			default:
				t.Errorf("la segunda ejecución no debe cambiar nada, pero hizo %s", e)
			}
		}
	}
	for _, want := range []string{
		"el binario ya estaba instalado y al día",
		"el enlace",
		"PostgreSQL con pgvector ya estaba",
		"el clúster ya estaba inicializado",
		"la base de datos \"lodan\" ya estaba al día",
		"Ollama 0.0.0-prueba responde",
		"ya estaban registrados, habilitados y en marcha",
		"el modelo " + h.in.Cfg.EmbedModel + " ya estaba",
		"Cursor: ya estaba",
		"la skill lodan-memoria ya estaba instalada y al día",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("falta %q en la salida de la segunda ejecución:\n%s", want, out)
		}
	}
	if strings.Contains(out, "✗") || strings.Contains(out, "  ! ") {
		t.Errorf("la segunda ejecución no debe dar avisos ni errores:\n%s", out)
	}
}

func TestInstallOpcionesSkip(t *testing.T) {
	t.Run("skip-ollama", func(t *testing.T) {
		h := newHarness(t, "")
		if err := h.install(Options{Yes: true, SkipOllama: true}); err != nil {
			t.Fatalf("Install: %v\n%s", err, h.output())
		}
		if h.ev.index("ollama.install") >= 0 || h.ev.index("ollama.pull") >= 0 {
			t.Errorf("con --skip-ollama no se instala Ollama ni se descarga el modelo: %v", h.ev.list)
		}
		if !strings.Contains(h.output(), "--skip-ollama") {
			t.Errorf("debe avisar de --skip-ollama:\n%s", h.output())
		}
		if call := strings.Join(h.el.calls[0], " "); strings.Contains(call, "--ollama-exe") {
			t.Errorf("no debe registrarse lodan-ollama: %s", call)
		}
	})

	t.Run("skip-service", func(t *testing.T) {
		h := newHarness(t, "")
		if err := h.install(Options{Yes: true, SkipService: true}); err != nil {
			t.Fatalf("Install: %v\n%s", err, h.output())
		}
		if len(h.el.calls) != 0 {
			t.Errorf("con --skip-service no se pide administrador: %v", h.el.calls)
		}
		if h.ev.index("ollama.install") >= 0 {
			t.Errorf("sin servicio no se descarga Ollama: %v", h.ev.list)
		}
		if !strings.Contains(h.output(), "--skip-service") {
			t.Errorf("debe avisar de --skip-service:\n%s", h.output())
		}
		// The rest still happens.
		if !fileHas(filepath.Join(h.home, ".cursor", "mcp.json"), h.bin()) {
			t.Error("los clientes deben configurarse igualmente")
		}
	})

	t.Run("skip-clients", func(t *testing.T) {
		h := newHarness(t, "")
		if err := h.install(Options{Yes: true, SkipClients: true}); err != nil {
			t.Fatalf("Install: %v\n%s", err, h.output())
		}
		for _, p := range []string{".cursor/mcp.json", ".codex/config.toml", ".claude.json"} {
			if pathExists(filepath.Join(h.home, filepath.FromSlash(p))) {
				t.Errorf("con --skip-clients no debe crearse %s", p)
			}
		}
		if !strings.Contains(h.output(), "--skip-clients") {
			t.Errorf("debe avisar de --skip-clients:\n%s", h.output())
		}
		if !pathExists(filepath.Join(h.home, ".claude", "skills", "lodan-memoria")) {
			t.Error("la skill se instala igualmente")
		}
	})
}

func TestInstallOnlyLimitaLosClientes(t *testing.T) {
	h := newHarness(t, "")
	if err := h.install(Options{Yes: true, Only: []string{"cursor"}}); err != nil {
		t.Fatalf("Install: %v\n%s", err, h.output())
	}
	if !fileHas(filepath.Join(h.home, ".cursor", "mcp.json"), h.bin()) {
		t.Error("Cursor debe configurarse")
	}
	if pathExists(filepath.Join(h.home, ".codex", "config.toml")) || pathExists(filepath.Join(h.home, ".claude.json")) {
		t.Error("con --only cursor no se toca ningún otro cliente")
	}
}

func TestInstallOnlyDesconocidoFallaAntesDeTocarNada(t *testing.T) {
	h := newHarness(t, "")
	before := snapshot(t, h.home)
	err := h.install(Options{Yes: true, Only: []string{"cursor", "noexiste"}})
	if err == nil || !strings.Contains(err.Error(), "noexiste") {
		t.Fatalf("debe rechazar el cliente desconocido: %v", err)
	}
	if len(h.ev.list) != 0 || !reflect.DeepEqual(before, snapshot(t, h.home)) {
		t.Fatal("no se debe hacer nada si --only es inválido")
	}
}

func TestInstallConfirmacionesLeenDeStdin(t *testing.T) {
	// First prompt: download Ollama; second: configure the clients. Both "no".
	h := newHarness(t, "n\nno\n")
	if err := h.install(Options{}); err != nil {
		t.Fatalf("Install: %v\n%s", err, h.output())
	}
	if h.ev.index("ollama.install") >= 0 {
		t.Error("no debe instalarse Ollama sin confirmación")
	}
	if pathExists(filepath.Join(h.home, ".cursor", "mcp.json")) {
		t.Error("no deben configurarse los clientes sin confirmación")
	}
	out := h.output()
	if !strings.Contains(out, "1,5 GB") || !strings.Contains(out, "[s/N]") {
		t.Errorf("la pregunta de Ollama debe mostrar el tamaño y [s/N]:\n%s", out)
	}
	if !strings.Contains(out, "Ollama no instalado") {
		t.Errorf("debe avisar de que no se instaló Ollama:\n%s", out)
	}

	// With "s" both are accepted.
	h = newHarness(t, "s\nsí\n")
	if err := h.install(Options{}); err != nil {
		t.Fatalf("Install: %v\n%s", err, h.output())
	}
	if h.ev.index("ollama.install") < 0 || !fileHas(filepath.Join(h.home, ".cursor", "mcp.json"), h.bin()) {
		t.Errorf("con «s» se instala Ollama y se configuran los clientes: %v\n%s", h.ev.list, h.output())
	}
}

func TestInstallSiFallaElPasoElevadoSigueConLoDemas(t *testing.T) {
	h := newHarness(t, "")
	h.el.err = errors.New("sudo cancelado")
	err := h.install(Options{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "1 error") {
		t.Fatalf("debe devolver un error: %v", err)
	}
	out := h.output()
	if !strings.Contains(out, "✗") || !strings.Contains(out, "sudo cancelado") {
		t.Errorf("debe mostrar el error con ✗:\n%s", out)
	}
	if !fileHas(filepath.Join(h.home, ".cursor", "mcp.json"), h.bin()) {
		t.Error("los pasos siguientes deben ejecutarse igualmente")
	}
}

func TestInstallPasoCriticoDetieneLaInstalacion(t *testing.T) {
	h := newHarness(t, "")
	h.in.Executable = func() (string, error) { return "", errors.New("sin ejecutable") }
	err := h.install(Options{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "Binario estable") {
		t.Fatalf("debe fallar en el primer paso: %v", err)
	}
	if strings.Contains(h.output(), "[2/8]") || len(h.ev.list) != 0 {
		t.Errorf("no debe seguir tras un paso crítico:\n%s", h.output())
	}
}

func TestInstallActualizaElBinarioYReiniciaLosServicios(t *testing.T) {
	h := installedHarness(t)
	if err := os.WriteFile(h.exe, []byte("binario nuevo\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := h.install(Options{Yes: true}); err != nil {
		t.Fatalf("Install: %v\n%s", err, h.output())
	}
	if got, _ := os.ReadFile(h.bin()); string(got) != "binario nuevo\n" {
		t.Fatalf("el binario estable debe actualizarse: %q", got)
	}
	if h.ev.count("elevate:install") != 2 {
		t.Errorf("con un binario nuevo hay que volver a registrar/reiniciar el servicio: %v", h.ev.list)
	}
}

func TestCopyExecutable(t *testing.T) {
	src := filepath.Join(t.TempDir(), "origen")
	dst := filepath.Join(t.TempDir(), "bin", "lodan")
	clWrite(t, src, "uno\n")
	if err := copyExecutable(src, dst); err != nil {
		t.Fatal(err)
	}
	if clRead(t, dst) != "uno\n" {
		t.Fatal("contenido incorrecto")
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(dst); err != nil || st.Mode().Perm() != 0o755 {
			t.Fatalf("permisos: %v %v", st, err)
		}
	}
	clWrite(t, src, "dos\n")
	if err := copyExecutable(src, dst); err != nil {
		t.Fatal(err)
	}
	if clRead(t, dst) != "dos\n" {
		t.Fatal("debe sobrescribir el destino")
	}
	entries, err := os.ReadDir(filepath.Dir(dst))
	if err != nil || len(entries) != 1 {
		t.Fatalf("no deben quedar archivos temporales: %v %v", entries, err)
	}
}

func TestLinkBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("los enlaces simbólicos son de Unix")
	}
	newInstaller := func(home string) (*Installer, *bytes.Buffer) {
		out := &bytes.Buffer{}
		return &Installer{Env: Env{GOOS: runtime.GOOS, Home: home}, Out: out}, out
	}
	dest := "/datos/lodan/bin/lodan"

	// No ~/.local/bin: nothing is created.
	home := t.TempDir()
	in, _ := newInstaller(home)
	in.linkBinary(dest)
	if pathExists(filepath.Join(home, ".local")) {
		t.Fatal("no debe crear ~/.local/bin")
	}

	// With ~/.local/bin: symlink, and idempotent.
	home = t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	in, out := newInstaller(home)
	in.linkBinary(dest)
	link := filepath.Join(home, ".local", "bin", "lodan")
	if target, err := os.Readlink(link); err != nil || target != dest {
		t.Fatalf("enlace: %q %v", target, err)
	}
	out.Reset()
	in.linkBinary(dest)
	if !strings.Contains(out.String(), "ya estaba") {
		t.Errorf("la segunda vez debe decir que ya estaba: %s", out.String())
	}

	// A different file is respected.
	home = t.TempDir()
	clWrite(t, filepath.Join(home, ".local", "bin", "lodan"), "otro programa\n")
	in, out = newInstaller(home)
	in.linkBinary(dest)
	if clRead(t, filepath.Join(home, ".local", "bin", "lodan")) != "otro programa\n" {
		t.Fatal("no debe pisar un archivo distinto")
	}
	if !strings.Contains(out.String(), "  ! ") {
		t.Errorf("debe avisar: %s", out.String())
	}
}

func TestFormatProgress(t *testing.T) {
	if got := formatProgress("pulling manifest", 0, 0); got != "pulling manifest" {
		t.Errorf("sin total: %q", got)
	}
	got := formatProgress("descargando", 750<<20, 1500<<20)
	if !strings.Contains(got, "50%") || !strings.Contains(got, "750 MB de 1,5 GB") {
		t.Errorf("con total: %q", got)
	}
	if got := humanBytes(3 << 29); got != "1,5 GB" {
		t.Errorf("humanBytes(1,5 GiB) = %q", got)
	}
}

func TestVersionAtLeast(t *testing.T) {
	for _, tc := range []struct {
		v, min string
		want   bool
	}{
		{"0.8.6", "0.7.0", true},
		{"0.7.0", "0.7.0", true},
		{"0.6.2", "0.7.0", false},
		{"1.0", "0.7.0", true},
		{"0.7", "0.7.0", true},
		{"0.7.0-beta", "0.7.0", true},
		{"", "0.7.0", false},
	} {
		if got := versionAtLeast(tc.v, tc.min); got != tc.want {
			t.Errorf("versionAtLeast(%q, %q) = %v, quería %v", tc.v, tc.min, got, tc.want)
		}
	}
}
