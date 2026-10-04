package service

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// nameRe restricts service names to a safe subset: the name ends up in file
// paths (unit, plist) and in command arguments.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// validateName checks that name is usable as a service identifier.
func validateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("nombre de servicio no válido %q: solo letras, dígitos, punto, guion y guion bajo", name)
	}
	return nil
}

// validate checks the fields every platform needs.
func (s Spec) validate() error {
	if err := validateName(s.Name); err != nil {
		return err
	}
	if s.Exec == "" {
		return errors.New("falta la ruta del ejecutable del servicio")
	}
	if !filepath.IsAbs(s.Exec) {
		return fmt.Errorf("la ruta del ejecutable debe ser absoluta: %q", s.Exec)
	}
	for k, v := range s.Env {
		if k == "" || strings.ContainsAny(k, "=\x00\n\r") {
			return fmt.Errorf("nombre de variable de entorno no válido: %q", k)
		}
		if strings.ContainsAny(v, "\x00\n\r") {
			return fmt.Errorf("valor no válido para la variable de entorno %s: no puede contener saltos de línea ni NUL", k)
		}
	}
	return nil
}

// sortedEnv returns the environment as "K=V" strings sorted by key, so the
// generated files are deterministic.
func sortedEnv(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		out = append(out, k+"="+env[k])
	}
	return out
}

// envArgs turns the environment into "--env K=V" argument pairs. It is the
// way to hand the environment to a Windows service, which has no per-service
// environment in the SCM API.
func envArgs(env map[string]string) []string {
	var out []string
	for _, kv := range sortedEnv(env) {
		out = append(out, "--env", kv)
	}
	return out
}

// firstNonEmpty returns the first argument that is not empty.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
