package embedding

import (
	"context"
	"hash/fnv"
	"strings"
	"unicode"
)

const defaultFakeDims = 64

// FakeEmbedder is a deterministic Embedder for tests, with no network.
//
// Each lowercase word contributes +1 or -1 to one dimension chosen by an FNV
// hash, and the vector is L2-normalized, so texts sharing words have a high
// cosine similarity. Query and document prefixes are not applied; for
// documents the title and the text are embedded together.
//
// Configure aliases before use: WithAlias is not safe for concurrent use with
// the embedding methods. The zero value is usable (64 dimensions).
type FakeEmbedder struct {
	// Dimensions is the vector size. It is not called Dims because the
	// Embedder interface already has a Dims method. Zero means 64.
	Dimensions int

	aliases map[string]string
}

// WithAlias makes word a be treated as word b (case-insensitive), to simulate
// synonyms. It returns f to allow chaining.
func (f *FakeEmbedder) WithAlias(a, b string) *FakeEmbedder {
	if f.aliases == nil {
		f.aliases = make(map[string]string)
	}
	f.aliases[strings.ToLower(a)] = strings.ToLower(b)
	return f
}

// Model returns the fake model name.
func (f *FakeEmbedder) Model() string { return "fake" }

// Dims returns the vector dimension.
func (f *FakeEmbedder) Dims() int {
	if f.Dimensions <= 0 {
		return defaultFakeDims
	}
	return f.Dimensions
}

// EmbedDocuments embeds each document as title plus text.
func (f *FakeEmbedder) EmbedDocuments(ctx context.Context, docs []Document) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, nil
	}
	out := make([][]float32, len(docs))
	for i, d := range docs {
		out[i] = f.embedText(d.Title + " " + d.Text)
	}
	return out, nil
}

// EmbedQuery embeds a query.
func (f *FakeEmbedder) EmbedQuery(ctx context.Context, q string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.embedText(q), nil
}

func (f *FakeEmbedder) embedText(text string) []float32 {
	dims := f.Dims()
	v := make([]float32, dims)
	for _, w := range tokenize(text) {
		if b, ok := f.aliases[w]; ok {
			w = b
		}
		h := fnv.New64a()
		_, _ = h.Write([]byte(w))
		sum := h.Sum64()
		idx := int(sum % uint64(dims))
		if (sum>>63)&1 == 1 {
			v[idx]--
		} else {
			v[idx]++
		}
	}
	if !normalizeL2(v) {
		// Empty text (or contributions that cancel out): unit vector on dim 0.
		v = make([]float32, dims)
		v[0] = 1
	}
	return v
}

// tokenize splits text into lowercase words made of letters and digits.
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
