package skills

import (
	"strings"
	"testing"
)

func TestFSContieneLaSkill(t *testing.T) {
	for _, name := range []string{
		"lodan-memoria/SKILL.md",
		"lodan-memoria/EXAMPLE.md",
		"lodan-memoria/scripts/validar_registro.py",
		"lodan-memoria/templates/registro.md",
		"lodan-memoria/references/herramientas.md",
		"lodan-memoria/references/que-guardar.md",
	} {
		data, err := FS.ReadFile(name)
		if err != nil {
			t.Errorf("falta %s en la skill embebida: %v", name, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("%s está vacío", name)
		}
	}

	skill, err := FS.ReadFile("lodan-memoria/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(skill), "---\nname: lodan-memoria\n") {
		t.Error("SKILL.md no empieza con el frontmatter esperado")
	}
}
