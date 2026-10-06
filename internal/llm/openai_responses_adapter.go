package llm

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// OpenAIResponsesAdapter 实现 OpenAI Responses API 协议。
// OpenAIResponsesAdapter implements the OpenAI Responses API protocol.
type OpenAIResponsesAdapter struct {
	baseURL string
	apiKey  string
	name    string
}

const maxResponsesSSEEventBytes = 4 << 20

// NewOpenAIResponsesAdapter 创建指向完整 /responses URL 的协议适配器。
// NewOpenAIResponsesAdapter creates an adapter for a complete /responses URL.
func NewOpenAIResponsesAdapter(baseURL, apiKey, providerName string) *OpenAIResponsesAdapter {
	return &OpenAIResponsesAdapter{baseURL: baseURL, apiKey: apiKey, name: providerName}
}

// Build 将 ChatMessage 和工具定义转换为 OpenAI Responses 请求。
// Build converts ChatMessage + tools into an OpenAI Responses request.
func (a *OpenAIResponsesAdapter) Build(messages []ChatMessage, model string, maxTokens int, tools []ToolDef, system string, temperature float32, topP float32) (*ProtocolRequest, error) {
	chatReq, err := NewOpenAIAdapter(a.baseURL, a.apiKey, a.name).Build(messages, model, maxTokens, tools, system, temperature, topP)
	if err != nil {
		return nil, err
	}
	var chatBody map[string]any
	if err := json.Unmarshal(chatReq.Body, &chatBody); err != nil {
		return nil, fmt.Errorf("decode intermediate openai chat request: %w", err)
	}
	input, err := responsesInputFromMessagesWithReplay(messages, system, model, a)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"model":  model,
		"input":  input,
		"stream": true,
	}
	if v, ok := chatBody["temperature"]; ok {
		body["temperature"] = v
	}
	if v, ok := chatBody["top_p"]; ok {
		body["top_p"] = v
	}
	if v, ok := chatBody["max_tokens"]; ok {
		body["max_output_tokens"] = v
	}
	if rawTools, ok := chatBody["tools"]; ok {
		respTools, err := responsesToolsFromChatTools(rawTools)
		if err != nil {
			return nil, err
		}
		if len(respTools) > 0 {
			body["tools"] = respTools
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal openai responses request: %w", err)
	}
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
		"Connection":    "keep-alive",
	}
	if a.apiKey != "" {
		headers["Authorization"] = "Bearer " + a.apiKey
	}
	return &ProtocolRequest{
		Method:  http.MethodPost,
		URL:     strings.TrimSpace(a.baseURL),
		Body:    encoded,
		Headers: headers,
	}, nil
}

func responsesInputFromMessagesWithReplay(messages []ChatMessage, system, model string, adapter *OpenAIResponsesAdapter) ([]any, error) {
	hasReplay := false
	for _, msg := range messages {
		if len(responsesOutputItemsFromMessage(msg)) > 0 {
			hasReplay = true
			break
		}
	}
	if !hasReplay {
		chatReq, err := NewOpenAIAdapter(adapter.baseURL, adapter.apiKey, adapter.name).Build(messages, model, 0, nil, system, 0, 0)
		if err != nil {
			return nil, err
		}
		var body map[string]any
		if err := json.Unmarshal(chatReq.Body, &body); err != nil {
			return nil, fmt.Errorf("decode intermediate openai chat messages: %w", err)
		}
		return responsesInputFromChatMessages(body["messages"])
	}

	input := make([]any, 0, len(messages)+1)
	ordinary := make([]ChatMessage, 0, len(messages))
	pendingSystem := system
	skipCalls := make(map[string]int)
	flushOrdinary := func() error {
		if len(ordinary) == 0 && pendingSystem == "" {
			return nil
		}
		chatReq, err := NewOpenAIAdapter(adapter.baseURL, adapter.apiKey, adapter.name).Build(ordinary, model, 0, nil, pendingSystem, 0, 0)
		if err != nil {
			return err
		}
		pendingSystem = ""
		ordinary = ordinary[:0]
		var body map[string]any
		if err := json.Unmarshal(chatReq.Body, &body); err != nil {
			return fmt.Errorf("decode intermediate openai chat messages: %w", err)
		}
		converted, err := responsesInputFromChatMessages(body["messages"])
		if err != nil {
			return err
		}
		input = append(input, converted...)
		return nil
	}

	for _, msg := range messages {
		items := responsesOutputItemsFromMessage(msg)
		if len(items) > 0 {
			if err := flushOrdinary(); err != nil {
				return nil, err
			}
			for _, stored := range items {
				var item map[string]any
				if err := json.Unmarshal(stored.Item, &item); err != nil {
					return nil, fmt.Errorf("decode stored responses output item: %w", err)
				}
				input = append(input, item)
				if item["type"] == "function_call" {
					if callID, _ := item["call_id"].(string); callID != "" {
						skipCalls[callID]++
					}
				}
			}
			continue
		}
		if msg.Type == TypeToolCall && skipCalls[msg.ToolID] > 0 {
			skipCalls[msg.ToolID]--
			continue
		}
		ordinary = append(ordinary, msg)
	}
	if err := flushOrdinary(); err != nil {
		return nil, err
	}
	return input, nil
}

