package gitsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/jackc/pgx/v5/pgxpool"
)

// remoteBranch is the branch of the sync repository every machine pushes to.
const remoteBranch = "main"

// Config locates the sync repository, its working copy and the key of this machine.
type Config struct {
	RepoURL   string // git URL of the repository
	Dir       string // working copy
	MachineID string
	KeyFile   string // age X25519 identity file
}

// InitResult describes what Init did.
type InitResult struct {
	KeyCreated bool   // false when KeyFile already existed (it is never overwritten)
	Recipient  string // "age1..." derived from the key
}

// ErrNotConfigured is returned when the sync repository has not been set up.
var ErrNotConfigured = errors.New("la sincronización no está configurada: ejecuta lodan sync init --repo <url>")

// Init prepares this machine: it creates the key file if it does not exist and clones the
// repository into c.Dir (or reuses a working copy of the same repository).
func Init(ctx context.Context, c Config) (InitResult, error) {
	if c.RepoURL == "" || c.Dir == "" || c.KeyFile == "" {
		return InitResult{}, errors.New("faltan el repositorio, la copia de trabajo o la clave")
	}
	if err := checkMachineID(c.MachineID); err != nil {
		return InitResult{}, err
	}
	created, err := ensureKey(c.KeyFile)
	if err != nil {
		return InitResult{}, err
	}
	id, err := readKey(c.KeyFile)
	if err != nil {
		return InitResult{}, err
	}
	if err := ensureWorkingCopy(ctx, c); err != nil {
		return InitResult{}, err
	}
	return InitResult{KeyCreated: created, Recipient: id.Recipient().String()}, nil
}

// Push exports the local changes, commits them and pushes them to the remote. Remote changes
// are integrated first, so machines can push in any order.
func Push(ctx context.Context, pool *pgxpool.Pool, c Config) (ExportResult, error) {
	id, err := ready(c)
	if err != nil {
		return ExportResult{}, err
	}
	if err := integrate(ctx, c.Dir); err != nil {
		return ExportResult{}, err
	}
	res, err := Export(ctx, pool, ExportOptions{Dir: c.Dir, MachineID: c.MachineID, Recipient: id.Recipient().String()})
	if err != nil {
		return ExportResult{}, err
	}

	// Commit everything pending in this machine's folder, including files of an earlier run
	// whose commit failed.
	folder := "changes/" + c.MachineID
	if _, err := os.Stat(filepath.Join(c.Dir, filepath.FromSlash(folder))); err == nil {
		if _, err := git(ctx, c.Dir, "add", "--", folder); err != nil {
			return res, err
		}
	}
	if staged, err := hasStaged(ctx, c.Dir); err != nil {
		return res, err
	} else if staged {
		msg := "sync: " + c.MachineID
		if res.File != "" {
			msg += " " + filepath.Base(res.File)
		}
		if _, err := git(ctx, c.Dir, append(identityArgs(ctx, c.Dir), "commit", "--quiet", "-m", msg)...); err != nil {
			return res, err
		}
	}

	ahead, err := aheadOfRemote(ctx, c.Dir)
	if err != nil || !ahead {
		return res, err
	}
	if _, err := git(ctx, c.Dir, "push", "--quiet", "origin", "HEAD:"+remoteBranch); err != nil {
		// Another machine pushed in between: integrate its commits and try once more.
		if err := integrate(ctx, c.Dir); err != nil {
			return res, err
		}
		if _, err := git(ctx, c.Dir, "push", "--quiet", "origin", "HEAD:"+remoteBranch); err != nil {
			return res, err
		}
	}
	return res, nil
}

// Pull brings the remote changes and imports the files of the other machines.
func Pull(ctx context.Context, pool *pgxpool.Pool, c Config) (ImportResult, error) {
	id, err := ready(c)
	if err != nil {
		return ImportResult{}, err
	}
	if err := integrate(ctx, c.Dir); err != nil {
		return ImportResult{}, err
	}
	return Import(ctx, pool, ImportOptions{Dir: c.Dir, MachineID: c.MachineID, Identity: id.String()})
}

// ready checks the configuration and returns the key.
func ready(c Config) (*age.X25519Identity, error) {
	if c.RepoURL == "" {
		return nil, ErrNotConfigured
	}
	if err := checkMachineID(c.MachineID); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(c.Dir, ".git")); err != nil {
		return nil, fmt.Errorf("no existe la copia de trabajo %s: ejecuta lodan sync init --repo %s", c.Dir, c.RepoURL)
	}
	return readKey(c.KeyFile)
}

