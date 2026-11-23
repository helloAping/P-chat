package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponsesRequestShapeSummaryPairsParallelCallsWithoutContent(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-luna","input":[{"type":"message","role":"user","content":"private PDF text sk-secret"},{"type":"reasoning","id":"rs_1","encrypted_content":"private-reasoning"},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_attachment","arguments":"secret arguments"},{"type":"function_call","id":"fc_2","call_id":"call_2","name":"read_attachment","arguments":"secret arguments"},{"type":"function_call","id":"fc_3","call_id":"call_3","name":"read_attachment","arguments":"secret arguments"},{"type":"function_call_output","call_id":"call_1","output":"private result"},{"type":"function_call_output","call_id":"call_2","output":"private result"},{"type":"function_call_output","call_id":"call_3","output":"private result"}]}`)
	summary, ok := responsesRequestShapeSummary(body)
	if !ok {
		t.Fatal("expected a request shape summary")
	}
	for _, want := range []string{"items=8", "reasoning=1", "calls=3", "native_calls=3", "outputs=3", "paired_outputs=3", "orphan_outputs=0"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary %q missing %q", summary, want)
		}
	}
	for _, forbidden := range []string{"private", "secret", "gpt-5.6-luna", "read_attachment", "call_1", "fc_1"} {
		if strings.Contains(summary, forbidden) {
			t.Errorf("summary leaked %q: %q", forbidden, summary)
		}
	}
}

func TestResponsesRequestShapeSummaryRejectsMalformedBody(t *testing.T) {
	if summary, ok := responsesRequestShapeSummary([]byte(`{"input":`)); ok || summary != "" {
		t.Fatalf("malformed request shape summary = %q, %t", summary, ok)
	}
}

func TestResponsesThreeParallelToolCallsReplayNativeItems(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","encrypted_content":"opaque"}}`,
		``,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_attachment","arguments":"{}"}}`,
		``,
		`data: {"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_2","call_id":"call_2","name":"read_attachment","arguments":"{}"}}`,
		``,
		`data: {"type":"response.output_item.done","output_index":3,"item":{"type":"function_call","id":"fc_3","call_id":"call_3","name":"read_attachment","arguments":"{}"}}`,
		``,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":100,"output_tokens":10}}}`,
		``,
		``,
	}, "\n")
	var items []ResponsesOutputItem
	var calls []ChatMessage
	for _, chunk := range collectResponsesChunks(t, stream) {
		if chunk.ResponsesOutputItem != nil {
			items = append(items, *chunk.ResponsesOutputItem)
		}
		if call := chunk.ToolCallDelta; call != nil {
			calls = append(calls, ChatMessage{Role: RoleAssistant, Type: TypeToolCall, ToolID: call.ID, ToolName: call.Name, ToolInput: call.ArgsJSON})
		}
	}
	if len(items) != 4 || len(calls) != 3 {
		t.Fatalf("parsed items=%d calls=%d", len(items), len(calls))
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	messages := []ChatMessage{
		{Role: RoleUser, Type: TypeText, Content: "read three PDFs"},
		{Role: RoleAssistant, Type: TypeText, Meta: map[string]any{ResponsesOutputItemsMetaKey: string(encoded)}},
	}
	messages = append(messages, calls...)
	for _, call := range calls {
		messages = append(messages, ChatMessage{Role: RoleTool, Type: TypeToolResult, ToolID: call.ToolID, ToolName: call.ToolName, Content: "document text"})
	}
	adapter := NewOpenAIResponsesAdapter("https://api.example.com/v1/responses", "", "test")
	req, err := adapter.Build(messages, "gpt-test", 2048, nil, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	summary, ok := responsesRequestShapeSummary(req.Body)
	if !ok || !strings.Contains(summary, "native_calls=3") || !strings.Contains(summary, "paired_outputs=3") || !strings.Contains(summary, "orphan_outputs=0") {
		t.Fatalf("bad continuation shape: %q", summary)
	}
}
