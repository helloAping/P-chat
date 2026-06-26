// Package knowledge handles external knowledge bases and embeddings.
//
// The current implementation supports:
//
//   - Local hash-based embeddings (no API key required; useful as a
//     zero-config fallback). Quality is mediocre but works for short
//     keyword-style queries.
//   - OpenAI text-embedding-3-small / -large embeddings, used when
//     `openai.api_key` is configured.
//
// Knowledge sources are directories of .md / .txt files (recursively
// scanned). The Indexer splits files into chunks, embeds them, and
// stores everything in the SQLite memory store.
package knowledge

import (
	"context"
)

// Embedder turns text into a fixed-dimension vector. The Dim() of the
// returned vector must be stable across calls (and across processes for
// the same model).
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
	Dim() int
	Name() string // "local-hash" or "openai:text-embedding-3-small" etc.
}

// CosineSimilarity computes the cosine similarity between two vectors.
// Returns 0 if either vector is zero-length or the dimensions don't
// match. Vectors need not be normalized.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (sqrt(na) * sqrt(nb)))
}

func sqrt(x float64) float64 {
	// Newton-Raphson, 10 iterations is plenty for our needs.
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 10; i++ {
		z = (z + x/z) / 2
	}
	return z
}
