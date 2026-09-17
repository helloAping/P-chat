package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// OpenAIResponsesAdapter 实现 OpenAI Responses API 协议。
// OpenAIResponsesAdapter implements the OpenAI Responses API protocol.
type OpenAIResponsesAdapter struct {
	baseURL string
	apiKey  string
	name    string
}

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
	input, err := responsesInputFromChatMessages(chatBody["messages"])
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
		reader := bufio.NewReaderSize(r, 1<<20)
		argDeltas := map[int]string{}
		emittedToolCalls := map[int]bool{}
		rawEvents := 0
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				if errors.Is(err, io.EOF) {
					ch <- StreamChunk{Done: true}
					return
				}
				ch <- StreamChunk{Err: err}
				return
			}
			line = bytes.TrimRight(line, "\r\n")
			if len(line) == 0 || !bytes.HasPrefix(line, []byte("data: ")) {
				continue
			}
			payload := bytes.TrimPrefix(line, []byte("data: "))
			if bytes.Equal(payload, []byte("[DONE]")) {
				ch <- StreamChunk{Done: true}
				return
			}
			rawEvents++
			if rawEvents <= 3 {
				log.Printf("[llm/%s/responses] raw #%d: %s", a.name, rawEvents, string(payload))
			}
			if proxyErr := extractProxyError(payload); proxyErr != "" {
				ch <- StreamChunk{Err: fmt.Errorf("openai responses proxy error: %s", proxyErr)}
				return
			}
			var ev responsesStreamEvent
			if err := json.Unmarshal(payload, &ev); err != nil {
				continue
			}
			switch ev.Type {
			case "response.output_text.delta", "response.refusal.delta":
				if ev.Delta != "" {
					ch <- StreamChunk{Content: ev.Delta}
				}
			case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
				if ev.Delta != "" {
					ch <- StreamChunk{Thinking: ev.Delta}
				}
			case "response.reasoning_summary_part.done":
				if ev.Part != nil && ev.Part.Text != "" {
					ch <- StreamChunk{Thinking: ev.Part.Text}
				}
			case "response.reasoning_text.done":
				if ev.Text != "" {
					ch <- StreamChunk{Thinking: ev.Text}
				}
			case "response.function_call_arguments.delta":
				argDeltas[ev.OutputIndex] += ev.Delta
			case "response.function_call_arguments.done":
				if emittedToolCalls[ev.OutputIndex] {
					continue
				}
				if ev.CallID == "" || ev.Name == "" {
					continue
				}
				args := ev.Arguments
				if args == "" {
					args = argDeltas[ev.OutputIndex]
				}
				ch <- StreamChunk{ToolCallDelta: &ToolCallDelta{
					Index:    ev.OutputIndex,
					ID:       ev.CallID,
					Name:     ev.Name,
					ArgsJSON: args,
				}}
				emittedToolCalls[ev.OutputIndex] = true
			case "response.output_item.done":
				if ev.Item != nil && ev.Item.Type == "function_call" {
					if emittedToolCalls[ev.OutputIndex] {
						continue
					}
					args := ev.Item.Arguments
					if args == "" {
						args = argDeltas[ev.OutputIndex]
					}
					id := ev.Item.CallID
					if id == "" {
						id = ev.Item.ID
					}
					ch <- StreamChunk{ToolCallDelta: &ToolCallDelta{
						Index:    ev.OutputIndex,
						ID:       id,
						Name:     ev.Item.Name,
						ArgsJSON: args,
					}}
					emittedToolCalls[ev.OutputIndex] = true
				}
			case "response.completed":
				if ev.Response != nil && ev.Response.Usage != nil {
					ch <- ev.Response.Usage.chunk()
				}
				ch <- StreamChunk{Done: true}
				return
			case "response.failed":
				if ev.Response != nil && ev.Response.Error != nil {
					ch <- StreamChunk{Err: errors.New(ev.Response.Error.Message)}
					return
				}
			}
		}
	}()
	return ch
}

type responsesStreamEvent struct {
	Type        string               `json:"type"`
	Delta       string               `json:"delta,omitempty"`
	Text        string               `json:"text,omitempty"`
	ItemID      string               `json:"item_id,omitempty"`
	CallID      string               `json:"call_id,omitempty"`
	Name        string               `json:"name,omitempty"`
	Arguments   string               `json:"arguments,omitempty"`
	OutputIndex int                  `json:"output_index,omitempty"`
	Part        *responsesPart       `json:"part,omitempty"`
	Item        *responsesOutputItem `json:"item,omitempty"`
	Response    *responsesEnvelope   `json:"response,omitempty"`
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
	Usage *responsesUsage `json:"usage,omitempty"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
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
		OutputText string `json:"output_text"`
		Output     []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("decode openai responses response: %w", err)
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
			}
		}
	}
	if text := strings.TrimSpace(sb.String()); text != "" {
		return text, nil
	}
	return "", fmt.Errorf("empty response")
}
