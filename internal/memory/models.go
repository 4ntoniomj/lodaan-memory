package memory

import (
	"time"
)

type MemoryItem struct {
	ID             string                 `json:"id"`
	Content        string                 `json:"content"`
	MemoryType     string                 `json:"memory_type"`
	Active         bool                   `json:"active"`
	CreatedAt      time.Time              `json:"created_at"`
	OccurredAt     *time.Time             `json:"occurred_at,omitempty"`
	ValidFrom      *time.Time             `json:"valid_from,omitempty"`
	ValidUntil     *time.Time             `json:"valid_until,omitempty"`
	SourceType     string                 `json:"source_type"`
	SourceRef      string                 `json:"source_ref,omitempty"`
	ConversationID string                 `json:"conversation_id,omitempty"`
	TurnIndex      *int64                 `json:"turn_index,omitempty"`
	Importance     *float32               `json:"importance,omitempty"`
	Embedding      []float32              `json:"embedding,omitempty"`
	EmbeddingModel string                 `json:"embedding_model,omitempty"`
	ContentHash    string                 `json:"content_hash,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

type MemoryLink struct {
	ID           string                 `json:"id"`
	SourceID     string                 `json:"source_id"`
	TargetID     string                 `json:"target_id"`
	RelationType string                 `json:"relation_type"`
	Active       bool                   `json:"active"`
	CreatedAt    time.Time              `json:"created_at"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}
