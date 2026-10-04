package install

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"lodan/internal/config"
	"lodan/internal/service"
	"lodan/skills"
)

// ErrCancelled is returned when the user does not confirm an operation.
var ErrCancelled = errors.New("cancelado por el usuario")

// defaultOllamaWait is how long install waits for a freshly registered Ollama
// service to answer before downloading the model.
// In a real Windows 11 test, Ollama 0.35 spent 67 s discovering the GPU (Vulkan) on its first start before answering any request.
const defaultOllamaWait = 3 * time.Minute

// Options are the flags of `lodan install`.
type Options struct {
	// Yes answers "yes" to every confirmation.
	Yes bool
	// DryRun describes every action without writing, downloading or elevating.
	DryRun bool
	// SkipOllama does not install Ollama when none answers (an existing one is still used).
	SkipOllama bool
	// SkipService does not register the system services.
	SkipService bool
	// SkipClients does not touch the configuration of the MCP clients.
	SkipClients bool
	// Only restricts the client configuration to these client ids.
	Only []string
}

// Installer runs install, uninstall and doctor. Every external effect goes
// through an injectable field, so the whole flow is tested with fakes; the
// zero value of each optional field selects the real implementation.
type Installer struct {
	// Cfg is the loaded configuration; install updates PGBinDir and saves it.
	Cfg config.Config
	// Env describes the operating system and the user directories.
	Env Env
	// In is where confirmations are read from; Out receives all the messages.
	In  io.Reader
	Out io.Writer
	// Manager controls the system services (only queried by the unprivileged process).
	Manager service.Manager
	// SkillFS holds the lodan-memoria folder of the skill to deploy.
	SkillFS fs.FS
	// Username is the account the services run as (ignored on Windows).
	Username string

	// Executable returns the path of the running binary (default: os.Executable with symlinks resolved).
	Executable func() (string, error)
	// Elevate runs exe with args as administrator (default: DefaultElevate).
	Elevate func(ctx context.Context, exe string, args []string) error
	// NewRuntime builds the PostgreSQL runtime under dir (default: Runtime{Dir: dir}).
	NewRuntime func(dir string) pgRuntime
	// NewCluster opens the cluster described by the configuration (default: database.NewCluster).
	NewCluster func(cfg config.Config) (clusterAPI, error)
	// Ollama groups the calls to Ollama (default: the functions of ollama.go).
	Ollama ollamaAPI
	// OllamaWait bounds the wait for Ollama after registering its service (default 3 min).
	OllamaWait time.Duration

	reader        *bufio.Reader
	opt           Options
	bin           string
	binUpdated    bool
	ollamaExe     string
	ollamaDir     string
	ollamaService bool
	warnings      int
	failures      int
	progressWidth int
}

// NewInstaller returns an Installer wired to the real system.
func NewInstaller(cfg config.Config, in io.Reader, out io.Writer) *Installer {
	username := ""
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	if username == "" {
		username = os.Getenv("USER")
	}
	return &Installer{
		Cfg:      cfg,
		Env:      CurrentEnv(),
		In:       in,
		Out:      out,
		Manager:  service.NewManager(),
		SkillFS:  skills.FS,
		Username: username,
	}
}

// --- injectable dependencies with their defaults ---

func (in *Installer) out() io.Writer {
	if in.Out == nil {
		return io.Discard
	}
	return in.Out
}

func (in *Installer) manager() service.Manager {
	if in.Manager == nil {
		return service.NewManager()
	}
	return in.Manager
}

func (in *Installer) ollama() ollamaAPI {
	if in.Ollama == nil {
		return realOllama{}
	}
	return in.Ollama
}

func (in *Installer) newRuntime(dir string) pgRuntime {
	if in.NewRuntime != nil {
		return in.NewRuntime(dir)
	}
	return Runtime{Dir: dir}
}

func (in *Installer) newCluster(cfg config.Config) (clusterAPI, error) {
	if in.NewCluster != nil {
		return in.NewCluster(cfg)
	}
	return newRealCluster(cfg)
}

