package llm

import (
	"encoding/json"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

// helper: run Build and unmarshal the resulting body into an
// openai.ChatCompletionRequest. The body is what would be POSTed
// to /chat/completions, so the Messages slice is exactly what the
// upstream sees. This is the surface the upstream rejects when
// the message structure is malformed, so these tests assert on
// it directly.
func mustBuildOpenAI(t *testing.T, a *OpenAIAdapter, msgs []ChatMessage, system string) openai.ChatCompletionRequest {
	t.Helper()
	req, err := a.Build(msgs, "test-model", 0, nil, system, 0, 0)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var out openai.ChatCompletionRequest
	if err := json.Unmarshal(req.Body, &out); err != nil {
		t.Fatalf("unmarshal body: %v\nbody=%s", err, string(req.Body))
	}
	return out
}

// TestOpenAIBuild_ParallelToolCalls_Merged is the regression
// test for the 2026-07-17 incident: the agent emitted 2 parallel
// list_files tool_calls, the previous adapter produced 2 separate
// assistant messages, and the upstream (api-convert.08ms.cn →
// Console Go) returned "Upstream request failed" with
// code=invalid_request_error. After the fix, both tool_calls
// must live in a single assistant message.
func TestOpenAIBuild_ParallelToolCalls_Merged(t *testing.T) {
	a := NewOpenAIAdapter("https://api.example.com", "sk-test", "test")
	msgs := []ChatMessage{
		{Role: RoleUser, Type: TypeText, Content: "list both dirs"},
		{Role: RoleAssistant, Type: TypeText, Content: "I'll list both."},
		{Role: RoleAssistant, Type: TypeToolCall, ToolID: "call_a1", ToolName: "list_files", ToolInput: `{"path":"."}`},
		{Role: RoleAssistant, Type: TypeToolCall, ToolID: "call_a2", ToolName: "list_files", ToolInput: `{"path":"internal"}`},
		{Role: RoleTool, Type: TypeToolResult, ToolID: "call_a1", ToolName: "list_files", Content: "f 1 a.go"},
		{Role: RoleTool, Type: TypeToolResult, ToolID: "call_a2", ToolName: "list_files", Content: "d 2 agent"},
	}
	out := mustBuildOpenAI(t, a, msgs, "")

	// Expect: 1 system/user combined? No — system was empty.
	//   0: user text
	//   1: assistant {content + tool_calls:[a1,a2]}  ← merged
	//   2: tool (a1)
	//   3: tool (a2)
	if got := len(out.Messages); got != 4 {
		t.Fatalf("messages count = %d, want 4 (user, merged assistant, tool a1, tool a2). body=%s",
			got, mustBody(a, msgs, ""))
	}

	// Message 1: assistant must carry BOTH content and tool_calls.
	asst := out.Messages[1]
	if asst.Role != openai.ChatMessageRoleAssistant {
		t.Fatalf("msg[1].Role = %q, want assistant", asst.Role)
	}
	if asst.Content != "I'll list both." {
		t.Errorf("msg[1].Content = %q, want %q", asst.Content, "I'll list both.")
	}
	if len(asst.ToolCalls) != 2 {
		t.Fatalf("msg[1].ToolCalls len = %d, want 2 (parallel tool_calls must be in one message)", len(asst.ToolCalls))
	}
	if asst.ToolCalls[0].ID != "call_a1" || asst.ToolCalls[1].ID != "call_a2" {
		t.Errorf("tool_call IDs = [%q, %q], want [call_a1, call_a2]",
			asst.ToolCalls[0].ID, asst.ToolCalls[1].ID)
	}
	if asst.ToolCalls[0].Function.Name != "list_files" || asst.ToolCalls[1].Function.Name != "list_files" {
		t.Errorf("tool_call names = [%q, %q], want [list_files, list_files]",
			asst.ToolCalls[0].Function.Name, asst.ToolCalls[1].Function.Name)
	}

	// Message 2,3: tool results, each referencing its own tool_call_id.
	if out.Messages[2].Role != openai.ChatMessageRoleTool || out.Messages[2].ToolCallID != "call_a1" {
		t.Errorf("msg[2] = %+v, want tool/call_a1", out.Messages[2])
	}
	if out.Messages[3].Role != openai.ChatMessageRoleTool || out.Messages[3].ToolCallID != "call_a2" {
		t.Errorf("msg[3] = %+v, want tool/call_a2", out.Messages[3])
	}

	// And the wire JSON must NOT contain two consecutive
	// {"role":"assistant",...} entries — that is the exact
	// shape the upstream rejected with "Upstream request
	// failed". Scan the body string for two assistant roles
	// in a row to lock the regression.
	body := string(mustBody(a, msgs, ""))
	if strings.Contains(body, `"role":"assistant","tool_calls":[{"id":"call_a1"`) &&
		strings.Contains(body, `"role":"assistant","tool_calls":[{"id":"call_a2"`) {
		// Coarse check: both call IDs should appear in the same
		// tool_calls array, not in two separate role:assistant
		// objects. Find call_a1 and call_a2 indices.
		i1 := strings.Index(body, `"id":"call_a1"`)
		i2 := strings.Index(body, `"id":"call_a2"`)
		if i1 > 0 && i2 > 0 {
			// They should be close together (within the same
			// tool_calls array). If they're separated by more
			// than 200 bytes, they're in different assistant
			// messages.
			gap := i2 - i1
			if gap > 200 {
				t.Errorf("call_a1 and call_a2 are %d bytes apart — likely in separate assistant messages. body=%s", gap, body)
			}
		}
	}
}

// TestOpenAIBuild_ParallelToolCalls_NoText covers the case
// where the model emits parallel tool_calls WITHOUT a preceding
// text reply. The agent's parser can produce this when the LLM
// streams tool_calls in their first delta.
func TestOpenAIBuild_ParallelToolCalls_NoText(t *testing.T) {
	a := NewOpenAIAdapter("https://api.example.com", "sk-test", "test")
	msgs := []ChatMessage{
		{Role: RoleUser, Type: TypeText, Content: "list both"},
		{Role: RoleAssistant, Type: TypeToolCall, ToolID: "call_x1", ToolName: "read_file", ToolInput: `{"path":"a.go"}`},
		{Role: RoleAssistant, Type: TypeToolCall, ToolID: "call_x2", ToolName: "read_file", ToolInput: `{"path":"b.go"}`},
		{Role: RoleTool, Type: TypeToolResult, ToolID: "call_x1", ToolName: "read_file", Content: "package a"},
		{Role: RoleTool, Type: TypeToolResult, ToolID: "call_x2", ToolName: "read_file", Content: "package b"},
	}
	out := mustBuildOpenAI(t, a, msgs, "")

	// 4 messages: user, merged assistant(tool_calls), tool, tool.
	if got := len(out.Messages); got != 4 {
		t.Fatalf("messages count = %d, want 4", got)
	}
	asst := out.Messages[1]
	if asst.Role != openai.ChatMessageRoleAssistant {
		t.Fatalf("msg[1].Role = %q, want assistant", asst.Role)
	}
	if asst.Content != "" {
		t.Errorf("msg[1].Content = %q, want empty (no text before tool_calls)", asst.Content)
	}
	if len(asst.ToolCalls) != 2 {
		t.Fatalf("msg[1].ToolCalls len = %d, want 2", len(asst.ToolCalls))
	}
}

// TestOpenAIBuild_SingleToolCall_StillWorks makes sure the merge
// logic doesn't break the single-tool_call case (the common one).
func TestOpenAIBuild_SingleToolCall_StillWorks(t *testing.T) {
	a := NewOpenAIAdapter("https://api.example.com", "sk-test", "test")
	msgs := []ChatMessage{
		{Role: RoleUser, Type: TypeText, Content: "hi"},
		{Role: RoleAssistant, Type: TypeToolCall, ToolID: "call_s1", ToolName: "read_file", ToolInput: `{"path":"a.go"}`},
		{Role: RoleTool, Type: TypeToolResult, ToolID: "call_s1", ToolName: "read_file", Content: "package a"},
		{Role: RoleAssistant, Type: TypeText, Content: "got it"},
	}
	out := mustBuildOpenAI(t, a, msgs, "")

	// Expect 4 messages, no merging beyond a single tool_call.
	if got := len(out.Messages); got != 4 {
		t.Fatalf("messages count = %d, want 4", got)
	}
	asst := out.Messages[1]
	if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].ID != "call_s1" {
		t.Errorf("msg[1].ToolCalls = %+v, want single call_s1", asst.ToolCalls)
	}
	if out.Messages[3].Content != "got it" {
		t.Errorf("msg[3] = %+v, want final assistant text", out.Messages[3])
	}
}

