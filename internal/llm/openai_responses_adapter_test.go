package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenAIResponsesBuildConvertsChatShape(t *testing.T) {
	adapter := NewOpenAIResponsesAdapter("https://api.example.com/v1/responses", "sk-test", "test")
	req, err := adapter.Build([]ChatMessage{
		{Role: RoleSystem, Type: TypeText, Content: "be concise"},
		{Role: RoleUser, Type: TypeText, Content: "read this"},
		{Role: RoleAssistant, Type: TypeText, Content: "Calling."},
		{Role: RoleAssistant, Type: TypeToolCall, ToolID: "call_1", ToolName: "read_file", ToolInput: `{"path":"a.go"}`},
		{Role: RoleTool, Type: TypeToolResult, ToolID: "call_1", ToolName: "read_file", Content: "package main"},
	}, "gpt-test", 2048, []ToolDef{{
		Name:        "read_file",
		Description: "Read one file",
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}}, "", 0.2, 0.9)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if req.URL != "https://api.example.com/v1/responses" {
		t.Fatalf("URL = %q", req.URL)
	}
	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("body decode: %v\n%s", err, req.Body)
	}
	if body["model"] != "gpt-test" || body["stream"] != true {
		t.Fatalf("basic body = %#v", body)
	}
	if body["max_output_tokens"].(float64) != 2048 {
		t.Fatalf("max_output_tokens = %#v", body["max_output_tokens"])
	}
	input := body["input"].([]any)
	if len(input) != 5 {
		t.Fatalf("input len = %d body=%s", len(input), req.Body)
	}
	if input[3].(map[string]any)["type"] != "function_call" {
		t.Fatalf("input[3] = %#v", input[3])
	}
	if input[4].(map[string]any)["type"] != "function_call_output" {
		t.Fatalf("input[4] = %#v", input[4])
	}
	tools := body["tools"].([]any)
	if tools[0].(map[string]any)["name"] != "read_file" {
		t.Fatalf("tools = %#v", tools)
	}
}

func TestOpenAIResponsesParseStream(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"hi "}`,
		``,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"thinking"}`,
		``,
		`data: {"type":"response.reasoning_text.done","text":" done"}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_1","delta":"{\"path\""}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_1","delta":":\"a.go\"}"}`,
		``,
		`data: {"type":"response.function_call_arguments.done","output_index":1,"item_id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"a.go\"}"}`,
		``,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":3,"input_tokens_details":{"cached_tokens":4}}}}`,
		``,
	}, "\n")
	var gotContent, gotThinking string
	var gotTool *ToolCallDelta
	var gotUsage StreamChunk
	var done bool
	for chunk := range NewOpenAIResponsesAdapter("", "", "test").ParseStream(strings.NewReader(stream)) {
		gotContent += chunk.Content
		gotThinking += chunk.Thinking
		if chunk.ToolCallDelta != nil {
			gotTool = chunk.ToolCallDelta
		}
		if chunk.TokensIn > 0 {
			gotUsage = chunk
		}
		if chunk.Done {
			done = true
		}
	}
	if gotContent != "hi " || gotThinking != "thinking done" {
		t.Fatalf("content=%q thinking=%q", gotContent, gotThinking)
	}
	if gotTool == nil || gotTool.ID != "call_1" || gotTool.Name != "read_file" || gotTool.ArgsJSON != `{"path":"a.go"}` {
		t.Fatalf("tool = %#v", gotTool)
	}
	if gotUsage.TokensIn != 10 || gotUsage.TokensOut != 3 || gotUsage.CacheUsage == nil || gotUsage.CacheUsage.HitTokens != 4 || gotUsage.CacheUsage.MissTokens != 6 {
		t.Fatalf("usage = %#v", gotUsage)
	}
	if !done {
		t.Fatal("stream did not emit Done")
	}
}

func TestOpenAIResponsesParseStreamWaitsForCallID(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.function_call_arguments.delta","output_index":2,"item_id":"fc_2","delta":"{\"path\":\"b.go\"}"}`,
		``,
		`data: {"type":"response.function_call_arguments.done","output_index":2,"item_id":"fc_2","name":"read_file","arguments":"{\"path\":\"b.go\"}"}`,
		``,
		`data: {"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_2","call_id":"call_2","name":"read_file","arguments":"{\"path\":\"b.go\"}"}}`,
		``,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`,
		``,
	}, "\n")
	var gotTool *ToolCallDelta
	for chunk := range NewOpenAIResponsesAdapter("", "", "test").ParseStream(strings.NewReader(stream)) {
		if chunk.ToolCallDelta != nil {
			gotTool = chunk.ToolCallDelta
		}
	}
	if gotTool == nil || gotTool.ID != "call_2" || gotTool.Name != "read_file" || gotTool.ArgsJSON != `{"path":"b.go"}` {
		t.Fatalf("tool = %#v", gotTool)
	}
}

func TestParseResponsesNonStreamContent(t *testing.T) {
	text, err := parseResponsesNonStreamContent([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if text != "hello" {
		t.Fatalf("text = %q", text)
	}
}
