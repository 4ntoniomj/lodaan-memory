package install

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
)

func skFS() fstest.MapFS {
	fsys := fstest.MapFS{}
	fsys["lodan-memoria/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: lodan-memoria\n---\n")}
	fsys["lodan-memoria/EXAMPLE.md"] = &fstest.MapFile{Data: []byte("ejemplo\n")}
	fsys["lodan-memoria/references/guia.md"] = &fstest.MapFile{Data: []byte("guía\n")}
	fsys["lodan-memoria/scripts/check.py"] = &fstest.MapFile{Data: []byte("#!/usr/bin/env python3\n")}
	fsys["lodan-memoria/templates/nota.md"] = &fstest.MapFile{Data: []byte("plantilla\n")}
	return fsys
}

// skEnv returns an environment with a temporary HOME and no `claude` CLI on
// the PATH. Claude Code only counts as installed if the test writes
// ~/.claude.json (see skClaudeCode).
func skEnv(t *testing.T) Env {
	t.Helper()
	clStubCLI(t, "", nil)
	return Env{GOOS: runtime.GOOS, Home: t.TempDir()}
}

// skClaudeCode makes detectClaudeCode(env) true by creating ~/.claude.json.
func skClaudeCode(t *testing.T, env Env) {
	t.Helper()
	clWrite(t, filepath.Join(env.Home, ".claude.json"), "{}\n")
}

