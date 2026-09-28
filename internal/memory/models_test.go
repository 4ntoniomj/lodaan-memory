package memory

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMemoryItem_JSON(t *testing.T) {
	now := time.Now()
	item := MemoryItem{
		ID:         "test-id",
		Content:    "Hello",
		MemoryType: "fact",
		Active:     true,
		CreatedAt:  now,
		SourceType: "user",
	}

	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var parsed MemoryItem
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if parsed.ID != item.ID || parsed.Content != item.Content {
		t.Errorf("Mismatch in unmarshaled struct")
	}
}
