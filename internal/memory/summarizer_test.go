package memory

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
)

// TestCompress_BoundedBatchesPerCall is the regression test for the
// 2026-08 resume memory spike. Sending "继续" on a pathological conversation
// (e.g. the runaway-loop session that left 2000+ rows) used to make Compress
// summarize ALL unsummarized messages in one synchronous call — 20+ sequential
// LLM requests blocking the SendMessage handler before the turn started.
// Compress must cap its work per invocation (maxCompressBatches batches) and
// advance the compression point incrementally.
func TestCompress_BoundedBatchesPerCall(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"summarized"}}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{LLM: config.LLMConfig{
		Default: "test",
		Providers: []config.ProviderConfig{{
			Name: "test", Protocol: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m",
		}},
	}}
	lc, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Enough messages to need more than maxCompressBatches batches of 100.
	convID := "conv_compress_bounded"
	if err := store.EnsureConversation(convID, "t"); err != nil {
		t.Fatal(err)
	}
	total := (maxCompressBatches + 3) * 100
	for i := 0; i < total; i++ {
		store.AddChatMessageTo(convID, llm.ChatMessage{
			Role:        llm.RoleUser,
			Type:        llm.TypeText,
			Content:     fmt.Sprintf("msg-%d", i),
			MsgType:     llm.MsgTypeText,
			SubmitToLLM: 1,
		})
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if n := store.CountChatMessages(convID); n != total {
		t.Fatalf("CountChatMessages = %d, want %d", n, total)
	}

	sm := NewSummarizer(store, lc, "test", 50)
	ok, summary, err := sm.Compress(context.Background(), convID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected compression to run")
	}
	if summary == "" {
		t.Fatal("expected a summary")
	}
	if got := calls.Load(); got > int64(maxCompressBatches) {
		t.Errorf("Compress made %d LLM calls in one invocation, want at most %d", got, maxCompressBatches)
	}

	// The compression point advanced by exactly maxCompressBatches batches.
	last := store.LastCompressedIDFor(convID)
	wantMin := int64(maxCompressBatches*100 - 50)
	wantMax := int64(maxCompressBatches*100 + 50)
	if last < wantMin || last > wantMax {
		t.Errorf("LastCompressedIDFor = %d, want near %d (first %d batches)", last, maxCompressBatches*100, maxCompressBatches)
	}

	// A second Compress advances further — the cap is per call, not total.
	before := last
	calls.Store(0)
	ok2, _, err2 := sm.Compress(context.Background(), convID)
	if err2 != nil {
		t.Fatal(err2)
	}
	if !ok2 {
		t.Fatal("expected second Compress to advance")
	}
	if store.LastCompressedIDFor(convID) <= before {
		t.Error("second Compress did not advance the compression point")
	}
	if got := calls.Load(); got > int64(maxCompressBatches) {
		t.Errorf("second Compress made %d LLM calls, want at most %d", got, maxCompressBatches)
	}
}

// TestMaxCompressBatches pins the cap so a change updates the plan docs.
func TestMaxCompressBatches(t *testing.T) {
	if maxCompressBatches < 1 {
		t.Errorf("maxCompressBatches = %d, want >= 1", maxCompressBatches)
	}
	if maxCompressBatches > 10 {
		t.Errorf("maxCompressBatches = %d, want <= 10 (must bound the synchronous LLM storm)", maxCompressBatches)
	}
}

// TestCompress_NoSummaryRangeExpansion is the regression test for the
// 2026-08 memory-spike incident. A conversation whose messages span two
// id schemes (old AUTOINCREMENT small ids + frontend-minted millisecond
// timestamps) can end up with a summaries row whose range covers ~10^15 ids —
// e.g. range_start=1, range_end=1785479327781664. The old Compress code
// expanded every summary range into an id→bool map (for i := s; i <= e; i++),
// which is O(span) — a quadrillion-entry map blew the process heap past 1GB
// inside the SendMessage handler and wedged the turn.
//
// Compress must now treat summary coverage as (start,end) pairs matched by
// binary search, so the same conversation is handled in O(messages ×
// log ranges). This test replays the exact corrupt shape and asserts the
// call completes fast (it would hang/OOM on the old code) while still
// advancing the compression point for the unsummarized tail.
func TestCompress_NoSummaryRangeExpansion(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"summarized"}}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{LLM: config.LLMConfig{
		Default: "test",
		Providers: []config.ProviderConfig{{
			Name: "test", Protocol: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m",
		}},
	}}
	lc, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	convID := "conv_mixed_ids"
	if err := store.EnsureConversation(convID, "t"); err != nil {
		t.Fatal(err)
	}

	// Old AUTOINCREMENT scheme: a handful of small ids.
	for i := 1; i <= 50; i++ {
		store.AddChatMessageToWithID(convID, llm.ChatMessage{
			Role: llm.RoleUser, Type: llm.TypeText, Content: fmt.Sprintf("old-%d", i),
			MsgType: llm.MsgTypeText, SubmitToLLM: 1,
		}, int64(i))
	}

	// Frontend-minted microsecond-timestamp scheme: ids ABOVE the corrupt
	// range end, so they are genuinely unsummarized and must be compressed.
	const tsSchemeBase = int64(1785479327781665) // just past the corrupt end
	for i := 0; i < 100; i++ {
		store.AddChatMessageToWithID(convID, llm.ChatMessage{
			Role: llm.RoleUser, Type: llm.TypeText, Content: fmt.Sprintf("new-%d", i),
			MsgType: llm.MsgTypeText, SubmitToLLM: 1,
		}, tsSchemeBase+int64(i))
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// The corrupt cross-scheme summary range: start in the small-id world,
	// end in the timestamp world — span ~1.78e15. This is what the affected
	// device's DB contained.
	if err := store.SaveSummary(convID, 1, int64(1785479327781664), ""); err != nil {
		t.Fatal(err)
	}

	sm := NewSummarizer(store, lc, "test", 50)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	ok, summary, err := sm.Compress(ctx, convID)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Compress took %v — likely re-expanded the huge range", elapsed)
	}
	if !ok {
		t.Fatal("expected the unsummarized tail to be compressed")
	}
	if summary == "" {
		t.Fatal("expected a summary")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("Compress made %d LLM calls, want 1 (single batch of the 100 unsummarized)", got)
	}
	if last := store.LastCompressedIDFor(convID); last != tsSchemeBase+99 {
		t.Errorf("LastCompressedIDFor = %d, want %d (compression point advanced past the corrupt range)", last, tsSchemeBase+99)
	}
}

// TestRangeContainsID exercises the binary-search coverage helper directly,
// including the cross-scheme huge-range case.
func TestRangeContainsID(t *testing.T) {
	ranges := []summaryRange{
		{start: 1, end: 100},
		{start: 100000, end: 1785479327781664}, // corrupt cross-scheme span
		{start: 1785479327782000, end: 1785479327782099},
	}
	cases := []struct {
		id   int64
		want bool
	}{
		{1, true}, {50, true}, {100, true}, {101, false},
		{99999, false}, {100000, true}, {1785479327781000, true},
		{1785479327781664, true}, {1785479327781665, false},
		{1785479327781999, false}, {1785479327782000, true}, {1785479327782099, true}, {1785479327782100, false},
	}
	for _, c := range cases {
		if got := rangeContainsID(ranges, c.id); got != c.want {
			t.Errorf("rangeContainsID(%d) = %v, want %v", c.id, got, c.want)
		}
	}
}
