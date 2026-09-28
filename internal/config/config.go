package config

import (
	"os"
)

type Config struct {
	PostgresURI   string
	OllamaURI     string
	MCPPort       string
	EmbeddingModel string
}

func LoadConfig() Config {
	return Config{
		PostgresURI:   getEnv("POSTGRES_URI", "postgres://localhost:54320/lodan?sslmode=disable"),
		OllamaURI:     getEnv("OLLAMA_URI", "http://localhost:11434"),
		MCPPort:       getEnv("MCP_PORT", "8080"),
		EmbeddingModel: getEnv("EMBEDDING_MODEL", "embeddinggemma:300m-qat-q4_0"),
	}
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