func responsesOutputItemsFromMessage(msg ChatMessage) []ResponsesOutputItem {
	if msg.Meta == nil {
		return nil
	}
	raw, ok := msg.Meta[ResponsesOutputItemsMetaKey]
	if !ok {
		return nil
	}
	var encoded []byte
	switch value := raw.(type) {
	case string:
		encoded = []byte(value)
	case []byte:
		encoded = value
	case json.RawMessage:
		encoded = value
	default:
		return nil
	}
	var items []ResponsesOutputItem
	if err := json.Unmarshal(encoded, &items); err != nil {
		return nil
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].OutputIndex < items[j].OutputIndex })
	return items
}

func responsesInputFromChatMessages(raw any) ([]any, error) {
	rawMessages, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("openai responses: messages missing from intermediate request")
	}
	input := make([]any, 0, len(rawMessages))
	for _, value := range rawMessages {
		msg, ok := value.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role == "tool" {
			callID, _ := msg["tool_call_id"].(string)
			content := textFromNonStreamContent(msg["content"])
			if callID != "" {
				input = append(input, map[string]any{
					"type":    "function_call_output",
					"call_id": callID,
					"output":  content,
				})
			}
			continue
		}
		content, hasContent := msg["content"]
		if hasContent {
			converted := responsesContentFromChatContent(content)
			if text, ok := converted.(string); ok && strings.TrimSpace(text) == "" {
				converted = nil
			}
			if converted != nil {
				input = append(input, map[string]any{
					"type":    "message",
					"role":    responsesInputRole(role),
					"content": converted,
				})
			}
		}
		if rawCalls, ok := msg["tool_calls"].([]any); ok {
			for _, rawCall := range rawCalls {
				call, ok := rawCall.(map[string]any)
				if !ok {
					continue
				}
				function, _ := call["function"].(map[string]any)
				name, _ := function["name"].(string)
				arguments, _ := function["arguments"].(string)
				callID, _ := call["id"].(string)
				if name == "" || callID == "" {
					continue
				}
				input = append(input, map[string]any{
					"type":      "function_call",
					"id":        callID,
					"call_id":   callID,
					"name":      name,
					"arguments": arguments,
				})
			}
		}
	}
	return input, nil
}

func responsesInputRole(role string) string {
	switch role {
	case "system", "developer", "assistant", "user":
		return role
	default:
		return "user"
	}
}

func responsesContentFromChatContent(content any) any {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		parts := make([]any, 0, len(v))
		for _, rawPart := range v {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			switch part["type"] {
			case "text":
				if text, _ := part["text"].(string); text != "" {
					parts = append(parts, map[string]any{"type": "input_text", "text": text})
				}
			case "image_url":
				imageURL, _ := part["image_url"].(map[string]any)
				if url, _ := imageURL["url"].(string); url != "" {
					parts = append(parts, map[string]any{"type": "input_image", "image_url": url})
				}
			case "file":
				file, _ := part["file"].(map[string]any)
				inputFile := map[string]any{"type": "input_file"}
				if filename, _ := file["filename"].(string); filename != "" {
					inputFile["filename"] = filename
				}
				if data, _ := file["file_data"].(string); data != "" {
					inputFile["file_data"] = data
				}
				parts = append(parts, inputFile)
			case "input_audio":
				parts = append(parts, part)
			default:
				parts = append(parts, part)
			}
		}
		if len(parts) == 0 {
			return nil
		}
		return parts
	default:
		return nil
	}
}

