package knowledge

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitText_Empty(t *testing.T) {
	if got := SplitText("", 100, 10); got != nil {
		t.Errorf("empty text should give nil chunks, got %v", got)
	}
}

func TestSplitText_WhitespaceOnly(t *testing.T) {
	if got := SplitText("   \n\t  ", 100, 10); got != nil {
		t.Errorf("whitespace-only should give nil chunks, got %v", got)
	}
}

func TestSplitText_Short(t *testing.T) {
	got := SplitText("hello world", 100, 10)
	if len(got) != 1 || got[0] != "hello world" {
		t.Errorf("short text should return one chunk, got %v", got)
	}
}

func TestSplitText_SplitsOnParagraph(t *testing.T) {
	text := strings.Repeat("paragraph.\n\n", 50) // ~700 chars
	got := SplitText(text, 200, 20)
	if len(got) < 2 {
		t.Errorf("expected multiple chunks, got %d", len(got))
	}
	// All chunks should be non-empty.
	for i, c := range got {
		if strings.TrimSpace(c) == "" {
			t.Errorf("chunk %d is empty: %q", i, c)
		}
	}
}

func TestSplitText_Overlap(t *testing.T) {
	text := strings.Repeat("sentence. ", 100) // ~1000 chars
	got := SplitText(text, 200, 50)
	if len(got) < 3 {
		t.Errorf("expected overlap to produce >= 3 chunks, got %d", len(got))
	}
}

func TestSplitText_DefaultChunkSize(t *testing.T) {
	// chunkSize <= 0 should fall back to a sane default.
	got := SplitText(strings.Repeat("x", 5000), 0, 0)
	if len(got) < 1 {
		t.Errorf("expected at least one chunk, got %d", len(got))
	}
}

func TestCosineSimilarity_Empty(t *testing.T) {
	if got := CosineSimilarity(nil, nil); got != 0 {
		t.Errorf("empty vectors should give 0 similarity, got %v", got)
	}
	if got := CosineSimilarity([]float32{1, 2}, nil); got != 0 {
		t.Errorf("mismatched dims should give 0, got %v", got)
	}
}

func TestCosineSimilarity_Identical(t *testing.T) {
	v := []float32{0.6, 0.8}
	if got := CosineSimilarity(v, v); math.Abs(float64(got-1.0)) > 0.001 {
		t.Errorf("identical vectors should have sim=1, got %v", got)
	}
}

func TestCosineSimilarity_Orthogonal(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0, 1}
	if got := CosineSimilarity(a, b); math.Abs(float64(got)) > 0.001 {
		t.Errorf("orthogonal vectors should have sim=0, got %v", got)
	}
}

func TestCosineSimilarity_Opposite(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{-1, 0}
	if got := CosineSimilarity(a, b); math.Abs(float64(got+1.0)) > 0.001 {
		t.Errorf("opposite vectors should have sim=-1, got %v", got)
	}
}

func TestCosineSimilarity_ZeroVector(t *testing.T) {
	a := []float32{0, 0}
	b := []float32{1, 2}
	if got := CosineSimilarity(a, b); got != 0 {
		t.Errorf("zero vector should give 0, got %v", got)
	}
}

func TestLocalHashEmbedder_Deterministic(t *testing.T) {
	e := NewLocalHashEmbedder()
	v1, _ := e.Embed(context.Background(), "hello world")
	v2, _ := e.Embed(context.Background(), "hello world")
	if len(v1) != e.Dim() {
		t.Errorf("vector dim = %d, want %d", len(v1), e.Dim())
	}
	if float32sEqual(v1, v2, 0.0001) == false {
		t.Error("same text should produce identical vectors")
	}
}

func TestLocalHashEmbedder_DifferentTexts(t *testing.T) {
	e := NewLocalHashEmbedder()
	v1, _ := e.Embed(context.Background(), "Go is a programming language")
	v2, _ := e.Embed(context.Background(), "Python is a snake")
	sim := CosineSimilarity(v1, v2)
	// Some keyword overlap is expected; not strict but should be < 1
	if sim > 0.99 {
		t.Errorf("unrelated texts should not have sim ≈ 1, got %v", sim)
	}
}

