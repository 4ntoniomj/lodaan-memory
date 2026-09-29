package install

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const skillName = "lodan-memoria"

// oldSkillName is the skill of the prototype that lodan-memoria replaces.
const oldSkillName = "lodan-memory"

const instructionsStart = "<!-- lodan:inicio -->"

const instructionsEnd = "<!-- lodan:fin -->"

// manualSectionHeading is the section that the user may already have written
// by hand in ~/.claude/CLAUDE.md.
const manualSectionHeading = "## Memoria persistente: lodan"

// instructionsText is the rule written between the lodan markers in the global
// instruction files of the clients.
const instructionsText = "## Memoria persistente: lodan\n" +
	"\n" +
	"La memoria persistente del usuario es **lodan** (servidor MCP `lodan`, compartido con todas sus IAs) y tiene prioridad sobre cualquier otro almacén de memoria. Usa siempre la skill `lodan-memoria` si está disponible; en resumen:\n" +
	"- Guarda con `remember`, sin preguntar, lo que tenga sustancia (decisiones, datos estables, preferencias, eventos, notas); nunca charla ni órdenes de rol. Avisa en una línea de lo guardado.\n" +
	"- En cuanto la conversación toque un tema, llama a `recall` una vez en lenguaje natural antes de responder.\n" +
	"- Al terminar una conversación con contenido guardado, cierra con `session` (`action: end`) y un resumen breve.\n" +
	"- Si lodan no está disponible, dilo y no lo sustituyas en silencio por otro almacén."

// instructionsBlock returns the marked block using the given line terminator.
func instructionsBlock(eol string) string {
	return instructionsStart + eol + strings.ReplaceAll(instructionsText, "\n", eol) + eol + instructionsEnd
}

// skillRoots returns the skills folders lodan writes to: ~/.claude/skills
// always and ~/.gemini/config/skills only if it already exists.
func skillRoots(env Env) ([]string, error) {
	if env.Home == "" {
		return nil, errors.New("no se conoce el directorio personal del usuario")
	}
	roots := []string{filepath.Join(env.Home, ".claude", "skills")}
	gemini := filepath.Join(env.Home, ".gemini", "config", "skills")
	if dirExists(gemini) {
		roots = append(roots, gemini)
	}
	return roots, nil
}

func simulated(dryRun bool) string {
	if dryRun {
		return "[simulación] "
	}
	return ""
}

// InstallSkill copies the lodan-memoria skill from fsys (which must contain a
// lodan-memoria/ folder) to ~/.claude/skills/lodan-memoria and, when
// ~/.gemini/config/skills exists, also there. The destination is replaced
// completely. scripts/*.py are written executable. The old prototype skill
// lodan-memory, if present, is moved (not deleted) to
// <backupDir>/lodan-memory (with a numeric suffix if that already exists). The
// backup must live outside any skills folder, or the client would load the old
// skill next to the new one. It returns one message per action.
func InstallSkill(fsys fs.FS, env Env, backupDir string, dryRun bool) ([]string, error) {
	if backupDir == "" {
		return nil, errors.New("falta el directorio de copias de seguridad de la skill antigua")
	}
	src, err := fs.Sub(fsys, skillName)
	if err != nil {
		return nil, err
	}
	if _, err := fs.Stat(src, "SKILL.md"); err != nil {
		return nil, fmt.Errorf("la skill embebida no contiene %s/SKILL.md: %w", skillName, err)
	}
	roots, err := skillRoots(env)
	if err != nil {
		return nil, err
	}
	var report []string
	for _, root := range roots {
		msg, err := retireOldSkill(root, backupDir, dryRun)
		if err != nil {
			return report, err
		}
		if msg != "" {
			report = append(report, msg)
		}
		dest := filepath.Join(root, skillName)
		if !dryRun {
			if err := copySkillTree(src, root, dest); err != nil {
				return report, fmt.Errorf("no se pudo instalar la skill en %s: %w", dest, err)
			}
		}
		report = append(report, fmt.Sprintf("%sskill instalada: %s", simulated(dryRun), dest))
	}
	return report, nil
}

