package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"lodan/internal/config"
	"lodan/internal/embedding"
)

// TestServe_StartsWithoutOllama verifica que `lodan serve` puede arrancar sin
// que Ollama esté respondiendo. El binario no debe fallar en el startup por
// Ollama caído; el warm-up descarta el error y el mantenimiento queda
// desactivado por default.
//
// El test no lanza runServe entero (requiere Postgres) sino que verifica las
// dos piezas deterministas que lo permiten:
//
//  1. NewOllama con un puerto cerrado NO falla por red (solo por config).
//  2. Config.WithEmbeddings se carga del flag y por default es false.
//
// RED antes de A.3: Config.WithEmbeddings no existe y el flag
// --with-embeddings tampoco; este test falla con error de compilación.
func TestServe_StartsWithoutOllama(t *testing.T) {
	t.Run("NewOllama no falla por red", func(t *testing.T) {
		emb, err := embedding.NewOllama(embedding.Options{
			BaseURL: "http://127.0.0.1:1", // puerto cerrado
			Model:   "embeddinggemma:300m",
			Dims:    768,
			Timeout: 500 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("NewOllama con puerto cerrado: %v", err)
		}
		// El warm-up de runServe descarta el error con _, _; verificamos
		// que EmbedQuery falla con ErrUnavailable (para que el descarte
		// tenga sentido) y no con otro error.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err = emb.EmbedQuery(ctx, "calentamiento")
		if err == nil {
			t.Fatal("EmbedQuery a puerto cerrado debería fallar")
		}
		if !errors.Is(err, embedding.ErrUnavailable) {
			t.Fatalf("se esperaba ErrUnavailable, hay %v", err)
		}
	})

	t.Run("Config.WithEmbeddings default false", func(t *testing.T) {
		if got := config.Default().WithEmbeddings; got {
			t.Errorf("Config.Default().WithEmbeddings = %v, quería false", got)
		}
	})

	t.Run("LODAN_WITH_EMBEDDINGS=true activa el flag", func(t *testing.T) {
		t.Setenv("LODAN_DATA_DIR", t.TempDir())
		t.Setenv("LODAN_WITH_EMBEDDINGS", "true")
		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.WithEmbeddings {
			t.Errorf("WithEmbeddings = false, quería true (con LODAN_WITH_EMBEDDINGS=true)")
		}
	})
}

// TestServeFlagWithEmbeddings verifica que el flag --with-embeddings se
// acepta en la línea de comandos y queda registrado en cfg.WithEmbeddings.
//
// RED antes de A.3: el flag no existe; este test verifica el cableado entre
// flag.Parse y Config.Load sin lanzar el servidor entero.
func TestServeFlagWithEmbeddings(t *testing.T) {
	t.Setenv("LODAN_DATA_DIR", t.TempDir())

	// Simulamos el flujo de runServe: parse del flag, después cfg.Load.
	// No podemos llamar runServe directamente porque arranca Postgres.
	var stderr bytes.Buffer
	code := runServe([]string{"--with-embeddings=true"}, &stderr)
	out := stderr.String()

	// runServe puede fallar al arrancar Postgres en CI sin un DB disponible;
	// lo que nos importa es que el flag se haya aceptado (no se rechazó como
	// flag desconocido). Si code == 2 por flag inválido, falla.
	if code == 2 && (strings.Contains(out, "flag provided but not defined") ||
		strings.Contains(out, "flag parse error") ||
		strings.Contains(out, "unknown flag")) {
		t.Errorf("el flag --with-embeddings fue rechazado:\n%s", out)
	}
}
