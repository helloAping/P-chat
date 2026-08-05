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

// TestChatWithTools_RecoversFromStalledLLMStream locks down the regression
// where an upstream that goes silent mid-stream (no [DONE], no close — e.g.
// a flaky proxy dropping deepseek after a few reasoning tokens) left the
// round's select blocked until the turn deadline. The UI showed a
// permanently spinning tool / sub-agent card. The agent-level stall
// watchdog (limits.round_stream_stall_timeout) must fire, terminate the
// round, and emit an error + done instead of hanging.
//
// The server pads a dead stream with SSE keep-alive blank lines so the
// client-side idle watchdog (which resets on ANY transport byte) cannot
// fire — only the agent-level stall guard can recover.
func TestChatWithTools_RecoversFromStalledLLMStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}
		// A few real chunks so the stream is fully established, then go
		// silent except for keep-alive padding. Exit when the client
		// disconnects (the stall guard cancels the request).
		for i := 0; i < 3; i++ {
			_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tick %d\"}}]}\n\n", i)
			flusher.Flush()
		}
		for {
			if _, err := fmt.Fprintf(w, "\n"); err != nil {
				return
			}
			flusher.Flush()
			time.Sleep(50 * time.Millisecond)
		}
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
			MaxRounds:               1,
			RoundStreamStallSeconds: 1, // 1s stall window keeps the test fast
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

	agt := New(cfg, llmClient, styleMgr, store, tool.NewRegistry())

	// The turn deadline (15s) is the safety net: if the stall guard is
	// broken the turn hangs here until it fires, and the elapsed check
	// below fails loudly.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	start := time.Now()
	var sawStallError bool
	var sawDone bool
	for chunk := range agt.ChatWithTools(ctx, ChatRequest{
		Style:     style.Tech,
		Provider:  "test",
		Model:     "test-model",
		SessionID: "stall-recovery-test",
		Messages: []llm.ChatMessage{{
			Role:        llm.RoleUser,
			Type:        llm.TypeText,
			Content:     "start",
			MsgType:     llm.MsgTypeText,
			SubmitToLLM: 1,
		}},
	}) {
		if strings.Contains(chunk.Error, "长时间无数据") || strings.Contains(chunk.Error, "stalled") {
			sawStallError = true
		}
		if chunk.Done {
			sawDone = true
		}
	}
	elapsed := time.Since(start)

	if !sawStallError {
		t.Fatalf("stalled upstream should terminate with a stall error, got none")
	}
	if !sawDone {
		t.Fatalf("stalled upstream should emit a done event")
	}
	// Stall guard fires at ~1s; a broken guard would hang until the 15s
	// deadline. Assert we recovered well before it.
	if elapsed > 10*time.Second {
		t.Fatalf("turn took %v to recover from a stalled stream; the stall watchdog likely didn't fire", elapsed)
	}
}
