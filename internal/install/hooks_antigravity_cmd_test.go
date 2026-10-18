package install

import (
	"encoding/base64"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

const (
	agCompactTail = ` hook compact --client antigravity-client --format antigravity`
	agSessionTail = ` hook session-start --client antigravity-client --format antigravity`
	agPSPrefix    = "powershell -NoProfile -NonInteractive -EncodedCommand "
)

// agCommands returns the two SessionStart commands of Antigravity's lodan group.
func agCommands(t *testing.T, e hkEnvT) []string {
	t.Helper()
	got := hkJSON(t, e.path(".gemini/config/hooks.json"))
	lodan, _ := got["lodan"].(map[string]any)
	list, _ := lodan["SessionStart"].([]any)
	var out []string
	for _, h := range list {
		m, _ := h.(map[string]any)
		c, _ := m["command"].(string)
		out = append(out, c)
	}
	return out
}

// agEncode builds the expected EncodedCommand payload: base64 of UTF-16LE.
func agEncode(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, 0, len(u)*2)
	for _, w := range u {
		b = binary.LittleEndian.AppendUint16(b, w)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// agDecode decodes the payload of an EncodedCommand command and fails if malformed.
func agDecode(t *testing.T, cmd string) string {
	t.Helper()
	if !strings.HasPrefix(cmd, agPSPrefix) {
		t.Fatalf("command = %q, quería prefijo %q", cmd, agPSPrefix)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(cmd, agPSPrefix))
	if err != nil {
		t.Fatalf("base64 inválido: %v", err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("UTF-16LE con longitud impar: %d", len(raw))
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(raw[2*i:])
	}
	return string(utf16.Decode(u))
}

// L15: bin without spaces -> no quotes, forward slashes.
func TestHooksAntigravityCmdSinEspacios(t *testing.T) {
	e := hkNew(t, ".gemini/config")
	hkInstall(t, e, `C:\x\lodan.exe`)
	want := []string{`C:/x/lodan.exe` + agCompactTail, `C:/x/lodan.exe` + agSessionTail}
	if got := agCommands(t, e); !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %q, quería %q", got, want)
	}
}

// L15: bin with spaces -> powershell -EncodedCommand with the bin in single quotes.
func TestHooksAntigravityCmdConEspacios(t *testing.T) {
	bin := `C:\Program Files\lodan\lodan.exe`
	e := hkNew(t, ".gemini/config")
	hkInstall(t, e, bin)
	got := agCommands(t, e)
	if len(got) != 2 {
		t.Fatalf("commands = %q, quería 2", got)
	}
	wantScripts := []string{
		`& 'C:\Program Files\lodan\lodan.exe'` + agCompactTail,
		`& 'C:\Program Files\lodan\lodan.exe'` + agSessionTail,
	}
	for i, c := range got {
		if s := agDecode(t, c); s != wantScripts[i] {
			t.Errorf("script %d = %q, quería %q", i, s, wantScripts[i])
		}
		if c != agPSPrefix+agEncode(wantScripts[i]) {
			t.Errorf("command %d = %q, base64 distinto del esperado", i, c)
		}
		if strings.ContainsAny(c, `"\`) {
			t.Errorf("command %d contiene comillas o barras invertidas: %q", i, c)
		}
	}
}

// L15: a single quote in the path is doubled (PowerShell style).
func TestHooksAntigravityCmdComillaSimple(t *testing.T) {
	bin := `C:\Users\O'Brien dir\lodan.exe`
	e := hkNew(t, ".gemini/config")
	hkInstall(t, e, bin)
	got := agCommands(t, e)
	if len(got) != 2 {
		t.Fatalf("commands = %q, quería 2", got)
	}
	want := []string{
		`& 'C:\Users\O''Brien dir\lodan.exe'` + agCompactTail,
		`& 'C:\Users\O''Brien dir\lodan.exe'` + agSessionTail,
	}
	for i, c := range got {
		if s := agDecode(t, c); s != want[i] {
			t.Errorf("script %d = %q, quería %q", i, s, want[i])
		}
	}
}

// L15: install twice changes nothing, with both forms.
func TestHooksAntigravityCmdIdempotente(t *testing.T) {
	for _, bin := range []string{`C:\x\lodan.exe`, `C:\Program Files\lodan\lodan.exe`} {
		e := hkNew(t, ".gemini/config")
		hkInstall(t, e, bin)
		before := hkRead(t, e.path(".gemini/config/hooks.json"))
		if files := hkInstall(t, e, bin); len(files) != 0 {
			t.Errorf("%s: la segunda instalación cambió %v", bin, files)
		}
		if hkRead(t, e.path(".gemini/config/hooks.json")) != before {
			t.Errorf("%s: hooks.json cambió en la segunda instalación", bin)
		}
		if n := len(agCommands(t, e)); n != 2 {
			t.Errorf("%s: handlers = %d, quería 2", bin, n)
		}
	}
}

// L15: uninstall removes both handlers and keeps herdr, with both forms.
func TestHooksAntigravityCmdDesinstalar(t *testing.T) {
	for _, bin := range []string{`C:\x\lodan.exe`, `C:\Program Files\lodan\lodan.exe`} {
		e := hkNew(t, ".gemini/config")
		hkWrite(t, e.path(".gemini/config/hooks.json"), hkHooksJSON)
		hkInstall(t, e, bin)
		files, err := UninstallHooks(e.env, false)
		if err != nil {
			t.Fatal(err)
		}
		if !hkHasSuffix(files, ".gemini/config/hooks.json") {
			t.Errorf("%s: files = %v, falta hooks.json", bin, files)
		}
		got := hkJSON(t, e.path(".gemini/config/hooks.json"))
		if _, ok := got["lodan"]; ok {
			t.Errorf("%s: queda el grupo lodan: %v", bin, got["lodan"])
		}
		if _, ok := got["herdr"]; !ok || len(got) != 1 {
			t.Errorf("%s: herdr no se conservó: %v", bin, got)
		}
		if again, err := UninstallHooks(e.env, false); err != nil || len(again) != 0 {
			t.Errorf("%s: segunda desinstalación: files = %v, err = %v", bin, again, err)
		}
	}
}

// L15: CheckHooks is true after install, with both forms.
func TestHooksAntigravityCmdCheck(t *testing.T) {
	for _, bin := range []string{`C:\x\lodan.exe`, `C:\Program Files\lodan\lodan.exe`} {
		e := hkNew(t, ".gemini/config")
		hkInstall(t, e, bin)
		if ok, d := CheckHooks(e.env, bin); !ok {
			t.Errorf("%s: CheckHooks = false (%q)", bin, d)
		}
	}
}
