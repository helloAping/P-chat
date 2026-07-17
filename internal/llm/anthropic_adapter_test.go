package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// testAnthropicMsg mirrors anthropicMessage but uses
// json.RawMessage for Content, so the test can unmarshal both
// string and array forms (the wire format varies — single text
// blocks marshal as a string, multi-block as an array). This
// avoids depending on the custom MarshalJSON's quirks.
type testAnthropicMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type testAnthropicBody struct {
	Model    string             `json:"model"`
	Messages []testAnthropicMsg `json:"messages"`
}

func mustBuildAnthropic(t *testing.T, a *AnthropicAdapter, msgs []ChatMessage, system string) testAnthropicBody {
	t.Helper()
	req, err := a.Build(msgs, "test-model", 0, nil, system, 0, 0)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var out testAnthropicBody
	if err := json.Unmarshal(req.Body, &out); err != nil {
		t.Fatalf("unmarshal: %v\nbody=%s", err, string(req.Body))
	}
	return out
}

// extractBlocks parses an Anthropic content field (string or
// array) into a []anthropicContentBlock. Single text messages
// arrive as `"hello"`, multi-block as `[...]`.
func extractBlocks(t *testing.T, raw json.RawMessage) []anthropicContentBlock {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	trim := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trim, "\"") {
		// String form → wrap as a single text block.
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("unmarshal string content: %v", err)
		}
		return []anthropicContentBlock{{Type: "text", Text: s}}
	}
	var blocks []anthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatalf("unmarshal array content: %v", err)
	}
	return blocks
}

// TestAnthropicBuild_ParallelToolCalls_Merged is the Anthropic
// counterpart of TestOpenAIBuild_ParallelToolCalls_Merged. The
// Anthropic protocol puts both tool_use blocks in the same
// content array, so two parallel tool_calls must share one
// assistant message.
func TestAnthropicBuild_ParallelToolCalls_Merged(t *testing.T) {
	a := NewAnthropicAdapter("https://api.example.com", "sk-test", "test")
	msgs := []ChatMessage{
		{Role: RoleUser, Type: TypeText, Content: "list both"},
		{Role: RoleAssistant, Type: TypeText, Content: "Listing."},
		{Role: RoleAssistant, Type: TypeToolCall, ToolID: "toolu_a1", ToolName: "list_files", ToolInput: `{"path":"."}`},
		{Role: RoleAssistant, Type: TypeToolCall, ToolID: "toolu_a2", ToolName: "list_files", ToolInput: `{"path":"internal"}`},
		{Role: RoleTool, Type: TypeToolResult, ToolID: "toolu_a1", ToolName: "list_files", Content: "f 1 a"},
		{Role: RoleTool, Type: TypeToolResult, ToolID: "toolu_a2", ToolName: "list_files", Content: "d 2 b"},
	}
	out := mustBuildAnthropic(t, a, msgs, "")

	// 3 top-level messages: user, merged assistant, user(tool_result x2).
	if got := len(out.Messages); got != 3 {
		t.Fatalf("messages count = %d, want 3. body=%s", got, string(mustBodyAnthropic(a, msgs, "")))
	}
	// Merged assistant must contain text + 2 tool_use blocks.
	asst := out.Messages[1]
	if asst.Role != "assistant" {
		t.Fatalf("msg[1].Role = %q, want assistant", asst.Role)
	}
	blocks := extractBlocks(t, asst.Content)
	if len(blocks) != 3 {
		t.Fatalf("assistant content block count = %d, want 3 (text + 2 tool_use). body=%s", len(blocks), string(mustBodyAnthropic(a, msgs, "")))
	}
	if blocks[0].Type != "text" || blocks[0].Text != "Listing." {
		t.Errorf("block[0] = %+v, want text Listing.", blocks[0])
	}
	if blocks[1].Type != "tool_use" || blocks[1].ID != "toolu_a1" {
		t.Errorf("block[1] = %+v, want tool_use toolu_a1", blocks[1])
	}
	if blocks[2].Type != "tool_use" || blocks[2].ID != "toolu_a2" {
		t.Errorf("block[2] = %+v, want tool_use toolu_a2", blocks[2])
	}
	// The 2 tool_results should be in the user message that follows
	// (Anthropic protocol requires tool_result blocks under user role).
	user := out.Messages[2]
	if user.Role != "user" {
		t.Fatalf("msg[2].Role = %q, want user", user.Role)
	}
	ub := extractBlocks(t, user.Content)
	if len(ub) != 2 {
		t.Fatalf("user tool_result block count = %d, want 2. body=%s", len(ub), string(mustBodyAnthropic(a, msgs, "")))
	}
	if ub[0].Type != "tool_result" || ub[0].ToolUseID != "toolu_a1" {
		t.Errorf("user block[0] = %+v, want tool_result toolu_a1", ub[0])
	}
	if ub[1].Type != "tool_result" || ub[1].ToolUseID != "toolu_a2" {
		t.Errorf("user block[1] = %+v, want tool_result toolu_a2", ub[1])
	}
}

