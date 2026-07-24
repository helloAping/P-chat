package server

import (
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/agent"
)

func TestChunkToEvent(t *testing.T) {
	t.Run("content", func(t *testing.T) {
		ev := chunkToEvent(agent.ChatStreamChunk{Content: "hello"}, "cs", "gpt-4o")
		if ev.Type != "content" {
			t.Errorf("Type = %q, want content", ev.Type)
		}
		if ev.Content != "hello" {
			t.Errorf("Content = %q, want hello", ev.Content)
		}
		if ev.Provider != "cs" || ev.Model != "gpt-4o" {
			t.Errorf("provider/model not stamped: %q/%q", ev.Provider, ev.Model)
		}
	})
	t.Run("done", func(t *testing.T) {
		ev := chunkToEvent(agent.ChatStreamChunk{Done: true, TokensIn: 10, TokensOut: 5, Duration: "2s"}, "cs", "gpt-4o")
		if ev.Type != "done" {
			t.Errorf("Type = %q, want done", ev.Type)
		}
		if ev.TokensIn != 10 {
			t.Errorf("TokensIn = %d, want 10", ev.TokensIn)
		}
	})
	t.Run("error", func(t *testing.T) {
		ev := chunkToEvent(agent.ChatStreamChunk{Error: "[auth_error] bad key"}, "cs", "gpt-4o")
		if ev.Type != "error" {
			t.Errorf("Type = %q, want error", ev.Type)
		}
		if ev.Error != "[auth_error] bad key" {
			t.Errorf("Error = %q", ev.Error)
		}
	})
	t.Run("tool", func(t *testing.T) {
		ev := chunkToEvent(agent.ChatStreamChunk{
			Phase: "tool", Step: "call-1-ok", ToolName: "read_file", ToolResult: "hi",
		}, "cs", "gpt-4o")
		if ev.Type != "tool" {
			t.Errorf("Type = %q, want tool", ev.Type)
		}
		if ev.ToolName != "read_file" {
			t.Errorf("ToolName = %q", ev.ToolName)
		}
		if ev.ToolStatus != "ok" {
			t.Errorf("ToolStatus = %q, want ok", ev.ToolStatus)
		}
	})
	t.Run("phase", func(t *testing.T) {
		ev := chunkToEvent(agent.ChatStreamChunk{Phase: "llm", Step: "round-1", Message: "thinking"}, "cs", "gpt-4o")
		if ev.Type != "phase" {
			t.Errorf("Type = %q, want phase", ev.Type)
		}
	})
	t.Run("thinking", func(t *testing.T) {
		ev := chunkToEvent(agent.ChatStreamChunk{Thinking: "let me think..."}, "cs", "gpt-4o")
		if ev.Type != "thinking" {
			t.Errorf("Type = %q, want thinking", ev.Type)
		}
		if ev.Thinking != "let me think..." {
			t.Errorf("Thinking = %q", ev.Thinking)
		}
		if ev.Content != "" {
			t.Errorf("Content = %q, want empty (thinking should not carry content)", ev.Content)
		}
	})
	t.Run("tool_with_sub_agent", func(t *testing.T) {
		ev := chunkToEvent(agent.ChatStreamChunk{
			Phase: "tool", Step: "call-1-ok", ToolName: "read_file", ToolResult: "hi",
			SubAgent: true, SubAgentTask: "list repo", SubAgentStatus: "ok",
		}, "cs", "gpt-4o")
		if ev.Type != "tool" {
			t.Errorf("Type = %q, want tool", ev.Type)
		}
		if !ev.SubAgent {
			t.Errorf("SubAgent = false, want true")
		}
		if ev.SubAgentTask != "list repo" {
			t.Errorf("SubAgentTask = %q, want %q", ev.SubAgentTask, "list repo")
		}
		if ev.SubAgentStatus != "ok" {
			t.Errorf("SubAgentStatus = %q, want ok", ev.SubAgentStatus)
		}
	})
	t.Run("sub_agent_lifecycle", func(t *testing.T) {
		// The sub-agent start/ok/err events come through as
		// plain phase events (no content / no tool_name) with
		// the SubAgent flag set. They should still surface as
		// "phase" so the client can hook them.
		ev := chunkToEvent(agent.ChatStreamChunk{
			Phase: "sub_agent_start", SubAgent: true, SubAgentTask: "build foo", SubAgentStatus: "start",
		}, "cs", "gpt-4o")
		if ev.Type != "phase" {
			t.Errorf("Type = %q, want phase", ev.Type)
		}
		if !ev.SubAgent {
			t.Errorf("SubAgent = false, want true")
		}
		if ev.SubAgentStatus != "start" {
			t.Errorf("SubAgentStatus = %q, want start", ev.SubAgentStatus)
		}
	})
	t.Run("tool_args_round_trip", func(t *testing.T) {
		ev := chunkToEvent(agent.ChatStreamChunk{
			Phase: "tool", Step: "call-2-ok", ToolName: "exec_command",
			ToolArgs: `{"cmd":"ls -la"}`, ToolResult: "...",
		}, "cs", "gpt-4o")
		if ev.ToolArgs != `{"cmd":"ls -la"}` {
			t.Errorf("ToolArgs = %q, want raw json", ev.ToolArgs)
		}
	})
	t.Run("session_status_busy", func(t *testing.T) {
		// The lifecycle signal emitted at the start of the
		// agent loop. Must be a top-level StreamEvent field so
		// the chat store can flip state.sessionWorking[id]
		// without needing a Phase/Type match.
		ev := chunkToEvent(agent.ChatStreamChunk{
			SessionStatus: "busy",
		}, "cs", "gpt-4o")
		if ev.SessionStatus != "busy" {
			t.Errorf("SessionStatus = %q, want busy", ev.SessionStatus)
		}
	})
	t.Run("session_status_idle", func(t *testing.T) {
		// The lifecycle signal emitted on every exit point
		// (success, error, cancel, max-rounds, stuck, panic).
		// The TodoPanel state machine uses "idle" to clear
		// stale todos the LLM never wrote `todos: []` for.
		ev := chunkToEvent(agent.ChatStreamChunk{
			SessionStatus: "idle",
		}, "cs", "gpt-4o")
		if ev.SessionStatus != "idle" {
			t.Errorf("SessionStatus = %q, want idle", ev.SessionStatus)
		}
	})
}