func (in *Installer) elevate() func(ctx context.Context, exe string, args []string) error {
	if in.Elevate != nil {
		return in.Elevate
	}
	return DefaultElevate(in.Env.GOOS)
}

func (in *Installer) executable() (string, error) {
	if in.Executable != nil {
		return in.Executable()
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

func (in *Installer) runtimeDir() string { return filepath.Join(in.Cfg.DataDir, "runtime") }

func (in *Installer) ollamaInstallDir() string { return filepath.Join(in.runtimeDir(), "ollama") }

func (in *Installer) backupDir() string { return filepath.Join(in.Cfg.DataDir, "backups", "skills") }

// --- output ---

func (in *Installer) printf(format string, a ...any) {
	fmt.Fprintf(in.out(), format, a...)
}

// doing reports an action in progress.
func (in *Installer) doing(format string, a ...any) {
	in.printf("  … "+format+"\n", a...)
}

// ok reports a finished action.
func (in *Installer) ok(format string, a ...any) {
	in.printf("  ✓ "+format+"\n", a...)
}

// would describes what a dry run would do.
func (in *Installer) would(format string, a ...any) {
	in.printf("  … [simulación] "+format+"\n", a...)
}

// warn reports something that needs attention but does not stop the installation.
func (in *Installer) warn(format string, a ...any) {
	in.warnings++
	in.printf("  ! "+format+"\n", a...)
}

// fail reports an error; the run continues unless the step is critical.
func (in *Installer) fail(format string, a ...any) {
	in.failures++
	in.printf("  ✗ "+format+"\n", a...)
}

// progress rewrites the current line with text (a single-line progress bar).
func (in *Installer) progress(text string) {
	width := utf8.RuneCountInString(text)
	pad := ""
	if in.progressWidth > width {
		pad = strings.Repeat(" ", in.progressWidth-width)
	}
	in.progressWidth = width
	in.printf("\r  … %s%s", text, pad)
}

// progressDone ends a progress line, if one is open.
func (in *Installer) progressDone() {
	if in.progressWidth > 0 {
		in.printf("\n")
		in.progressWidth = 0
	}
}

// reportLine prints one message returned by the skill and instruction helpers.
func (in *Installer) reportLine(line string) {
	sim := false
	if rest, ok := strings.CutPrefix(line, "[simulación] "); ok {
		line, sim = rest, true
	}
	if msg, ok := strings.CutPrefix(line, "aviso: "); ok {
		in.warn("%s", msg)
		return
	}
	if head, ok := strings.CutSuffix(line, ": sin cambios"); ok {
		in.ok("%s: ya estaba", head)
		return
	}
	if sim {
		in.would("%s", line)
		return
	}
	in.ok("%s", line)
}

// formatProgress renders "label: 45% (700 MB de 1,5 GB)".
func formatProgress(label string, done, total int64) string {
	if total <= 0 {
		return label
	}
	return fmt.Sprintf("%s: %d%% (%s de %s)", label, done*100/total, humanBytes(done), humanBytes(total))
}

func humanBytes(n int64) string {
	if n >= 1<<30 {
		return strings.Replace(fmt.Sprintf("%.1f GB", float64(n)/(1<<30)), ".", ",", 1)
	}
	return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
}

// --- input ---

func (in *Installer) readLine() string {
	if in.reader == nil {
		var r io.Reader = strings.NewReader("")
		if in.In != nil {
			r = in.In
		}
		in.reader = bufio.NewReader(r)
	}
	line, err := in.reader.ReadString('\n')
	if err != nil {
		// End of input: finish the prompt line so the next message starts clean.
		in.printf("\n")
	}
	return strings.TrimSpace(line)
}

// confirm asks a yes/no question (default no). With --yes it answers yes.
func (in *Installer) confirm(question string) bool {
	if in.opt.Yes {
		in.printf("  ? %s [s/N]: s (--yes)\n", question)
		return true
	}
	in.printf("  ? %s [s/N]: ", question)
	switch strings.ToLower(in.readLine()) {
	case "s", "si", "sí", "y", "yes":
		return true
	}
	return false
}

// reset prepares the state of a new run.
func (in *Installer) reset(opt Options) {
	in.opt = opt
	in.bin = StableBinary(in.Cfg.DataDir, in.Env.GOOS)
	in.binUpdated = false
	in.ollamaExe = ""
	in.ollamaDir = in.ollamaInstallDir()
	in.ollamaService = false
	in.warnings, in.failures, in.progressWidth = 0, 0, 0
}

// Install runs the installation steps in order. Steps 1 to 3 are critical: if
// one fails the run stops. Any other failure is reported and the run goes on.
// It returns an error when something failed.
func (in *Installer) Install(ctx context.Context, opt Options) error {
	in.reset(opt)
	if err := in.validateOnly(opt.Only); err != nil {
		return err
	}

	title := "lodan: instalación"
	if opt.DryRun {
		title += " (simulación: no se escribe, no se descarga y no se pide administrador)"
	}
	in.printf("%s\nDatos en %s\n", title, in.Cfg.DataDir)

	type step struct {
		title    string
		critical bool
		run      func(context.Context) error
	}
	steps := []step{
		{"Binario estable", true, in.stepBinary},
		{"PostgreSQL y pgvector", true, in.stepRuntime},
		{"Base de datos", true, in.stepDatabase},
		{"Ollama", false, in.stepOllama},
		{"Servicios del sistema", false, in.stepServices},
		{"Modelo de embeddings", false, in.stepModel},
		{"Clientes MCP", false, in.stepClients},
		{"Skill e instrucciones", false, in.stepSkill},
	}
	for i, s := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		in.printf("\n[%d/%d] %s\n", i+1, len(steps), s.title)
		err := s.run(ctx)
		in.progressDone()
		if err != nil {
			in.fail("%v", err)
			if s.critical {
				in.printf("\nLa instalación se detiene aquí: los pasos siguientes dependen de este.\n")
				return fmt.Errorf("la instalación no se completó: falló el paso «%s»", s.title)
			}
		}
	}

	in.printf("\nResumen\n")
	switch {
	case opt.DryRun:
		in.printf("  … [simulación] no se ha cambiado nada; quita --dry-run para instalar de verdad.\n")
	case in.failures > 0:
		in.printf("  ✗ Instalación con %d error(es) y %d aviso(s).\n", in.failures, in.warnings)
	case in.warnings > 0:
		in.printf("  ✓ Instalación completada con %d aviso(s).\n", in.warnings)
	default:
		in.printf("  ✓ Instalación completada.\n")
	}
	if !opt.DryRun {
		in.printf("  Reinicia tus clientes de IA para que carguen el servidor lodan.\n")
	}
	in.printf("  Comprueba el resultado con: lodan doctor\n")

	if in.failures > 0 {
		return fmt.Errorf("la instalación terminó con %d error(es)", in.failures)
	}
	return nil
}

// validateOnly rejects client ids that do not exist, before touching anything.
func (in *Installer) validateOnly(only []string) error {
	if len(only) == 0 {
		return nil
	}
	known := map[string]bool{}
	var ids []string
	for _, c := range buildClients(in.Env) {
		known[c.ID] = true
		ids = append(ids, c.ID)
	}
	for _, id := range only {
		if !known[id] {
			return fmt.Errorf("cliente desconocido %q en --only; los válidos son: %s", id, strings.Join(ids, ", "))
		}
	}
	return nil
}

// --- step 1: stable binary ---

func (in *Installer) stepBinary(ctx context.Context) error {
	dest := in.bin
	exe, err := in.executable()
	if err != nil {
		return fmt.Errorf("no se pudo localizar el ejecutable actual: %w", err)
	}

	// Leftovers of earlier updates (see copyExecutable): removed once nothing uses them.
	if !in.opt.DryRun {
		removeOldExecutables(dest)
	}

	switch {
	case samePath(exe, dest):
		in.ok("el binario ya estaba en su sitio: %s", dest)
	case sameContent(exe, dest):
		in.ok("el binario ya estaba instalado y al día: %s", dest)
	case in.opt.DryRun:
		in.would("se copiaría %s a %s", exe, dest)
	default:
		in.doing("copiando el binario a %s", dest)
		movedTo, err := copyExecutable(exe, dest)
		if err != nil {
			return fmt.Errorf("no se pudo copiar el binario a %s: %w", dest, err)
		}
		in.binUpdated = true
		in.ok("binario copiado a %s", dest)
		if movedTo != "" {
			in.warn("no se pudo sobrescribir %s (en uso, seguramente por los servicios): el binario anterior queda en %s y los servicios siguen con la versión anterior hasta reiniciarlos; %s",
				dest, movedTo, in.restartHint())
		}
	}
	in.linkBinary(dest)
	return nil
}

// restartHint is the instruction to restart the services that run the stable binary.
func (in *Installer) restartHint() string {
	how := "con sudo"
	if in.Env.GOOS == "windows" {
		how = "como administrador"
	}
	cmds := "«lodan service restart»"
	// On Windows lodan-ollama runs through the wrapper `lodan service ollama`, so it also uses
	// the stable binary.
	if in.Env.GOOS == "windows" {
		cmds += fmt.Sprintf(" y «lodan service restart --name %s»", OllamaServiceName)
	}
	return fmt.Sprintf("ejecuta %s %s", cmds, how)
}

// linkBinary creates ~/.local/bin/lodan -> dest on Unix, if ~/.local/bin exists
// and nothing else is called lodan there.
func (in *Installer) linkBinary(dest string) {
	if in.Env.GOOS == "windows" || in.Env.Home == "" {
		return
	}
	dir := filepath.Join(in.Env.Home, ".local", "bin")
	if !dirExists(dir) {
		return
	}
	link := filepath.Join(dir, "lodan")
	st, err := os.Lstat(link)
	switch {
	case err == nil:
		if st.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(link); err == nil && target == dest {
				in.ok("el enlace %s ya estaba", link)
				return
			}
		}
		in.warn("no se crea el enlace %s: ya existe otro archivo con ese nombre", link)
		return
	case !errors.Is(err, fs.ErrNotExist):
		in.warn("no se pudo comprobar %s: %v", link, err)
		return
	}
	if in.opt.DryRun {
		in.would("se crearía el enlace %s -> %s", link, dest)
		return
	}
	if err := os.Symlink(dest, link); err != nil {
		in.warn("no se pudo crear el enlace %s: %v", link, err)
		return
	}
	in.ok("enlace creado: %s -> %s", link, dest)
}