// TestAnthropicBuild_SingleToolCall verifies the single-tool_call
// path still produces a single assistant message with one
// tool_use block.
func TestAnthropicBuild_SingleToolCall(t *testing.T) {
	a := NewAnthropicAdapter("https://api.example.com", "sk-test", "test")
	msgs := []ChatMessage{
		{Role: RoleUser, Type: TypeText, Content: "hi"},
		{Role: RoleAssistant, Type: TypeToolCall, ToolID: "toolu_s1", ToolName: "read_file", ToolInput: `{"path":"a.go"}`},
		{Role: RoleTool, Type: TypeToolResult, ToolID: "toolu_s1", ToolName: "read_file", Content: "package a"},
		{Role: RoleAssistant, Type: TypeText, Content: "done"},
	}
	out := mustBuildAnthropic(t, a, msgs, "")
	// user, assistant(tool_use), user(tool_result), assistant(text)
	if got := len(out.Messages); got != 4 {
		t.Fatalf("messages count = %d, want 4", got)
	}
	asst := extractBlocks(t, out.Messages[1].Content)
	if len(asst) != 1 || asst[0].Type != "tool_use" {
		t.Errorf("msg[1] = %+v, want 1 tool_use block", asst)
	}
	final := extractBlocks(t, out.Messages[3].Content)
	if len(final) != 1 || final[0].Type != "text" || final[0].Text != "done" {
		t.Errorf("msg[3] = %+v, want text 'done'", final)
	}
}

// TestAnthropicBuild_ToolResultResetsAssistant ensures the
// post-tool_result assistant entry is NOT merged with the
// pre-tool_result assistant entry (different turn).
func TestAnthropicBuild_ToolResultResetsAssistant(t *testing.T) {
	a := NewAnthropicAdapter("https://api.example.com", "sk-test", "test")
	msgs := []ChatMessage{
		{Role: RoleUser, Type: TypeText, Content: "hi"},
		{Role: RoleAssistant, Type: TypeToolCall, ToolID: "toolu_r1", ToolName: "x", ToolInput: `{}`},
		{Role: RoleTool, Type: TypeToolResult, ToolID: "toolu_r1", ToolName: "x", Content: "ok"},
		{Role: RoleAssistant, Type: TypeToolCall, ToolID: "toolu_r2", ToolName: "y", ToolInput: `{}`},
		{Role: RoleTool, Type: TypeToolResult, ToolID: "toolu_r2", ToolName: "y", Content: "ok"},
	}
	out := mustBuildAnthropic(t, a, msgs, "")
	// user, asst(tc1), user(tool_r1), asst(tc2), user(tool_r2)
	if got := len(out.Messages); got != 5 {
		t.Fatalf("messages count = %d, want 5", got)
	}
	b1 := extractBlocks(t, out.Messages[1].Content)
	if len(b1) != 1 || b1[0].ID != "toolu_r1" {
		t.Errorf("msg[1] tool_use id = %q, want toolu_r1", b1[0].ID)
	}
	b3 := extractBlocks(t, out.Messages[3].Content)
	if len(b3) != 1 || b3[0].ID != "toolu_r2" {
		t.Errorf("msg[3] tool_use id = %q, want toolu_r2", b3[0].ID)
	}
}

// mustBodyAnthropic returns the raw request body bytes for the
// given input. Used in error messages only.
func mustBodyAnthropic(a *AnthropicAdapter, msgs []ChatMessage, system string) []byte {
	req, err := a.Build(msgs, "test-model", 0, nil, system, 0, 0)
	if err != nil {
		return []byte("<build error: " + err.Error() + ">")
	}
	return req.Body
}
