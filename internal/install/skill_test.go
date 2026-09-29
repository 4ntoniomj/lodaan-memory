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

func skEnv(t *testing.T) Env {
	t.Helper()
	return Env{GOOS: runtime.GOOS, Home: t.TempDir()}
}

func TestSkillInstalaYRetiraLaAntigua(t *testing.T) {
	env := skEnv(t)
	claudeSkills := filepath.Join(env.Home, ".claude", "skills")
	geminiSkills := filepath.Join(env.Home, ".gemini", "config", "skills")
	clWrite(t, filepath.Join(claudeSkills, "lodan-memory", "SKILL.md"), "antigua claude\n")
	clWrite(t, filepath.Join(geminiSkills, "lodan-memory", "SKILL.md"), "antigua gemini\n")
	clWrite(t, filepath.Join(claudeSkills, "lodan-memoria", "obsoleto.txt"), "sobra\n")
	clWrite(t, filepath.Join(claudeSkills, "otra-skill", "SKILL.md"), "ajena\n")

	// Dry run: nothing changes.
	report, err := InstallSkill(skFS(), env, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(report) != 4 || !strings.Contains(strings.Join(report, "\n"), "[simulación]") {
		t.Fatalf("informe del dry run inesperado: %v", report)
	}
	if !pathExists(filepath.Join(claudeSkills, "lodan-memory")) || !pathExists(filepath.Join(claudeSkills, "lodan-memoria", "obsoleto.txt")) {
		t.Fatal("el dry run no debe tocar nada")
	}

	report, err = InstallSkill(skFS(), env, false)
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
		if !pathExists(filepath.Join(root, ".lodan-memory.bak-lodan", "SKILL.md")) {
			t.Fatalf("falta la copia de la skill antigua en %s", root)
		}
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
	report, err = InstallSkill(skFS(), env, false)
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
		if !pathExists(filepath.Join(root, ".lodan-memory.bak-lodan")) {
			t.Fatalf("la copia de la skill antigua debe conservarse en %s", root)
		}
	}
	if !pathExists(filepath.Join(claudeSkills, "otra-skill", "SKILL.md")) {
		t.Fatal("no se debe tocar otras skills")
	}
}

func TestSkillSoloClaudeSiNoHayGemini(t *testing.T) {
	env := skEnv(t)
	report, err := InstallSkill(skFS(), env, false)
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
	if _, err := InstallSkill(fsys, env, false); err == nil {
		t.Fatal("debería fallar si la skill embebida no tiene SKILL.md")
	}
}

func TestSkillRetiradaAntiguaNoPisaCopiaPrevia(t *testing.T) {
	env := skEnv(t)
	root := filepath.Join(env.Home, ".claude", "skills")
	clWrite(t, filepath.Join(root, ".lodan-memory.bak-lodan", "SKILL.md"), "copia previa\n")
	clWrite(t, filepath.Join(root, "lodan-memory", "SKILL.md"), "antigua\n")
	if _, err := InstallSkill(skFS(), env, false); err != nil {
		t.Fatal(err)
	}
	if clRead(t, filepath.Join(root, ".lodan-memory.bak-lodan", "SKILL.md")) != "copia previa\n" {
		t.Fatal("la copia previa no debe sobrescribirse")
	}
	if clRead(t, filepath.Join(root, ".lodan-memory.bak-lodan.1", "SKILL.md")) != "antigua\n" {
		t.Fatal("la skill antigua debe ir a una carpeta con sufijo")
	}
}

func skBlock() string {
	return instructionsBlock("\n")
}

func TestInstruccionesAnadirSustituirYQuitar(t *testing.T) {
	env := skEnv(t)
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
