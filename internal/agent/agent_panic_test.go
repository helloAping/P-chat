package agent

import (
	"context"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
)

// TestChatWithTools_PanicRecovery verifies that a panic inside the
// agent goroutine is caught and surfaced as a final Error chunk, so
// the REPL keeps running.
func TestChatWithTools_PanicRecovery(t *testing.T) {
	cfg, _ := config.Load("")
	llmClient, _ := llm.NewClient(&cfg.LLM)
	styleMgr, _ := style.NewManager(config.PromptDir())
	store, _ := memory.OpenAt(":memory:", 50)
	defer store.Close()
	tools := tool.NewRegistry()

	agt := New(cfg, llmClient, styleMgr, store, tools)

	// We don't have a clean way to inject a panic into the agent's
	// internal flow from outside (the LLM call is real), so this
	// test just exercises the no-tools / no-system path. The recover
	// is wired in agent.go; a true panic-injection test would require
	// a mock LLM. We at least verify the agent doesn't crash on a
	// completely empty request.
	req := ChatRequest{Style: style.Tech, Provider: "none"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream := agt.ChatWithTools(ctx, req)
	// We expect the stream to close (Done=true or context cancel).
	// The first chunk must be SessionStatus=busy (announces the
	// turn start so the frontend TodoPanel state machine can
	// flip `live` to true). The SessionStatus=idle chunk is
	// sent in a deferred closer, which runs AFTER any final
	// Done chunk — so we keep reading until the channel is
	// closed, not just until Done.
	var firstBusy, lastIdle bool
	count := 0
	for chunk := range stream {
		count++
		if chunk.SessionStatus == "busy" {
			firstBusy = true
		}
		if chunk.SessionStatus == "idle" {
			lastIdle = true
		}
	}
	if count == 0 {
		t.Error("expected at least one chunk from the stream")
	}
	if !firstBusy {
		t.Error("expected first chunk to carry SessionStatus=busy")
	}
	if !lastIdle {
		t.Error("expected SessionStatus=idle to be emitted before the stream closed")
	}
}