// samePath reports whether a and b are the same file.
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(sa, sb)
}

// sameContent reports whether both files exist and have the same SHA-256.
func sameContent(a, b string) bool {
	ha, err := fileSHA256(a)
	if err != nil {
		return false
	}
	hb, err := fileSHA256(b)
	return err == nil && ha == hb
}

// renameFile and removeFile are the file operations of copyExecutable; variables so that tests
// can simulate a destination that is in use (a failing rename), which only happens on Windows.
var (
	renameFile = os.Rename
	removeFile = os.Remove
)

// oldSuffix ends the name of the previous binary that copyExecutable moves aside.
const oldSuffix = ".old"

// copyExecutable copies src to dst (mode 0755) through a temporary file in the
// same folder, and renames it over dst (atomic; on Unix this works even if dst is running).
//
// On Windows a running executable cannot be overwritten, but it can be renamed. So if the direct
// rename fails and dst exists, dst is moved aside to dst+".old" (an earlier ".old" is deleted
// first; if it cannot be deleted because it is still running, a name with a date suffix is used
// instead) and the new file takes its place. In that case it returns the path where the previous
// binary went, so the caller can warn that whoever runs it keeps the old version until it is
// restarted. It returns "" when the direct rename worked. removeOldExecutables deletes those
// leftovers in a later run.
func copyExecutable(src, dst string) (movedTo string, err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".lodan-*.tmp")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}

	renameErr := renameFile(tmpName, dst)
	if renameErr == nil {
		done = true
		return "", nil
	}

	// The destination is probably in use (Windows): move it aside and put the new one in its place.
	if _, err := os.Lstat(dst); err != nil {
		return "", renameErr // nothing to move aside: the failure has another cause
	}
	old, err := freeOldName(dst)
	if err != nil {
		return "", errors.Join(renameErr, err)
	}
	if err := renameFile(dst, old); err != nil {
		return "", errors.Join(renameErr, err)
	}
	if err := renameFile(tmpName, dst); err != nil {
		_ = renameFile(old, dst) // put the previous binary back
		return "", err
	}
	done = true
	return old, nil
}

