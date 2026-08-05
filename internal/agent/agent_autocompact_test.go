package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
	"github.com/p-chat/pchat/internal/upgrade"
)

// TestTryAutoCompact_CompressFailureDoesNotLoop locks down the 2026-08-05
// CPU-spike regression: when the summarizer's Compress keeps failing (e.g.
// the summarizer LLM is unreachable), tryAutoCompact used to fall back to
// truncateToFit and `return true`, which made the caller `continue` into
// another round → tryAutoCompact → Compress (still failing) → truncate →
// ... an infinite loop. On a 3900-message session that dead-loop burned
// CPU on O(n²) truncateToFit and drove GC to ~50/s (num_gc 580 → 32000 in
// 13 min) while the turn never ended.
//
// Fix: on Compress failure the context is truncated once and the function
// returns false, so the caller falls through to the LLM call with the
// reduced context instead of looping. This test wires a Summarizer whose
// Compress always fails (llm == nil → returns false,"",nil) and asserts
// tryAutoCompact returns false AND that the message list was truncated.
func TestTryAutoCompact_CompressFailureDoesNotLoop(t *testing.T) {
	cfg := &config.Config{
		LLM: config.LLMConfig{
			Default: "test",
			Providers: []config.ProviderConfig{{
				Name:     "test",
				Protocol: "openai",
				BaseURL:  "http://127.0.0.1:9", // unreachable; not used here
				APIKey:   "test-key",
				Model:    "test-model",
			}},
		},
		Limits: config.LimitsConfig{MaxRounds: 5},
	}
	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.OpenAt(":memory:", 50)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := upgrade.SeedForTesting(store.DB()); err != nil {
		t.Fatal(err)
	}
	styleMgr, err := style.NewManager(store.DB())
	if err != nil {
		t.Fatal(err)
	}

	agt := New(cfg, llmClient, styleMgr, store, tool.NewRegistry())
	// A Summarizer with a nil LLM client makes Compress return
	// (false, "", nil) — the "summarizer can't work" failure mode.
	agt.SetSummarizer(memory.NewSummarizer(store, nil, "test", 50))

	// Build an over-budget message list: system + many large messages so
	// EstimatePromptTokens >> usable(35808 for 64k ctx / 20k buffer).
	msgs := make([]llm.ChatMessage, 0, 200)
	msgs = append(msgs, llm.ChatMessage{Role: llm.RoleSystem, Type: llm.TypeText, Content: "sys"})
	big := strings.Repeat("内容内容", 400) // ~400 CJK chars ≈ 600 tokens each
	for i := 0; i < 200; i++ {
		msgs = append(msgs, llm.ChatMessage{Role: llm.RoleUser, Type: llm.TypeText, Content: big})
	}
	if total := llm.EstimatePromptTokens(msgs, nil); total <= 35808 {
		t.Fatalf("test setup: messages not over budget (total=%d)", total)
	}
	origLen := len(msgs)

	ch := make(chan ChatStreamChunk, 64)
	seq := uint64(0)
	nextSeq := func() uint64 { seq++; return seq - 1 }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	compacted := agt.tryAutoCompact(ctx, &msgs, ChatRequest{
		SessionID: "loop-test",
		Provider:  "test",
		Model:     "test-model",
	}, nil, ch, nextSeq, 1, 5)

	// The whole point: it must NOT ask the caller to continue/loop.
	if compacted {
		t.Fatalf("tryAutoCompact returned true on a failing summarizer — caller would continue and dead-loop")
	}
	// The fallback should still have truncated the over-budget list once.
	if len(msgs) >= origLen {
		t.Fatalf("fallback truncation did not fire: len=%d (was %d)", len(msgs), origLen)
	}
	// And it must return promptly (a looping implementation would run the
	// full 10s context deadline).
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("tryAutoCompact took %v — likely looping", elapsed)
	}
	// Drain any chunks so the goroutine-free path is clean.
	for len(ch) > 0 {
		<-ch
	}
}

// TestTryAutoCompact_NoSummarizerShortCircuits ensures a nil summarizer
// (the default wiring) returns false immediately without touching the
// message list — auto-compaction is opt-in.
func TestTryAutoCompact_NoSummarizerShortCircuits(t *testing.T) {
	cfg := &config.Config{
		LLM: config.LLMConfig{
			Default: "test",
			Providers: []config.ProviderConfig{{Name: "test", Protocol: "openai", BaseURL: "http://127.0.0.1:9", APIKey: "k", Model: "m"}},
		},
		Limits: config.LimitsConfig{MaxRounds: 5},
	}
	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.OpenAt(":memory:", 50)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	styleMgr, err := style.NewManager(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	agt := New(cfg, llmClient, styleMgr, store, tool.NewRegistry()) // no SetSummarizer

	msgs := []llm.ChatMessage{{Role: llm.RoleSystem, Type: llm.TypeText, Content: "sys"}}
	ch := make(chan ChatStreamChunk, 8)
	seq := uint64(0)
	if got := agt.tryAutoCompact(context.Background(), &msgs, ChatRequest{SessionID: "s", Provider: "test", Model: "m"}, nil, ch, func() uint64 { seq++; return seq - 1 }, 1, 5); got {
		t.Fatalf("nil summarizer should short-circuit to false")
	}
}
