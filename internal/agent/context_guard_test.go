package agent

import (
	"context"
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
// No summarizer is set, so tryAutoCompact short-circuits and the request
// payload reaches ensureWithinWindow untouched — the cleanest way to
// exercise the guard in isolation.
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

// TestEnsureWithinWindow_InWindowNoOp locks down the regression half of
// T1: an in-window payload must pass through untouched — no truncation,
// no error. The gate must never degrade the normal request path.
func TestEnsureWithinWindow_InWindowNoOp(t *testing.T) {
	agt, _ := newGuardTestAgent(t, "http://127.0.0.1:9")
	msgs := []llm.ChatMessage{
		{Role: llm.RoleSystem, Type: llm.TypeText, Content: "sys"},
		{Role: llm.RoleUser, Type: llm.TypeText, Content: strings.Repeat("a", 1000)},
	}
	orig := make([]llm.ChatMessage, len(msgs))
	copy(orig, msgs)

	out, err := agt.ensureWithinWindow(msgs, nil, guardReq())
	if err != nil {
		t.Fatalf("in-window request must pass, got error: %v", err)
	}
	if len(out) != len(orig) {
		t.Fatalf("in-window request must not truncate: len=%d want %d", len(out), len(orig))
	}
	if out[0].Content != orig[0].Content || out[1].Content != orig[1].Content {
		t.Fatal("in-window request must return the payload unchanged")
	}
}

// TestEnsureWithinWindow_TruncatesToFit covers the recovery half: many
// small messages push the payload over the window, but dropping the
// oldest ones brings it back inside. The guard must truncate and return
// nil (proceed), keeping the newest context.
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

	out, err := agt.ensureWithinWindow(msgs, nil, guardReq())
	if err != nil {
		t.Fatalf("truncatable payload must proceed, got error: %v", err)
	}
	if len(out) >= origLen {
		t.Fatalf("expected truncation, len=%d (was %d)", len(out), origLen)
	}
	if after := llm.EstimatePromptTokens(out, nil); after > 35808 {
		t.Fatalf("truncated payload still over window: %d", after)
	}
}

// TestEnsureWithinWindow_RefusesSingleOversizedMessage is the core T1
// acceptance: a single message larger than the message budget cannot be
// fixed by truncation (truncateToFit keeps the newest message anyway), so
// the guard must REFUSE with an explicit error instead of sending it.
func TestEnsureWithinWindow_RefusesSingleOversizedMessage(t *testing.T) {
	agt, _ := newGuardTestAgent(t, "http://127.0.0.1:9")
	// 200k ASCII chars ≈ 50k tokens > usable(35808), single message.
	msgs := []llm.ChatMessage{
		{Role: llm.RoleSystem, Type: llm.TypeText, Content: "sys"},
		{Role: llm.RoleUser, Type: llm.TypeText, Content: strings.Repeat("x", 200_000)},
	}
	if total := llm.EstimatePromptTokens(msgs, nil); total <= 35808 {
		t.Fatalf("setup: single message not oversized (total=%d)", total)
	}

	out, err := agt.ensureWithinWindow(msgs, nil, guardReq())
	if err == nil {
		t.Fatal("single oversized message must be refused, got nil error")
	}
	if !strings.Contains(err.Error(), "上下文超限") {
		t.Fatalf("refusal error should tell the user to start a new conversation, got: %v", err)
	}
	_ = out
}

// TestEnsureWithinWindow_RefusesOversizedTools covers the T2 direction:
// when the tool schema block alone reaches the usable window, no amount
// of message trimming can help — the guard must refuse up front.
func TestEnsureWithinWindow_RefusesOversizedTools(t *testing.T) {
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

	if _, err := agt.ensureWithinWindow(msgs, tools, guardReq()); err == nil {
		t.Fatal("oversized tools must be refused, got nil error")
	}
}

// TestChatWithTools_RefusesOverWindowRequest is the end-to-end T1
// acceptance: ChatWithTools must NEVER emit an over-window request. A
// single oversized user message forces the guard's refuse branch; the
// mock upstream must never be contacted, and the stream must end with an
// error+done chunk.
func TestChatWithTools_RefusesOverWindowRequest(t *testing.T) {
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit.Store(true) // if the request ever reaches the LLM, the guard failed
	}))
	defer srv.Close()

	agt, _ := newGuardTestAgent(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var sawRefusal bool
	var sawDone bool
	for chunk := range agt.ChatWithTools(ctx, ChatRequest{
		Style:     style.Tech,
		Provider:  "test",
		Model:     "test-model",
		SessionID: "guard-refuse-test",
		Messages: []llm.ChatMessage{{
			Role:    llm.RoleUser,
			Type:    llm.TypeText,
			Content: strings.Repeat("x", 200_000), // single oversized message
		}},
	}) {
		if strings.Contains(chunk.Error, "上下文超限") {
			sawRefusal = true
		}
		if chunk.Done {
			sawDone = true
		}
	}

	if hit.Load() {
		t.Fatal("over-window request was sent to the upstream — the T1 guard failed")
	}
	if !sawRefusal {
		t.Fatal("expected a context-overflow refusal error chunk")
	}
	if !sawDone {
		t.Fatal("expected a done chunk terminating the stream")
	}
}
