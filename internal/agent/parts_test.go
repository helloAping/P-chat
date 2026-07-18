package agent

import (
	"encoding/json"
	"testing"
)

func TestPartsAccumulator_Text(t *testing.T) {
	acc := newPartsAccumulator()
	acc.update(ChatStreamChunk{Content: "hello "})
	acc.update(ChatStreamChunk{Content: "world"})

	parts := acc.snapshot()
	if len(parts) != 1 || parts[0].Kind != "text" || parts[0].Text != "hello world" {
		t.Errorf("want single text part with 'hello world', got %+v", parts)
	}
}

func TestPartsAccumulator_ThinkingThenText(t *testing.T) {
	// The thinking part is Streaming=true WHILE only-thinking
	// deltas arrive. The first content delta flips it to
	// Streaming=false — that's the "thinking block is done"
	// signal, and it's why a multi-round tool flow doesn't
	// leave every prior round's thinking part stuck in
	// streaming. (Before 2026-07-09 the flip only happened
	// on `c.Done`, which is emitted once per conversation,
	// not per round — so a second round's start was hiding
	// behind a still-streaming first-round thinking part.)
	acc := newPartsAccumulator()
	acc.update(ChatStreamChunk{Thinking: "let me "})
	acc.update(ChatStreamChunk{Thinking: "think..."})

	parts := acc.snapshot()
	if len(parts) != 1 || parts[0].Kind != "thinking" || parts[0].Text != "let me think..." {
		t.Fatalf("after only thinking deltas: want single thinking part 'let me think...', got %+v", parts)
	}
	if !parts[0].Streaming {
		t.Error("thinking part should be streaming=true while only thinking deltas arrive")
	}

	acc.update(ChatStreamChunk{Content: "answer"})

	parts = acc.snapshot()
	if len(parts) != 2 {
		t.Fatalf("after content delta: want 2 parts, got %d", len(parts))
	}
	if parts[0].Kind != "thinking" || parts[0].Text != "let me think..." {
		t.Errorf("thinking part wrong: %+v", parts[0])
	}
	if parts[0].Streaming {
		t.Error("first content delta must flip trailing thinking to Streaming=false (bug fix 2026-07-09)")
	}
	if parts[1].Kind != "text" || parts[1].Text != "answer" {
		t.Errorf("text part wrong: %+v", parts[1])
	}
}

// TestPartsAccumulator_ContentFlipsStreamingThinking locks
// in the multi-round bug fix: a content delta that follows a
// thinking delta must immediately flip the trailing thinking
// part to Streaming=false. Without this, the LLM's
// "still-typing" indicator on the thinking block stays lit
// for the entire rest of the conversation (until `c.Done`,
// which is only emitted once at the end of the whole flow),
// making the user see every prior round's thinking block as
// "still in progress" — exactly the bug reported in
// 2026-07-09 ("一直在思考中 没办法修改状态").
//
// The previous test covers the basic thinking→content case;
// this one specifically verifies the streaming flag on the
// thinking part flips, plus the multi-round scenario where
// a second tool call's thinking should not retroactively
// leave the first round's thinking stuck.
func TestPartsAccumulator_ContentFlipsStreamingThinking(t *testing.T) {
	acc := newPartsAccumulator()
	// Round 1: thinking then content
	acc.update(ChatStreamChunk{Thinking: "first round think"})
	if !acc.snapshot()[0].Streaming {
		t.Fatal("round 1 thinking should be streaming after the first delta")
	}
	acc.update(ChatStreamChunk{Content: "first round answer"})
	parts := acc.snapshot()
	if parts[0].Streaming {
		t.Error("round 1: content delta must flip thinking to streaming=false")
	}

	// Tool call. Thinking part is still streaming=false.
	acc.update(ChatStreamChunk{ToolName: "read_file", ToolArgs: `{"path":"x"}`})
	acc.update(ChatStreamChunk{Phase: "tool", Step: "call-1-ok", ToolName: "read_file", ToolResult: "data", ToolElapsed: "5ms"})
	parts = acc.snapshot()
	if parts[0].Streaming {
		t.Error("after tool call: thinking should still be streaming=false (only content deltas flip it)")
	}

	// Round 2: another thinking + content cycle. The first
	// round's thinking part must remain streaming=false
	// (the flip is one-way per part). A new thinking part
	// is created for round 2.
	acc.update(ChatStreamChunk{Thinking: "second round think"})
	parts = acc.snapshot()
	if parts[0].Streaming {
		t.Error("round 1's thinking part must NOT be flipped back to streaming=true by a later round")
	}
	// The new round 2 thinking is its own part.
	round2Idx := -1
	for i, p := range parts {
		if i > 0 && p.Kind == "thinking" && p.Text == "second round think" {
			round2Idx = i
		}
	}
	if round2Idx < 0 {
		t.Fatalf("round 2 thinking part not found: %+v", parts)
	}
	if !parts[round2Idx].Streaming {
		t.Error("round 2's new thinking part should be streaming=true")
	}
	acc.update(ChatStreamChunk{Content: "second round answer"})
	parts = acc.snapshot()
	if parts[round2Idx].Streaming {
		t.Error("round 2: content delta must flip its thinking to streaming=false")
	}
	if parts[0].Streaming {
		t.Error("round 1's thinking part must stay streaming=false after round 2's content")
	}
}

