package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaClient_GenerateEmbedding(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("Expected path /api/embed, got %s", r.URL.Path)
		}
		
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		
		if req.Model != "test-model" {
			t.Errorf("Expected model 'test-model', got %s", req.Model)
		}
		
		resp := embedResponse{
			Embeddings: [][]float32{{0.1, 0.2, 0.3}},
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	client := NewOllamaClient(mockServer.URL, "test-model")
	emb, err := client.GenerateEmbedding(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	
	if len(emb) != 3 {
		t.Errorf("Expected 3 dimensions, got %d", len(emb))
	}
}