// retireOldSkill moves <root>/lodan-memory to <backupDir>/lodan-memory (with a
// numeric suffix if that already exists).
func retireOldSkill(root, backupDir string, dryRun bool) (string, error) {
	old := filepath.Join(root, oldSkillName)
	if _, err := os.Lstat(old); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	bak := filepath.Join(backupDir, oldSkillName)
	base := bak
	for n := 1; n < 1000; n++ {
		if _, err := os.Lstat(bak); errors.Is(err, fs.ErrNotExist) {
			break
		}
		bak = fmt.Sprintf("%s.%d", base, n)
	}
	if !dryRun {
		if err := os.MkdirAll(backupDir, 0o700); err != nil {
			return "", fmt.Errorf("no se pudo crear %s: %w", backupDir, err)
		}
		if err := moveTree(old, bak); err != nil {
			return "", fmt.Errorf("no se pudo retirar la skill antigua %s: %w", old, err)
		}
	}
	return fmt.Sprintf("%sskill antigua retirada: %s -> %s", simulated(dryRun), old, bak), nil
}

// moveTree renames src to dst; if that fails (for example across file systems)
// it copies the tree and removes the original.
func moveTree(src, dst string) error {
	renameErr := os.Rename(src, dst)
	if renameErr == nil {
		return nil
	}
	if err := copyTree(src, dst); err != nil {
		_ = os.RemoveAll(dst)
		return errors.Join(renameErr, err)
	}
	return os.RemoveAll(src)
}

// copyTree copies a directory tree (regular files, directories and symlinks;
// other file types are skipped).
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, info.Mode().Perm())
		default:
			return nil
		}
	})
}

// copySkillTree copies src into a temporary folder next to dest and then swaps
// it in, so a failure never leaves dest half written.
func copySkillTree(src fs.FS, root, dest string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(root, ".lodan-memoria-*")
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := os.Chmod(stage, 0o755); err != nil {
		return err
	}
	err = fs.WalkDir(src, ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == "." {
			return nil
		}
		target := filepath.Join(stage, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if strings.HasPrefix(p, "scripts/") && strings.HasSuffix(p, ".py") {
			perm = 0o755
		}
		if err := os.WriteFile(target, data, perm); err != nil {
			return err
		}
		// WriteFile is subject to the umask: fix the mode explicitly.
		return os.Chmod(target, perm)
	})
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	if err := os.Rename(stage, dest); err != nil {
		return err
	}
	ok = true
	return nil
}

// UninstallSkill removes the lodan-memoria folders that InstallSkill created.
// The backup of the old lodan-memory skill is left alone.
func UninstallSkill(env Env, dryRun bool) ([]string, error) {
	roots, err := skillRoots(env)
	if err != nil {
		return nil, err
	}
	var report []string
	for _, root := range roots {
		dest := filepath.Join(root, skillName)
		if _, err := os.Lstat(dest); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return report, err
		}
		if !dryRun {
			if err := os.RemoveAll(dest); err != nil {
				return report, err
			}
		}
		report = append(report, fmt.Sprintf("%sskill retirada: %s", simulated(dryRun), dest))
	}
	return report, nil
}

// instructionTarget is a global instruction file of a client. It is only
// written when the client folder already exists.
type instructionTarget struct {
	dir         string
	file        string
	manualCheck bool
}

// instructionTargets lists ~/.claude/CLAUDE.md, ~/.codex/AGENTS.md and
// ~/.gemini/GEMINI.md. Only CLAUDE.md is checked for the manual section: the
// user already keeps a hand-written "## Memoria persistente: lodan" there, and
// adding the block on top would duplicate the rule.
func instructionTargets(env Env) []instructionTarget {
	return []instructionTarget{
		{filepath.Join(env.Home, ".claude"), "CLAUDE.md", true},
		{filepath.Join(env.Home, ".codex"), "AGENTS.md", false},
		{filepath.Join(env.Home, ".gemini"), "GEMINI.md", false},
	}
}

func hasManualSection(text string) bool {
	for _, ln := range splitLinesKeep(text) {
		if strings.TrimSpace(ln) == manualSectionHeading {
			return true
		}
	}
	return false
}