func TestPartsAccumulator_ToolStartThenOk(t *testing.T) {
	acc := newPartsAccumulator()
	acc.update(ChatStreamChunk{ToolName: "read_file", ToolArgs: `{"path":"x"}`})
	acc.update(ChatStreamChunk{Phase: "tool", Step: "call-1-ok", ToolName: "read_file", ToolResult: "data", ToolElapsed: "5ms"})

	parts := acc.snapshot()
	if len(parts) != 1 || parts[0].Kind != "tool" {
		t.Fatalf("want single tool part, got %+v", parts)
	}
	if parts[0].Name != "read_file" {
		t.Errorf("name = %q", parts[0].Name)
	}
	if parts[0].Status != "ok" {
		t.Errorf("status = %q, want ok", parts[0].Status)
	}
	if parts[0].Result != "data" {
		t.Errorf("result = %q", parts[0].Result)
	}
	if parts[0].Elapsed != "5ms" {
		t.Errorf("elapsed = %q", parts[0].Elapsed)
	}
}

func TestPartsAccumulator_ToolError(t *testing.T) {
	acc := newPartsAccumulator()
	acc.update(ChatStreamChunk{ToolName: "exec_command"})
	acc.update(ChatStreamChunk{Phase: "tool", Step: "call-1-err", ToolName: "exec_command", ToolError: "boom"})

	parts := acc.snapshot()
	if len(parts) != 1 || parts[0].Status != "error" || parts[0].Error != "boom" {
		t.Errorf("want error tool part, got %+v", parts)
	}
}

func TestPartsAccumulator_SubAgent(t *testing.T) {
	acc := newPartsAccumulator()
	acc.update(ChatStreamChunk{
		SubAgent: true, SubAgentStatus: "start", SubAgentTask: "list repo",
	})
	acc.update(ChatStreamChunk{SubAgent: true, SubAgentTask: "list repo", Content: "found 3 files"})
	acc.update(ChatStreamChunk{SubAgent: true, SubAgentTask: "list repo", Content: " and 2 dirs"})
	acc.update(ChatStreamChunk{
		SubAgent: true, SubAgentStatus: "ok", SubAgentTask: "list repo", Duration: "1.2s",
	})

	parts := acc.snapshot()
	if len(parts) != 1 {
		t.Fatalf("want single sub_agent part, got %+v", parts)
	}
	sub := parts[0]
	if sub.Kind != "sub_agent" || sub.Task != "list repo" || sub.Status != "ok" || sub.Elapsed != "1.2s" {
		t.Errorf("outer sub_agent wrong: %+v", sub)
	}
	if len(sub.Parts) != 1 || sub.Parts[0].Kind != "text" || sub.Parts[0].Text != "found 3 files and 2 dirs" {
		t.Errorf("inner text wrong: %+v", sub.Parts)
	}
}

// TestPartsAccumulator_SubAgentMetadata verifies that the
// four new sub-agent metadata fields (agent_type, agent_color,
// agent_model, task_id) ride along on the lifecycle events
// and survive a snapshot. This is what the SubAgentCard
// reads to render the agent name + accent + model chip + the
// resumable task_id badge.
func TestPartsAccumulator_SubAgentMetadata(t *testing.T) {
	acc := newPartsAccumulator()
	acc.update(ChatStreamChunk{
		SubAgent: true, SubAgentStatus: "start", SubAgentTask: "audit",
		SubAgentType: "explore", SubAgentColor: "#44BA81",
		SubAgentModel: "gpt-4o-mini", SubAgentTaskID: "audit-2025-01-15",
	})
	acc.update(ChatStreamChunk{
		SubAgent: true, SubAgentStatus: "ok", SubAgentTask: "audit", Duration: "3.4s",
	})

	parts := acc.snapshot()
	if len(parts) != 1 {
		t.Fatalf("want single sub_agent part, got %+v", parts)
	}
	sub := parts[0]
	if sub.AgentType != "explore" {
		t.Errorf("AgentType = %q, want explore", sub.AgentType)
	}
	if sub.AgentColor != "#44BA81" {
		t.Errorf("AgentColor = %q, want #44BA81", sub.AgentColor)
	}
	if sub.AgentModel != "gpt-4o-mini" {
		t.Errorf("AgentModel = %q, want gpt-4o-mini", sub.AgentModel)
	}
	if sub.TaskID != "audit-2025-01-15" {
		t.Errorf("TaskID = %q, want audit-2025-01-15", sub.TaskID)
	}
	if sub.Elapsed != "3.4s" {
		t.Errorf("Elapsed = %q, want 3.4s", sub.Elapsed)
	}
}

