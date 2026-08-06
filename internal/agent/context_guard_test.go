package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
	"github.com/p-chat/pchat/internal/upgrade"
)

// newGuardTestAgent builds an agent wired exactly like the other agent
// tests: a real llm.Client (points at baseURL, used only when a request
// actually escapes the guard), a throwaway in-memory store and a registry.
// No summarizer is set, so ensureWithinWindow's level 1 (summary-replace)
// short-circuits and the test exercises the head-trim / truncate / trim /
// minimal-set levels in isolation.
func newGuardTestAgent(t *testing.T, baseURL string) (*Agent, *config.Config) {
	t.Helper()
	cfg := &config.Config{
		LLM: config.LLMConfig{
			Default: "test",
			Providers: []config.ProviderConfig{{
				Name:     "test",
				Protocol: "openai",
				BaseURL:  baseURL,
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
	t.Cleanup(func() { store.Close() })
	if err := upgrade.SeedForTesting(store.DB()); err != nil {
		t.Fatal(err)
	}
	styleMgr, err := style.NewManager(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	agt := New(cfg, llmClient, styleMgr, store, tool.NewRegistry())
	return agt, cfg
}

// guardReq is the minimal ChatRequest the guard needs (provider/model).
func guardReq() ChatRequest {
	return ChatRequest{Provider: "test", Model: "test-model"}
}

// guardSink drains the guard's phase-event channel and returns the steps
// that fired, so tests can assert the user was told about convergence.
func guardSink() (chan ChatStreamChunk, func() []string) {
	ch := make(chan ChatStreamChunk, 64)
	var steps []string
	return ch, func() []string {
		for len(ch) > 0 {
			c := <-ch
			if isGuardStep(c.Step) {
				steps = append(steps, c.Step)
			}
		}
		return steps
	}
}

// TestEnsureWithinWindow_InWindowNoOp locks down the regression half of
// T1: an in-window payload must pass through untouched — no truncation,
// no events. The gate must never degrade the normal request path.
func TestEnsureWithinWindow_InWindowNoOp(t *testing.T) {
	agt, _ := newGuardTestAgent(t, "http://127.0.0.1:9")
	msgs := []llm.ChatMessage{
		{Role: llm.RoleSystem, Type: llm.TypeText, Content: "sys"},
		{Role: llm.RoleUser, Type: llm.TypeText, Content: strings.Repeat("a", 1000)},
	}
	orig := make([]llm.ChatMessage, len(msgs))
	copy(orig, msgs)

	ch, steps := guardSink()
	seq := uint64(0)
	out, tools := agt.ensureWithinWindow(context.Background(), msgs, nil, guardReq(), ch, func() uint64 { seq++; return seq - 1 }, 1, 5, true)
	if len(out) != len(orig) {
		t.Fatalf("in-window request must not truncate: len=%d want %d", len(out), len(orig))
	}
	if out[0].Content != orig[0].Content || out[1].Content != orig[1].Content {
		t.Fatal("in-window request must return the payload unchanged")
	}
	if tools != nil {
		t.Fatalf("in-window request must not trim tools: got %d tools", len(tools))
	}
	if got := steps(); len(got) != 0 {
		t.Fatalf("in-window request must emit no guard events, got %v", got)
	}
}

// TestEnsureWithinWindow_TruncatesToFit covers the level-2 recovery half:
// many small messages push the payload over the window, but dropping the
// oldest ones brings it back inside. The guard must truncate and return
// the converged payload (proceed), keeping the newest context.
func TestEnsureWithinWindow_TruncatesToFit(t *testing.T) {
	agt, _ := newGuardTestAgent(t, "http://127.0.0.1:9")
	msgs := []llm.ChatMessage{
		{Role: llm.RoleSystem, Type: llm.TypeText, Content: "sys"},
	}
	// ~600 tokens each (CJK); 200 of them ≈ 120k >> usable(35808).
	big := strings.Repeat("内容", 200)
	for i := 0; i < 200; i++ {
		msgs = append(msgs, llm.ChatMessage{Role: llm.RoleUser, Type: llm.TypeText, Content: big})
	}
	if total := llm.EstimatePromptTokens(msgs, nil); total <= 35808 {
		t.Fatalf("setup: not over budget (total=%d)", total)
	}
	origLen := len(msgs)

	ch, steps := guardSink()
	seq := uint64(0)
	out, _ := agt.ensureWithinWindow(context.Background(), msgs, nil, guardReq(), ch, func() uint64 { seq++; return seq - 1 }, 1, 5, true)
	if len(out) >= origLen {
		t.Fatalf("expected truncation, len=%d (was %d)", len(out), origLen)
	}
	if after := llm.EstimatePromptTokens(out, nil); after > 35808 {
		t.Fatalf("truncated payload still over window: %d", after)
	}
	if len(out) == 0 || out[0].Role != llm.RoleSystem {
		t.Fatal("system prompt must survive head-trimming")
	}
	if !hasGuardStep(t, steps(), "guard-head-trim") {
		t.Fatal("expected a guard-head-trim phase event")
	}
}

// TestEnsureWithinWindow_SingleOversizedMessage_ConvergesAndSends is the
// core T1 acceptance: a single message larger than the message budget
// cannot be fixed by head-trimming (truncateToFit keeps the newest
// message anyway). The NEW contract (Codex-style) is that the guard must
// NOT refuse — it truncates the oversized content and still returns an
// in-window payload with the system prompt and the user message intact.
func TestEnsureWithinWindow_SingleOversizedMessage_ConvergesAndSends(t *testing.T) {
	agt, _ := newGuardTestAgent(t, "http://127.0.0.1:9")
	// 200k ASCII chars ≈ 50k tokens > usable(35808), single message.
	userMsg := strings.Repeat("x", 200_000)
	msgs := []llm.ChatMessage{
		{Role: llm.RoleSystem, Type: llm.TypeText, Content: "sys"},
		{Role: llm.RoleUser, Type: llm.TypeText, Content: userMsg},
	}
	if total := llm.EstimatePromptTokens(msgs, nil); total <= 35808 {
		t.Fatalf("setup: single message not oversized (total=%d)", total)
	}

	ch, steps := guardSink()
	seq := uint64(0)
	out, _ := agt.ensureWithinWindow(context.Background(), msgs, nil, guardReq(), ch, func() uint64 { seq++; return seq - 1 }, 1, 5, true)
	if after := llm.EstimatePromptTokens(out, nil); after > 35808 {
		t.Fatalf("converged payload still over window: %d", after)
	}
	if len(out) != 2 || out[0].Role != llm.RoleSystem || out[1].Role != llm.RoleUser {
		t.Fatalf("system + latest user message must be preserved, got %d messages", len(out))
	}
	if out[1].Content == userMsg {
		t.Fatal("oversized user content must have been truncated")
	}
	if got := steps(); len(got) == 0 {
		t.Fatal("expected a guard phase event (truncate-content or minimal)")
	}
}

// TestEnsureWithinWindow_OversizedTools_TrimmedNotRefused covers the T2
// direction: when the tool schema block alone reaches the usable window,
// no amount of message trimming can help. The guard must TRIM the tool
// list (level 3a) instead of refusing.
func TestEnsureWithinWindow_OversizedTools_TrimmedNotRefused(t *testing.T) {
	agt, _ := newGuardTestAgent(t, "http://127.0.0.1:9")
	// One tool with a ~150k-char description ≈ 37.5k tokens > usable.
	tools := []llm.ToolDef{{
		Name:        "huge_tool",
		Description: strings.Repeat("y", 150_000),
	}}
	if est := llm.EstimateTokensTools(tools); est <= 35808 {
		t.Fatalf("setup: tools not oversized (est=%d)", est)
	}
	msgs := []llm.ChatMessage{{Role: llm.RoleSystem, Type: llm.TypeText, Content: "sys"}}

	ch, steps := guardSink()
	seq := uint64(0)
	out, outTools := agt.ensureWithinWindow(context.Background(), msgs, tools, guardReq(), ch, func() uint64 { seq++; return seq - 1 }, 1, 5, true)
	if after := llm.EstimatePromptTokens(out, outTools); after > 35808 {
		t.Fatalf("converged payload still over window: %d", after)
	}
	if len(outTools) >= len(tools) {
		t.Fatalf("oversized tools must be trimmed: got %d tools (was %d)", len(outTools), len(tools))
	}
	if got := steps(); !hasGuardStep(t, got, "guard-trim-tools") {
		t.Fatalf("expected a guard-trim-tools phase event, got %v", got)
	}
}

// TestEnsureWithinWindow_ExtremeFallback_StillSends is the pathological
// case: an oversized system prompt (huge skill/summary), a giant user
// message AND tools alone over the window. Every softer level fails; the
// minimal set must still produce an in-window payload that keeps the
// system prompt and the newest user message, and never an error.
func TestEnsureWithinWindow_ExtremeFallback_StillSends(t *testing.T) {
	agt, _ := newGuardTestAgent(t, "http://127.0.0.1:9")
	msgs := []llm.ChatMessage{
		{Role: llm.RoleSystem, Type: llm.TypeText, Content: strings.Repeat("sys-prompt", 20_000)}, // ~60k tokens alone
		{Role: llm.RoleUser, Type: llm.TypeText, Content: strings.Repeat("latest-intent", 10_000)},
	}
	tools := []llm.ToolDef{{Name: "big", Description: strings.Repeat("z", 100_000)}}

	ch, _ := guardSink()
	seq := uint64(0)
	out, outTools := agt.ensureWithinWindow(context.Background(), msgs, tools, guardReq(), ch, func() uint64 { seq++; return seq - 1 }, 1, 5, true)
	if after := llm.EstimatePromptTokens(out, outTools); after > 35808 {
		t.Fatalf("minimal set still over window: %d", after)
	}
	if len(out) == 0 || out[0].Role != llm.RoleSystem {
		t.Fatal("system prompt must survive the extreme fallback")
	}
	foundUser := false
	for i := len(out) - 1; i >= 1; i-- {
		if out[i].Role == llm.RoleUser {
			foundUser = true
			break
		}
	}
	if !foundUser {
		t.Fatal("latest user intent must survive the extreme fallback")
	}
}

// TestChatWithTools_OverWindowRequestStillSent is the end-to-end T1
// acceptance: ChatWithTools must STILL SEND an over-window request after
// converging it. The old contract (refuse + "请开新对话" error + never
// contact upstream) is replaced by the Codex-style contract (converge +
// send + done). The mock upstream must be contacted, and the stream must
// end with a plain done (no refusal error).
func TestChatWithTools_OverWindowRequestStillSent(t *testing.T) {
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit.Store(true)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	agt, _ := newGuardTestAgent(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var sawGuardEvent bool
	var sawError bool
	var sawDone bool
	for chunk := range agt.ChatWithTools(ctx, ChatRequest{
		Style:     style.Tech,
		Provider:  "test",
		Model:     "test-model",
		SessionID: "guard-send-test",
		Messages: []llm.ChatMessage{{
			Role:        llm.RoleUser,
			Type:        llm.TypeText,
			Content:     strings.Repeat("x", 200_000), // single oversized message
			MsgType:     llm.MsgTypeText,
			SubmitToLLM: 1,
		}},
	}) {
		if isGuardStep(chunk.Step) {
			sawGuardEvent = true
		}
		if chunk.Error != "" {
			sawError = true
		}
		if chunk.Done {
			sawDone = true
		}
	}

	if !hit.Load() {
		t.Fatal("converged over-window request was NOT sent to the upstream — the conversation was interrupted instead of continued")
	}
	if !sawGuardEvent {
		t.Fatal("expected a guard phase event telling the user context was trimmed")
	}
	if sawError {
		t.Fatal("convergence must not surface a refusal error — the conversation must continue")
	}
	if !sawDone {
		t.Fatal("expected a done chunk terminating the stream normally")
	}
}

// TestChatWithTools_OverWindowWithTools_StillSent covers the end-to-end
// path where the tool schema block alone overflows the window: the guard
// trims tools and still sends, instead of the old "refuse up front".
func TestChatWithTools_OverWindowWithTools_StillSent(t *testing.T) {
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit.Store(true)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	// Register a tool with a giant description so the registry produces a
	// tool schema block that alone overflows the window. The agent is built
	// manually below so the test controls the registry.
	registry := tool.NewRegistry()
	registry.RegisterForTest(tool.Tool{
		Name:        "huge_test_tool",
		Description: strings.Repeat("y", 150_000),
	})
	cfg := &config.Config{
		LLM: config.LLMConfig{
			Default: "test",
			Providers: []config.ProviderConfig{{
				Name:     "test",
				Protocol: "openai",
				BaseURL:  srv.URL,
				APIKey:   "test-key",
				Model:    "test-model",
			}},
		},
		Limits: config.LimitsConfig{MaxRounds: 2},
	}
	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.OpenAt(":memory:", 50)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := upgrade.SeedForTesting(store.DB()); err != nil {
		t.Fatal(err)
	}
	styleMgr, err := style.NewManager(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	agt2 := New(cfg, llmClient, styleMgr, store, registry)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var sawGuardEvent bool
	var sawError bool
	var sawDone bool
	for chunk := range agt2.ChatWithTools(ctx, ChatRequest{
		Style:     style.Tech,
		Provider:  "test",
		Model:     "test-model",
		SessionID: "guard-tools-send-test",
		Messages: []llm.ChatMessage{{
			Role:        llm.RoleUser,
			Type:        llm.TypeText,
			Content:     "hello",
			MsgType:     llm.MsgTypeText,
			SubmitToLLM: 1,
		}},
	}) {
		if isGuardStep(chunk.Step) {
			sawGuardEvent = true
		}
		if chunk.Error != "" {
			sawError = true
		}
		if chunk.Done {
			sawDone = true
		}
	}

	if !hit.Load() {
		t.Fatal("request with over-window tools was NOT sent — tools should have been trimmed, not the turn refused")
	}
	if !sawGuardEvent {
		t.Fatal("expected a guard phase event for tool trimming")
	}
	if sawError {
		t.Fatal("tool-trimming convergence must not surface an error")
	}
	if !sawDone {
		t.Fatal("expected a done chunk")
	}
}

// hasGuardStep is a tiny assertion helper for the emitted event steps.
func hasGuardStep(t *testing.T, steps []string, want string) bool {
	t.Helper()
	for _, s := range steps {
		if s == want {
			return true
		}
	}
	return false
}