// freeOldName returns a path next to dst, free to receive the previous binary: dst+".old" if it
// can be (an existing one is deleted), otherwise dst+".old-<date>" when the existing ".old" cannot
// be deleted because it is still running.
func freeOldName(dst string) (string, error) {
	old := dst + oldSuffix
	if _, err := os.Lstat(old); err != nil {
		return old, nil // does not exist (or cannot be inspected: the rename will tell)
	}
	if err := removeFile(old); err == nil {
		return old, nil
	}
	dated := old + "-" + time.Now().Format("20060102-150405")
	if _, err := os.Lstat(dated); err == nil {
		return "", fmt.Errorf("%s está en uso y ya existe %s", old, dated)
	}
	return dated, nil
}

// removeOldExecutables deletes the previous binaries that copyExecutable left next to dst
// (dst+".old" and dst+".old-<date>"). Whatever cannot be deleted (it is still running) is left
// for the next run, silently.
func removeOldExecutables(dst string) {
	entries, err := os.ReadDir(filepath.Dir(dst))
	if err != nil {
		return
	}
	base := filepath.Base(dst)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || (name != base+oldSuffix && !strings.HasPrefix(name, base+oldSuffix+"-")) {
			continue
		}
		_ = removeFile(filepath.Join(filepath.Dir(dst), name))
	}
}