func responsesToolsFromChatTools(raw any) ([]any, error) {
	rawTools, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("openai responses: tools must be an array")
	}
	tools := make([]any, 0, len(rawTools))
	for _, value := range rawTools {
		tool, ok := value.(map[string]any)
		if !ok || tool["type"] != "function" {
			continue
		}
		function, _ := tool["function"].(map[string]any)
		name, _ := function["name"].(string)
		if name == "" {
			continue
		}
		out := map[string]any{"type": "function", "name": name}
		if description, _ := function["description"].(string); description != "" {
			out["description"] = description
		}
		if parameters, ok := function["parameters"]; ok {
			out["parameters"] = parameters
		}
		tools = append(tools, out)
	}
	return tools, nil
}

// ParseStream 读取 OpenAI Responses SSE 事件并输出统一的 StreamChunk。
// ParseStream reads OpenAI Responses SSE events and emits StreamChunk values.
func (a *OpenAIResponsesAdapter) ParseStream(r io.Reader) <-chan StreamChunk {
	ch := make(chan StreamChunk, 64)
	go func() {
		defer close(ch)
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 64<<10), maxResponsesSSEEventBytes)
		argDeltas := map[int]string{}
		emittedToolCalls := map[int]bool{}
		deltaSeen := map[string]bool{}
		rawEvents := 0
		parseFailures := 0
		var dataLines []string
		dataBytes := 0
		handlePayload := func(payload []byte) bool {
			if strings.TrimSpace(string(payload)) == "[DONE]" {
				ch <- StreamChunk{Err: fmt.Errorf("openai responses stream ended with [DONE] before response.completed")}
				return true
			}
			rawEvents++
			if rawEvents <= 3 {
				// 调试模式只记录有界、脱敏预览。Debug mode logs bounded, redacted previews only.
				logLLMDebugPayload(fmt.Sprintf("[llm/%s/responses]", a.name), rawEvents, payload)
			}
			if responseErr := responsesErrorFromEnvelope(payload); responseErr != nil {
				logProxySSE(fmt.Sprintf("[llm/%s/responses]", a.name), rawEvents, payload)
				ch <- StreamChunk{Err: newResponsesAPIError(a.name, responseErr.Type, responseErr.Code, responseErr.Message)}
				return true
			}
			if proxyErr := extractProxyError(payload); proxyErr != "" {
				logProxySSE(fmt.Sprintf("[llm/%s/responses]", a.name), rawEvents, payload)
				ch <- StreamChunk{Err: fmt.Errorf("openai responses proxy error: %s", proxyErr)}
				return true
			}
			var ev responsesStreamEvent
			if err := json.Unmarshal(payload, &ev); err != nil {
				parseFailures++
				logMalformedSSE(fmt.Sprintf("[llm/%s/responses]", a.name), rawEvents, parseFailures, payload, err)
				ch <- StreamChunk{Err: fmt.Errorf("decode openai responses SSE event: %w", err)}
				return true
			}
			itemKey := fmt.Sprintf("%s:%d:%s:%d:%d", ev.Type, ev.OutputIndex, ev.ItemID, ev.ContentIndex, ev.SummaryIndex)
			switch ev.Type {
			case "response.output_text.delta", "response.refusal.delta":
				if ev.Delta != "" {
					deltaSeen[itemKey] = true
					ch <- StreamChunk{Content: ev.Delta}
				}
			case "response.output_text.done", "response.refusal.done":
				deltaType := strings.TrimSuffix(ev.Type, ".done") + ".delta"
				deltaKey := fmt.Sprintf("%s:%d:%s:%d:%d", deltaType, ev.OutputIndex, ev.ItemID, ev.ContentIndex, ev.SummaryIndex)
				fallback := ev.Text
				if fallback == "" {
					fallback = ev.Refusal
				}
				if fallback != "" && !deltaSeen[deltaKey] {
					ch <- StreamChunk{Content: fallback}
				}
			case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
				if ev.Delta != "" {
					deltaSeen[itemKey] = true
					ch <- StreamChunk{Thinking: ev.Delta}
				}
			case "response.reasoning_summary_part.done":
				deltaKey := fmt.Sprintf("%s:%d:%s:%d:%d", "response.reasoning_summary_text.delta", ev.OutputIndex, ev.ItemID, ev.ContentIndex, ev.SummaryIndex)
				if ev.Part != nil && ev.Part.Text != "" && !deltaSeen[deltaKey] {
					ch <- StreamChunk{Thinking: ev.Part.Text}
				}
			case "response.reasoning_summary_text.done", "response.reasoning_text.done":
				deltaType := strings.TrimSuffix(ev.Type, ".done") + ".delta"
				deltaKey := fmt.Sprintf("%s:%d:%s:%d:%d", deltaType, ev.OutputIndex, ev.ItemID, ev.ContentIndex, ev.SummaryIndex)
				if ev.Text != "" && !deltaSeen[deltaKey] {
					ch <- StreamChunk{Thinking: ev.Text}
				}
			case "response.function_call_arguments.delta":
				if len(argDeltas[ev.OutputIndex])+len(ev.Delta) > maxResponsesSSEEventBytes {
					ch <- StreamChunk{Err: fmt.Errorf("openai responses function arguments exceed %d bytes", maxResponsesSSEEventBytes)}
					return true
				}
				argDeltas[ev.OutputIndex] += ev.Delta
			case "response.function_call_arguments.done":
				if emittedToolCalls[ev.OutputIndex] {
					return false
				}
				if ev.CallID == "" || ev.Name == "" {
					return false
				}
				args := ev.Arguments
				if args == "" {
					args = argDeltas[ev.OutputIndex]
				}
				ch <- StreamChunk{ToolCallDelta: &ToolCallDelta{
					Index:    ev.OutputIndex,
					ID:       ev.CallID,
					ItemID:   ev.ItemID,
					Name:     ev.Name,
					ArgsJSON: args,
				}}
				emittedToolCalls[ev.OutputIndex] = true
			case "response.output_item.done":
				if len(ev.Item) == 0 {
					return false
				}
				itemCopy := append(json.RawMessage(nil), ev.Item...)
				ch <- StreamChunk{ResponsesOutputItem: &ResponsesOutputItem{OutputIndex: ev.OutputIndex, Item: itemCopy}}
				var item responsesOutputItem
				if err := json.Unmarshal(ev.Item, &item); err != nil {
					ch <- StreamChunk{Err: fmt.Errorf("decode openai responses output item: %w", err)}
					return true
				}
				if item.Type == "function_call" {
					if emittedToolCalls[ev.OutputIndex] {
						return false
					}
					if item.CallID == "" || item.Name == "" {
						ch <- StreamChunk{Err: fmt.Errorf("openai responses function_call item missing call_id or name")}
						return true
					}
					args := item.Arguments
					if args == "" {
						args = argDeltas[ev.OutputIndex]
					}
					ch <- StreamChunk{ToolCallDelta: &ToolCallDelta{
						Index:    ev.OutputIndex,
						ID:       item.CallID,
						ItemID:   item.ID,
						Name:     item.Name,
						ArgsJSON: args,
					}}
					emittedToolCalls[ev.OutputIndex] = true
				}
			case "response.completed":
				if ev.Response != nil && ev.Response.Usage != nil {
					ch <- ev.Response.Usage.chunk()
				}
				ch <- StreamChunk{Done: true}
				return true
			case "response.incomplete":
				reason := "unknown"
				if ev.Response != nil && ev.Response.IncompleteDetails != nil && ev.Response.IncompleteDetails.Reason != "" {
					reason = ev.Response.IncompleteDetails.Reason
				}
				ch <- StreamChunk{Err: fmt.Errorf("openai responses incomplete: %s", sanitizeProviderDetail(reason, 256))}
				return true
			case "response.failed":
				if ev.Response != nil && ev.Response.Error != nil {
					ch <- StreamChunk{Err: newResponsesAPIError(a.name, ev.Response.Error.Type, ev.Response.Error.Code, ev.Response.Error.Message)}
					return true
				}
				ch <- StreamChunk{Err: fmt.Errorf("openai responses failed without error details")}
				return true
			case "error":
				ch <- StreamChunk{Err: newResponsesAPIError(a.name, ev.ErrorType, ev.Code, ev.Message)}
				return true
			}
			return false
		}

		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if len(dataLines) > 0 {
					payload := []byte(strings.Join(dataLines, "\n"))
					dataLines = dataLines[:0]
					dataBytes = 0
					if handlePayload(payload) {
						return
					}
				}
				continue
			}
			if strings.HasPrefix(line, ":") {
				continue
			}
			field, value, found := strings.Cut(line, ":")
			if !found || field != "data" {
				continue
			}
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
			dataBytes += len(value) + 1
			if dataBytes > maxResponsesSSEEventBytes {
				ch <- StreamChunk{Err: fmt.Errorf("openai responses SSE event exceeds %d bytes", maxResponsesSSEEventBytes)}
				return
			}
			dataLines = append(dataLines, value)
		}
		if err := scanner.Err(); err != nil {
			ch <- StreamChunk{Err: fmt.Errorf("read openai responses SSE stream: %w", err)}
			return
		}
		// SSE 标准会丢弃未以空行结束的 pending event；对 Responses 来说这仍是未完成流，不能报成功。
		// SSE discards an event without a terminating blank line; for Responses this is incomplete and must not report success.
		ch <- StreamChunk{Err: errors.New("openai responses stream ended before a terminal response event")}
	}()
	return ch
}

