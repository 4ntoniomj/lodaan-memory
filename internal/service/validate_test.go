package service

import (
	"slices"
	"testing"
)

func TestEnvArgsOrdenadoPorClave(t *testing.T) {
	got := envArgs(map[string]string{"B": "2", "A": "con espacios", "C": ""})
	want := []string{"--env", "A=con espacios", "--env", "B=2", "--env", "C="}
	if !slices.Equal(got, want) {
		t.Errorf("envArgs = %q, se esperaba %q", got, want)
	}
	if got := envArgs(nil); len(got) != 0 {
		t.Errorf("sin entorno no debe haber argumentos: %q", got)
	}
}

func TestValidateName(t *testing.T) {
	for _, ok := range []string{"lodan", "lodan-ollama", "a.b_c-1"} {
		if err := validateName(ok); err != nil {
			t.Errorf("%q debería ser válido: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "../x", "a/b", "con espacio", "-x", ".x"} {
		if err := validateName(bad); err == nil {
			t.Errorf("%q debería ser inválido", bad)
		}
	}
}