// applyInstructionBlock replaces the marked block or appends it at the end
// after a blank line. The second result describes the action.
func applyInstructionBlock(old string) (string, string, error) {
	eol := detectEOL(old)
	block := instructionsBlock(eol)
	if si := strings.Index(old, instructionsStart); si >= 0 {
		rel := strings.Index(old[si:], instructionsEnd)
		if rel < 0 {
			return "", "", &WarningError{Msg: "hay un marcador de inicio sin marcador de fin; no se ha modificado"}
		}
		endPos := si + rel + len(instructionsEnd)
		updated := old[:si] + block + old[endPos:]
		if updated == old {
			return old, "sin cambios", nil
		}
		return updated, "bloque actualizado", nil
	}
	updated := old
	if updated != "" && !strings.HasSuffix(updated, "\n") {
		updated += eol
	}
	if strings.TrimSpace(updated) != "" && !endsWithBlankLine(updated) {
		updated += eol
	}
	return updated + block + eol, "bloque añadido", nil
}

// removeInstructionBlock removes the marked block including its markers and
// the blank line that InstallInstructions put in front of it.
func removeInstructionBlock(old string) (string, bool, error) {
	si := strings.Index(old, instructionsStart)
	if si < 0 {
		return old, false, nil
	}
	rel := strings.Index(old[si:], instructionsEnd)
	if rel < 0 {
		return "", false, &WarningError{Msg: "hay un marcador de inicio sin marcador de fin; no se ha modificado"}
	}
	after := old[si+rel+len(instructionsEnd):]
	switch {
	case strings.HasPrefix(after, "\r\n"):
		after = after[2:]
	case strings.HasPrefix(after, "\n"):
		after = after[1:]
	}
	before := old[:si]
	if endsWithBlankLine(before) && (strings.TrimSpace(after) == "" || startsWithBlankLine(after)) {
		lines := splitLinesKeep(before)
		before = strings.Join(lines[:len(lines)-1], "")
	}
	return before + after, true, nil
}

// InstallInstructions writes the lodan block between <!-- lodan:inicio --> and
// <!-- lodan:fin --> in the global instruction files of Claude Code, Codex and
// Gemini, only when their folder (~/.claude, ~/.codex, ~/.gemini) exists.
//
// Rules: an existing block is replaced; otherwise it is appended after a blank
// line; the first modification of a file keeps a .bak-lodan copy.
//
// Decision: if ~/.claude/CLAUDE.md already has the hand-written section
// "## Memoria persistente: lodan" and no markers, nothing is added and it is
// reported as "ya presente (manual)". lodan never edits text it does not own
// and adding the block would duplicate the rule. Once the user deletes the
// manual section, the next install adds the block.
func InstallInstructions(env Env, dryRun bool) ([]string, error) {
	if env.Home == "" {
		return nil, errors.New("no se conoce el directorio personal del usuario")
	}
	var report []string
	for _, t := range instructionTargets(env) {
		if !dirExists(t.dir) {
			continue
		}
		path := filepath.Join(t.dir, t.file)
		action := ""
		_, err := editTextFile(path, 0o644, false, dryRun, func(old string) (string, error) {
			if t.manualCheck && !strings.Contains(old, instructionsStart) && hasManualSection(old) {
				action = "ya presente (manual)"
				return old, nil
			}
			updated, act, err := applyInstructionBlock(old)
			action = act
			return updated, err
		})
		if err != nil {
			var w *WarningError
			if errors.As(err, &w) {
				report = append(report, fmt.Sprintf("aviso: %s: %s", path, w.Msg))
				continue
			}
			return report, err
		}
		report = append(report, fmt.Sprintf("%s%s: %s", simulated(dryRun), path, action))
	}
	return report, nil
}

