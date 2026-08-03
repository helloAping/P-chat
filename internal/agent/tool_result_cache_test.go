package agent

import (
	"strings"
	"testing"
	"time"
)

func TestTruncatedResultCache_RoundTrip(t *testing.T) {
	// Isolate the package-level cache so the test can't see
	// entries parked by other tests / runs.
	old := truncatedCache
	truncatedCache = &toolResultCache{
		entries:    make(map[string]truncatedResult),
		maxEntries: maxCachedToolResults,
		maxBytes:   maxCachedToolResultBytes,
	}
	t.Cleanup(func() { truncatedCache = old })

	storeTruncatedResult("sess-1", "call_abc", strings.Repeat("x", 100_000))
	got, ok := LookupTruncatedResult("sess-1", "call_abc")
	if !ok {
		t.Fatal("expected cached result to be found")
	}
	if len(got) != 100_000 {
		t.Fatalf("content length = %d, want 100000", len(got))
	}
}

func TestTruncatedResultCache_SessionIsolation(t *testing.T) {
	old := truncatedCache
	truncatedCache = &toolResultCache{
		entries:    make(map[string]truncatedResult),
		maxEntries: maxCachedToolResults,
		maxBytes:   maxCachedToolResultBytes,
	}
	t.Cleanup(func() { truncatedCache = old })

	storeTruncatedResult("sess-1", "call_x", "hello")
	if _, ok := LookupTruncatedResult("sess-2", "call_x"); ok {
		t.Fatal("result leaked across sessions")
	}
	if _, ok := LookupTruncatedResult("sess-1", "call_missing"); ok {
		t.Fatal("missing tool id returned a hit")
	}
}

func TestTruncatedResultCache_TTL(t *testing.T) {
	old := truncatedCache
	truncatedCache = &toolResultCache{
		entries:    make(map[string]truncatedResult),
		maxEntries: maxCachedToolResults,
		maxBytes:   maxCachedToolResultBytes,
	}
	t.Cleanup(func() { truncatedCache = old })

	storeTruncatedResult("sess-1", "call_ttl", "stale")
	// Backdate the entry past the TTL.
	truncatedCache.mu.Lock()
	e := truncatedCache.entries["call_ttl"]
	e.storedAt = time.Now().Add(-toolResultCacheTTL - time.Minute)
	truncatedCache.entries["call_ttl"] = e
	truncatedCache.mu.Unlock()

	if _, ok := LookupTruncatedResult("sess-1", "call_ttl"); ok {
		t.Fatal("expired result was returned")
	}
}

func TestTruncatedResultCache_EvictsOldestOnByteCap(t *testing.T) {
	old := truncatedCache
	truncatedCache = &toolResultCache{
		entries:    make(map[string]truncatedResult),
		maxEntries: 10,
		maxBytes:   100,
	}
	t.Cleanup(func() { truncatedCache = old })

	storeTruncatedResult("sess-1", "call_big1", strings.Repeat("a", 80))
	storeTruncatedResult("sess-1", "call_big2", strings.Repeat("b", 80))
	// 160 bytes > 100 cap → at least one entry evicted.
	if _, ok := LookupTruncatedResult("sess-1", "call_big1"); ok {
		t.Fatal("oldest entry was not evicted on byte cap")
	}
	if _, ok := LookupTruncatedResult("sess-1", "call_big2"); !ok {
		t.Fatal("newest entry should survive")
	}
}