type responsesStreamEvent struct {
	Type         string             `json:"type"`
	Delta        string             `json:"delta,omitempty"`
	Text         string             `json:"text,omitempty"`
	ItemID       string             `json:"item_id,omitempty"`
	CallID       string             `json:"call_id,omitempty"`
	Name         string             `json:"name,omitempty"`
	Arguments    string             `json:"arguments,omitempty"`
	OutputIndex  int                `json:"output_index,omitempty"`
	ContentIndex int                `json:"content_index,omitempty"`
	SummaryIndex int                `json:"summary_index,omitempty"`
	Refusal      string             `json:"refusal,omitempty"`
	Code         string             `json:"code,omitempty"`
	Message      string             `json:"message,omitempty"`
	ErrorType    string             `json:"error_type,omitempty"`
	Part         *responsesPart     `json:"part,omitempty"`
	Item         json.RawMessage    `json:"item,omitempty"`
	Response     *responsesEnvelope `json:"response,omitempty"`
}

type responsesPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type responsesOutputItem struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type responsesEnvelope struct {
	Status            string          `json:"status,omitempty"`
	Usage             *responsesUsage `json:"usage,omitempty"`
	Error             *responsesError `json:"error,omitempty"`
	IncompleteDetails *struct {
		Reason string `json:"reason,omitempty"`
	} `json:"incomplete_details,omitempty"`
}

