package knowledge

import (
	"context"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// LocalHashEmbedder is a zero-dependency embedding that hashes the
// normalized text into a fixed-dimension vector. Quality is poor but
// sufficient for keyword-style retrieval across the user's own
// knowledge base. The Dim is 256 by default.
type LocalHashEmbedder struct {
	dim int
}

func NewLocalHashEmbedder() *LocalHashEmbedder {
	return &LocalHashEmbedder{dim: 256}
}

func (e *LocalHashEmbedder) Name() string { return "local-hash" }
func (e *LocalHashEmbedder) Dim() int     { return e.dim }

func (e *LocalHashEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	return e.embedOne(text), nil
}

func (e *LocalHashEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = e.embedOne(t)
	}
	return out, nil
}

func (e *LocalHashEmbedder) embedOne(text string) []float32 {
	vec := make([]float32, e.dim)
	tokens := tokenize(text)
	if len(tokens) == 0 {
		return vec
	}

	// For each token, hash to an index in [0, dim) and add a
	// sub-linear weight (so common terms don't drown out rare ones).
	for _, tok := range tokens {
		h := fnv.New32a()
		_, _ = h.Write([]byte(tok))
		idx := h.Sum32() % uint32(e.dim)
		vec[idx] += 1.0
	}

	// Light stemming: hash bigrams too.
	for i := 0; i+1 < len(tokens); i++ {
		h := fnv.New32a()
		_, _ = h.Write([]byte(tokens[i]))
		_, _ = h.Write([]byte{' '})
		_, _ = h.Write([]byte(tokens[i+1]))
		idx := h.Sum32() % uint32(e.dim)
		vec[idx] += 0.5
	}

	// L2 normalize.
	normalize(vec)
	return vec
}

func tokenize(text string) []string {
	text = strings.ToLower(text)
	var out []string
	var cur strings.Builder
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
		} else {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return
	}
	norm := math.Sqrt(sum)
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
}