// TestOpenAIBuild_TextAndToolCall_Merged covers the "I'll call
// X then call X" pattern — text reply followed by a tool_call.
// OpenAI accepts both fields on the same assistant message.
func TestOpenAIBuild_TextAndToolCall_Merged(t *testing.T) {
	a := NewOpenAIAdapter("https://api.example.com", "sk-test", "test")
	msgs := []ChatMessage{
		{Role: RoleUser, Type: TypeText, Content: "read a.go"},
		{Role: RoleAssistant, Type: TypeText, Content: "Reading."},
		{Role: RoleAssistant, Type: TypeToolCall, ToolID: "call_t1", ToolName: "read_file", ToolInput: `{"path":"a.go"}`},
		{Role: RoleTool, Type: TypeToolResult, ToolID: "call_t1", ToolName: "read_file", Content: "package a"},
		{Role: RoleAssistant, Type: TypeText, Content: "Done."},
	}
	out := mustBuildOpenAI(t, a, msgs, "")

	// 4 messages: user, merged assistant, tool, final assistant text.
	if got := len(out.Messages); got != 4 {
		t.Fatalf("messages count = %d, want 4. body=%s", got, mustBody(a, msgs, ""))
	}
	asst := out.Messages[1]
	if asst.Content != "Reading." || len(asst.ToolCalls) != 1 || asst.ToolCalls[0].ID != "call_t1" {
		t.Errorf("merged assistant = %+v, want Content=Reading. + 1 tool_call call_t1", asst)
	}
}

