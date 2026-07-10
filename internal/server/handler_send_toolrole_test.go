package server

import (
	"testing"

	"github.com/p-chat/pchat/internal/llm"
)

// TestBuildLLMMessages_FiltersDisplayOnly verifies that
// rows with SubmitToLLM == 0 (system prompts, thinking,
// raw command output) are dropped from the LLM-bound
// history. Without this filter the LLM would re-read
// internal scaffolding on every turn.
func TestBuildLLMMessages_FiltersDisplayOnly(t *testing.T) {
	in := []llm.ChatMessage{
		{Role: llm.RoleSystem, Type: llm.TypeText, Content: "sys", MsgType: llm.MsgTypeText, SubmitToLLM: 0},
		{Role: llm.RoleUser, Type: llm.TypeText, Content: "hi", MsgType: llm.MsgTypeText, SubmitToLLM: 1},
		{Role: llm.RoleAssistant, Type: llm.TypeThinking, Content: "thinking", MsgType: llm.MsgTypeText, SubmitToLLM: 0},
		{Role: llm.RoleAssistant, Type: llm.TypeText, Content: "answer", MsgType: llm.MsgTypeText, SubmitToLLM: 1},
		{Role: llm.RoleTool, Type: llm.TypeToolResult, ToolID: "c1", ToolName: "exec_command", Content: "stdout", MsgType: llm.MsgTypeCommand, SubmitToLLM: 0},
	}
	got := buildLLMMessages(in)
	if len(got) != 2 {
		t.Fatalf("got %d msgs, want 2 (display-only filtered out)", len(got))
	}
	if got[0].Content != "hi" {
		t.Errorf("got[0].Content = %q, want %q", got[0].Content, "hi")
	}
	if got[1].Content != "answer" {
		t.Errorf("got[1].Content = %q, want %q", got[1].Content, "answer")
	}
}

// TestBuildLLMMessages_TaskResultRoleRewrite is the bug #3
// regression test. The historical fix (commit 51039e2)
// flipped every tool_result to role=user so the LLM wouldn't
// reject orphan tool messages. That scrambled the main
// tool loop: read_file / write_file / exec_command /
// todo_write / question results all need to keep role=tool
// so the LLM can match them against their tool_call.
//
// The fix scopes the rewrite to `task` results only — the
// sub-agent system entry. Sub-agent responses arrive as
// tool_result on the parent's stream, but the LLM never
// saw the tool_call (it lives in the parent's tool loop
// only), so the role=user rewrite prevents the orphan
// rejection strict providers would otherwise raise.
func TestBuildLLMMessages_TaskResultRoleRewrite(t *testing.T) {
	in := []llm.ChatMessage{
		// Main tool loop: tool_call + tool_result must
		// stay as role=tool so the LLM can match them.
		{Role: llm.RoleAssistant, Type: llm.TypeToolCall, ToolID: "call_read_1", ToolName: "read_file", ToolInput: `{"path":"/tmp/x"}`, MsgType: llm.MsgTypeTool, SubmitToLLM: 1},
		{Role: llm.RoleTool, Type: llm.TypeToolResult, ToolID: "call_read_1", ToolName: "read_file", Content: "file contents", MsgType: llm.MsgTypeTool, SubmitToLLM: 1},

		// Sub-agent system: parent issues task call,
		// sub-agent returns an opaque summary. The LLM
		// only sees the result; rewriting to role=user
		// is the workaround for orphan-tool rejection.
		{Role: llm.RoleAssistant, Type: llm.TypeToolCall, ToolID: "call_task_1", ToolName: "task", ToolInput: `{"description":"explore"}`, MsgType: llm.MsgTypeTool, SubmitToLLM: 1},
		{Role: llm.RoleTool, Type: llm.TypeToolResult, ToolID: "call_task_1", ToolName: "task", Content: `{"answer":"found 3 bugs"}`, MsgType: llm.MsgTypeTool, SubmitToLLM: 1},

		// Other tool results that should keep role=tool
		// (the historical bug scrambled these too).
		{Role: llm.RoleTool, Type: llm.TypeToolResult, ToolID: "call_exec_1", ToolName: "exec_command", Content: "ok", MsgType: llm.MsgTypeCommand, SubmitToLLM: 1},
		{Role: llm.RoleTool, Type: llm.TypeToolResult, ToolID: "call_q_1", ToolName: "question", Content: `{"questions":[],"answers":{}}`, MsgType: llm.MsgTypeTool, SubmitToLLM: 1},
	}
	got := buildLLMMessages(in)
	if len(got) != len(in) {
		t.Fatalf("got %d msgs, want %d (no display-only rows to filter)", len(got), len(in))
	}
	for i, m := range got {
		if m.Role != in[i].Role {
			continue
		}
	}
	// The actual assertions: read_file, exec_command, question
	// results keep role=tool; task result becomes role=user.
	for _, m := range got {
		switch m.ToolID {
		case "call_read_1", "call_exec_1", "call_q_1":
			if m.Type == llm.TypeToolResult && m.Role != llm.RoleTool {
				t.Errorf("%s result role = %q, want %q (was being globally rewritten to user)",
					m.ToolName, m.Role, llm.RoleTool)
			}
		case "call_task_1":
			if m.Type == llm.TypeToolResult && m.Role != llm.RoleUser {
				t.Errorf("task result role = %q, want %q (orphan sub-agent result needs user role for strict providers)",
					m.Role, llm.RoleUser)
			}
		}
	}
}

// TestBuildLLMMessages_PreservesRoleForNonToolMessages is
// a guard against accidentally widening the rewrite —
// text / system / assistant messages must not be touched.
func TestBuildLLMMessages_PreservesRoleForNonToolMessages(t *testing.T) {
	in := []llm.ChatMessage{
		{Role: llm.RoleUser, Type: llm.TypeText, Content: "hi", MsgType: llm.MsgTypeText, SubmitToLLM: 1},
		{Role: llm.RoleAssistant, Type: llm.TypeText, Content: "hello", MsgType: llm.MsgTypeText, SubmitToLLM: 1},
	}
	got := buildLLMMessages(in)
	if got[0].Role != llm.RoleUser {
		t.Errorf("user msg role = %q, want %q", got[0].Role, llm.RoleUser)
	}
	if got[1].Role != llm.RoleAssistant {
		t.Errorf("assistant msg role = %q, want %q", got[1].Role, llm.RoleAssistant)
	}
}

// TestBuildLLMMessages_EmptyInput confirms the helper
// doesn't crash on an empty history. The caller's caller
// always appends the new user prompt afterwards, so
// returning an empty slice here is the right behaviour
// (the append still adds the prompt).
func TestBuildLLMMessages_EmptyInput(t *testing.T) {
	got := buildLLMMessages(nil)
	if len(got) != 0 {
		t.Errorf("got %d msgs, want 0", len(got))
	}
}
