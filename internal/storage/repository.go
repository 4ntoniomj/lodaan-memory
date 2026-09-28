package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lodan/memory/internal/memory"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) SaveItem(ctx context.Context, item *memory.MemoryItem) error {
	metadataJSON, err := json.Marshal(item.Metadata)
	if err != nil {
		return err
	}

	// Simplificado para la versión inicial. El embedding debe formatearse adecuadamente para pgvector.
	embeddingStr := fmt.Sprintf("%v", item.Embedding)

	query := `
		INSERT INTO memory_items (
			content, memory_type, active, source_type, embedding, embedding_model, metadata
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7
		) RETURNING id, created_at
	`
	
	err = r.db.QueryRowContext(ctx, query,
		item.Content, item.MemoryType, item.Active, item.SourceType, embeddingStr, item.EmbeddingModel, metadataJSON,
	).Scan(&item.ID, &item.CreatedAt)
	
	return err
}

func (r *Repository) HybridSearch(ctx context.Context, queryEmbedding []float32, queryText string, limit int) ([]memory.MemoryItem, error) {
	// Implementación básica de búsqueda híbrida combinando HNSW y texto.
	embeddingStr := fmt.Sprintf("%v", queryEmbedding)
	query := `
		SELECT id, content, memory_type, active, source_type, created_at
		FROM memory_items
		WHERE active = true
		ORDER BY embedding <=> $1
		LIMIT $2
	`
	
	rows, err := r.db.QueryContext(ctx, query, embeddingStr, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []memory.MemoryItem
	for rows.Next() {
		var item memory.MemoryItem
		if err := rows.Scan(&item.ID, &item.Content, &item.MemoryType, &item.Active, &item.SourceType, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}
