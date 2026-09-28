package storage

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/google/uuid"
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
	if item.ID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		item.ID = id.String()
	}

	metadataJSON, err := json.Marshal(item.Metadata)
	if err != nil {
		return err
	}

	vec := pgvector.NewVector(item.Embedding)

	query := `
		INSERT INTO memory_items (
			id, content, memory_type, active, source_type, source_ref, 
			conversation_id, turn_index, importance, embedding, embedding_model, metadata
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
		) RETURNING created_at
	`
	
	err = r.db.QueryRowContext(ctx, query,
		item.ID, item.Content, item.MemoryType, item.Active, item.SourceType, item.SourceRef,
		item.ConversationID, item.TurnIndex, item.Importance, vec, item.EmbeddingModel, metadataJSON,
	).Scan(&item.CreatedAt)
	
	return err
}

func (r *Repository) UpdateItemState(ctx context.Context, id string, active bool) error {
	_, err := r.db.ExecContext(ctx, "UPDATE memory_items SET active = $1 WHERE id = $2", active, id)
	return err
}

func (r *Repository) SupersedeItem(ctx context.Context, oldID string, newItem *memory.MemoryItem) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. Deactivate old item
	_, err = tx.ExecContext(ctx, "UPDATE memory_items SET active = false WHERE id = $1", oldID)
	if err != nil {
		return err
	}

	// 2. Insert new item
	if newItem.ID == "" {
		id, _ := uuid.NewV7()
		newItem.ID = id.String()
	}
	metaJSON, _ := json.Marshal(newItem.Metadata)
	vec := pgvector.NewVector(newItem.Embedding)

	query := `
		INSERT INTO memory_items (
			id, content, memory_type, active, source_type, source_ref, 
			conversation_id, turn_index, importance, embedding, embedding_model, metadata
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
		) RETURNING created_at
	`
	err = tx.QueryRowContext(ctx, query,
		newItem.ID, newItem.Content, newItem.MemoryType, newItem.Active, newItem.SourceType, newItem.SourceRef,
		newItem.ConversationID, newItem.TurnIndex, newItem.Importance, vec, newItem.EmbeddingModel, metaJSON,
	).Scan(&newItem.CreatedAt)
	if err != nil {
		return err
	}

	// 3. Link them
	linkID, _ := uuid.NewV7()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO memory_links (id, source_id, target_id, relation_type, active)
		VALUES ($1, $2, $3, 'supersedes', true)
	`, linkID.String(), newItem.ID, oldID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (r *Repository) HybridSearch(ctx context.Context, queryEmbedding []float32, queryText string, limit int) ([]memory.MemoryItem, error) {
	vec := pgvector.NewVector(queryEmbedding)
	
	query := `
		WITH vector_search AS (
			SELECT id, content, memory_type, active, source_type, created_at,
			       embedding <=> $1 AS vector_score
			FROM memory_items
			WHERE active = true
			ORDER BY vector_score
			LIMIT 100
		),
		fts_search AS (
			SELECT id, ts_rank(search_vector, websearch_to_tsquery('english', $2)) AS fts_score
			FROM memory_items
			WHERE active = true AND search_vector @@ websearch_to_tsquery('english', $2)
		)
		SELECT v.id, v.content, v.memory_type, v.active, v.source_type, v.created_at
		FROM vector_search v
		LEFT JOIN fts_search f ON v.id = f.id
		ORDER BY (COALESCE(f.fts_score, 0.0) + (1.0 / (1.0 + v.vector_score))) DESC
		LIMIT $3
	`
	
	rows, err := r.db.QueryContext(ctx, query, vec, queryText, limit)
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

func (r *Repository) SaveLink(ctx context.Context, link *memory.MemoryLink) error {
	if link.ID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		link.ID = id.String()
	}

	metadataJSON, err := json.Marshal(link.Metadata)
	if err != nil {
		return err
	}

	query := `
		INSERT INTO memory_links (id, source_id, target_id, relation_type, active, metadata)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING created_at
	`
	return r.db.QueryRowContext(ctx, query,
		link.ID, link.SourceID, link.TargetID, link.RelationType, link.Active, metadataJSON,
	).Scan(&link.CreatedAt)
}