// TestOpenAIBuild_ToolResultResetsAssistant makes sure that
// after a tool_result, the next assistant entry is NOT merged
// with the previous assistant (that would be a different turn).
func TestOpenAIBuild_ToolResultResetsAssistant(t *testing.T) {
	a := NewOpenAIAdapter("https://api.example.com", "sk-test", "test")
	msgs := []ChatMessage{
		{Role: RoleUser, Type: TypeText, Content: "hi"},
		{Role: RoleAssistant, Type: TypeToolCall, ToolID: "call_r1", ToolName: "x", ToolInput: `{}`},
		{Role: RoleTool, Type: TypeToolResult, ToolID: "call_r1", ToolName: "x", Content: "ok"},
		// New turn: another tool_call. Must NOT merge with the
		// earlier tool_call because a tool_result sits in between.
		{Role: RoleAssistant, Type: TypeToolCall, ToolID: "call_r2", ToolName: "y", ToolInput: `{}`},
		{Role: RoleTool, Type: TypeToolResult, ToolID: "call_r2", ToolName: "y", Content: "ok"},
	}
	out := mustBuildOpenAI(t, a, msgs, "")
	if got := len(out.Messages); got != 5 {
		t.Fatalf("messages count = %d, want 5 (user, asst tc1, tool r1, asst tc2, tool r2)", got)
	}
	// Each assistant entry has exactly 1 tool_call.
	if len(out.Messages[1].ToolCalls) != 1 || out.Messages[1].ToolCalls[0].ID != "call_r1" {
		t.Errorf("msg[1].ToolCalls = %+v, want [call_r1]", out.Messages[1].ToolCalls)
	}
	if len(out.Messages[3].ToolCalls) != 1 || out.Messages[3].ToolCalls[0].ID != "call_r2" {
		t.Errorf("msg[3].ToolCalls = %+v, want [call_r2]", out.Messages[3].ToolCalls)
	}
}

// TestOpenAIBuild_SystemPrepended verifies the system prompt
// becomes the first message and is not affected by the merge
// logic.
func TestOpenAIBuild_SystemPrepended(t *testing.T) {
	a := NewOpenAIAdapter("https://api.example.com", "sk-test", "test")
	msgs := []ChatMessage{
		{Role: RoleUser, Type: TypeText, Content: "hi"},
	}
	out := mustBuildOpenAI(t, a, msgs, "you are a helper")
	if len(out.Messages) != 2 {
		t.Fatalf("messages count = %d, want 2", len(out.Messages))
	}
	if out.Messages[0].Role != openai.ChatMessageRoleSystem || out.Messages[0].Content != "you are a helper" {
		t.Errorf("msg[0] = %+v, want system", out.Messages[0])
	}
}

// mustBody returns the raw request body bytes for the given input.
// Used in error messages for debugging — small enough that the
// extra marshal cost is irrelevant.
func mustBody(a *OpenAIAdapter, msgs []ChatMessage, system string) []byte {
	req, err := a.Build(msgs, "test-model", 0, nil, system, 0, 0)
	if err != nil {
		return []byte("<build error: " + err.Error() + ">")
	}
	return req.Body
}