func TestSkillInstalaYRetiraLaAntigua(t *testing.T) {
	env := skEnv(t)
	claudeSkills := filepath.Join(env.Home, ".claude", "skills")
	geminiSkills := filepath.Join(env.Home, ".gemini", "config", "skills")
	clWrite(t, filepath.Join(claudeSkills, "lodan-memory", "SKILL.md"), "antigua claude\n")
	clWrite(t, filepath.Join(geminiSkills, "lodan-memory", "SKILL.md"), "antigua gemini\n")
	clWrite(t, filepath.Join(claudeSkills, "lodan-memoria", "obsoleto.txt"), "sobra\n")
	clWrite(t, filepath.Join(claudeSkills, "otra-skill", "SKILL.md"), "ajena\n")
	backupDir := filepath.Join(t.TempDir(), "backups", "skills")

	// Dry run: nothing changes.
	report, err := InstallSkill(skFS(), env, backupDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(report) != 4 || !strings.Contains(strings.Join(report, "\n"), "[simulación]") {
		t.Fatalf("informe del dry run inesperado: %v", report)
	}
	if !pathExists(filepath.Join(claudeSkills, "lodan-memory")) || !pathExists(filepath.Join(claudeSkills, "lodan-memoria", "obsoleto.txt")) {
		t.Fatal("el dry run no debe tocar nada")
	}
	if pathExists(backupDir) {
		t.Fatal("el dry run no debe crear el directorio de copias")
	}

	report, err = InstallSkill(skFS(), env, backupDir, false)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(report, "\n")
	if !strings.Contains(joined, "skill antigua retirada") || !strings.Contains(joined, "skill instalada") {
		t.Fatalf("informe inesperado: %v", report)
	}
	for _, root := range []string{claudeSkills, geminiSkills} {
		for _, rel := range []string{"SKILL.md", "EXAMPLE.md", "references/guia.md", "scripts/check.py", "templates/nota.md"} {
			if !pathExists(filepath.Join(root, "lodan-memoria", filepath.FromSlash(rel))) {
				t.Fatalf("falta %s en %s", rel, root)
			}
		}
		if pathExists(filepath.Join(root, "lodan-memory")) {
			t.Fatalf("la skill antigua debe retirarse de %s", root)
		}
		// The backup must not stay inside a skills folder (it would load twice).
		if pathExists(filepath.Join(root, ".lodan-memory.bak-lodan")) {
			t.Fatalf("la copia de la skill antigua no debe quedar dentro de %s", root)
		}
	}
	if clRead(t, filepath.Join(backupDir, "lodan-memory", "SKILL.md")) != "antigua claude\n" {
		t.Fatal("falta la copia de la skill antigua de Claude en el directorio de copias")
	}
	if clRead(t, filepath.Join(backupDir, "lodan-memory.1", "SKILL.md")) != "antigua gemini\n" {
		t.Fatal("falta la copia de la skill antigua de Gemini (con sufijo) en el directorio de copias")
	}
	if pathExists(filepath.Join(claudeSkills, "lodan-memoria", "obsoleto.txt")) {
		t.Fatal("el reemplazo debe ser completo")
	}
	if !pathExists(filepath.Join(claudeSkills, "otra-skill", "SKILL.md")) {
		t.Fatal("no se debe tocar otras skills")
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(filepath.Join(claudeSkills, "lodan-memoria", "scripts", "check.py"))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o755 {
			t.Fatalf("permisos de check.py = %v, quiero 0755", st.Mode().Perm())
		}
		st, err = os.Stat(filepath.Join(claudeSkills, "lodan-memoria", "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o644 {
			t.Fatalf("permisos de SKILL.md = %v, quiero 0644", st.Mode().Perm())
		}
	}

	// Second run: replaces again without errors and without another retirement.
	report, err = InstallSkill(skFS(), env, backupDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(report, "\n"), "skill antigua") {
		t.Fatalf("no queda skill antigua que retirar: %v", report)
	}

	// Uninstall removes lodan-memoria and keeps the backup.
	report, err = UninstallSkill(env, false)
	if err != nil || len(report) != 2 {
		t.Fatalf("UninstallSkill: %v %v", report, err)
	}
	for _, root := range []string{claudeSkills, geminiSkills} {
		if pathExists(filepath.Join(root, "lodan-memoria")) {
			t.Fatalf("lodan-memoria debería haberse quitado de %s", root)
		}
	}
	if !pathExists(filepath.Join(backupDir, "lodan-memory", "SKILL.md")) {
		t.Fatal("la copia de la skill antigua debe conservarse")
	}
	if !pathExists(filepath.Join(claudeSkills, "otra-skill", "SKILL.md")) {
		t.Fatal("no se debe tocar otras skills")
	}
}

func TestSkillSoloClaudeSiNoHayGemini(t *testing.T) {
	env := skEnv(t)
	report, err := InstallSkill(skFS(), env, filepath.Join(t.TempDir(), "backups"), false)
	if err != nil || len(report) != 1 {
		t.Fatalf("InstallSkill: %v %v", report, err)
	}
	if pathExists(filepath.Join(env.Home, ".gemini")) {
		t.Fatal("no debe crearse ~/.gemini")
	}
}

func TestSkillSinSKILLmdEsError(t *testing.T) {
	env := skEnv(t)
	fsys := fstest.MapFS{}
	fsys["lodan-memoria/EXAMPLE.md"] = &fstest.MapFile{Data: []byte("x")}
	if _, err := InstallSkill(fsys, env, filepath.Join(t.TempDir(), "backups"), false); err == nil {
		t.Fatal("debería fallar si la skill embebida no tiene SKILL.md")
	}
}

func TestSkillSinDirectorioDeCopiasEsError(t *testing.T) {
	env := skEnv(t)
	if _, err := InstallSkill(skFS(), env, "", false); err == nil {
		t.Fatal("debería fallar sin directorio de copias")
	}
}

func TestSkillRetiradaAntiguaNoPisaCopiaPrevia(t *testing.T) {
	env := skEnv(t)
	root := filepath.Join(env.Home, ".claude", "skills")
	backupDir := filepath.Join(t.TempDir(), "backups", "skills")
	clWrite(t, filepath.Join(backupDir, "lodan-memory", "SKILL.md"), "copia previa\n")
	clWrite(t, filepath.Join(root, "lodan-memory", "SKILL.md"), "antigua\n")
	if _, err := InstallSkill(skFS(), env, backupDir, false); err != nil {
		t.Fatal(err)
	}
	if clRead(t, filepath.Join(backupDir, "lodan-memory", "SKILL.md")) != "copia previa\n" {
		t.Fatal("la copia previa no debe sobrescribirse")
	}
	if clRead(t, filepath.Join(backupDir, "lodan-memory.1", "SKILL.md")) != "antigua\n" {
		t.Fatal("la skill antigua debe ir a una carpeta con sufijo")
	}
}

func TestCheckSkill(t *testing.T) {
	env := skEnv(t)
	backupDir := filepath.Join(t.TempDir(), "backups")
	root := filepath.Join(env.Home, ".claude", "skills")

	if ok, detail := CheckSkill(skFS(), env); ok || !strings.Contains(detail, "falta la skill") {
		t.Fatalf("sin instalar debería fallar: %v %q", ok, detail)
	}
	if _, err := InstallSkill(skFS(), env, backupDir, false); err != nil {
		t.Fatal(err)
	}
	if ok, detail := CheckSkill(skFS(), env); !ok {
		t.Fatalf("tras instalar debería estar bien: %q", detail)
	}

	// A modified file, an extra file and the old skill are all reported.
	clWrite(t, filepath.Join(root, "lodan-memoria", "SKILL.md"), "editada\n")
	if ok, detail := CheckSkill(skFS(), env); ok || !strings.Contains(detail, "SKILL.md es distinto") {
		t.Fatalf("archivo modificado: %v %q", ok, detail)
	}
	if _, err := InstallSkill(skFS(), env, backupDir, false); err != nil {
		t.Fatal(err)
	}
	clWrite(t, filepath.Join(root, "lodan-memoria", "extra.md"), "sobra\n")
	if ok, detail := CheckSkill(skFS(), env); ok || !strings.Contains(detail, "sobra extra.md") {
		t.Fatalf("archivo sobrante: %v %q", ok, detail)
	}
	if _, err := InstallSkill(skFS(), env, backupDir, false); err != nil {
		t.Fatal(err)
	}
	clWrite(t, filepath.Join(root, "lodan-memory", "SKILL.md"), "antigua\n")
	if ok, detail := CheckSkill(skFS(), env); ok || !strings.Contains(detail, "skill antigua") {
		t.Fatalf("skill antigua: %v %q", ok, detail)
	}
}

func TestCheckInstructions(t *testing.T) {
	env := skEnv(t)
	if ok, detail := CheckInstructions(env); !ok || !strings.Contains(detail, "no hay carpetas") {
		t.Fatalf("sin carpetas no hay nada que comprobar: %v %q", ok, detail)
	}

	codex := filepath.Join(env.Home, ".codex", "AGENTS.md")
	clWrite(t, codex, "# Reglas\n")
	if ok, detail := CheckInstructions(env); ok || !strings.Contains(detail, codex) {
		t.Fatalf("sin bloque debería fallar: %v %q", ok, detail)
	}
	if _, err := InstallInstructions(env, false); err != nil {
		t.Fatal(err)
	}
	if ok, detail := CheckInstructions(env); !ok {
		t.Fatalf("con el bloque debería estar bien: %q", detail)
	}
	clWrite(t, codex, strings.Replace(clRead(t, codex), "prioridad", "XXXX", 1))
	if ok, _ := CheckInstructions(env); ok {
		t.Fatal("un bloque desactualizado debe reportarse")
	}

	// The hand-written section counts as present in CLAUDE.md.
	env2 := skEnv(t)
	skClaudeCode(t, env2)
	clWrite(t, filepath.Join(env2.Home, ".claude", "CLAUDE.md"), "# Mío\n\n## Memoria persistente: lodan\n\n- regla\n")
	if ok, detail := CheckInstructions(env2); !ok {
		t.Fatalf("la sección manual cuenta como presente: %q", detail)
	}
}

func TestCopyTree(t *testing.T) {
	src := filepath.Join(t.TempDir(), "origen")
	dst := filepath.Join(t.TempDir(), "destino")
	clWrite(t, filepath.Join(src, "a.txt"), "a\n")
	clWrite(t, filepath.Join(src, "sub", "b.txt"), "b\n")
	if runtime.GOOS != "windows" {
		if err := os.Symlink("a.txt", filepath.Join(src, "enlace")); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	if clRead(t, filepath.Join(dst, "a.txt")) != "a\n" || clRead(t, filepath.Join(dst, "sub", "b.txt")) != "b\n" {
		t.Fatal("copia incompleta")
	}
	if runtime.GOOS != "windows" {
		if target, err := os.Readlink(filepath.Join(dst, "enlace")); err != nil || target != "a.txt" {
			t.Fatalf("el enlace simbólico debe conservarse: %q %v", target, err)
		}
	}
}

func skBlock() string {
	return instructionsBlock("\n")
}

func TestInstruccionesAnadirSustituirYQuitar(t *testing.T) {
	env := skEnv(t)
	skClaudeCode(t, env)
	codex := filepath.Join(env.Home, ".codex", "AGENTS.md")
	claude := filepath.Join(env.Home, ".claude", "CLAUDE.md")
	gemini := filepath.Join(env.Home, ".gemini", "GEMINI.md")
	clWrite(t, codex, "# Reglas\n")
	clWrite(t, claude, "# Mío\n\ntexto sin sección de lodan\n")
	if err := os.MkdirAll(filepath.Dir(gemini), 0o755); err != nil {
		t.Fatal(err)
	}

	// Dry run.
	report, err := InstallInstructions(env, true)
	if err != nil || len(report) != 3 {
		t.Fatalf("dry run: %v %v", report, err)
	}
	if clRead(t, codex) != "# Reglas\n" || pathExists(gemini) || pathExists(codex+".bak-lodan") {
		t.Fatal("el dry run no debe tocar nada")
	}

	report, err = InstallInstructions(env, false)
	if err != nil || len(report) != 3 {
		t.Fatalf("InstallInstructions: %v %v", report, err)
	}
	if got, want := clRead(t, codex), "# Reglas\n\n"+skBlock()+"\n"; got != want {
		t.Fatalf("AGENTS.md inesperado:\n%q\nquiero:\n%q", got, want)
	}
	if got, want := clRead(t, claude), "# Mío\n\ntexto sin sección de lodan\n\n"+skBlock()+"\n"; got != want {
		t.Fatalf("CLAUDE.md inesperado:\n%q", got)
	}
	if got, want := clRead(t, gemini), skBlock()+"\n"; got != want {
		t.Fatalf("GEMINI.md (nuevo) inesperado:\n%q", got)
	}
	if clRead(t, codex+".bak-lodan") != "# Reglas\n" {
		t.Fatal("falta la copia .bak-lodan del original")
	}
	if pathExists(gemini + ".bak-lodan") {
		t.Fatal("no hay original de GEMINI.md que copiar")
	}
	block := strings.Contains(clRead(t, codex), "`remember`") && strings.Contains(clRead(t, codex), "<!-- lodan:fin -->")
	if !block {
		t.Fatal("el bloque debe llevar el texto y los marcadores")
	}

	// Second run: nothing changes.
	report, err = InstallInstructions(env, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.Join(report, "\n"), "sin cambios") != 3 {
		t.Fatalf("la segunda vez debe ser 'sin cambios': %v", report)
	}

	// A modified block is replaced by the current text.
	modified := strings.Replace(clRead(t, codex), "prioridad", "XXXX", 1)
	clWrite(t, codex, modified)
	report, err = InstallInstructions(env, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(report, "\n"), "bloque actualizado") {
		t.Fatalf("debería sustituir el bloque: %v", report)
	}
	if got, want := clRead(t, codex), "# Reglas\n\n"+skBlock()+"\n"; got != want {
		t.Fatalf("AGENTS.md tras sustituir:\n%q", got)
	}

	// Removal restores the original text; the file created by lodan disappears.
	report, err = UninstallInstructions(env, false)
	if err != nil || len(report) != 3 {
		t.Fatalf("UninstallInstructions: %v %v", report, err)
	}
	if clRead(t, codex) != "# Reglas\n" {
		t.Fatalf("AGENTS.md no se restauró:\n%q", clRead(t, codex))
	}
	if clRead(t, claude) != "# Mío\n\ntexto sin sección de lodan\n" {
		t.Fatalf("CLAUDE.md no se restauró:\n%q", clRead(t, claude))
	}
	if pathExists(gemini) {
		t.Fatal("GEMINI.md solo tenía el bloque y debería haberse borrado")
	}
	if report, err := UninstallInstructions(env, false); err != nil || len(report) != 0 {
		t.Fatalf("segunda retirada: %v %v", report, err)
	}
}

func TestInstruccionesSeccionManualEnClaude(t *testing.T) {
	env := skEnv(t)
	skClaudeCode(t, env)
	claude := filepath.Join(env.Home, ".claude", "CLAUDE.md")
	codex := filepath.Join(env.Home, ".codex", "AGENTS.md")
	manual := "# Mío\n\n## Memoria persistente: lodan\n\n- regla manual\n"
	clWrite(t, claude, manual)
	clWrite(t, codex, manual)

	report, err := InstallInstructions(env, false)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(report, "\n")
	if !strings.Contains(joined, claude+": ya presente (manual)") {
		t.Fatalf("CLAUDE.md debe reportarse como manual: %v", report)
	}
	if clRead(t, claude) != manual || pathExists(claude+".bak-lodan") {
		t.Fatal("CLAUDE.md no debe tocarse")
	}
	// The manual exception only applies to CLAUDE.md.
	if !strings.Contains(clRead(t, codex), "<!-- lodan:inicio -->") {
		t.Fatal("AGENTS.md sí debe recibir el bloque")
	}
	// Uninstall does not touch the manual section.
	if _, err := UninstallInstructions(env, false); err != nil {
		t.Fatal(err)
	}
	if clRead(t, claude) != manual {
		t.Fatal("la sección manual no debe quitarse")
	}
}

func TestInstruccionesSinCarpetaNoCreaNada(t *testing.T) {
	env := skEnv(t)
	report, err := InstallInstructions(env, false)
	if err != nil || len(report) != 0 {
		t.Fatalf("no debería hacer nada: %v %v", report, err)
	}
	for _, d := range []string{".claude", ".codex", ".gemini"} {
		if pathExists(filepath.Join(env.Home, d)) {
			t.Fatalf("no debe crearse ~/%s", d)
		}
	}
}

func TestClaudeCodeNoSeDetectaSoloPorLaCarpeta(t *testing.T) {
	env := skEnv(t)
	if err := os.MkdirAll(filepath.Join(env.Home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if detectClaudeCode(env) {
		t.Fatal("la carpeta ~/.claude por sí sola no debe contar como Claude Code instalado")
	}
	for _, c := range DetectClients(env) {
		if c.ID == "claude-code" {
			t.Fatal("claude-code no debe detectarse solo con ~/.claude")
		}
	}
	skClaudeCode(t, env)
	if !detectClaudeCode(env) {
		t.Fatal("con ~/.claude.json Claude Code sí está instalado")
	}
}

func TestSkillSinClaudeCodeNoEscribeInstrucciones(t *testing.T) {
	env := skEnv(t)
	if _, err := InstallSkill(skFS(), env, filepath.Join(t.TempDir(), "backups"), false); err != nil {
		t.Fatal(err)
	}
	// The skill creates ~/.claude, which must not make lodan think that
	// Claude Code is installed.
	if !pathExists(filepath.Join(env.Home, ".claude", "skills", skillName, "SKILL.md")) {
		t.Fatal("la skill debe instalarse siempre en ~/.claude/skills")
	}
	report, err := InstallInstructions(env, false)
	if err != nil || len(report) != 0 {
		t.Fatalf("sin Claude Code no hay instrucciones que escribir: %v %v", report, err)
	}
	if pathExists(filepath.Join(env.Home, ".claude", "CLAUDE.md")) {
		t.Fatal("no debe escribirse ~/.claude/CLAUDE.md sin Claude Code")
	}
	if ok, detail := CheckInstructions(env); !ok || !strings.Contains(detail, "no hay carpetas") {
		t.Fatalf("doctor no debe exigir el bloque sin Claude Code: %v %q", ok, detail)
	}
}

func TestInstruccionesConClaudeCodeSinCarpetaCreanLaCarpeta(t *testing.T) {
	env := skEnv(t)
	skClaudeCode(t, env)
	if _, err := InstallInstructions(env, false); err != nil {
		t.Fatal(err)
	}
	if !fileHas(filepath.Join(env.Home, ".claude", "CLAUDE.md"), instructionsStart) {
		t.Fatal("con Claude Code detectado debe escribirse el bloque aunque ~/.claude no exista")
	}
}

func TestDesinstalarQuitaLaCarpetaClaudeSiLaCreoLodan(t *testing.T) {
	env := skEnv(t)
	if _, err := InstallSkill(skFS(), env, filepath.Join(t.TempDir(), "backups"), false); err != nil {
		t.Fatal(err)
	}
	// Dry run: nothing is removed.
	if _, err := UninstallSkill(env, true); err != nil || !pathExists(filepath.Join(env.Home, ".claude", "skills", skillName)) {
		t.Fatalf("el dry run no debe tocar nada: %v", err)
	}
	report, err := UninstallInstructions(env, false)
	if err != nil || len(report) != 0 {
		t.Fatalf("UninstallInstructions: %v %v", report, err)
	}
	report, err = UninstallSkill(env, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(report, "\n"), "carpeta vacía retirada") {
		t.Fatalf("debe informar de la carpeta vacía retirada: %v", report)
	}
	if pathExists(filepath.Join(env.Home, ".claude")) {
		t.Fatal("~/.claude debe desaparecer: la creó lodan y quedó vacía")
	}
}

func TestDesinstalarQuitaClaudeTrasRetirarSkillEInstrucciones(t *testing.T) {
	env := skEnv(t)
	skClaudeCode(t, env)
	if _, err := InstallSkill(skFS(), env, filepath.Join(t.TempDir(), "backups"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallInstructions(env, false); err != nil {
		t.Fatal(err)
	}
	// Same order as lodan uninstall: instructions first, then the skill.
	if _, err := UninstallInstructions(env, false); err != nil {
		t.Fatal(err)
	}
	if !pathExists(filepath.Join(env.Home, ".claude", "skills", skillName)) {
		t.Fatal("la skill sigue ahí hasta que se retira")
	}
	if _, err := UninstallSkill(env, false); err != nil {
		t.Fatal(err)
	}
	if pathExists(filepath.Join(env.Home, ".claude")) {
		t.Fatal("~/.claude debe desaparecer")
	}
	if !pathExists(filepath.Join(env.Home, ".claude.json")) {
		t.Fatal("~/.claude.json no es de lodan y no debe borrarse")
	}
}

func TestDesinstalarNoBorraClaudeConContenidoAjeno(t *testing.T) {
	env := skEnv(t)
	settings := filepath.Join(env.Home, ".claude", "settings.json")
	clWrite(t, settings, "{}\n")
	if _, err := InstallSkill(skFS(), env, filepath.Join(t.TempDir(), "backups"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallSkill(env, false); err != nil {
		t.Fatal(err)
	}
	if !pathExists(settings) {
		t.Fatal("~/.claude con contenido ajeno no se debe borrar")
	}
	if pathExists(filepath.Join(env.Home, ".claude", "skills")) {
		t.Fatal("skills/ quedó vacía y debe retirarse")
	}

	// Another skill inside skills/ keeps the folder, and so ~/.claude.
	env = skEnv(t)
	other := filepath.Join(env.Home, ".claude", "skills", "otra-skill", "SKILL.md")
	clWrite(t, other, "ajena\n")
	if _, err := InstallSkill(skFS(), env, filepath.Join(t.TempDir(), "backups"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallSkill(env, false); err != nil {
		t.Fatal(err)
	}
	if !pathExists(other) {
		t.Fatal("no se debe tocar otras skills ni sus carpetas")
	}
}

func TestDesinstalarQuitaElBloqueAunqueNoSeDetecteClaudeCode(t *testing.T) {
	env := skEnv(t)
	claude := filepath.Join(env.Home, ".claude", "CLAUDE.md")
	clWrite(t, claude, "# Mío\n\n"+skBlock()+"\n")
	if detectClaudeCode(env) {
		t.Fatal("precondición: Claude Code no debe detectarse")
	}
	report, err := UninstallInstructions(env, false)
	if err != nil || len(report) != 1 {
		t.Fatalf("UninstallInstructions: %v %v", report, err)
	}
	if clRead(t, claude) != "# Mío\n" {
		t.Fatalf("CLAUDE.md no se restauró:\n%q", clRead(t, claude))
	}

	// A file that only held the block is deleted, and ~/.claude with it.
	env = skEnv(t)
	claude = filepath.Join(env.Home, ".claude", "CLAUDE.md")
	clWrite(t, claude, skBlock()+"\n")
	if _, err := UninstallInstructions(env, false); err != nil {
		t.Fatal(err)
	}
	if pathExists(filepath.Join(env.Home, ".claude")) {
		t.Fatal("~/.claude quedó vacía y debe retirarse")
	}
}

func TestInstruccionesMarcadorSinFinEsAviso(t *testing.T) {
	env := skEnv(t)
	codex := filepath.Join(env.Home, ".codex", "AGENTS.md")
	broken := "# Reglas\n<!-- lodan:inicio -->\nmedio escrito\n"
	clWrite(t, codex, broken)
	report, err := InstallInstructions(env, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report) != 1 || !strings.HasPrefix(report[0], "aviso:") {
		t.Fatalf("debería avisar: %v", report)
	}
	if clRead(t, codex) != broken {
		t.Fatal("no debe tocarse")
	}
}