// --- step 2: PostgreSQL runtime ---

func (in *Installer) stepRuntime(ctx context.Context) error {
	rt := in.newRuntime(in.runtimeDir())
	binDir := rt.PostgresBinDir()

	switch {
	case len(rt.MissingPostgresFiles()) == 0:
		in.ok("PostgreSQL con pgvector ya estaba en %s", binDir)
	case in.opt.DryRun:
		in.would("se descargaría micromamba (con su SHA-256) y se crearía el entorno de PostgreSQL con pgvector en %s", binDir)
	default:
		got, err := rt.EnsurePostgres(ctx, func(line string) { in.doing("%s", line) })
		if err != nil {
			return err
		}
		binDir = got
		in.ok("PostgreSQL con pgvector instalado en %s", binDir)
	}

	switch {
	case in.Cfg.PGBinDir == binDir && pathExists(in.Cfg.ConfigFile()):
		in.ok("la configuración ya apuntaba a los binarios de PostgreSQL")
	case in.opt.DryRun:
		in.would("se guardaría pg_bin_dir = %s en %s", binDir, in.Cfg.ConfigFile())
	default:
		in.Cfg.PGBinDir = binDir
		if err := in.Cfg.Save(); err != nil {
			return err
		}
		in.ok("configuración guardada en %s", in.Cfg.ConfigFile())
	}
	return nil
}

// --- step 3: cluster, database and migrations ---