// ensureKey creates an age X25519 key at path if there is none. It never overwrites a file.
func ensureKey(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("no se pudo comprobar la clave %s: %w", path, err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return false, fmt.Errorf("no se pudo generar la clave: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("no se pudo crear la carpeta de la clave: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, fmt.Errorf("no se pudo crear la clave %s: %w", path, err)
	}
	content := fmt.Sprintf("# clave de sincronización de lodan, creada el %s\n# clave pública: %s\n%s\n",
		time.Now().Format("2006-01-02"), id.Recipient(), id)
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return false, fmt.Errorf("no se pudo escribir la clave %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("no se pudo escribir la clave %s: %w", path, err)
	}
	return true, nil
}

// readKey reads the first X25519 identity of the key file.
func readKey(path string) (*age.X25519Identity, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no existe la clave de sincronización %s: cópiala de otra máquina o ejecuta lodan sync init", path)
	}
	if err != nil {
		return nil, fmt.Errorf("no se pudo leer la clave %s: %w", path, err)
	}
	ids, err := age.ParseIdentities(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("la clave %s no es válida: %w", path, err)
	}
	for _, id := range ids {
		if x, ok := id.(*age.X25519Identity); ok {
			return x, nil
		}
	}
	return nil, fmt.Errorf("la clave %s no contiene una identidad X25519", path)
}

// ensureWorkingCopy clones the repository into c.Dir, or checks that the existing working
// copy points to the same repository.
func ensureWorkingCopy(ctx context.Context, c Config) error {
	if _, err := os.Stat(filepath.Join(c.Dir, ".git")); err == nil {
		url, err := git(ctx, c.Dir, "remote", "get-url", "origin")
		if err != nil {
			return err
		}
		if strings.TrimSpace(url) != c.RepoURL {
			return fmt.Errorf("la copia de trabajo %s apunta a %s, no a %s", c.Dir, strings.TrimSpace(url), c.RepoURL)
		}
		return nil
	}
	if entries, err := os.ReadDir(c.Dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("la carpeta %s ya existe y no es una copia del repositorio", c.Dir)
	}
	if err := os.MkdirAll(filepath.Dir(c.Dir), 0o700); err != nil {
		return fmt.Errorf("no se pudo crear la carpeta de la copia de trabajo: %w", err)
	}
	if _, err := git(ctx, "", "clone", "--quiet", c.RepoURL, c.Dir); err != nil {
		return err
	}
	// An empty repository has no branch yet: commits go to main.
	if _, err := git(ctx, c.Dir, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		if _, err := git(ctx, c.Dir, "symbolic-ref", "HEAD", "refs/heads/"+remoteBranch); err != nil {
			return err
		}
	}
	return nil
}

// integrate brings the remote commits, putting the local unpushed ones on top.
func integrate(ctx context.Context, dir string) error {
	out, err := git(ctx, dir, "ls-remote", "--heads", "origin", remoteBranch)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		return nil // the remote has no commits yet
	}
	_, err = git(ctx, dir, append(identityArgs(ctx, dir), "pull", "--rebase", "--quiet", "origin", remoteBranch)...)
	return err
}

// aheadOfRemote reports whether the working copy has commits the remote does not have.
func aheadOfRemote(ctx context.Context, dir string) (bool, error) {
	if _, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		return false, nil // no commits at all
	}
	if _, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+remoteBranch); err != nil {
		return true, nil // the remote branch does not exist yet
	}
	out, err := git(ctx, dir, "rev-list", "--count", "refs/remotes/origin/"+remoteBranch+"..HEAD")
	if err != nil {
		return false, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return false, fmt.Errorf("respuesta inesperada de git rev-list: %q", out)
	}
	return n > 0, nil
}

func hasStaged(ctx context.Context, dir string) (bool, error) {
	cmd := gitCommand(ctx, dir, "diff", "--cached", "--quiet")
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return false, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return true, nil
	default:
		return false, fmt.Errorf("git diff --cached: %w", err)
	}
}

// identityArgs gives git an author when the user has none configured (for example, a
// service account), so commits never fail for that reason. The user's identity wins.
func identityArgs(ctx context.Context, dir string) []string {
	if out, err := git(ctx, dir, "config", "user.email"); err == nil && strings.TrimSpace(out) != "" {
		return nil
	}
	if os.Getenv("GIT_AUTHOR_EMAIL") != "" && os.Getenv("GIT_COMMITTER_EMAIL") != "" {
		return nil
	}
	return []string{"-c", "user.name=lodan", "-c", "user.email=lodan@localhost"}
}

func gitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	// Never wait for a password prompt: credentials come from the user's git configuration.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd
}

// git runs a git command and returns its standard output.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := gitCommand(ctx, dir, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", errors.New("git no está instalado o no está en el PATH")
		}
		name := args[0]
		if name == "-c" && len(args) > 4 {
			name = args[4]
		}
		return stdout.String(), fmt.Errorf("git %s: %v: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
