// Package config holds lodan's settings: defaults, config.json and LODAN_* environment overrides.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is the full lodan configuration.
type Config struct {
	DataDir               string  `json:"-"` // never persisted: comes from LODAN_DATA_DIR or the OS default
	PGPort                int     `json:"pg_port"`
	PGBinDir              string  `json:"pg_bin_dir"`
	OllamaURL             string  `json:"ollama_url"`
	EmbedModel            string  `json:"embed_model"`
	EmbedDims             int     `json:"embed_dims"`
	EmbedKeepAlive        string  `json:"embed_keep_alive"`
	EmbedTimeoutMs        int     `json:"embed_timeout_ms"` // max wait for the embedding when saving
	HTTPAddr              string  `json:"http_addr"`
	RecallMaxBytes        int     `json:"recall_max_bytes"`
	SessionIdleMinutes    int     `json:"session_idle_minutes"`
	DupSimilarity         float64 `json:"dup_similarity"`
	TopicSimilarity       float64 `json:"topic_similarity"`        // two topic names are equivalent
	TopicDetectSimilarity float64 `json:"topic_detect_similarity"` // a query is detected as belonging to a topic
}

// Default returns the configuration with built-in defaults.
// DataDir and PGBinDir are left empty.
func Default() Config {
	return Config{
		PGPort:                54329,
		OllamaURL:             "http://127.0.0.1:11434",
		EmbedModel:            "embeddinggemma",
		EmbedDims:             768,
		EmbedKeepAlive:        "-1",
		EmbedTimeoutMs:        2000,
		HTTPAddr:              "127.0.0.1:7438",
		RecallMaxBytes:        6000,
		SessionIdleMinutes:    30,
		DupSimilarity:         0.92,
		TopicSimilarity:       0.72, // calibrated with the reference model, see specs/001-nucleo-memoria/calibracion.md
		TopicDetectSimilarity: 0.40,
	}
}

// Load builds the configuration. Priority: defaults < config.json < environment.
// DataDir comes only from LODAN_DATA_DIR or the OS default, never from config.json.
func Load() (Config, error) {
	cfg := Default()

	dataDir := os.Getenv("LODAN_DATA_DIR")
	if dataDir == "" {
		d, err := DefaultDataDir()
		if err != nil {
			return Config{}, err
		}
		dataDir = d
	}
	cfg.DataDir = dataDir

	data, err := os.ReadFile(cfg.ConfigFile())
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("configuración inválida en %s: %w", cfg.ConfigFile(), err)
		}
		cfg.DataDir = dataDir
	case errors.Is(err, os.ErrNotExist):
		// Sin fichero: se usan los valores por defecto.
	default:
		return Config{}, fmt.Errorf("no se pudo leer %s: %w", cfg.ConfigFile(), err)
	}

	if err := applyEnv(&cfg); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// applyEnv overrides cfg with the LODAN_* environment variables that are set and non-empty.
func applyEnv(cfg *Config) error {
	strs := []struct {
		name string
		dst  *string
	}{
		{"LODAN_PG_BIN_DIR", &cfg.PGBinDir},
		{"LODAN_OLLAMA_URL", &cfg.OllamaURL},
		{"LODAN_EMBED_MODEL", &cfg.EmbedModel},
		{"LODAN_EMBED_KEEP_ALIVE", &cfg.EmbedKeepAlive},
		{"LODAN_HTTP_ADDR", &cfg.HTTPAddr},
	}
	for _, s := range strs {
		if v := os.Getenv(s.name); v != "" {
			*s.dst = v
		}
	}

	ints := []struct {
		name string
		dst  *int
	}{
		{"LODAN_PG_PORT", &cfg.PGPort},
		{"LODAN_EMBED_DIMS", &cfg.EmbedDims},
		{"LODAN_EMBED_TIMEOUT_MS", &cfg.EmbedTimeoutMs},
		{"LODAN_RECALL_MAX_BYTES", &cfg.RecallMaxBytes},
		{"LODAN_SESSION_IDLE_MINUTES", &cfg.SessionIdleMinutes},
	}
	for _, i := range ints {
		v := os.Getenv(i.name)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("%s: %q no es un entero válido", i.name, v)
		}
		*i.dst = n
	}

	floats := []struct {
		name string
		dst  *float64
	}{
		{"LODAN_DUP_SIMILARITY", &cfg.DupSimilarity},
		{"LODAN_TOPIC_SIMILARITY", &cfg.TopicSimilarity},
		{"LODAN_TOPIC_DETECT_SIMILARITY", &cfg.TopicDetectSimilarity},
	}
	for _, f := range floats {
		v := os.Getenv(f.name)
		if v == "" {
			continue
		}
		x, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return fmt.Errorf("%s: %q no es un número válido", f.name, v)
		}
		*f.dst = x
	}
	return nil
}

