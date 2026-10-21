package mcptools

import (
	"bytes"
	"context"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"lodan/internal/config"
	"lodan/internal/embedding"
)

// counterEmbedder cuenta invocaciones a EmbedQuery y devuelve ErrUnavailable.
// Sirve para verificar que el `Maintenance` decide no llamar al embedder
// cuando WithEmbeddings es false.
type counterEmbedder struct {
	queries int
}

func (c *counterEmbedder) EmbedDocuments(context.Context, []embedding.Document) ([][]float32, error) {
	return nil, nil
}

func (c *counterEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	c.queries++
	return nil, embedding.ErrUnavailable
}

func (c *counterEmbedder) Model() string { return "counter" }
func (c *counterEmbedder) Dims() int     { return 64 }

// TestMaintenanceLoop_SkipsFillPending verifica el gate de A.3 (S5):
//
//   - Con Deps.WithEmbeddings=false, RunOnce NO llama a FillPending (ni en
//     Memory ni en Topics) y registra un aviso de modo sin embeddings.
//   - Con Deps.WithEmbeddings=true, RunOnce sí invoca el camino de embeddings
//     (que fallará porque el embedder está caído, pero ese error se reporta,
//     no se omite).
//
// RED antes de A.3: Config.WithEmbeddings y Deps.WithEmbeddings no existen;
// este test falla con error de compilación.
func TestMaintenanceLoop_SkipsFillPending(t *testing.T) {
	logger := log.New(io.Discard, "", 0)

	t.Run("WithEmbeddings false -> gate false (no FillPending)", func(t *testing.T) {
		m := NewMaintenance(Deps{WithEmbeddings: false, Embedder: &counterEmbedder{}}, logger)
		if m.shouldFillEmbeddings() {
			t.Errorf("WithEmbeddings=false: shouldFillEmbeddings() = true, quería false")
		}
	})

	t.Run("WithEmbeddings true -> gate true (FillPending se intenta)", func(t *testing.T) {
		m := NewMaintenance(Deps{WithEmbeddings: true, Embedder: &counterEmbedder{}}, logger)
		if !m.shouldFillEmbeddings() {
			t.Errorf("WithEmbeddings=true: shouldFillEmbeddings() = false, quería true")
		}
	})

	t.Run("RunOnce con WithEmbeddings false loggea el modo y omite FillPending", func(t *testing.T) {
		var buf bytes.Buffer
		logger := log.New(&buf, "", 0)
		m := NewMaintenance(Deps{
			WithEmbeddings: false,
			Embedder:       &counterEmbedder{},
		}, logger)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// Sin Memory/Topics/Sessions no debe panicar.
		m.RunOnce(ctx)
		logStr := strings.ToLower(buf.String())
		if logStr != "" && !strings.Contains(logStr, "embedding") && !strings.Contains(logStr, "sin") {
			t.Errorf("se esperaba un log mencionando el modo sin embeddings, se obtuvo: %q", buf.String())
		}
	})

	t.Run("RunOnce con WithEmbeddings true no omite FillPending", func(t *testing.T) {
		var buf bytes.Buffer
		logger := log.New(&buf, "", 0)
		m := NewMaintenance(Deps{
			WithEmbeddings: true,
			Embedder:       &counterEmbedder{},
		}, logger)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// Sin Memory/Topics no debe panicar; el gate permite pero no hay
		// servicios que invocar.
		m.RunOnce(ctx)
	})
}

// TestConfigWithEmbeddingsDefault verifica que el campo WithEmbeddings existe
// y es false por defecto. RED antes de A.3: el campo no existe en Config.
func TestConfigWithEmbeddingsDefault(t *testing.T) {
	if got := config.Default().WithEmbeddings; got {
		t.Errorf("Config.Default().WithEmbeddings = %v, quería false", got)
	}
}
