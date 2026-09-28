package memory

import (
	"context"
	"fmt"
)

type Storage interface {
	SaveItem(ctx context.Context, item *MemoryItem) error
	UpdateItemState(ctx context.Context, id string, active bool) error
	SupersedeItem(ctx context.Context, oldID string, newItem *MemoryItem) error
	HybridSearch(ctx context.Context, queryEmbedding []float32, queryText string, limit int) ([]MemoryItem, error)
	SaveLink(ctx context.Context, link *MemoryLink) error
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

func (s *Service) StoreMemory(ctx context.Context, content, memType, sourceType string, convID string, turn *int64) (*MemoryItem, error) {
	emb, err := s.embed.GenerateEmbedding(ctx, content)
	if err != nil {
		return nil, fmt.Errorf("embedding failed: %w", err)
	}

	item := &MemoryItem{
		Content:        content,
		MemoryType:     memType,
		Active:         true,
		SourceType:     sourceType,
		ConversationID: convID,
		TurnIndex:      turn,
		Embedding:      emb,
		EmbeddingModel: "embeddinggemma:300m-qat-q4_0", // or from config
	}

	if err := s.storage.SaveItem(ctx, item); err != nil {
		return nil, fmt.Errorf("failed to save memory: %w", err)
	}

	return item, nil
}

func (s *Service) SupersedeMemory(ctx context.Context, oldID string, content, memType, sourceType string, convID string, turn *int64) (*MemoryItem, error) {
	emb, err := s.embed.GenerateEmbedding(ctx, content)
	if err != nil {
		return nil, fmt.Errorf("embedding failed: %w", err)
	}

	item := &MemoryItem{
		Content:        content,
		MemoryType:     memType,
		Active:         true,
		SourceType:     sourceType,
		ConversationID: convID,
		TurnIndex:      turn,
		Embedding:      emb,
		EmbeddingModel: "embeddinggemma:300m-qat-q4_0",
	}

	if err := s.storage.SupersedeItem(ctx, oldID, item); err != nil {
		return nil, fmt.Errorf("failed to supersede memory: %w", err)
	}

	return item, nil
}

func (s *Service) Search(ctx context.Context, query string, limit int) ([]MemoryItem, error) {
	emb, err := s.embed.GenerateEmbedding(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embedding failed: %w", err)
	}

	items, err := s.storage.HybridSearch(ctx, emb, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	return items, nil
}

func (s *Service) LinkMemories(ctx context.Context, sourceID, targetID, relationType string) error {
	link := &MemoryLink{
		SourceID:     sourceID,
		TargetID:     targetID,
		RelationType: relationType,
		Active:       true,
	}
	return s.storage.SaveLink(ctx, link)
}