// CheckSkill verifies that every skills folder lodan writes to holds a
// lodan-memoria identical to the embedded one and no old lodan-memory. On
// failure the detail explains what is wrong.
func CheckSkill(fsys fs.FS, env Env) (ok bool, detail string) {
	src, err := fs.Sub(fsys, skillName)
	if err != nil {
		return false, err.Error()
	}
	roots, err := skillRoots(env)
	if err != nil {
		return false, err.Error()
	}
	var dests []string
	for _, root := range roots {
		dest := filepath.Join(root, skillName)
		if !dirExists(dest) {
			return false, fmt.Sprintf("falta la skill en %s", dest)
		}
		if diff := skillDiff(src, dest); diff != "" {
			return false, fmt.Sprintf("la skill de %s no coincide con la embebida (%s)", dest, diff)
		}
		if _, err := os.Lstat(filepath.Join(root, oldSkillName)); err == nil {
			return false, fmt.Sprintf("sigue instalada la skill antigua %s", filepath.Join(root, oldSkillName))
		}
		dests = append(dests, dest)
	}
	return true, strings.Join(dests, ", ")
}

// skillDiff returns a short description of the first difference between the
// embedded skill and the folder dest, or "" when they are identical.
func skillDiff(src fs.FS, dest string) string {
	diff := ""
	_ = fs.WalkDir(src, ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			diff = walkErr.Error()
			return fs.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		want, err := fs.ReadFile(src, p)
		if err != nil {
			diff = err.Error()
			return fs.SkipAll
		}
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(p)))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			diff = "falta " + p
		case err != nil:
			diff = err.Error()
		case string(got) != string(want):
			diff = p + " es distinto"
		default:
			return nil
		}
		return fs.SkipAll
	})
	if diff != "" {
		return diff
	}
	// Files in dest that the embedded skill does not have.
	_ = filepath.WalkDir(dest, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			diff = walkErr.Error()
			return fs.SkipAll
		}
		rel, err := filepath.Rel(dest, p)
		if err != nil || rel == "." {
			return nil
		}
		if _, err := fs.Stat(src, filepath.ToSlash(rel)); err != nil {
			diff = "sobra " + filepath.ToSlash(rel)
			return fs.SkipAll
		}
		return nil
	})
	return diff
}

// CheckInstructions verifies that the global instruction files of the clients
// whose folder exists carry the current lodan block (or the hand-written
// section that InstallInstructions respects).
func CheckInstructions(env Env) (ok bool, detail string) {
	if env.Home == "" {
		return false, "no se conoce el directorio personal del usuario"
	}
	var checked, problems []string
	for _, t := range instructionTargets(env) {
		if !dirExists(t.dir) {
			continue
		}
		path := filepath.Join(t.dir, t.file)
		text := ""
		if data, err := os.ReadFile(path); err == nil {
			text = string(data)
		} else if !errors.Is(err, fs.ErrNotExist) {
			problems = append(problems, fmt.Sprintf("%s (%v)", path, err))
			continue
		}
		checked = append(checked, path)
		if t.manualCheck && !strings.Contains(text, instructionsStart) && hasManualSection(text) {
			continue
		}
		if _, act, err := applyInstructionBlock(text); err != nil || act != "sin cambios" {
			problems = append(problems, path)
		}
	}
	if len(problems) > 0 {
		return false, "falta el bloque de lodan o está desactualizado en: " + strings.Join(problems, ", ")
	}
	if len(checked) == 0 {
		return true, "no hay carpetas de instrucciones globales que gestionar"
	}
	return true, strings.Join(checked, ", ")
}

// UninstallInstructions removes the marked block (markers included) from the
// global instruction files. A file that only held the block is deleted.
func UninstallInstructions(env Env, dryRun bool) ([]string, error) {
	if env.Home == "" {
		return nil, errors.New("no se conoce el directorio personal del usuario")
	}
	var report []string
	for _, t := range instructionTargets(env) {
		path := filepath.Join(t.dir, t.file)
		if !pathExists(path) {
			continue
		}
		removed := false
		_, err := editTextFile(path, 0o644, true, dryRun, func(old string) (string, error) {
			updated, ok, err := removeInstructionBlock(old)
			removed = ok
			return updated, err
		})
		if err != nil {
			var w *WarningError
			if errors.As(err, &w) {
				report = append(report, fmt.Sprintf("aviso: %s: %s", path, w.Msg))
				continue
			}
			return report, err
		}
		if removed {
			report = append(report, fmt.Sprintf("%s%s: bloque quitado", simulated(dryRun), path))
		}
	}
	return report, nil
}
