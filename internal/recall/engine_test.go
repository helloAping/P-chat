package recall

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// makeTempEngine builds an Engine whose underlying wiki store
// lives in a t.TempDir(). The store is empty (no nodes indexed),
// so Search/PrintSearch exercise the "no results" path.
//
// Registers a t.Cleanup that closes the wiki store so SQLite's
// WAL files can be released before t.TempDir() tries to remove
// the directory.
func makeTempEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	e := NewEngine("test", dir, &bytes.Buffer{})
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestNewEngineDefaultWriter(t *testing.T) {
	e := NewEngine("test", "/tmp", nil)
	if e.out == nil {
		t.Fatal("NewEngine with nil writer returned nil out")
	}
	if e.wikiName != "test" || e.wikiDir != "/tmp" {
		t.Errorf("name/dir not stored: %q / %q", e.wikiName, e.wikiDir)
	}
}

func TestPrintSearchNoResults(t *testing.T) {
	// Empty wiki store: PrintSearch should report "no results"
	// and return nil (not an error).
	var buf bytes.Buffer
	e := NewEngine("empty-recall-test", t.TempDir(), &buf)
	t.Cleanup(func() { _ = e.Close() })
	err := e.PrintSearch(context.Background(), "anything", 5)
	if err != nil {
		t.Fatalf("PrintSearch on empty wiki: %v", err)
	}
	if !strings.Contains(buf.String(), "no results") {
		t.Errorf("expected 'no results' message, got: %q", buf.String())
	}
}

func TestPrintSearchNilContext(t *testing.T) {
	// ctx is `any` so passing nil must not panic; the engine
	// substitutes context.Background() and still produces output.
	var buf bytes.Buffer
	e := NewEngine("nil-ctx-test", t.TempDir(), &buf)
	t.Cleanup(func() { _ = e.Close() })
	if err := e.PrintSearch(nil, "q", 3); err != nil {
		t.Fatalf("PrintSearch with nil ctx: %v", err)
	}
	if !strings.Contains(buf.String(), "no results") {
		t.Errorf("expected 'no results' for nil ctx path, got: %q", buf.String())
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	// Empty query in the underlying LookupSearch maps to a
	// "browse" path (L2 listing) that returns an empty
	// IndexSearchResult for an empty wiki. Engine.Search
	// must return without erroring.
	e := makeTempEngine(t)
	items, err := e.Search(context.Background(), "", 5)
	if err != nil {
		t.Fatalf("Search with empty query: %v", err)
	}
	// We don't assert on items vs nil — both are fine.
	_ = items
}

func TestSearchTopKClamps(t *testing.T) {
	// Search must accept any topK without panicking. The
	// clamp inside (topK <= 0 → 5, topK > 50 → 50) is
	// exercised at every call site, so we just need a
	// representative sweep.
	e := makeTempEngine(t)
	for _, in := range []int{-1, 0, 1, 5, 50, 100, 1000} {
		if _, err := e.Search(context.Background(), "x", in); err != nil {
			t.Errorf("Search(topK=%d) error: %v", in, err)
		}
	}
}