// Validate checks that the configuration values are coherent.
func (c Config) Validate() error {
	if c.PGPort < 1 || c.PGPort > 65535 {
		return fmt.Errorf("pg_port debe estar entre 1 y 65535, y es %d", c.PGPort)
	}
	if c.EmbedDims <= 0 {
		return fmt.Errorf("embed_dims debe ser mayor que 0, y es %d", c.EmbedDims)
	}
	if c.EmbedTimeoutMs < 100 {
		return fmt.Errorf("embed_timeout_ms debe ser al menos 100, y es %d", c.EmbedTimeoutMs)
	}
	if !(c.DupSimilarity > 0 && c.DupSimilarity <= 1) {
		return fmt.Errorf("dup_similarity debe estar en (0,1], y es %v", c.DupSimilarity)
	}
	if !(c.TopicSimilarity > 0 && c.TopicSimilarity <= 1) {
		return fmt.Errorf("topic_similarity debe estar en (0,1], y es %v", c.TopicSimilarity)
	}
	if !(c.TopicDetectSimilarity > 0 && c.TopicDetectSimilarity <= 1) {
		return fmt.Errorf("topic_detect_similarity debe estar en (0,1], y es %v", c.TopicDetectSimilarity)
	}
	if c.RecallMaxBytes < 500 {
		return fmt.Errorf("recall_max_bytes debe ser al menos 500, y es %d", c.RecallMaxBytes)
	}
	if c.SessionIdleMinutes < 1 {
		return fmt.Errorf("session_idle_minutes debe ser al menos 1, y es %d", c.SessionIdleMinutes)
	}

	host, port, err := net.SplitHostPort(c.HTTPAddr)
	if err != nil {
		return fmt.Errorf("http_addr %q no es válida (se espera host:puerto): %w", c.HTTPAddr, err)
	}
	if host != "127.0.0.1" && host != "localhost" {
		return fmt.Errorf("http_addr %q no es local: solo se admite 127.0.0.1 o localhost", c.HTTPAddr)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("http_addr %q: el puerto debe estar entre 1 y 65535", c.HTTPAddr)
	}
	return nil
}

// Save writes config.json (indented) into DataDir, creating the directory if needed.
func (c Config) Save() error {
	if c.DataDir == "" {
		return errors.New("no se puede guardar la configuración: DataDir está vacío")
	}
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return fmt.Errorf("no se pudo crear %s: %w", c.DataDir, err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("no se pudo serializar la configuración: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(c.ConfigFile(), data, 0o600); err != nil {
		return fmt.Errorf("no se pudo escribir %s: %w", c.ConfigFile(), err)
	}
	return nil
}

// DetectPGBinDir locates the PostgreSQL binaries directory.
// Order: cfg.PGBinDir, `pg_config --bindir`, then the directory of initdb in PATH.
func DetectPGBinDir(cfg Config) (string, error) {
	if cfg.PGBinDir != "" {
		if isDir(cfg.PGBinDir) {
			return cfg.PGBinDir, nil
		}
		return "", fmt.Errorf("el directorio de binarios de PostgreSQL configurado no existe: %s", cfg.PGBinDir)
	}

	if pgConfig, err := exec.LookPath("pg_config"); err == nil {
		if out, err := exec.Command(pgConfig, "--bindir").Output(); err == nil {
			if dir := strings.TrimSpace(string(out)); dir != "" && isDir(dir) {
				return dir, nil
			}
		}
	}

	if initdb, err := exec.LookPath("initdb"); err == nil {
		return filepath.Dir(initdb), nil
	}

	return "", errors.New("no se encontraron los binarios de PostgreSQL: instala PostgreSQL, añádelo al PATH o define LODAN_PG_BIN_DIR")
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
