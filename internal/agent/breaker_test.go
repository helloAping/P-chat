package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// TestBreakerState_RecordFailure semantics: same signature extends the
// streak; a different signature starts a new streak; cum only grows.
func TestBreakerState_RecordFailure(t *testing.T) {
	b := &breakerState{}
	if s, c := b.recordFailure("exec|cmd-a"); s != 1 || c != 1 {
		t.Fatalf("first failure: streak=%d cum=%d, want 1/1", s, c)
	}
	if s, c := b.recordFailure("exec|cmd-a"); s != 2 || c != 2 {
		t.Fatalf("same sig again: streak=%d cum=%d, want 2/2", s, c)
	}
	if s, c := b.recordFailure("exec|cmd-b"); s != 1 || c != 3 {
		t.Fatalf("different sig: streak=%d cum=%d, want 1/3", s, c)
	}
	b.reset()
	if s, c, _ := b.peek(); s != 0 || c != 0 {
		t.Fatalf("after reset: streak=%d cum=%d, want 0/0", s, c)
	}
	b.recordFailure("exec|cmd-c")
	b.resetStreak()
	if s, c, _ := b.peek(); s != 0 || c != 1 {
		t.Fatalf("after resetStreak: streak=%d cum=%d, want 0/1 (cum survives)", s, c)
	}
}

// TestNormalizeToolFailureSig_VariantCommandsMatch locks down the
// normalization the plan demands: the LLM's habit of varying a filename
// or test filter every attempt (`test_all.txt` → `test_out3.txt`, with or
// without a findstr pipe) must NOT bypass the same-command breaker — the
// 2026-08 my-blog session did exactly this for 810 failures.
func TestNormalizeToolFailureSig_VariantCommandsMatch(t *testing.T) {
	variants := []string{
		`{"command":"go test ./internal/service/ -v > test_out3.txt 2>&1 & type test_out3.txt"}`,
		`{"command":"go test ./internal/service/ -v > test_all.txt 2>&1 & type test_all.txt"}`,
		`{"command":"go test ./internal/service/ -v 2>&1 | findstr /C:\"FAIL\" /C:\"PASS\""}`,
		`{"command":"go test ./internal/service/ -run \"TestGetSetting|TestSetSetting\" -v"}`,
	}
	sig := normalizeToolFailureSig("exec_command", variants[0])
	for i, v := range variants {
		if got := normalizeToolFailureSig("exec_command", v); got != sig {
			t.Errorf("variant %d (%s) normalized to %q, want %q — must count as the same failing command", i, v, got, sig)
		}
	}
}

// TestNormalizeToolFailureSig_DifferentCommandsStayDifferent ensures the
// normalization is not so loose that unrelated commands collide.
func TestNormalizeToolFailureSig_DifferentCommandsStayDifferent(t *testing.T) {
	a := normalizeToolFailureSig("exec_command", `{"command":"go test ./internal/service/ -v"}`)
	b := normalizeToolFailureSig("exec_command", `{"command":"go test ./internal/agent/ -v"}`)
	c := normalizeToolFailureSig("exec_command", `{"command":"npm run build"}`)
	if a == b {
		t.Fatalf("different packages must not share a signature: %q == %q", a, b)
	}
	if a == c {
		t.Fatalf("different commands must not share a signature: %q == %q", a, c)
	}
	// Non-command tools fall back to name + exact args.
	d := normalizeToolFailureSig("read_file", `{"path":"a.txt"}`)
	e := normalizeToolFailureSig("read_file", `{"path":"b.txt"}`)
	if d == e {
		t.Fatal("read_file with different paths must not share a signature")
	}
}

// TestIsAutoResumeTurn distinguishes server-injected auto-resumes
// (ClientMsgID==0 + TodoMode=resume) from real user messages — only the
// former keep the cross-turn breaker accumulating.
func TestIsAutoResumeTurn(t *testing.T) {
	if !isAutoResumeTurn(ChatRequest{ClientMsgID: 0, TodoMode: TodoModeResume}) {
		t.Fatal("ClientMsgID=0 + TodoMode=resume must be an auto-resume turn")
	}
	if isAutoResumeTurn(ChatRequest{ClientMsgID: 5, TodoMode: TodoModeResume}) {
		t.Fatal("a real user message (ClientMsgID>0) must NOT be treated as auto-resume")
	}
	if isAutoResumeTurn(ChatRequest{ClientMsgID: 0, TodoMode: ""}) {
		t.Fatal("a plain turn without TodoMode=resume must NOT be treated as auto-resume")
	}
}

