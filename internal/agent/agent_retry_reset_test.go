// Tests for the mid-stream retry accumulator reset (REL-01).
//
// Regression: fullContentBuilder / fullThinkingBuilder / argsAccum /
// streamBytes / totalIn/totalOut lived at round scope while the LLM retry
// loop re-entered the stream reader inside the same round. A mid-stream
// failure (rate_limit / stall) followed by a retry therefore stacked the
// retried attempt's deltas on top of the failed attempt's partial output —
// the user saw duplicated text and tool args JSON was concatenated into
// invalid JSON. The fix resets every per-attempt accumulator and emits a
// stream_reset event so the frontend drops the partial parts too.
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

// TestChatWithTools_RetryResetsPartialStream drives a two-attempt round:
// attempt 1 streams two content deltas and then goes silent (the agent-level
// stall watchdog fires and marks the attempt retryable with KindTimeout);
// attempt 2 streams the full answer. The final persisted assistant message
// must contain ONLY attempt 2's content, and a stream_reset event must have
// been emitted between the partial deltas and the retried ones.
func TestChatWithTools_RetryResetsPartialStream(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, ok := w.(http.Flusher)
		if !ok {
			return
		}
		if calls.Add(1) == 1 {
			// Attempt 1: partial content, then total silence — no
			// [DONE], no keep-alives. The agent-level stall watchdog
			// (round_stream_stall_timeout=1s) fires, marks the attempt
			// retryable, and cancels the request; the cancellation
			// unblocks the handler via r.Context().Done().
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"PARTIAL-ONE \"}}]}\n\n")
			fl.Flush()
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"PARTIAL-TWO \"}}]}\n\n")
			fl.Flush()
			<-r.Context().Done()
			return
		}
		// Attempt 2: the full answer.
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"FINAL-ANSWER\"}}]}\n\n")
		fl.Flush()
		fmt.Fprintf(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer srv.Close()

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
		Limits: config.LimitsConfig{
			MaxRounds: 1,
			// One retry with a zero-second backoff keeps the test fast.
			LLMRetryBackoffs: []int{0},
			// 1s stall window: attempt 1 goes silent after the partial
			// deltas, so the watchdog (not the 15s turn deadline) is
			// what triggers the retry.
			RoundStreamStallSeconds: 1,
		},
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
	// The assistant-message persist path writes with a FK to the
	// conversations table — seed the conversation up front.
	if err := store.EnsureConversation("retry-reset-test", "t"); err != nil {
		t.Fatal(err)
	}

	agt := New(cfg, llmClient, styleMgr, store, tool.NewRegistry())

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Mirror the frontend's append/reset contract: content deltas append to
	// the live buffer; a stream_reset drops it before the retry re-streams.
	var liveText strings.Builder
	var sawStreamReset bool
	var sawDone bool
	resetAfterPartial := false
	for chunk := range agt.ChatWithTools(ctx, ChatRequest{
		Style:     style.Tech,
		Provider:  "test",
		Model:     "test-model",
		SessionID: "retry-reset-test",
		Messages: []llm.ChatMessage{{
			Role:        llm.RoleUser,
			Type:        llm.TypeText,
			Content:     "start",
			MsgType:     llm.MsgTypeText,
			SubmitToLLM: 1,
		}},
	}) {
		switch {
		case chunk.StreamReset:
			sawStreamReset = true
			if strings.Contains(liveText.String(), "PARTIAL-ONE") {
				resetAfterPartial = true
			}
			liveText.Reset()
		case chunk.Content != "":
			liveText.WriteString(chunk.Content)
		case chunk.Done:
			sawDone = true
		}
	}

	if !sawDone {
		t.Fatal("expected a done chunk")
	}
	if calls.Load() != 2 {
		t.Fatalf("expected exactly 2 upstream calls (1 failure + 1 retry), got %d", calls.Load())
	}
	if !sawStreamReset {
		t.Fatal("expected a stream_reset event before the retried attempt")
	}
	if !resetAfterPartial {
		t.Fatal("stream_reset must arrive AFTER the partial deltas were sent")
	}
	if got := liveText.String(); got != "FINAL-ANSWER" {
		t.Errorf("live text after reset+retry = %q, want %q (no duplicated partial output)", got, "FINAL-ANSWER")
	}

	// The persisted assistant message must also carry only the retried
	// attempt's content — the failed attempt's partial text must not leak
	// into the conversation history.
	msgs := store.GetChatMessagesFor("retry-reset-test", 20)
	var assistant string
	for _, m := range msgs {
		if m.Role == llm.RoleAssistant {
			assistant += m.Content
		}
	}
	if strings.Contains(assistant, "PARTIAL-ONE") || strings.Contains(assistant, "PARTIAL-TWO") {
		t.Errorf("persisted assistant content contains failed-attempt partials: %q", assistant)
	}
	if !strings.Contains(assistant, "FINAL-ANSWER") {
		t.Errorf("persisted assistant content missing retried answer: %q", assistant)
	}
}

// TestPartsAccumulator_StreamReset locks down the accumulator side of the
// reset: text and thinking parts are dropped, while skill / tool / question
// parts (which never belong to the failed attempt's stream) survive.
func TestPartsAccumulator_StreamReset(t *testing.T) {
	acc := newPartsAccumulator()
	acc.update(ChatStreamChunk{SkillName: "demo", SkillStatus: "ready"})
	acc.update(ChatStreamChunk{Thinking: "partial thinking"})
	acc.update(ChatStreamChunk{Content: "partial content"})
	acc.update(ChatStreamChunk{ToolName: "read_file", ToolCallStatus: "ok", ToolResult: "data"})

	acc.update(ChatStreamChunk{StreamReset: true})

	parts := acc.snapshot()
	if len(parts) != 2 {
		t.Fatalf("after reset got %d parts, want 2 (skill + tool): %+v", len(parts), parts)
	}
	if parts[0].Kind != "skill" || parts[1].Kind != "tool" {
		t.Errorf("surviving parts = %q/%q, want skill/tool", parts[0].Kind, parts[1].Kind)
	}
	for _, p := range parts {
		if p.Kind == "text" || p.Kind == "thinking" {
			t.Errorf("streamable part %q survived the reset: %+v", p.Kind, p)
		}
	}
}

// TestPartsAccumulator_StreamResetEmpty is the defensive path: a reset with
// no prior streamable parts must be a no-op (no panic, no phantom parts).
func TestPartsAccumulator_StreamResetEmpty(t *testing.T) {
	acc := newPartsAccumulator()
	acc.update(ChatStreamChunk{StreamReset: true})
	if parts := acc.snapshot(); len(parts) != 0 {
		t.Fatalf("reset on empty accumulator produced %d parts, want 0", len(parts))
	}
}