func TestLocalHashEmbedder_Batch(t *testing.T) {
	e := NewLocalHashEmbedder()
	texts := []string{"alpha", "beta", "gamma"}
	embs, err := e.EmbedBatch(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(embs) != 3 {
		t.Errorf("expected 3 embeddings, got %d", len(embs))
	}
	for i, v := range embs {
		if len(v) != e.Dim() {
			t.Errorf("embedding %d has wrong dim %d", i, len(v))
		}
	}
}

func TestLocalHashEmbedder_NameAndDim(t *testing.T) {
	e := NewLocalHashEmbedder()
	if e.Name() != "local-hash" {
		t.Errorf("Name = %q, want local-hash", e.Name())
	}
	if e.Dim() != 256 {
		t.Errorf("Dim = %d, want 256", e.Dim())
	}
}

func TestFloat32sRoundTrip(t *testing.T) {
	src := []float32{1.5, -3.25, 0.0, 1e-5, 1e5}
	b := float32sToBytes(src)
	if len(b) != len(src)*4 {
		t.Errorf("bytes len = %d, want %d", len(b), len(src)*4)
	}
	dst := bytesToFloat32s(b, len(src))
	for i := range src {
		if math.Abs(float64(src[i]-dst[i])) > 0.001 {
			t.Errorf("roundtrip[%d]: %v != %v", i, src[i], dst[i])
		}
	}
}

func TestIndexFile_DedupByHash(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db := openTestDB(t, dbPath)
	defer db.Close()

	e := NewLocalHashEmbedder()
	ix := NewIndexer(db, e)

	file := filepath.Join(dir, "doc.md")
	content := "Hello, world! This is a test document about Go programming."
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	n, err := ix.IndexFile(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("expected at least one chunk")
	}

	// Re-indexing the same file should be a no-op (idempotent).
	n2, err := ix.IndexFile(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Errorf("expected 0 chunks on re-index (unchanged file), got %d", n2)
	}
}

func TestIndexFile_PicksUpChanges(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db := openTestDB(t, dbPath)
	defer db.Close()

	e := NewLocalHashEmbedder()
	ix := NewIndexer(db, e)

	file := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(file, []byte("original content about apples"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := ix.IndexFile(ctx, file); err != nil {
		t.Fatal(err)
	}

	// Modify the file.
	if err := os.WriteFile(file, []byte("modified content about bananas"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := ix.IndexFile(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("expected re-index of changed file to add chunks")
	}
}

func TestIndexDir_SkipsHiddenAndUnsupported(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db := openTestDB(t, dbPath)
	defer db.Close()

	// Mix valid and invalid files.
	if err := os.WriteFile(filepath.Join(dir, "real.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "image.png"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hidden", "x.md"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "x.md"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	e := NewLocalHashEmbedder()
	ix := NewIndexer(db, e)
	n, err := ix.IndexDir(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Errorf("expected >= 2 chunks (real.md + real.txt), got %d", n)
	}
}

func TestRetriever_TopKAndSourcePrefix(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db := openTestDB(t, dbPath)
	defer db.Close()

	e := NewLocalHashEmbedder()
	ix := NewIndexer(db, e)
	r := NewRetriever(db, e)

	kbDir := t.TempDir()
	for i, content := range []string{
		"Go is a statically typed programming language.",
		"Python is dynamically typed and often used for scripting.",
		"Rust is a systems language focused on safety and performance.",
	} {
		path := filepath.Join(kbDir, "doc_"+string(rune('a'+i))+".md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ix.IndexDir(context.Background(), kbDir); err != nil {
		t.Fatal(err)
	}

	hits, err := r.Search(context.Background(), "Go programming", 2, "kb:")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit")
	}
	if len(hits) > 2 {
		t.Errorf("expected <= 2 hits, got %d", len(hits))
	}
	for _, h := range hits {
		if !strings.HasPrefix(h.Source, "kb:") {
			t.Errorf("hit source should start with kb:, got %q", h.Source)
		}
	}
}

func TestRetriever_NoSourcePrefix(t *testing.T) {
	// When sourcePrefix is empty, the retriever returns chunks from
	// all sources (or none if the table is empty).
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db := openTestDB(t, dbPath)
	defer db.Close()

	e := NewLocalHashEmbedder()
	r := NewRetriever(db, e)

	hits, err := r.Search(context.Background(), "anything", 5, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("empty KB should give 0 hits, got %d", len(hits))
	}
}

// float32sEquals returns true if the two slices are within eps of each other.
func float32sEqual(a, b []float32, eps float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		diff := a[i] - b[i]
		if diff < 0 {
			diff = -diff
		}
		if diff > eps {
			return false
		}
	}
	return true
}