// TestPartsAccumulator_SubAgentMetadataBackfill verifies
// that a model that arrives on a later chunk (after the
// start event) still gets stamped onto the sub-agent part.
// This matters because the sub-agent runner may not know
// the resolved model until after the first round.
func TestPartsAccumulator_SubAgentMetadataBackfill(t *testing.T) {
	acc := newPartsAccumulator()
	acc.update(ChatStreamChunk{
		SubAgent: true, SubAgentStatus: "start", SubAgentTask: "audit",
		SubAgentType: "explore",
		// No SubAgentModel yet.
	})
	// Mid-stream content chunk that carries the model.
	acc.update(ChatStreamChunk{
		SubAgent: true, SubAgentTask: "audit", SubAgentModel: "claude-haiku-4-5",
		Content: "thinking...",
	})
	acc.update(ChatStreamChunk{
		SubAgent: true, SubAgentStatus: "ok", SubAgentTask: "audit", Duration: "2s",
	})

	parts := acc.snapshot()
	if len(parts) != 1 {
		t.Fatalf("want single sub_agent part, got %+v", parts)
	}
	if parts[0].AgentModel != "claude-haiku-4-5" {
		t.Errorf("AgentModel = %q, want claude-haiku-4-5 (backfilled)", parts[0].AgentModel)
	}
	if parts[0].AgentType != "explore" {
		t.Errorf("AgentType = %q, want explore (from start event)", parts[0].AgentType)
	}
}

func TestPartsAccumulator_DoneClearsStreaming(t *testing.T) {
	acc := newPartsAccumulator()
	acc.update(ChatStreamChunk{Thinking: "reasoning..."})
	acc.update(ChatStreamChunk{Done: true})

	parts := acc.snapshot()
	if parts[0].Streaming {
		t.Error("Done chunk should clear streaming flag on thinking parts")
	}
}

func TestPartsAccumulator_ToolStatusFromStep(t *testing.T) {
	cases := []struct {
		step, errMsg, want string
	}{
		{"call-1-ok", "", "ok"},
		{"call-1-warn", "", "warn"},
		{"call-1-err", "", "error"},
		{"call-1", "boom", "error"},
		{"call-1", "", "start"},
	}
	for _, c := range cases {
		if got := ToolStatusFromStep(c.step, c.errMsg); got != c.want {
			t.Errorf("ToolStatusFromStep(%q, %q) = %q, want %q", c.step, c.errMsg, got, c.want)
		}
	}
}

func TestPartsAccumulator_EmptySnapshot(t *testing.T) {
	acc := newPartsAccumulator()
	if got := acc.snapshot(); got != nil {
		t.Errorf("empty acc should snapshot nil, got %+v", got)
	}
}

func TestPartsAccumulator_RoundTripJSON(t *testing.T) {
	// The on-wire shape must round-trip through the same JSON
	// the server's MessagePart expects. This is what the
	// handler decodes when restoring history.
	acc := newPartsAccumulator()
	acc.update(ChatStreamChunk{Thinking: "thinking\n"})
	acc.update(ChatStreamChunk{Content: "text\n"})
	acc.update(ChatStreamChunk{ToolName: "read_file", ToolArgs: "{}"})

	parts := acc.snapshot()
	b, err := json.Marshal(parts)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Spot-check: thinking is "thinking" not "thinking\n" because
	// we didn't append a newline. Content is "text\n".
	var roundTripped []map[string]any
	if err := json.Unmarshal(b, &roundTripped); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(roundTripped) != 3 {
		t.Fatalf("want 3 parts, got %d", len(roundTripped))
	}
	if roundTripped[0]["kind"] != "thinking" {
		t.Errorf("first part kind = %v", roundTripped[0]["kind"])
	}
	if roundTripped[2]["kind"] != "tool" || roundTripped[2]["name"] != "read_file" {
		t.Errorf("third part wrong: %+v", roundTripped[2])
	}
}
