package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// tagLinkOf reads tag_link_strength from the JSON form of the config, so the
// test does not depend on the Go field name.
func tagLinkOf(t *testing.T, c Config) float64 {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	v, ok := m["tag_link_strength"].(float64)
	if !ok {
		t.Fatalf("la configuración no tiene el campo tag_link_strength: %s", raw)
	}
	return v
}

func writeConfigJSON(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(dir+string(os.PathSeparator)+"config.json", []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestTagLinkStrength covers the configuration part of S8 (T3).
func TestTagLinkStrength(t *testing.T) {
	t.Run("default is 3.0 in Default and in Load without file", func(t *testing.T) {
		if got := tagLinkOf(t, Default()); got != 3.0 {
			t.Errorf("Default() tag_link_strength = %v, quería 3", got)
		}
		isolate(t)
		t.Setenv("LODAN_TAG_LINK_STRENGTH", "")
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if got := tagLinkOf(t, c); got != 3.0 {
			t.Errorf("Load() tag_link_strength = %v, quería 3", got)
		}
	})

	t.Run("config.json sets it", func(t *testing.T) {
		dir := isolate(t)
		t.Setenv("LODAN_TAG_LINK_STRENGTH", "")
		writeConfigJSON(t, dir, `{"tag_link_strength": 4.5}`)
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if got := tagLinkOf(t, c); got != 4.5 {
			t.Errorf("tag_link_strength = %v, quería 4.5", got)
		}
	})

	t.Run("LODAN_TAG_LINK_STRENGTH overrides config.json", func(t *testing.T) {
		dir := isolate(t)
		writeConfigJSON(t, dir, `{"tag_link_strength": 4.5}`)
		t.Setenv("LODAN_TAG_LINK_STRENGTH", "2.5")
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if got := tagLinkOf(t, c); got != 2.5 {
			t.Errorf("tag_link_strength = %v, quería 2.5", got)
		}
	})

	t.Run("a value <= 0 fails validation with the exact message", func(t *testing.T) {
		cases := []struct{ env, want string }{
			{"0", "tag_link_strength debe ser mayor que 0, y es 0"},
			{"-1.5", "tag_link_strength debe ser mayor que 0, y es -1.5"},
		}
		for _, c := range cases {
			isolate(t)
			t.Setenv("LODAN_TAG_LINK_STRENGTH", c.env)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("env %q: error = %v, quería que contuviera %q", c.env, err, c.want)
			}
		}
		dir := isolate(t)
		t.Setenv("LODAN_TAG_LINK_STRENGTH", "")
		writeConfigJSON(t, dir, `{"tag_link_strength": -2}`)
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "tag_link_strength debe ser mayor que 0, y es -2") {
			t.Errorf("config.json -2: error = %v", err)
		}
	})

	t.Run("a non numeric env value is rejected like the other float variables", func(t *testing.T) {
		isolate(t)
		t.Setenv("LODAN_TAG_LINK_STRENGTH", "abc")
		_, err := Load()
		want := `LODAN_TAG_LINK_STRENGTH: "abc" no es un número válido`
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, quería que contuviera %q", err, want)
		}
	})

	t.Run("previous: other float variables still apply", func(t *testing.T) {
		isolate(t)
		t.Setenv("LODAN_TAG_LINK_STRENGTH", "")
		t.Setenv("LODAN_DUP_SIMILARITY", "0.5")
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if c.DupSimilarity != 0.5 {
			t.Errorf("DupSimilarity = %v, quería 0.5", c.DupSimilarity)
		}
	})
}
