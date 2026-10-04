package config

import (
	"os"
	"strings"
	"testing"
)

// isolate points the data dir to a temp dir and clears every LODAN_* variable.
func isolate(t *testing.T) string {
	t.Helper()
	for _, name := range []string{
		"LODAN_PG_PORT", "LODAN_PG_BIN_DIR", "LODAN_OLLAMA_URL", "LODAN_EMBED_MODEL",
		"LODAN_EMBED_DIMS", "LODAN_EMBED_KEEP_ALIVE", "LODAN_EMBED_TIMEOUT_MS", "LODAN_HTTP_ADDR",
		"LODAN_RECALL_MAX_BYTES", "LODAN_SESSION_IDLE_MINUTES",
		"LODAN_DUP_SIMILARITY", "LODAN_TOPIC_SIMILARITY", "LODAN_TOPIC_DETECT_SIMILARITY",
	} {
		t.Setenv(name, "")
	}
	dir := t.TempDir()
	t.Setenv("LODAN_DATA_DIR", dir)
	return dir
}

func TestDefault(t *testing.T) {
	c := Default()
	want := Config{
		PGPort:                54329,
		OllamaURL:             "http://127.0.0.1:11434",
		EmbedModel:            "embeddinggemma:300m-qat-q4_0",
		EmbedDims:             768,
		EmbedKeepAlive:        "-1",
		EmbedTimeoutMs:        2000,
		HTTPAddr:              "127.0.0.1:7438",
		RecallMaxBytes:        6000,
		SessionIdleMinutes:    30,
		DupSimilarity:         0.92,
		TopicSimilarity:       0.72,
		TopicDetectSimilarity: 0.40,
	}
	if c != want {
		t.Fatalf("Default() = %+v, se esperaba %+v", c, want)
	}
	c.DataDir = t.TempDir()
	if err := c.Validate(); err != nil {
		t.Fatalf("Default() no valida: %v", err)
	}
}