func TestWriteSSEFrame(t *testing.T) {
	var out strings.Builder
	ev := StreamEvent{Type: "content", Content: "hi", Seq: 42}

	if err := writeSSEFrame(&out, ev); err != nil {
		t.Fatalf("writeSSEFrame returned error: %v", err)
	}

	got := out.String()
	if !strings.HasPrefix(got, `data: {"type":"content","content":"hi"`) {
		t.Fatalf("frame data prefix mismatch: %q", got)
	}
	if !strings.Contains(got, "\nid: 42\n\n") {
		t.Fatalf("frame id line missing: %q", got)
	}
}

func TestStreamEventFromChunkAddsDoneIDs(t *testing.T) {
	ev := streamEventFromChunk(
		agent.ChatStreamChunk{Done: true, Seq: 7},
		"openai",
		"gpt-test",
		streamDoneIDs{userMessageID: 10, lastMessageID: 12},
	)

	if ev.Type != "done" {
		t.Fatalf("Type = %q, want done", ev.Type)
	}
	if ev.UserMessageID != 10 || ev.LastMessageID != 12 {
		t.Fatalf("ids = (%d,%d), want (10,12)", ev.UserMessageID, ev.LastMessageID)
	}
	if ev.Seq != 7 || ev.Provider != "openai" || ev.Model != "gpt-test" {
		t.Fatalf("metadata not preserved: %+v", ev)
	}
}
