// Package embedding computes text embeddings for lodan.
//
// It defines the Embedder interface, an Ollama client (POST /api/embed),
// the per-model prompt formats and a deterministic fake embedder for tests.
package embedding

import (
	"context"
	"errors"
	"math"
	"strings"
)

// ErrUnavailable is returned (wrapped) when the embedding backend cannot be
// reached. Callers should use errors.Is and store the record as pending.
var ErrUnavailable = errors.New("embedding: Ollama no disponible")

// Document is a piece of text to be indexed, with an optional title.
type Document struct {
	Title string
	Text  string
}

// Embedder turns text into L2-normalized vectors of a fixed dimension.
type Embedder interface {
	// EmbedDocuments embeds a batch of documents, in order. An empty batch
	// returns nil without any work.
	EmbedDocuments(ctx context.Context, docs []Document) ([][]float32, error)
	// EmbedQuery embeds a search query.
	EmbedQuery(ctx context.Context, q string) ([]float32, error)
	// Model returns the model name used to produce the vectors.
	Model() string
	// Dims returns the dimension of the vectors.
	Dims() int
}

// promptFormat describes how a model expects queries and documents to be
// prefixed before embedding.
type promptFormat struct {
	query func(q string) string
	doc   func(title, text string) string
}

func plainQuery(q string) string { return q }

// plainDoc is the document format of models without their own one: the text,
// preceded by the title and a blank line when there is a title.
func plainDoc(title, text string) string {
	if strings.TrimSpace(title) == "" {
		return text
	}
	return title + "\n\n" + text
}

// promptFormats is indexed by the base model name (the part before ':').
var promptFormats = map[string]promptFormat{
	// Prefixes from the official EmbeddingGemma model card.
	"embeddinggemma": {
		query: func(q string) string { return "task: search result | query: " + q },
		doc: func(title, text string) string {
			if strings.TrimSpace(title) == "" {
				title = "none"
			}
			return "title: " + title + " | text: " + text
		},
	},
	"qwen3-embedding": {
		query: func(q string) string {
			return "Instruct: Given a search query, retrieve relevant memories\nQuery:" + q
		},
		doc: plainDoc,
	},
	"nomic-embed-text-v2-moe": {
		query: func(q string) string { return "search_query: " + q },
		doc:   func(_, text string) string { return "search_document: " + text },
	},
	"bge-m3": {
		query: plainQuery,
		doc:   plainDoc,
	},
}

// baseModelName strips the tag of a model name: "embeddinggemma:300m" -> "embeddinggemma".
func baseModelName(model string) string {
	name, _, _ := strings.Cut(model, ":")
	return strings.ToLower(strings.TrimSpace(name))
}

// formatFor returns the prompt format of a model; unknown models get no prefixes.
func formatFor(model string) promptFormat {
	if f, ok := promptFormats[baseModelName(model)]; ok {
		return f
	}
	return promptFormat{query: plainQuery, doc: plainDoc}
}

// normalizeL2 scales v in place to unit L2 norm. It returns false, leaving v
// untouched, if the norm is zero or not finite.
func normalizeL2(v []float32) bool {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 || math.IsNaN(sum) || math.IsInf(sum, 0) {
		return false
	}
	inv := 1 / math.Sqrt(sum)
	for i := range v {
		v[i] = float32(float64(v[i]) * inv)
	}
	return true
}

// Cosine returns the cosine similarity of a and b. It returns 0 if the
// lengths differ, either vector is empty or either has zero norm.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