func (in *Installer) stepDatabase(ctx context.Context) error {
	if in.opt.DryRun {
		in.would("se inicializaría el clúster en %s, se arrancaría de forma temporal, se crearía la base de datos %q y se aplicarían las migraciones", in.Cfg.PGDataDir(), databaseName)
		return nil
	}
	cl, err := in.newCluster(in.Cfg)
	if err != nil {
		return err
	}
	unlock, err := cl.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	if cl.Initialized() {
		in.ok("el clúster ya estaba inicializado en %s", in.Cfg.PGDataDir())
	} else {
		in.doing("inicializando el clúster de PostgreSQL…")
		if err := cl.Init(ctx); err != nil {
			return err
		}
		in.ok("clúster inicializado en %s", in.Cfg.PGDataDir())
	}

	running, err := cl.Status(ctx)
	if err != nil {
		return err
	}
	if !running {
		in.doing("arrancando PostgreSQL de forma temporal…")
		if err := cl.Start(ctx); err != nil {
			return err
		}
		// The temporary server is stopped whatever happens next, so the service
		// starts with everything ready and owns the process.
		defer func() {
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			if err := cl.Stop(sctx); err != nil {
				in.warn("no se pudo parar el PostgreSQL temporal: %v", err)
			} else {
				in.ok("PostgreSQL temporal parado")
			}
		}()
	}

	if err := cl.EnsureDatabase(ctx, databaseName); err != nil {
		return err
	}
	changed, err := cl.Migrate(ctx)
	if err != nil {
		return err
	}
	if changed {
		in.ok("base de datos %q lista y migraciones aplicadas", databaseName)
	} else {
		in.ok("la base de datos %q ya estaba al día", databaseName)
	}
	return nil
}

// --- step 4: Ollama ---

func (in *Installer) stepOllama(ctx context.Context) error {
	api := in.ollama()
	url := in.Cfg.OllamaURL
	dir := in.ollamaDir

	if version, up := api.Detect(ctx, url); up {
		in.ok("Ollama %s responde en %s: se reutiliza", version, url)
		return nil
	}
	if in.opt.SkipOllama {
		in.warn("no hay Ollama en %s y se omite su instalación (--skip-ollama): sin él no se calculan embeddings", url)
		return nil
	}
	if in.opt.SkipService {
		in.warn("no hay Ollama en %s: sin servicio (--skip-service) no habría quien lo arrancara, así que no se descarga; instala Ollama por tu cuenta", url)
		return nil
	}

	if exe, err := api.Find(dir); err == nil {
		in.ollamaExe, in.ollamaService = exe, true
		in.ok("el Ollama de lodan ya estaba instalado en %s (se registrará como servicio)", exe)
		return nil
	}

	size := ollamaSize(in.Env.GOOS)
	if in.opt.DryRun {
		in.ollamaExe = filepath.Join(dir, "bin", "ollama"+exeSuffix(in.Env.GOOS))
		in.ollamaService = true
		in.would("no hay Ollama en %s: se pediría confirmación y se descargarían %s en %s", url, size, dir)
		return nil
	}
	if !in.confirm(fmt.Sprintf("No hay ningún Ollama en %s. ¿Descargar e instalar el de lodan (unos %s) en %s?", url, size, dir)) {
		in.warn("Ollama no instalado: sin él no se calculan embeddings; vuelve a ejecutar lodan install cuando quieras")
		return nil
	}
	exe, err := api.Install(ctx, dir, func(done, total int64) {
		in.progress(formatProgress("descargando Ollama", done, total))
	})
	in.progressDone()
	if err != nil {
		return err
	}
	in.ollamaExe, in.ollamaService = exe, true
	in.ok("Ollama instalado en %s (escuchará solo en 127.0.0.1)", exe)
	return nil
}

// ollamaSize is the approximate size of the Ollama download on goos.
func ollamaSize(goos string) string {
	if goos == "darwin" {
		return "160 MB"
	}
	return "1,5 GB"
}

// --- step 5: system services ---

// serviceNames lists the services this run is responsible for.
func (in *Installer) serviceNames() []string {
	names := []string{service.DefaultName}
	if in.ollamaService {
		names = append(names, OllamaServiceName)
	}
	return names
}

// serviceParams describes the services to register for this run.
func (in *Installer) serviceParams() ServiceParams {
	p := ServiceParams{DataDir: in.Cfg.DataDir}
	if in.Env.GOOS != "windows" {
		p.User = in.Username
		p.Home = in.Env.Home
	}
	if in.ollamaService {
		p.OllamaExe, p.OllamaDir = in.ollamaExe, in.ollamaDir
	}
	return p
}

