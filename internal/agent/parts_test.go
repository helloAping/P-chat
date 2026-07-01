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
	acc := newPartsAccumulator()
	acc.update(ChatStreamChunk{Thinking: "let me "})
	acc.update(ChatStreamChunk{Thinking: "think..."})
	acc.update(ChatStreamChunk{Content: "answer"})

	parts := acc.snapshot()
	if len(parts) != 2 {
		t.Fatalf("want 2 parts, got %d", len(parts))
	}
	if parts[0].Kind != "thinking" || parts[0].Text != "let me think..." {
		t.Errorf("thinking part wrong: %+v", parts[0])
	}
	if !parts[0].Streaming {
		t.Error("thinking part should be marked streaming=true while deltas arrive")
	}
	if parts[1].Kind != "text" || parts[1].Text != "answer" {
		t.Errorf("text part wrong: %+v", parts[1])
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
		if got := toolStatusFromStep(c.step, c.errMsg); got != c.want {
			t.Errorf("toolStatusFromStep(%q, %q) = %q, want %q", c.step, c.errMsg, got, c.want)
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
