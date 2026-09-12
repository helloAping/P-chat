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

func TestBuildLLMMessages_PreservesDisplayOnlyImageReferences(t *testing.T) {
	in := []llm.ChatMessage{
		{Role: llm.RoleUser, Type: llm.TypeText, Content: "look at the old image", MsgType: llm.MsgTypeText, SubmitToLLM: 1},
		{Role: llm.RoleUser, Type: llm.TypeImage, Content: "upl://old-upl", Name: "old.png", MimeType: "image/png", UploadID: "old-upl", MsgType: llm.MsgTypeImage, SubmitToLLM: 0},
		{Role: llm.RoleAssistant, Type: llm.TypeThinking, Content: "thinking", MsgType: llm.MsgTypeText, SubmitToLLM: 0},
	}

	got := buildLLMMessages(in)
	if len(got) != 2 {
		t.Fatalf("got %d msgs, want text + image reference", len(got))
	}
	if got[1].Type != llm.TypeImage || got[1].UploadID != "old-upl" {
		t.Fatalf("got[1] = %#v, want preserved image reference", got[1])
	}
}

// 已配对的 task 结果必须保留角色，旧版孤立结果仍保留兼容回退。
// Paired task results retain their role; orphan legacy results keep the fallback.
func TestBuildLLMMessages_TaskResultRoleRewrite(t *testing.T) {
	in := []llm.ChatMessage{
		// Main tool loop: tool_call + tool_result must
		// stay as role=tool so the LLM can match them.
		{Role: llm.RoleAssistant, Type: llm.TypeToolCall, ToolID: "call_read_1", ToolName: "read_file", ToolInput: `{"path":"/tmp/x"}`, MsgType: llm.MsgTypeTool, SubmitToLLM: 1},
		{Role: llm.RoleTool, Type: llm.TypeToolResult, ToolID: "call_read_1", ToolName: "read_file", Content: "file contents", MsgType: llm.MsgTypeTool, SubmitToLLM: 1},

		// 已配对的子代理调用与普通工具一样保留完整历史。
		// Paired sub-agent calls preserve history just like ordinary tools.
		{Role: llm.RoleAssistant, Type: llm.TypeToolCall, ToolID: "call_task_1", ToolName: "task", ToolInput: `{"description":"explore"}`, MsgType: llm.MsgTypeTool, SubmitToLLM: 1},
		{Role: llm.RoleTool, Type: llm.TypeToolResult, ToolID: "call_task_1", ToolName: "task", Content: `{"answer":"found 3 bugs"}`, MsgType: llm.MsgTypeTool, SubmitToLLM: 1},
		{Role: llm.RoleTool, Type: llm.TypeToolResult, ToolID: "orphan_task", ToolName: "task", Content: "legacy result", MsgType: llm.MsgTypeTool, SubmitToLLM: 1},
		{Role: llm.RoleAssistant, Type: llm.TypeToolCall, ToolID: "hidden_task", ToolName: "task", SubmitToLLM: 0},
		{Role: llm.RoleTool, Type: llm.TypeToolResult, ToolID: "hidden_task", ToolName: "task", Content: "hidden call result", MsgType: llm.MsgTypeTool, SubmitToLLM: 1},

		// Other tool results that should keep role=tool
		// (the historical bug scrambled these too).
		{Role: llm.RoleTool, Type: llm.TypeToolResult, ToolID: "call_exec_1", ToolName: "exec_command", Content: "ok", MsgType: llm.MsgTypeCommand, SubmitToLLM: 1},
		{Role: llm.RoleTool, Type: llm.TypeToolResult, ToolID: "call_q_1", ToolName: "question", Content: `{"questions":[],"answers":{}}`, MsgType: llm.MsgTypeTool, SubmitToLLM: 1},
	}
	got := buildLLMMessages(in)
	if len(got) != len(in)-1 {
		t.Fatalf("got %d msgs, want %d (hidden call filtered)", len(got), len(in)-1)
	}
	for _, m := range got {
		switch m.ToolID {
		case "call_read_1", "call_exec_1", "call_q_1", "call_task_1":
			if m.Type == llm.TypeToolResult && m.Role != llm.RoleTool {
				t.Errorf("%s result role = %q, want %q (was being globally rewritten to user)",
					m.ToolName, m.Role, llm.RoleTool)
			}
		case "orphan_task", "hidden_task":
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