// servicesNeeded reports whether the privileged step has to run: a service is
// missing, stopped or disabled, or the binary changed under it.
func (in *Installer) servicesNeeded(names []string) bool {
	if in.binUpdated {
		return true
	}
	for _, n := range names {
		st, err := in.manager().Status(n)
		if err != nil || st.State != service.StateRunning || !st.Enabled {
			return true
		}
	}
	return false
}

func (in *Installer) stepServices(ctx context.Context) error {
	if in.opt.SkipService {
		in.warn("omitido (--skip-service): PostgreSQL arrancará bajo demanda cuando un cliente lance «lodan serve»")
		return nil
	}
	names := in.serviceNames()
	if !in.servicesNeeded(names) {
		in.ok("los servicios %s ya estaban registrados, habilitados y en marcha", strings.Join(names, ", "))
		return nil
	}

	p := in.serviceParams()
	if in.Env.GOOS != "windows" && p.User == "" {
		return errors.New("no se pudo determinar el usuario con el que debe ejecutarse el servicio")
	}
	// On Windows the elevated process writes its output here, because its window closes at the
	// end and the error would be lost.
	logFile := in.elevatedLogFile()
	p.LogFile = logFile
	args := ServiceInstallArgs(p)
	how := "sudo"
	if in.Env.GOOS == "windows" {
		how = "UAC"
	}
	if in.opt.DryRun {
		in.would("se registrarían y arrancarían los servicios %s con permisos de administrador (%s): %s %s",
			strings.Join(names, ", "), how, in.bin, strings.Join(args, " "))
		return nil
	}

	// Created by the regular user so the logs of launchd belong to them.
	if err := os.MkdirAll(in.Cfg.LogsDir(), 0o700); err != nil {
		return fmt.Errorf("no se pudo crear %s: %w", in.Cfg.LogsDir(), err)
	}
	in.doing("registrando los servicios %s: hacen falta permisos de administrador (%s)", strings.Join(names, ", "), how)
	resetElevatedLog(logFile)
	if err := in.elevate()(ctx, in.bin, args); err != nil {
		return withElevatedLog(fmt.Errorf("no se pudo registrar el servicio: %w", err), logFile)
	}

	for _, name := range names {
		st, err := in.manager().Status(name)
		switch {
		case err != nil:
			in.warn("servicio %s: no se pudo verificar su estado: %v", name, err)
		case st.State == service.StateNotInstalled:
			return withElevatedLog(fmt.Errorf("el servicio %s no aparece registrado tras el paso con administrador (¿se canceló?)", name), logFile)
		case st.State == service.StateRunning:
			in.ok("servicio %s registrado y en marcha", name)
		default:
			msg := fmt.Sprintf("servicio %s registrado, pero su estado es «%s»: revísalo con «lodan service status --name %s»", name, st.State, name)
			if text := elevatedLogText(logFile); text != "" {
				msg += "\n  " + text
			}
			in.warn("%s", msg)
		}
	}
	return nil
}

// --- step 6: embedding model ---

func (in *Installer) stepModel(ctx context.Context) error {
	model, url := in.Cfg.EmbedModel, in.Cfg.OllamaURL
	if in.opt.DryRun {
		in.would("se comprobaría que Ollama tiene el modelo %s y, si falta, se descargaría con /api/pull", model)
		return nil
	}
	api := in.ollama()

	_, up := api.Detect(ctx, url)
	if !up && in.ollamaService && !in.opt.SkipService {
		up = in.waitOllama(ctx)
	}
	if !up {
		in.warn("Ollama no responde en %s: no se ha descargado el modelo %s; cuando Ollama esté en marcha ejecuta «ollama pull %s» o vuelve a lanzar lodan install", url, model, model)
		return nil
	}

	has, err := api.HasModel(ctx, url, model)
	if err != nil {
		return err
	}
	if has {
		in.ok("el modelo %s ya estaba en Ollama", model)
		return nil
	}

	in.doing("descargando el modelo %s", model)
	var (
		lastStatus string
		lastPrint  time.Time
	)
	err = api.Pull(ctx, url, model, func(status string, done, total int64) {
		if status == lastStatus && time.Since(lastPrint) < 100*time.Millisecond {
			return
		}
		lastStatus, lastPrint = status, time.Now()
		in.progress(formatProgress(status, done, total))
	})
	in.progressDone()
	if err != nil {
		return err
	}
	in.ok("modelo %s descargado", model)
	return nil
}

