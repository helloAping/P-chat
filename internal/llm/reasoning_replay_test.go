package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDeepSeekReasoningReplay(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			var adapter ProtocolAdapter = NewOpenAIAdapter("https://example.test", "", "test")
			if protocol == "anthropic" {
				adapter = NewAnthropicAdapter("https://example.test", "", "test")
			}
			messages := []ChatMessage{
				{Role: RoleSystem, Type: TypeText, Content: "static"},
				{Role: RoleUser, Type: TypeText, Content: "inspect"},
				{Role: RoleAssistant, Type: TypeText, Meta: map[string]any{"thinking": "original reasoning"}},
				{Role: RoleAssistant, Type: TypeToolCall, ToolID: "call1", ToolName: "read_file", ToolInput: `{}`},
				{Role: RoleTool, Type: TypeToolResult, ToolID: "call1", Content: "result"},
				{Role: RoleSystem, Type: TypeText, Name: RuntimeContextPrefix + "todo", Content: "state changed"},
			}
			tools := []ToolDef{{Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`)}}
			req, err := adapter.Build(messages, "deepseek-flash", 1000, tools, "", 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(req.Body), "original reasoning") {
				t.Fatalf("reasoning was lost: %s", req.Body)
			}
			var wire struct {
				System   string            `json:"system"`
				Messages []json.RawMessage `json:"messages"`
			}
			if err := json.Unmarshal(req.Body, &wire); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(wire.System, "state changed") || !strings.Contains(string(wire.Messages[len(wire.Messages)-1]), "state changed") {
				t.Fatal("runtime state was hoisted")
			}
			req, err = adapter.Build(messages, "other-model", 1000, tools, "", 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(req.Body), "original reasoning") {
				t.Fatal("DeepSeek fields leaked into another model")
			}
		})
	}
}
