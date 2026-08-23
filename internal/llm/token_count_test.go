package llm

import (
	"strings"
	"testing"
)

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

func TestEstimateTokensMessagesBoundsImageBase64(t *testing.T) {
	hugeBase64 := strings.Repeat("A", 740_000)
	if rawText := EstimateTokens(hugeBase64); rawText < 180_000 {
		t.Fatalf("test setup: raw base64 estimate = %d, want screenshot-sized text estimate", rawText)
	}

	textEstimate := EstimateTokensMessages([]ChatMessage{
		{Role: RoleUser, Type: TypeText, Content: hugeBase64},
	})
	imageEstimate := EstimateTokensMessages([]ChatMessage{
		{
			Role:     RoleUser,
			Type:     TypeImage,
			Content:  hugeBase64,
			Name:     "screen.png",
			MimeType: "image/png",
		},
	})

	if imageEstimate >= 5_000 {
		t.Fatalf("image estimate = %d, want bounded multimodal estimate, not raw base64 text", imageEstimate)
	}
	if textEstimate <= imageEstimate*20 {
		t.Fatalf("text estimate = %d, image estimate = %d; image base64 was not meaningfully bounded", textEstimate, imageEstimate)
	}
}

func TestEstimateTokensBytesMatchesStringVariant(t *testing.T) {
	cases := []string{
		"",
		"hello",
		"用户发布菜谱有两种选项",
		`{"type":"object","properties":{"command":{"type":"string"},"dry_run":{"type":"boolean"}}}`,
		"mixed ASCII + 中文 + 123456",
	}
	for _, c := range cases {
		want := EstimateTokens(c)
		got := EstimateTokensBytes([]byte(c))
		if got != want {
			t.Errorf("EstimateTokensBytes(%q) = %d, EstimateTokens = %d", c, got, want)
		}
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
