package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type OllamaClient interface {
	GenerateEmbedding(ctx context.Context, text string) ([]float32, error)
}

type ollamaClient struct {
	uri   string
	model string
}

func NewOllamaClient(uri, model string) OllamaClient {
	return &ollamaClient{
		uri:   uri,
		model: model,
	}
}

type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

func (c *ollamaClient) GenerateEmbedding(ctx context.Context, text string) ([]float32, error) {
	reqBody := embedRequest{
		Model: c.model,
		Input: text,
	}
	
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.uri+"/api/embed", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	
	var resBody embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&resBody); err != nil {
		return nil, err
	}
	
	if len(resBody.Embeddings) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}
	
	return resBody.Embeddings[0], nil
}
