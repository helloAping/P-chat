package llm

import "testing"

func TestEstimatePromptTokensIncludesToolSchema(t *testing.T) {
	msgs := []ChatMessage{
		{Role: RoleUser, Type: TypeText, Content: "hello"},
	}
	tools := []ToolDef{
		{
			Name:        "exec_command",
			Description: "Run a command",
			Parameters:  []byte(`{"type":"object","properties":{"command":{"type":"string"}}}`),
		},
	}

	messageOnly := EstimateTokensMessages(msgs)
	prompt := EstimatePromptTokens(msgs, tools)
	if prompt <= messageOnly {
		t.Fatalf("prompt estimate = %d, message-only estimate = %d; tool schema was not counted", prompt, messageOnly)
	}
}

func TestEstimateTokensMessagesIncludesToolFields(t *testing.T) {
	plain := EstimateTokensMessages([]ChatMessage{
		{Role: RoleTool, Type: TypeToolResult, Content: "ok"},
	})
	withMetadata := EstimateTokensMessages([]ChatMessage{
		{
			Role:      RoleTool,
			Type:      TypeToolResult,
			Content:   "ok",
			ToolID:    "call_123",
			ToolName:  "exec_command",
			ToolInput: `{"command":"dir"}`,
			Name:      "result.txt",
			MimeType:  "text/plain",
		},
	})
	if withMetadata <= plain {
		t.Fatalf("metadata estimate = %d, plain estimate = %d; tool metadata was not counted", withMetadata, plain)
	}
}

func TestUsableContextUsesConservativeUnknownModelFallback(t *testing.T) {
	if DefaultContextWindow != 64_000 {
		t.Fatalf("DefaultContextWindow = %d, want 64000", DefaultContextWindow)
	}
	if UsableContext(0) >= 64_000 {
		t.Fatalf("unknown-model usable context should reserve output and buffer, got %d", UsableContext(0))
	}
}
