package llm

import (
	"encoding/json"
	"errors"
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
		``,
	}, "\n")
	var gotTool *ToolCallDelta
	var gotItem *ResponsesOutputItem
	for chunk := range NewOpenAIResponsesAdapter("", "", "test").ParseStream(strings.NewReader(stream)) {
		if chunk.ToolCallDelta != nil {
			gotTool = chunk.ToolCallDelta
		}
		if chunk.ResponsesOutputItem != nil {
			gotItem = chunk.ResponsesOutputItem
		}
	}
	if gotTool == nil || gotTool.ID != "call_2" || gotTool.ItemID != "fc_2" || gotTool.Name != "read_file" || gotTool.ArgsJSON != `{"path":"b.go"}` {
		t.Fatalf("tool = %#v", gotTool)
	}
	if gotItem == nil || gotItem.OutputIndex != 2 || !strings.Contains(string(gotItem.Item), `"id":"fc_2"`) {
		t.Fatalf("responses item = %#v", gotItem)
	}
}

func TestOpenAIResponsesBuildReplaysNativeOutputItemsInOrder(t *testing.T) {
	items := []ResponsesOutputItem{
		{OutputIndex: 0, Item: json.RawMessage(`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"plan"}],"encrypted_content":"secret"}`)},
		{OutputIndex: 1, Item: json.RawMessage(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"a.go\"}"}`)},
		{OutputIndex: 2, Item: json.RawMessage(`{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Calling."}]}`)},
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewOpenAIResponsesAdapter("https://api.example.com/v1/responses", "", "test")
	req, err := adapter.Build([]ChatMessage{
		{Role: RoleUser, Type: TypeText, Content: "read a.go"},
		{Role: RoleAssistant, Type: TypeText, Content: "Calling.", Meta: map[string]any{ResponsesOutputItemsMetaKey: string(encoded)}},
		{Role: RoleAssistant, Type: TypeToolCall, ToolID: "call_1", ToolName: "read_file", ToolInput: `{"path":"a.go"}`},
		{Role: RoleTool, Type: TypeToolResult, ToolID: "call_1", ToolName: "read_file", Content: "package main"},
	}, "gpt-test", 0, nil, "system", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, item := range body.Input {
		types = append(types, item["type"].(string))
	}
	want := []string{"message", "message", "reasoning", "function_call", "message", "function_call_output"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("types = %v, want %v\nbody=%s", types, want, req.Body)
	}
	if body.Input[2]["encrypted_content"] != "secret" {
		t.Fatalf("reasoning item lost fields: %#v", body.Input[2])
	}
	if body.Input[3]["id"] != "fc_1" || body.Input[3]["call_id"] != "call_1" {
		t.Fatalf("function call identity changed: %#v", body.Input[3])
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

func collectResponsesChunks(t *testing.T, stream string) []StreamChunk {
	t.Helper()
	var chunks []StreamChunk
	for chunk := range NewOpenAIResponsesAdapter("", "", "test").ParseStream(strings.NewReader(stream)) {
		chunks = append(chunks, chunk)
	}
	return chunks
}

func terminalResponsesChunk(t *testing.T, chunks []StreamChunk) StreamChunk {
	t.Helper()
	if len(chunks) == 0 {
		t.Fatal("stream emitted no chunks")
	}
	return chunks[len(chunks)-1]
}

func TestOpenAIResponsesParseStreamAcceptsStandardSSEDataFraming(t *testing.T) {
	stream := strings.Join([]string{
		`data:{"type":`,
		`data: "response.output_text.delta","delta":"hello"}`,
		``,
		`data:{"type":"response.completed","response":{}}`,
		``,
		``,
	}, "\n")
	chunks := collectResponsesChunks(t, stream)
	var content string
	for _, chunk := range chunks {
		content += chunk.Content
	}
	if content != "hello" {
		t.Fatalf("content = %q, want hello", content)
	}
	if terminal := terminalResponsesChunk(t, chunks); !terminal.Done || terminal.Err != nil {
		t.Fatalf("terminal = %#v, want successful completion", terminal)
	}
}

func TestOpenAIResponsesParseStreamRejectsPrematureEOF(t *testing.T) {
	chunks := collectResponsesChunks(t, `data: {"type":"response.output_text.delta","delta":"partial"}\n\n`)
	terminal := terminalResponsesChunk(t, chunks)
	if terminal.Err == nil || terminal.Done {
		t.Fatalf("terminal = %#v, want protocol error without Done", terminal)
	}
}

func TestOpenAIResponsesParseStreamRejectsIncomplete(t *testing.T) {
	stream := "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n"
	terminal := terminalResponsesChunk(t, collectResponsesChunks(t, stream))
	if terminal.Err == nil || terminal.Done || !strings.Contains(terminal.Err.Error(), "max_output_tokens") {
		t.Fatalf("terminal = %#v, want incomplete error", terminal)
	}
}

func TestOpenAIResponsesParseStreamClassifiesOfficialError(t *testing.T) {
	stream := "data: {\"type\":\"error\",\"code\":\"server_error\",\"message\":\"upstream unavailable\",\"param\":null}\n\n"
	terminal := terminalResponsesChunk(t, collectResponsesChunks(t, stream))
	var apiErr *APIError
	if !errors.As(terminal.Err, &apiErr) {
		t.Fatalf("error = %T %v, want *APIError", terminal.Err, terminal.Err)
	}
	if apiErr.Kind != KindServer || terminal.Done {
		t.Fatalf("terminal = %#v apiErr=%#v", terminal, apiErr)
	}
}

func TestOpenAIResponsesParseStreamClassifiesFailedResponse(t *testing.T) {
	stream := "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"rate_limit_error\",\"message\":\"slow down\"}}}\n\n"
	terminal := terminalResponsesChunk(t, collectResponsesChunks(t, stream))
	var apiErr *APIError
	if !errors.As(terminal.Err, &apiErr) || apiErr.Kind != KindRateLimit {
		t.Fatalf("error = %T %#v, want rate-limit APIError", terminal.Err, terminal.Err)
	}
}

func TestOpenAIResponsesParseStreamClassifiesProxyErrorEnvelope(t *testing.T) {
	stream := "data: {\"error\":{\"type\":\"server_error\",\"code\":\"route_not_found\",\"message\":\"no available route\"}}\n\n"
	terminal := terminalResponsesChunk(t, collectResponsesChunks(t, stream))
	var apiErr *APIError
	if !errors.As(terminal.Err, &apiErr) || apiErr.Kind != KindServer {
		t.Fatalf("error = %T %#v, want server APIError", terminal.Err, terminal.Err)
	}
}

func TestOpenAIResponsesParseStreamStopsRetryForNestedBadRequest(t *testing.T) {
	stream := `data: {"error":{"message":"Provider request failed: status=400, body={\"model\":\"gpt-test\"}","type":"server_error","code":"PROVIDER_BAD_RESPONSE"}}` + "\n\n"
	terminal := terminalResponsesChunk(t, collectResponsesChunks(t, stream))
	var apiErr *APIError
	if !errors.As(terminal.Err, &apiErr) || apiErr.Kind != KindBadRequest || apiErr.StatusCode != 400 || terminal.Done {
		t.Fatalf("terminal = %#v, want non-retryable upstream 400", terminal)
	}
}

func TestOpenAIResponsesParseStreamRejectsMalformedEvent(t *testing.T) {
	stream := strings.Join([]string{
		`data: {not-json}`,
		``,
		`data: {"type":"response.completed","response":{}}`,
		``,
		``,
	}, "\n")
	terminal := terminalResponsesChunk(t, collectResponsesChunks(t, stream))
	if terminal.Err == nil || terminal.Done {
		t.Fatalf("terminal = %#v, want parse error", terminal)
	}
}

func TestOpenAIResponsesParseStreamRejectsMalformedFunctionCallItem(t *testing.T) {
	stream := "data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"arguments\":\"{}\"}}\n\n"
	terminal := terminalResponsesChunk(t, collectResponsesChunks(t, stream))
	if terminal.Err == nil || terminal.Done || !strings.Contains(terminal.Err.Error(), "missing call_id or name") {
		t.Fatalf("terminal = %#v, want malformed function-call error", terminal)
	}
}

func TestOpenAIResponsesParseStreamDoesNotDuplicateReasoningDoneFallback(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"delta":"think"}`,
		``,
		`data: {"type":"response.reasoning_text.done","item_id":"rs_1","output_index":0,"text":"think"}`,
		``,
		`data: {"type":"response.completed","response":{}}`,
		``,
		``,
	}, "\n")
	var thinking string
	for _, chunk := range collectResponsesChunks(t, stream) {
		thinking += chunk.Thinking
	}
	if thinking != "think" {
		t.Fatalf("thinking = %q, want think", thinking)
	}
}

func TestOpenAIResponsesParseStreamUsesDoneFallbacksWhenDeltasAreAbsent(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_text.done","item_id":"msg_1","output_index":0,"content_index":0,"text":"answer"}`,
		``,
		`data: {"type":"response.refusal.done","item_id":"msg_1","output_index":0,"content_index":1,"refusal":"refused"}`,
		``,
		`data: {"type":"response.completed","response":{}}`,
		``,
		``,
	}, "\n")
	var content string
	for _, chunk := range collectResponsesChunks(t, stream) {
		content += chunk.Content
	}
	if content != "answerrefused" {
		t.Fatalf("content = %q", content)
	}
}

func TestOpenAIResponsesParseStreamRejectsStandaloneDoneSentinel(t *testing.T) {
	terminal := terminalResponsesChunk(t, collectResponsesChunks(t, "data: [DONE]\n\n"))
	if terminal.Err == nil || terminal.Done {
		t.Fatalf("terminal = %#v, want protocol error", terminal)
	}
}

func TestParseResponsesNonStreamContentRejectsIncomplete(t *testing.T) {
	_, err := parseResponsesNonStreamContent([]byte(`{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output_text":"partial"}`))
	if err == nil || !strings.Contains(err.Error(), "max_output_tokens") {
		t.Fatalf("error = %v, want incomplete reason", err)
	}
}

func TestParseResponsesNonStreamContentReturnsRefusal(t *testing.T) {
	text, err := parseResponsesNonStreamContent([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"I cannot help with that."}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if text != "I cannot help with that." {
		t.Fatalf("text = %q", text)
	}
}

func TestParseResponsesNonStreamContentClassifiesError(t *testing.T) {
	_, err := parseResponsesNonStreamContent([]byte(`{"status":"failed","error":{"code":"server_error","message":"unavailable"}}`))
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != KindServer {
		t.Fatalf("error = %T %#v, want server APIError", err, err)
	}
}

func TestInjectReasoningRemovesUnsupportedResponsesSamplingParameters(t *testing.T) {
	body := injectReasoning([]byte(`{"temperature":0.2,"top_p":0.9,"top_logprobs":3}`), "openai_responses", "high")
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"temperature", "top_p", "top_logprobs"} {
		if _, ok := got[key]; ok {
			t.Fatalf("%s was not removed: %s", key, body)
		}
	}
	if got["reasoning"].(map[string]any)["effort"] != "high" {
		t.Fatalf("reasoning = %#v", got["reasoning"])
	}
}
