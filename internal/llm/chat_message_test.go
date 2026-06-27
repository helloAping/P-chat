package llm

import (
	"encoding/json"
	"testing"
)

func TestChatMessage_RoundTrip(t *testing.T) {
	msg := ChatMessage{
		Role:     RoleUser,
		Type:     TypeImage,
		Content:  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk",
		Name:     "photo.png",
		MimeType: "image/png",
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var back ChatMessage
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Role != RoleUser {
		t.Errorf("role = %q, want user", back.Role)
	}
	if back.Type != TypeImage {
		t.Errorf("type = %q, want image", back.Type)
	}
	if back.Content != msg.Content {
		t.Errorf("content mismatch")
	}
	if back.Name != "photo.png" {
		t.Errorf("name = %q", back.Name)
	}
	if back.MimeType != "image/png" {
		t.Errorf("mime_type = %q", back.MimeType)
	}
}

func TestChatMessage_ToolCall(t *testing.T) {
	msg := ChatMessage{
		Role:      RoleAssistant,
		Type:      TypeToolCall,
		Content:   `{"path":"/etc/hosts"}`,
		ToolID:    "call_abc123",
		ToolName:  "read_file",
		ToolInput: `{"path":"/etc/hosts"}`,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var back ChatMessage
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.ToolID != "call_abc123" {
		t.Errorf("tool_id = %q", back.ToolID)
	}
	if back.ToolName != "read_file" {
		t.Errorf("tool_name = %q", back.ToolName)
	}
}

func TestChatMessage_ToolResult(t *testing.T) {
	msg := ChatMessage{
		Role:      RoleTool,
		Type:      TypeToolResult,
		Content:   "line 1\nline 2\n",
		ToolID:    "call_abc123",
		ToolName:  "read_file",
		ToolError: false,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var back ChatMessage
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.ToolError {
		t.Error("ToolError should be false")
	}
}

func TestChatMessage_ToolResultError(t *testing.T) {
	msg := ChatMessage{
		Role:      RoleTool,
		Type:      TypeToolResult,
		Content:   "permission denied",
		ToolID:    "call_err",
		ToolName:  "bash",
		ToolError: true,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var back ChatMessage
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.ToolError {
		t.Error("ToolError should be true")
	}
}

func TestChatMessage_Thinking(t *testing.T) {
	msg := ChatMessage{
		Role:    RoleAssistant,
		Type:    TypeThinking,
		Content: "The user is asking about...",
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var back ChatMessage
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Role != RoleAssistant {
		t.Errorf("role = %q", back.Role)
	}
	if back.Type != TypeThinking {
		t.Errorf("type = %q", back.Type)
	}
}

func TestChatMessage_TextDefaults(t *testing.T) {
	msg := ChatMessage{
		Role:    RoleUser,
		Content: "hello",
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var back ChatMessage
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Type != "" {
		t.Errorf("default type should be empty, got %q", back.Type)
	}
}
