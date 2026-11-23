package server

import (
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/agent"
)

func TestIsCoalescableChunk(t *testing.T) {
	cases := []struct {
		name string
		c    agent.ChatStreamChunk
		want bool
	}{
		{"content", agent.ChatStreamChunk{Content: "hi", Seq: 1}, true},
		{"thinking", agent.ChatStreamChunk{Thinking: "…", Seq: 2}, true},
		{"both", agent.ChatStreamChunk{Content: "a", Thinking: "b"}, false},
		{"empty", agent.ChatStreamChunk{Phase: "llm"}, false},
		{"done", agent.ChatStreamChunk{Content: "x", Done: true}, false},
		{"tool", agent.ChatStreamChunk{Content: "x", ToolName: "read_file"}, false},
		{"tool args", agent.ChatStreamChunk{ToolName: "write_file", ToolCallStatus: "args", ToolArgs: "{"}, false},
		{"error", agent.ChatStreamChunk{Error: "boom"}, false},
		{"stream reset", agent.ChatStreamChunk{StreamReset: true}, false},
		{"session status", agent.ChatStreamChunk{SessionStatus: "busy"}, false},
	}
	for _, c := range cases {
		if got := isCoalescableChunk(c.c); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestReadCoalescedChunkMergesContentBurst(t *testing.T) {
	ch := make(chan agent.ChatStreamChunk, 8)
	ch <- agent.ChatStreamChunk{Content: "Hel", Seq: 1, TokensIn: 1}
	ch <- agent.ChatStreamChunk{Content: "lo", Seq: 2, TokensIn: 2}
	ch <- agent.ChatStreamChunk{Content: "!", Seq: 3, TokensIn: 3}
	// Non-coalescable forces flush of the buffer.
	ch <- agent.ChatStreamChunk{Phase: "llm", Step: "round-1-done", Message: "done"}
	close(ch)

	var hold *agent.ChatStreamChunk
	got, ok := readCoalescedChunk(ch, &hold)
	if !ok {
		t.Fatal("expected coalesced content")
	}
	if got.Content != "Hello!" {
		t.Fatalf("content = %q, want Hello!", got.Content)
	}
	if got.Seq != 3 || got.TokensIn != 3 {
		t.Fatalf("seq/tokens = %d/%d, want 3/3", got.Seq, got.TokensIn)
	}
	if hold == nil || hold.Phase != "llm" {
		t.Fatalf("hold = %#v, want phase llm", hold)
	}

	next, ok := readCoalescedChunk(ch, &hold)
	if !ok || next.Phase != "llm" || hold != nil {
		t.Fatalf("next = %#v hold=%#v", next, hold)
	}
}

func TestReadCoalescedChunkDoesNotMergeAcrossKinds(t *testing.T) {
	ch := make(chan agent.ChatStreamChunk, 4)
	ch <- agent.ChatStreamChunk{Content: "a", Seq: 1}
	ch <- agent.ChatStreamChunk{Thinking: "t", Seq: 2}
	close(ch)

	var hold *agent.ChatStreamChunk
	got, ok := readCoalescedChunk(ch, &hold)
	if !ok || got.Content != "a" {
		t.Fatalf("got = %#v", got)
	}
	if hold == nil || hold.Thinking != "t" {
		t.Fatalf("hold = %#v", hold)
	}
}

func TestReadCoalescedChunkFlushesOnWindow(t *testing.T) {
	ch := make(chan agent.ChatStreamChunk, 2)
	ch <- agent.ChatStreamChunk{Content: "x", Seq: 1}
	// No more chunks within the window — should return after timer.

	var hold *agent.ChatStreamChunk
	start := time.Now()
	got, ok := readCoalescedChunk(ch, &hold)
	elapsed := time.Since(start)
	if !ok || got.Content != "x" {
		t.Fatalf("got = %#v ok=%v", got, ok)
	}
	if elapsed < sseCoalesceWindow/2 {
		t.Fatalf("returned too fast (%v); expected ~%v wait", elapsed, sseCoalesceWindow)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("coalesce wait too long: %v", elapsed)
	}
}

func TestReadCoalescedChunkCapsFrameSize(t *testing.T) {
	big := strings.Repeat("a", sseCoalesceMaxBytes-10)
	ch := make(chan agent.ChatStreamChunk, 4)
	ch <- agent.ChatStreamChunk{Content: big, Seq: 1}
	ch <- agent.ChatStreamChunk{Content: strings.Repeat("b", 64), Seq: 2}
	close(ch)

	var hold *agent.ChatStreamChunk
	got, ok := readCoalescedChunk(ch, &hold)
	if !ok || got.Content != big {
		t.Fatalf("first frame len=%d ok=%v", len(got.Content), ok)
	}
	if hold == nil || !strings.HasPrefix(hold.Content, "b") {
		t.Fatalf("hold = %#v", hold)
	}
}

func TestReadCoalescedChunkKeepsSubAgentsSeparate(t *testing.T) {
	ch := make(chan agent.ChatStreamChunk, 4)
	ch <- agent.ChatStreamChunk{Content: "parent", Seq: 1}
	ch <- agent.ChatStreamChunk{Content: "child", Seq: 2, SubAgent: true, SubAgentTask: "explore"}
	close(ch)

	var hold *agent.ChatStreamChunk
	got, _ := readCoalescedChunk(ch, &hold)
	if got.Content != "parent" || hold == nil || !hold.SubAgent {
		t.Fatalf("got=%#v hold=%#v", got, hold)
	}
}