// waitOllama polls Ollama until it answers or OllamaWait (3 min by default) passes.
func (in *Installer) waitOllama(ctx context.Context) bool {
	limit := in.OllamaWait
	if limit <= 0 {
		limit = defaultOllamaWait
	}
	in.doing("esperando a que Ollama responda en %s (hasta %s)", in.Cfg.OllamaURL, limit)
	deadline := time.Now().Add(limit)
	for {
		if _, up := in.ollama().Detect(ctx, in.Cfg.OllamaURL); up {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// --- step 7: MCP clients ---

// clientEntryEnv is the env of the lodan entry in a client: empty unless the
// data directory is not the default (Hermes does not inherit the environment,
// so it always gets it).
func (in *Installer) clientEntryEnv(c Client) map[string]string {
	env := map[string]string{}
	def, err := config.DefaultDataDir()
	if c.ID == "hermes" || err != nil || filepath.Clean(def) != filepath.Clean(in.Cfg.DataDir) {
		env["LODAN_DATA_DIR"] = in.Cfg.DataDir
	}
	return env
}

func (in *Installer) stepClients(ctx context.Context) error {
	if in.opt.SkipClients {
		in.warn("omitido (--skip-clients): no se toca la configuración de ningún cliente")
		return nil
	}

	detected := DetectClients(in.Env)
	var selected []Client
	if len(in.opt.Only) == 0 {
		selected = detected
	} else {
		byID := map[string]Client{}
		for _, c := range detected {
			byID[c.ID] = c
		}
		for _, id := range in.opt.Only {
			if c, ok := byID[id]; ok {
				selected = append(selected, c)
			} else {
				in.warn("el cliente %s no está instalado en este equipo: se omite", id)
			}
		}
	}
	if len(selected) == 0 {
		in.ok("no hay clientes MCP que configurar")
		return nil
	}

	names := make([]string, len(selected))
	for i, c := range selected {
		names[i] = c.Name
	}
	in.printf("  Clientes detectados: %s\n", strings.Join(names, ", "))
	if !in.opt.DryRun && !in.confirm(fmt.Sprintf("¿Añadir lodan a estos %d clientes?", len(selected))) {
		in.warn("clientes sin configurar: vuelve a ejecutar lodan install para hacerlo")
		return nil
	}

	for _, c := range selected {
		changed, err := Configure(c, in.bin, in.clientEntryEnv(c), in.opt.DryRun)
		switch {
		case err != nil && IsWarning(err):
			in.warn("%s: %v", c.Name, err)
		case err != nil:
			in.fail("%s: %v", c.Name, err)
		case !changed:
			in.ok("%s: ya estaba (%s)", c.Name, c.File())
		case in.opt.DryRun:
			in.would("%s: se añadiría la entrada lodan en %s", c.Name, c.File())
		default:
			in.ok("%s: entrada lodan añadida en %s", c.Name, c.File())
		}
	}
	return nil
}

// --- step 8: skill and instructions ---

func (in *Installer) stepSkill(ctx context.Context) error {
	if in.SkillFS == nil {
		return errors.New("falta la skill embebida en el binario")
	}
	if ok, _ := CheckSkill(in.SkillFS, in.Env); ok {
		in.ok("la skill lodan-memoria ya estaba instalada y al día")
	} else {
		lines, err := InstallSkill(in.SkillFS, in.Env, in.backupDir(), in.opt.DryRun)
		for _, l := range lines {
			in.reportLine(l)
		}
		if err != nil {
			return err
		}
	}

	lines, err := InstallInstructions(in.Env, in.opt.DryRun)
	for _, l := range lines {
		in.reportLine(l)
	}
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		in.ok("no hay archivos de instrucciones globales que actualizar")
	}
	return nil
}
