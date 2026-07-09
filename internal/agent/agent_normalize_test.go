package agent

import (
	"testing"

	"github.com/p-chat/pchat/internal/llm"
)

// TestNeedsNormalizedToolResultsDefault locks in the contract
// that the legacy `normalizeToolResults` is NOT applied to
// standard providers. The previous global application broke
// the `question` tool flow: the LLM lost the tool_call +
// tool_result pairing in its context, interpreted the tool
// result as a user message, and dutifully re-asked the
// question — an infinite loop.
//
// If you add a new provider that genuinely needs the
// transformation, add it to the switch in
// `needsNormalizedToolResults` and update this test.
func TestNeedsNormalizedToolResultsDefault(t *testing.T) {
	// Standard well-behaved providers — none should normalize.
	known := []string{
		"openai",
		"anthropic",
		"claude",
		"deepseek",
		"ollama",
		"cs",        // Doubao proxy, OpenAI-compatible
		"doubao",
		"mimo",
		"xiaomi",
		"",          // empty / unset
		"unknown-provider",
	}
	for _, name := range known {
		if needsNormalizedToolResults(name) {
			t.Errorf("needsNormalizedToolResults(%q) = true; standard providers must NOT normalize (would break the question tool loop)", name)
		}
	}
}

// TestNormalizeToolResultsRoundTrip documents the (legacy,
// opt-in) transformation: tool_call rows are removed,
// tool_result rows are converted to user role. The test
// exists so the function's behaviour is pinned even when no
// provider currently uses it — the day a quirky proxy shows
// up and we add its name to the switch above, this test
// still tells us what the function does.
func TestNormalizeToolResultsRoundTrip(t *testing.T) {
	in := []llm.ChatMessage{
		{Role: llm.RoleUser, Type: llm.TypeText, Content: "hi"},
		{Role: llm.RoleAssistant, Type: llm.TypeText, Content: "let me ask"},
		{Role: llm.RoleAssistant, Type: llm.TypeToolCall, ToolID: "call_1", ToolName: "question", ToolInput: `{"questions":[]}`},
		{Role: llm.RoleTool, Type: llm.TypeToolResult, ToolID: "call_1", ToolName: "question", Content: `{"questions":[],"answers":{}}`},
	}
	out := normalizeToolResults(in)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages (tool_call dropped), got %d", len(out))
	}
	// First two preserved as-is
	if out[0].Content != "hi" || out[0].Role != llm.RoleUser {
		t.Errorf("user message mangled: %+v", out[0])
	}
	if out[1].Content != "let me ask" || out[1].Role != llm.RoleAssistant {
		t.Errorf("assistant text mangled: %+v", out[1])
	}
	// tool_result is converted to user role
	last := out[2]
	if last.Role != llm.RoleUser {
		t.Errorf("tool_result role: want %q, got %q", llm.RoleUser, last.Role)
	}
	if last.Type != llm.TypeToolResult {
		t.Errorf("tool_result type: want %q, got %q", llm.TypeToolResult, last.Type)
	}
	if last.Content != `{"questions":[],"answers":{}}` {
		t.Errorf("tool_result content: %q", last.Content)
	}
	if last.ToolID != "call_1" || last.ToolName != "question" {
		t.Errorf("tool_result metadata lost: %+v", last)
	}
}
