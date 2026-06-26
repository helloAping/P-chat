package subagent

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/style"
)

func TestCache_StoresAndRetrieves(t *testing.T) {
	c := NewCache(2 * time.Second)
	if c == nil {
		t.Fatal("NewCache with positive TTL should not return nil")
	}

	req := Request{Description: "1+1", Style: style.Tech, Provider: "p"}
	if _, ok := c.Get(req.Description, req.Style, req.Provider); ok {
		t.Error("empty cache should miss")
	}

	r := Result{Content: "2", TokensIn: 5, TokensOut: 1, Elapsed: 100 * time.Millisecond, Rounds: 1}
	c.Put(req.Description, req.Style, req.Provider, r)

	got, ok := c.Get(req.Description, req.Style, req.Provider)
	if !ok {
		t.Fatal("expected hit after Put")
	}
	if got.Content != "2" {
		t.Errorf("content = %q, want 2", got.Content)
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	c := NewCache(50 * time.Millisecond)
	req := Request{Description: "x", Style: style.Tech, Provider: "p"}
	c.Put(req.Description, req.Style, req.Provider, Result{Content: "y"})

	if _, ok := c.Get(req.Description, req.Style, req.Provider); !ok {
		t.Fatal("expected hit right after Put")
	}
	time.Sleep(100 * time.Millisecond)
	if _, ok := c.Get(req.Description, req.Style, req.Provider); ok {
		t.Error("expected miss after TTL")
	}
}

func TestCache_DistinguishesInputs(t *testing.T) {
	c := NewCache(time.Minute)
	c.Put("a", style.Tech, "p", Result{Content: "A"})
	c.Put("b", style.Tech, "p", Result{Content: "B"})
	c.Put("a", style.Cute, "p", Result{Content: "A2"})
	c.Put("a", style.Tech, "q", Result{Content: "A3"})

	if got, _ := c.Get("a", style.Tech, "p"); got.Content != "A" {
		t.Errorf("a/tech/p = %q, want A", got.Content)
	}
	if got, _ := c.Get("b", style.Tech, "p"); got.Content != "B" {
		t.Errorf("b/tech/p = %q, want B", got.Content)
	}
	if got, _ := c.Get("a", style.Cute, "p"); got.Content != "A2" {
		t.Errorf("a/cute/p = %q, want A2", got.Content)
	}
	if got, _ := c.Get("a", style.Tech, "q"); got.Content != "A3" {
		t.Errorf("a/tech/q = %q, want A3", got.Content)
	}
}

func TestCache_StatsCounters(t *testing.T) {
	c := NewCache(time.Minute)
	req := Request{Description: "z", Style: style.Tech, Provider: "p"}
	// 1 miss
	c.Get(req.Description, req.Style, req.Provider)
	// 1 store
	c.Put(req.Description, req.Style, req.Provider, Result{Content: "x"})
	// 2 hits
	c.Get(req.Description, req.Style, req.Provider)
	c.Get(req.Description, req.Style, req.Provider)

	st := c.Stats()
	if st.Misses != 1 {
		t.Errorf("misses = %d, want 1", st.Misses)
	}
	if st.Stores != 1 {
		t.Errorf("stores = %d, want 1", st.Stores)
	}
	if st.Hits != 2 {
		t.Errorf("hits = %d, want 2", st.Hits)
	}
	want := 2.0 / 3.0
	if diff := st.HitRatio - want; diff > 0.01 || diff < -0.01 {
		t.Errorf("HitRatio = %.3f, want %.3f", st.HitRatio, want)
	}
}

func TestCache_NilSafe(t *testing.T) {
	var c *Cache
	if _, ok := c.Get("x", style.Tech, "p"); ok {
		t.Error("nil cache should always miss")
	}
	// Put and Len should not panic.
	c.Put("x", style.Tech, "p", Result{Content: "y"})
	if c.Len() != 0 {
		t.Error("nil cache should report 0 entries")
	}
}

func TestCache_ConcurrentAccess(t *testing.T) {
	c := NewCache(time.Minute)
	const N = 100
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Put("k", style.Tech, "p", Result{Content: "v"})
			c.Get("k", style.Tech, "p")
		}()
	}
	wg.Wait()
	st := c.Stats()
	if st.Stores < N {
		t.Errorf("expected >= %d stores, got %d", N, st.Stores)
	}
}

func TestCache_LenAndConcurrentMapCleanup(t *testing.T) {
	c := NewCache(10 * time.Millisecond)
	styles := []style.Style{style.Tech, style.Cute, style.Guofeng}
	for i := 0; i < 50; i++ {
		// Vary description to create 50 distinct cache keys.
		c.Put(fmt.Sprintf("k-%d", i), styles[i%len(styles)], "p", Result{Content: "v"})
	}
	if c.Len() < 50 {
		t.Errorf("expected >= 50 entries, got %d", c.Len())
	}
	// Wait for expiry; lazy eviction happens on Get.
	time.Sleep(50 * time.Millisecond)
	c.Get("k-0", style.Tech, "p")
	if c.Len() >= 50 {
		t.Errorf("lazy eviction should have removed expired entries; got %d", c.Len())
	}
}
