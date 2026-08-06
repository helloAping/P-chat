package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// TestTryAutoCompact_BackfillDoesNotDuplicateSummary is the T2 regression
// for the 2026-08-05 my-blog dead-loop root cause. Previously the success
// backfill APPENDED the full accumulated summary to the system message on
// EVERY compaction, while the turn-start injection had already added one
// copy — so the system message grew by ~54KB per round until the request
// carried ~80万 tokens (window 6.4万). The backfill must REPLACE the old
// block with the fresh summary (one block, ever), and must keep the
// newest messages visible (hist non-empty) so the LLM never loses its
// most recent tool interactions.
func TestTryAutoCompact_BackfillDoesNotDuplicateSummary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"S1"}}]}`)
	}))
	defer srv.Close()

	cfg := &config.Config{
		LLM: config.LLMConfig{
			Default: "test",
			Providers: []config.ProviderConfig{{
				Name: "test", Protocol: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m",
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
	agt.SetSummarizer(memory.NewSummarizer(store, llmClient, "test", 50))

	convID := "backfill-no-dup"
	if err := store.EnsureConversation(convID, "t"); err != nil {
		t.Fatal(err)
	}
	// Messages 1..40 are already covered by the "old" summary (small, so
	// the fresh summary S1 added by this compaction survives the injection
	// cap — the cap itself is covered by TestAppendSummaryInjection_CapsSize).
	if err := store.SaveSummary(convID, 1, 40, "old-summary-v0"); err != nil {
		t.Fatal(err)
	}
	// Messages 41..50 are un-summarized; the newest 6 (45..50) must stay
	// visible after compaction (summaryProtectedNewest).
	for i := 41; i <= 50; i++ {
		store.AddChatMessageToWithID(convID, llm.ChatMessage{
			Role:        llm.RoleUser,
			Type:        llm.TypeText,
			Content:     fmt.Sprintf("msg-%d", i),
			MsgType:     llm.MsgTypeText,
			SubmitToLLM: 1,
		}, int64(i))
	}

	// System prompt with a turn-start summary injection (as the agent does
	// at request build), plus an in-flight tail of BIG messages to push the
	// estimate over the window so tryAutoCompact fires. The backfill then
	// REPLACES this list with the DB-derived tail (small rows), so the
	// post-compact payload is small — exactly the production shape.
	msgs := []llm.ChatMessage{
		{Role: llm.RoleSystem, Type: llm.TypeText, Content: appendSummaryInjection("static", "old-summary-v0")},
	}
	big := strings.Repeat("内容内容", 2000) // ≈ 4k tokens each
	for i := 41; i <= 50; i++ {
		msgs = append(msgs, llm.ChatMessage{Role: llm.RoleUser, Type: llm.TypeText, Content: big})
	}
	if total := llm.EstimatePromptTokens(msgs, nil); total <= 35808 {
		t.Fatalf("setup: not over budget (total=%d)", total)
	}

	ch := make(chan ChatStreamChunk, 64)
	seq := uint64(0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	compacted := agt.tryAutoCompact(ctx, &msgs, ChatRequest{
		SessionID: convID,
		Provider:  "test",
		Model:     "m",
	}, nil, ch, func() uint64 { seq++; return seq - 1 }, 1, 5)
	if !compacted {
		t.Fatal("expected compaction to succeed")
	}

	// Exactly ONE summary block — the fresh summary REPLACED the old one.
	if got := strings.Count(msgs[0].Content, summaryBlockStart); got != 1 {
		t.Fatalf("system prompt has %d summary blocks after backfill, want exactly 1 (the T2 duplication bug)", got)
	}
	// The fresh summary (S1) must be present; the summary must be bounded.
	if !strings.Contains(msgs[0].Content, "S1") {
		t.Fatal("fresh summary S1 missing after backfill")
	}
	// The newest messages must still be in the request — the LLM keeps its
	// recent context (messages=1 dead-loop must never recur).
	if len(msgs) < 2 {
		t.Fatal("backfill collapsed the request to system-only — the LLM would lose its recent context")
	}
	if got := llm.EstimatePromptTokens(msgs, nil); got > 35808 {
		t.Fatalf("backfilled payload still over window: %d", got)
	}
	for len(ch) > 0 {
		<-ch
	}
}