func TestLoadSinFichero(t *testing.T) {
	dir := isolate(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != dir {
		t.Errorf("DataDir = %q, se esperaba %q", c.DataDir, dir)
	}
	if c.PGPort != 54329 {
		t.Errorf("PGPort = %d, se esperaba el valor por defecto", c.PGPort)
	}
}

func TestLoadPrioridadJSONEntorno(t *testing.T) {
	dir := isolate(t)
	json := `{"pg_port": 6000, "embed_model": "otro-modelo", "recall_max_bytes": 1234, "data_dir": "/no/debe/usarse"}`
	if err := os.WriteFile(dir+"/config.json", []byte(json), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.PGPort != 6000 || c.EmbedModel != "otro-modelo" || c.RecallMaxBytes != 1234 {
		t.Errorf("el JSON no se aplicó: %+v", c)
	}
	if c.EmbedDims != 768 {
		t.Errorf("EmbedDims = %d, se esperaba el defecto 768", c.EmbedDims)
	}
	if c.DataDir != dir {
		t.Errorf("DataDir = %q, el JSON no debe cambiarlo (se esperaba %q)", c.DataDir, dir)
	}

	t.Setenv("LODAN_PG_PORT", "7000")
	t.Setenv("LODAN_DUP_SIMILARITY", "0.5")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.PGPort != 7000 {
		t.Errorf("PGPort = %d, el entorno debe ganar al JSON", c.PGPort)
	}
	if c.DupSimilarity != 0.5 {
		t.Errorf("DupSimilarity = %v, se esperaba 0.5", c.DupSimilarity)
	}
	if c.EmbedModel != "otro-modelo" {
		t.Errorf("EmbedModel = %q, debe conservar el valor del JSON", c.EmbedModel)
	}
}

func TestEmbedTimeoutMs(t *testing.T) {
	dir := isolate(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.EmbedTimeoutMs != 2000 {
		t.Errorf("EmbedTimeoutMs = %d, se esperaba el defecto 2000", c.EmbedTimeoutMs)
	}

	if err := os.WriteFile(dir+"/config.json", []byte(`{"embed_timeout_ms": 3000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err = Load(); err != nil || c.EmbedTimeoutMs != 3000 {
		t.Fatalf("con JSON: EmbedTimeoutMs = %d, %v; se esperaba 3000", c.EmbedTimeoutMs, err)
	}

	t.Setenv("LODAN_EMBED_TIMEOUT_MS", "500")
	if c, err = Load(); err != nil || c.EmbedTimeoutMs != 500 {
		t.Fatalf("con entorno: EmbedTimeoutMs = %d, %v; se esperaba 500", c.EmbedTimeoutMs, err)
	}

	t.Setenv("LODAN_EMBED_TIMEOUT_MS", "99")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "embed_timeout_ms") {
		t.Fatalf("un valor por debajo de 100 debía rechazarse nombrando embed_timeout_ms, error: %v", err)
	}
}

func TestTopicDetectSimilarity(t *testing.T) {
	dir := isolate(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.TopicDetectSimilarity != 0.40 {
		t.Errorf("TopicDetectSimilarity = %v, se esperaba el defecto 0.40", c.TopicDetectSimilarity)
	}
	if c.TopicDetectSimilarity >= c.TopicSimilarity {
		t.Errorf("el umbral de detección (%v) debería ser menor que el de equivalencia (%v)",
			c.TopicDetectSimilarity, c.TopicSimilarity)
	}

	if err := os.WriteFile(dir+"/config.json", []byte(`{"topic_detect_similarity": 0.3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err = Load(); err != nil || c.TopicDetectSimilarity != 0.3 {
		t.Fatalf("con JSON: TopicDetectSimilarity = %v, %v; se esperaba 0.3", c.TopicDetectSimilarity, err)
	}

	t.Setenv("LODAN_TOPIC_DETECT_SIMILARITY", "0.5")
	if c, err = Load(); err != nil || c.TopicDetectSimilarity != 0.5 {
		t.Fatalf("con entorno: TopicDetectSimilarity = %v, %v; se esperaba 0.5", c.TopicDetectSimilarity, err)
	}

	for _, v := range []string{"0", "1.5", "-0.1"} {
		t.Setenv("LODAN_TOPIC_DETECT_SIMILARITY", v)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "topic_detect_similarity") {
			t.Errorf("el valor %q debía rechazarse nombrando topic_detect_similarity, error: %v", v, err)
		}
	}
}

func TestLoadEntornoMalFormado(t *testing.T) {
	casos := []struct{ name, value string }{
		{"LODAN_PG_PORT", "abc"},
		{"LODAN_EMBED_DIMS", "7.5"},
		{"LODAN_EMBED_TIMEOUT_MS", "2s"},
		{"LODAN_RECALL_MAX_BYTES", "mucho"},
		{"LODAN_SESSION_IDLE_MINUTES", "x"},
		{"LODAN_DUP_SIMILARITY", "alta"},
		{"LODAN_TOPIC_SIMILARITY", "0,85"},
		{"LODAN_TOPIC_DETECT_SIMILARITY", "media"},
	}
	for _, tc := range casos {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			t.Setenv(tc.name, tc.value)
			_, err := Load()
			if err == nil {
				t.Fatalf("se esperaba error con %s=%q", tc.name, tc.value)
			}
			if !strings.Contains(err.Error(), tc.name) {
				t.Errorf("el error %q no nombra la variable %s", err, tc.name)
			}
		})
	}
}

func TestLoadJSONMalFormado(t *testing.T) {
	dir := isolate(t)
	if err := os.WriteFile(dir+"/config.json", []byte("{no es json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("se esperaba error con config.json inválido")
	}
}

func TestValidate(t *testing.T) {
	novalidas := map[string]func(*Config){
		"http no local":      func(c *Config) { c.HTTPAddr = "0.0.0.0:7438" },
		"http host externo":  func(c *Config) { c.HTTPAddr = "example.com:7438" },
		"http sin puerto":    func(c *Config) { c.HTTPAddr = "127.0.0.1" },
		"http puerto fuera":  func(c *Config) { c.HTTPAddr = "127.0.0.1:70000" },
		"pg_port cero":       func(c *Config) { c.PGPort = 0 },
		"pg_port alto":       func(c *Config) { c.PGPort = 65536 },
		"dims cero":          func(c *Config) { c.EmbedDims = 0 },
		"timeout bajo":       func(c *Config) { c.EmbedTimeoutMs = 99 },
		"dup cero":           func(c *Config) { c.DupSimilarity = 0 },
		"topic mayor que 1":  func(c *Config) { c.TopicSimilarity = 1.1 },
		"recall bytes bajo":  func(c *Config) { c.RecallMaxBytes = 499 },
		"sesión sin minutos": func(c *Config) { c.SessionIdleMinutes = 0 },
		"detect cero":        func(c *Config) { c.TopicDetectSimilarity = 0 },
		"detect mayor que 1": func(c *Config) { c.TopicDetectSimilarity = 1.1 },
	}
	for name, mutar := range novalidas {
		t.Run(name, func(t *testing.T) {
			c := Default()
			mutar(&c)
			if err := c.Validate(); err == nil {
				t.Fatalf("Validate debía rechazar %+v", c)
			}
		})
	}

	for _, addr := range []string{"127.0.0.1:7438", "localhost:8080"} {
		c := Default()
		c.HTTPAddr = addr
		if err := c.Validate(); err != nil {
			t.Errorf("Validate rechaza %q: %v", addr, err)
		}
	}
}

func TestSaveLoadIdaYVuelta(t *testing.T) {
	dir := isolate(t)
	orig := Default()
	orig.DataDir = dir + "/sub/datos"
	t.Setenv("LODAN_DATA_DIR", orig.DataDir)
	orig.PGPort = 6543
	orig.EmbedModel = "modelo-x"
	orig.HTTPAddr = "localhost:9000"
	orig.TopicSimilarity = 0.8

	if err := orig.Save(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(orig.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 && !isWindows() {
		t.Errorf("permisos de config.json = %v, se esperaba 0600", info.Mode().Perm())
	}
	dinfo, err := os.Stat(orig.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if dinfo.Mode().Perm() != 0o700 && !isWindows() {
		t.Errorf("permisos de DataDir = %v, se esperaba 0700", dinfo.Mode().Perm())
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got != orig {
		t.Fatalf("ida y vuelta distinta:\n got  %+v\n want %+v", got, orig)
	}
}

func TestRutas(t *testing.T) {
	c := Config{DataDir: "/datos"}
	casos := map[string]string{
		c.PGDataDir():  "/datos/pg",
		c.ConfigFile(): "/datos/config.json",
		c.SecretFile(): "/datos/secret",
		c.LogsDir():    "/datos/logs",
		c.LockFile():   "/datos/lodan.lock",

		c.HeartbeatFile():   "/datos/supervisor.heartbeat",
		c.MaintenanceFile(): "/datos/maintenance.pending",
	}
	if isWindows() {
		t.Skip("las rutas esperadas usan separador Unix")
	}
	for got, want := range casos {
		if got != want {
			t.Errorf("ruta = %q, se esperaba %q", got, want)
		}
	}
}

func TestDetectPGBinDirConfigurado(t *testing.T) {
	dir := t.TempDir()
	got, err := DetectPGBinDir(Config{PGBinDir: dir})
	if err != nil || got != dir {
		t.Fatalf("DetectPGBinDir = %q, %v; se esperaba %q", got, err, dir)
	}
	if _, err := DetectPGBinDir(Config{PGBinDir: dir + "/no-existe"}); err == nil {
		t.Fatal("se esperaba error con un directorio configurado inexistente")
	}
}

func isWindows() bool { return os.PathSeparator == '\\' }
