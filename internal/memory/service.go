package memory

import (
	"context"
	"fmt"
	"time"
)

type Storage interface {
	SaveItem(ctx context.Context, item *MemoryItem) error
	HybridSearch(ctx context.Context, queryEmbedding []float32, queryText string, limit int) ([]MemoryItem, error)
	// Additional methods for full implementation
}

type EmbeddingClient interface {
	GenerateEmbedding(ctx context.Context, text string) ([]float32, error)
}

type Service struct {
	storage Storage
	embed   EmbeddingClient
}

func NewService(store Storage, embed EmbeddingClient) *Service {
	return &Service{
		storage: store,
		embed:   embed,
	}
}

// StoreMemory implements non-destructive versioning and stores new items
func (s *Service) StoreMemory(ctx context.Context, content, memType, sourceType string) (*MemoryItem, error) {
	emb, err := s.embed.GenerateEmbedding(ctx, content)
	if err != nil {
		return nil, fmt.Errorf("embedding failed: %w", err)
	}

	item := &MemoryItem{
		Content:        content,
		MemoryType:     memType,
		Active:         true,
		SourceType:     sourceType,
		Embedding:      emb,
		EmbeddingModel: "embeddinggemma:300m-qat-q4_0", // should be from config
	}

	if err := s.storage.SaveItem(ctx, item); err != nil {
		return nil, fmt.Errorf("failed to save memory: %w", err)
	}

	return item, nil
}

// Search progressive hybrid retrieval engine
func (s *Service) Search(ctx context.Context, query string, limit int) ([]MemoryItem, error) {
	emb, err := s.embed.GenerateEmbedding(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embedding failed: %w", err)
	}

	// In a real implementation we would pass the query string for text search alongside the embedding
	items, err := s.storage.HybridSearch(ctx, emb, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	return items, nil
}
