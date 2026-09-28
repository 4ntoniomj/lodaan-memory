package export

import (
	"context"
	"encoding/json"
	"io"

	"github.com/lodan/memory/internal/memory"
)

const ExportVersion = "memory-export-v1"

type ExportFormat struct {
	Version   string              `json:"version"`
	Model     string              `json:"model"`
	Items     []memory.MemoryItem `json:"items"`
	Links     []memory.MemoryLink `json:"links"`
}

type Exporter struct {
	storage memory.Storage
}

func NewExporter(store memory.Storage) *Exporter {
	return &Exporter{storage: store}
}

func (e *Exporter) Export(ctx context.Context, w io.Writer) error {
	// En una implementación real, iteraríamos usando un cursor o paginación sobre el storage
	// para no cargar toda la base de datos en RAM.
	format := ExportFormat{
		Version: ExportVersion,
		Model:   "embeddinggemma:300m-qat-q4_0",
		Items:   []memory.MemoryItem{},
		Links:   []memory.MemoryLink{},
	}
	
	enc := json.NewEncoder(w)
	return enc.Encode(format)
}

func (e *Exporter) Import(ctx context.Context, r io.Reader) error {
	var format ExportFormat
	dec := json.NewDecoder(r)
	if err := dec.Decode(&format); err != nil {
		return err
	}
	
	// Aquí iteraríamos insertando transaccionalmente.
	// if format.Version != ExportVersion { ...manejar migración... }
	
	return nil
}
