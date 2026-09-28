package storage

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/lodan/memory/internal/memory"
	"github.com/pgvector/pgvector-go"
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

	vec := pgvector.NewVector(item.Embedding)

	query := `
		INSERT INTO memory_items (
			content, memory_type, active, source_type, embedding, embedding_model, metadata
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7
		) RETURNING id, created_at
	`
	
	err = r.db.QueryRowContext(ctx, query,
		item.Content, item.MemoryType, item.Active, item.SourceType, vec, item.EmbeddingModel, metadataJSON,
	).Scan(&item.ID, &item.CreatedAt)
	
	return err
}

func (r *Repository) HybridSearch(ctx context.Context, queryEmbedding []float32, queryText string, limit int) ([]memory.MemoryItem, error) {
	vec := pgvector.NewVector(queryEmbedding)
	query := `
		SELECT id, content, memory_type, active, source_type, created_at
		FROM memory_items
		WHERE active = true
		ORDER BY embedding <=> $1
		LIMIT $2
	`
	
	rows, err := r.db.QueryContext(ctx, query, vec, limit)
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