// breakerMockLLM serves the SSE stream for the cross-turn breaker tests:
// requests 1..3 (turn 1 failures) and 5 (turn 2 failure) return an
// exec_command tool call; requests 4 and 6 return plain text to end the
// turn. Everything is driven by a request counter.
func breakerMockLLM(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		n := calls.Add(1)
		switch n {
		case 4, 6:
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		default:
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"exec_command\",\"arguments\":\"{\\\"command\\\":\\\"go test ./internal/service/ -v\\\"}\"}}]}}]}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	return srv, &calls
}

// newBreakerTestAgent wires an agent whose exec_command always fails, so
// every dispatched tool call feeds the cross-turn breaker.
func newBreakerTestAgent(t *testing.T, baseURL string) (*Agent, *config.Config) {
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
		Limits: config.LimitsConfig{MaxRounds: 20},
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
	registry := tool.NewRegistry()
	registry.Register(tool.Tool{
		Name:        "exec_command",
		Description: "failing test tool",
	}, func(ctx context.Context, args json.RawMessage) (*tool.CallResult, error) {
		return &tool.CallResult{Content: "boom", IsError: true}, nil
	})
	agt := New(cfg, llmClient, styleMgr, store, registry)
	return agt, cfg
}

// TestChatWithTools_CrossTurnSameToolBreakerFires is the T4 acceptance
// test from the plan: the same failing command repeated across TWO turns
// must trip the breaker on the second turn. Turn 1 accumulates 3
// same-signature failures; turn 2 (an auto-resume) adds a 4th and the
// cross-turn breaker fires — something the old turn-local counters could
// never do.
func TestChatWithTools_CrossTurnSameToolBreakerFires(t *testing.T) {
	srv, _ := breakerMockLLM(t)
	defer srv.Close()
	agt, _ := newBreakerTestAgent(t, srv.URL)

	userMsg := []llm.ChatMessage{{Role: llm.RoleUser, Type: llm.TypeText, Content: "run tests"}}

	// Turn 1: a genuine user turn (ClientMsgID>0) — resets the breaker at
	// start (no-op, fresh), then accumulates 3 same-command failures and
	// ends with a text round.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	for chunk := range agt.ChatWithTools(ctx, ChatRequest{
		Style:       style.Tech,
		Provider:    "test",
		Model:       "test-model",
		SessionID:   "breaker-fire-test",
		ClientMsgID: 1,
		Messages:    userMsg,
	}) {
		if chunk.Step == "cross-turn-same-tool-limit" {
			t.Fatal("breaker must NOT fire inside turn 1 (only 3 failures so far)")
		}
	}
	cancel()

	// Turn 2: auto-resume (ClientMsgID==0 + TodoMode=resume) — must NOT
	// reset the breaker; the first same-command failure pushes the streak
	// to 4 and fires.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel2()
	var fired bool
	for chunk := range agt.ChatWithTools(ctx2, ChatRequest{
		Style:       style.Tech,
		Provider:    "test",
		Model:       "test-model",
		SessionID:   "breaker-fire-test",
		ClientMsgID: 0,
		TodoMode:    TodoModeResume,
		Messages:    userMsg,
	}) {
		if chunk.Step == "cross-turn-same-tool-limit" {
			fired = true
		}
	}
	if !fired {
		t.Fatal("cross-turn same-tool breaker did NOT fire on the second turn — the 810-failure dead-loop guard is missing")
	}
}

// TestChatWithTools_UserTurnResetsBreaker covers the recovery requirement:
// a genuine user message resets the breaker, so a stale failure streak from
// a previous user's chain cannot trip it and hurt the new intent.
func TestChatWithTools_UserTurnResetsBreaker(t *testing.T) {
	srv, _ := breakerMockLLM(t)
	defer srv.Close()
	agt, _ := newBreakerTestAgent(t, srv.URL)

	userMsg := []llm.ChatMessage{{Role: llm.RoleUser, Type: llm.TypeText, Content: "run tests"}}

	// Turn 1: 3 same-command failures (streak=3), ends with a text round.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	for chunk := range agt.ChatWithTools(ctx, ChatRequest{
		Style:       style.Tech,
		Provider:    "test",
		Model:       "test-model",
		SessionID:   "breaker-reset-test",
		ClientMsgID: 1,
		Messages:    userMsg,
	}) {
		if chunk.Step == "cross-turn-same-tool-limit" {
			t.Fatal("turn 1 must not fire the breaker")
		}
	}
	cancel()

	// Turn 2: a NEW user message (ClientMsgID>0) resets the breaker; one
	// failure must NOT fire it (streak restarts at 1).
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel2()
	for chunk := range agt.ChatWithTools(ctx2, ChatRequest{
		Style:       style.Tech,
		Provider:    "test",
		Model:       "test-model",
		SessionID:   "breaker-reset-test",
		ClientMsgID: 2,
		Messages:    userMsg,
	}) {
		if chunk.Step == "cross-turn-same-tool-limit" {
			t.Fatal("a fresh user turn must reset the breaker — stale streaks must not fire")
		}
	}
}