type responsesError struct {
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Param   any    `json:"param,omitempty"`
}

func responsesErrorFromEnvelope(body []byte) *responsesError {
	var envelope struct {
		Error *responsesError `json:"error,omitempty"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil
	}
	return envelope.Error
}

type responsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
	InputDetails *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

func (u *responsesUsage) chunk() StreamChunk {
	chunk := StreamChunk{TokensIn: u.InputTokens, TokensOut: u.OutputTokens}
	if u.InputDetails != nil && u.InputDetails.CachedTokens != nil {
		hit := *u.InputDetails.CachedTokens
		miss := u.InputTokens - hit
		if miss < 0 {
			miss = 0
		}
		chunk.CacheUsage = &CacheUsage{HitTokens: hit, MissTokens: miss}
	}
	return chunk
}

func parseResponsesNonStreamContent(body []byte) (string, error) {
	var resp struct {
		Status            string          `json:"status,omitempty"`
		OutputText        string          `json:"output_text"`
		Error             *responsesError `json:"error,omitempty"`
		IncompleteDetails *struct {
			Reason string `json:"reason,omitempty"`
		} `json:"incomplete_details,omitempty"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal,omitempty"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("decode openai responses response: %w", err)
	}
	if resp.Error != nil {
		return "", newResponsesAPIError("", resp.Error.Type, resp.Error.Code, resp.Error.Message)
	}
	if resp.Status == "incomplete" {
		reason := "unknown"
		if resp.IncompleteDetails != nil && resp.IncompleteDetails.Reason != "" {
			reason = resp.IncompleteDetails.Reason
		}
		return "", fmt.Errorf("openai responses incomplete: %s", sanitizeProviderDetail(reason, 256))
	}
	if resp.Status != "" && resp.Status != "completed" {
		return "", fmt.Errorf("openai responses ended with status %s", sanitizeProviderDetail(resp.Status, 64))
	}
	if text := strings.TrimSpace(resp.OutputText); text != "" {
		return text, nil
	}
	var sb strings.Builder
	for _, item := range resp.Output {
		if item.Type != "" && item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "" || part.Type == "output_text" || part.Type == "text" {
				sb.WriteString(part.Text)
			} else if part.Type == "refusal" {
				sb.WriteString(part.Refusal)
			}
		}
	}
	if text := strings.TrimSpace(sb.String()); text != "" {
		return text, nil
	}
	return "", fmt.Errorf("empty response")
}
