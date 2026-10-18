package gitsync_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"lodan/internal/gitsync"
	"lodan/internal/memory"
)

// gitEnv skips the test without git and makes commits work in a clean environment.
func gitEnv(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está instalado")
	}
	t.Setenv("GIT_AUTHOR_NAME", "Lodan Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Lodan Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
}

func gitOut(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func newBare(t *testing.T) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "remote.git")
	gitOut(t, "init", "--bare", "--initial-branch=main", bare)
	return bare
}

func commitCount(t *testing.T, bare string) int {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", bare, "rev-list", "--count", "main").CombinedOutput()
	if err != nil {
		return 0 // main does not exist yet
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("rev-list --count: %q", out)
	}
	return n
}

func gctx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), opTime)
	t.Cleanup(cancel)
	return ctx
}

// cfgFor builds a Config for a machine with its own working copy and key file.
func cfgFor(t *testing.T, bare, machine string) gitsync.Config {
	t.Helper()
	return gitsync.Config{
		RepoURL:   bare,
		Dir:       filepath.Join(t.TempDir(), "wc-"+machine),
		MachineID: machine,
		KeyFile:   filepath.Join(t.TempDir(), machine+".key"),
	}
}

// sharedKey writes the same age identity as key file of every config.
func sharedKey(t *testing.T, cs ...gitsync.Config) {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if err := os.WriteFile(c.KeyFile, []byte(id.String()+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// S7, S9: Init creates the key file when missing and clones the working copy.
func TestInitCreaClaveYClona(t *testing.T) {
	gitEnv(t)
	bare := newBare(t)
	c := cfgFor(t, bare, "maquina-a")

	res, err := gitsync.Init(gctx(t), c)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !res.KeyCreated {
		t.Error("KeyCreated = false, se esperaba true con la clave ausente")
	}
	data, err := os.ReadFile(c.KeyFile)
	if err != nil {
		t.Fatalf("la clave no se creó: %v", err)
	}
	ids, err := age.ParseIdentities(strings.NewReader(string(data)))
	if err != nil || len(ids) != 1 {
		t.Fatalf("la clave no es una identidad age válida: %v (%d)", err, len(ids))
	}
	x, ok := ids[0].(*age.X25519Identity)
	if !ok {
		t.Fatalf("la identidad no es X25519: %T", ids[0])
	}
	if res.Recipient != x.Recipient().String() || !strings.HasPrefix(res.Recipient, "age1") {
		t.Errorf("Recipient = %q, se esperaba %q", res.Recipient, x.Recipient())
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(c.KeyFile)
		if info.Mode().Perm() != 0o600 {
			t.Errorf("permisos de la clave = %v, se esperaba 0600", info.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(c.Dir, ".git")); err != nil {
		t.Errorf("la copia de trabajo no es un repositorio clonado: %v", err)
	}
	rel, err := filepath.Rel(c.Dir, c.KeyFile)
	if err == nil && !strings.HasPrefix(rel, "..") {
		t.Errorf("la clave %s está dentro de la copia de trabajo %s", c.KeyFile, c.Dir)
	}
}

// S7, S9: Init never overwrites an existing key and can run twice.
func TestInitNoSobrescribeClaveYEsRepetible(t *testing.T) {
	gitEnv(t)
	bare := newBare(t)
	c := cfgFor(t, bare, "maquina-a")
	id, _ := age.GenerateX25519Identity()
	orig := []byte(id.String() + "\n")
	if err := os.WriteFile(c.KeyFile, orig, 0o600); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		res, err := gitsync.Init(gctx(t), c)
		if err != nil {
			t.Fatalf("Init #%d: %v", i+1, err)
		}
		if res.KeyCreated {
			t.Errorf("Init #%d: KeyCreated = true con una clave existente", i+1)
		}
		if res.Recipient != id.Recipient().String() {
			t.Errorf("Init #%d: Recipient = %q, se esperaba %q", i+1, res.Recipient, id.Recipient())
		}
		got, err := os.ReadFile(c.KeyFile)
		if err != nil || string(got) != string(orig) {
			t.Fatalf("Init #%d modificó la clave existente (err %v)", i+1, err)
		}
	}
}

// S7, S9: push then pull moves a record to the other machine; only encrypted files under changes/<machine>/ reach the remote.
func TestPushPullExtremoAExtremo(t *testing.T) {
	gitEnv(t)
	bare := newBare(t)
	p := newPair(t)
	ca, cb := cfgFor(t, bare, p.a.id), cfgFor(t, bare, p.b.id)
	sharedKey(t, ca, cb)
	for _, c := range []gitsync.Config{ca, cb} {
		if _, err := gitsync.Init(gctx(t), c); err != nil {
			t.Fatalf("Init %s: %v", c.MachineID, err)
		}
	}

	p.a.rem(t, "Titulo sincronizado", "contenido secreto del registro alfa", memory.KindFact, "")
	res, err := gitsync.Push(gctx(t), p.a.pool, ca)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if res.File == "" || res.Memories != 1 {
		t.Fatalf("Push = %+v, se esperaba un fichero con 1 memoria", res)
	}
	if n := commitCount(t, bare); n != 1 {
		t.Fatalf("commits en el remoto = %d, se esperaba 1", n)
	}

	files := strings.Fields(gitOut(t, "--git-dir", bare, "ls-tree", "-r", "--name-only", "main"))
	if len(files) != 1 {
		t.Fatalf("ficheros en el remoto = %v, se esperaba uno solo", files)
	}
	if !strings.HasPrefix(files[0], "changes/"+p.a.id+"/") || !strings.HasSuffix(files[0], ".jsonl.age") {
		t.Errorf("fichero %q fuera de changes/%s/", files[0], p.a.id)
	}
	if files[0] != res.File {
		t.Errorf("File = %q, el remoto tiene %q", res.File, files[0])
	}

	log := gitOut(t, "--git-dir", bare, "log", "-p", "--all")
	keyData, _ := os.ReadFile(ca.KeyFile)
	secret := strings.TrimSpace(string(keyData))
	if strings.Contains(log, "AGE-SECRET-KEY") || strings.Contains(log, secret) {
		t.Error("la clave privada aparece en el historial del remoto")
	}
	if strings.Contains(log, "contenido secreto") || strings.Contains(log, "Titulo sincronizado") {
		t.Error("contenido legible en el historial del remoto")
	}

	pr, err := gitsync.Pull(gctx(t), p.b.pool, cb)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if pr.Files != 1 || len(pr.Errors) != 0 {
		t.Fatalf("Pull = %+v, se esperaba 1 fichero sin errores", pr)
	}
	if n := p.b.count(t, "title = $1", "Titulo sincronizado"); n != 1 {
		t.Errorf("registros en B = %d, se esperaba 1", n)
	}
}

// S7: Push with nothing new writes no file and creates no commit.
func TestPushSinCambiosNoCreaCommit(t *testing.T) {
	gitEnv(t)
	bare := newBare(t)
	m := newMachine(t, "maquina-a", "cliente-a")
	c := cfgFor(t, bare, m.id)
	if _, err := gitsync.Init(gctx(t), c); err != nil {
		t.Fatal(err)
	}
	m.rem(t, "Uno", "contenido uno del registro", memory.KindFact, "")
	if _, err := gitsync.Push(gctx(t), m.pool, c); err != nil {
		t.Fatalf("Push 1: %v", err)
	}
	before := commitCount(t, bare)
	res, err := gitsync.Push(gctx(t), m.pool, c)
	if err != nil {
		t.Fatalf("Push 2: %v", err)
	}
	if res.File != "" {
		t.Errorf("File = %q, se esperaba vacío sin cambios", res.File)
	}
	if after := commitCount(t, bare); after != before || before != 1 {
		t.Errorf("commits antes %d, después %d; se esperaba 1 sin cambios", before, after)
	}
}

// S7: Pull on a remote without commits is not an error.
func TestPullRemotoSinCommits(t *testing.T) {
	gitEnv(t)
	bare := newBare(t)
	m := newMachine(t, "maquina-b", "cliente-b")
	c := cfgFor(t, bare, m.id)
	if _, err := gitsync.Init(gctx(t), c); err != nil {
		t.Fatalf("Init: %v", err)
	}
	res, err := gitsync.Pull(gctx(t), m.pool, c)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if res.Files != 0 || len(res.Errors) != 0 {
		t.Errorf("Pull = %+v, se esperaba 0 ficheros sin errores", res)
	}
}

// S7: Push integrates remote changes first, so alternating pushes without pulling both succeed.
func TestPushAlternadoSinPullPrevio(t *testing.T) {
	gitEnv(t)
	bare := newBare(t)
	p := newPair(t)
	ca, cb := cfgFor(t, bare, p.a.id), cfgFor(t, bare, p.b.id)
	sharedKey(t, ca, cb)
	for _, c := range []gitsync.Config{ca, cb} {
		if _, err := gitsync.Init(gctx(t), c); err != nil {
			t.Fatal(err)
		}
	}

	p.a.rem(t, "De A", "contenido que escribe la máquina A", memory.KindFact, "")
	if _, err := gitsync.Push(gctx(t), p.a.pool, ca); err != nil {
		t.Fatalf("Push de A: %v", err)
	}
	time.Sleep(pause)
	p.b.rem(t, "De B", "contenido que escribe la máquina B", memory.KindFact, "")
	if _, err := gitsync.Push(gctx(t), p.b.pool, cb); err != nil {
		t.Fatalf("Push de B sin Pull previo: %v", err)
	}
	if n := commitCount(t, bare); n != 2 {
		t.Errorf("commits en el remoto = %d, se esperaba 2", n)
	}
	files := strings.Fields(gitOut(t, "--git-dir", bare, "ls-tree", "-r", "--name-only", "main"))
	if len(files) != 2 {
		t.Errorf("ficheros en el remoto = %v, se esperaban 2", files)
	}

	if _, err := gitsync.Pull(gctx(t), p.a.pool, ca); err != nil {
		t.Fatalf("Pull de A: %v", err)
	}
	if n := p.a.count(t, "title = $1", "De B"); n != 1 {
		t.Errorf("registros 'De B' en A = %d, se esperaba 1", n)
	}
}
