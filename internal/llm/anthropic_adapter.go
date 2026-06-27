package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// AnthropicAdapter implements ProtocolAdapter for the Anthropic
// Messages API (POST /v1/messages). It converts ChatMessage lists
// into Anthropic message blocks and parses the SSE stream
// response.
type AnthropicAdapter struct {
	baseURL string
	apiKey  string
	name    string // provider name for log/error messages
}

// NewAnthropicAdapter creates an adapter for an Anthropic-compatible
// endpoint.
func NewAnthropicAdapter(baseURL, apiKey, providerName string) *AnthropicAdapter {
	return &AnthropicAdapter{
		baseURL: baseURL,
		apiKey:  apiKey,
		name:    providerName,
	}
}

// Build converts ChatMessage + tools into an Anthropic
// MessagesRequest JSON body.
//
// Message type mapping:
//
//	text         → {role: user/assistant, content: [{type: text, text: "..."}]}
//	image        → {role: user, content: [{type: image, source: {type: base64, media_type, data}}]}
//	tool_call    → {role: assistant, content: [{type: tool_use, id, name, input}]}
//	tool_result  → {role: user, content: [{type: tool_result, tool_use_id, content}]}
//	thinking     → skipped (agent-internal)
//	system role  → extracted to top-level system field
//	audio, file  → text marker
func (a *AnthropicAdapter) Build(messages []ChatMessage, model string, maxTokens int, tools []ToolDef, system string, temperature float32, topP float32) (*ProtocolRequest, error) {
	var anthropicMsgs []anthropicMessage

	for _, msg := range messages {
		// System role messages accumulate into the top-level
		// system string; they are not sent as messages.
		if msg.Role == RoleSystem {
			if system != "" {
				system += "\n\n"
			}
			system += msg.Content
			continue
		}

		switch msg.Type {
		case TypeThinking:
			continue // agent-internal only

		case TypeToolCall:
			// Map tool_call into an Anthropic tool_use block
			// inside an assistant role message.
			inputJSON := json.RawMessage(msg.ToolInput)
			if inputJSON == nil {
				inputJSON = json.RawMessage("{}")
			}
			toolUseBlock := map[string]interface{}{
				"type":  "tool_use",
				"id":    msg.ToolID,
				"name":  msg.ToolName,
				"input": inputJSON,
			}
			anthropicMsgs = append(anthropicMsgs, anthropicMessage{
				Role:    "assistant",
				Content: anthropicBlocksRaw{anthropicBlockFromMap(toolUseBlock)},
			})

		case TypeToolResult:
			content := msg.Content
			if msg.ToolError {
				content = "error: " + msg.Content
			}
			block := map[string]interface{}{
				"type":         "tool_result",
				"tool_use_id":  msg.ToolID,
				"content":      content,
			}
			if msg.ToolError {
				block["is_error"] = true
			}
			// Anthropic requires tool_result messages to have
			// role "user" (the protocol doesn't have a separate
			// "tool" role).
			anthropicMsgs = append(anthropicMsgs, anthropicMessage{
				Role:    "user",
				Content: anthropicBlocksRaw{anthropicBlockFromMap(block)},
			})

		case TypeImage:
			// Build an image block with base64 source.
			block := map[string]interface{}{
				"type": "image",
				"source": map[string]interface{}{
					"type":       "base64",
					"media_type": msg.MimeType,
					"data":       msg.Content,
				},
			}
			anthropicMsgs = append(anthropicMsgs, anthropicMessage{
				Role:    anthropicRole(msg.Role),
				Content: anthropicBlocksRaw{anthropicBlockFromMap(block)},
			})

		case TypeText:
			role := anthropicRole(msg.Role)
			if msg.Content != "" {
				anthropicMsgs = append(anthropicMsgs, anthropicMessage{
					Role:    role,
					Content: anthropicBlocksRaw{{Type: "text", Text: msg.Content}},
				})
			}

		default:
			// TypeAudio, TypeFile, or empty — emit as text
			// marker.
			role := anthropicRole(msg.Role)
			content := msg.Content
			switch msg.Type {
			case TypeAudio:
				content = fmt.Sprintf("(attached audio: %s)", msg.Name)
			case TypeFile:
				content = fmt.Sprintf("(attached file: %s)", msg.Name)
			}
			if content != "" {
				anthropicMsgs = append(anthropicMsgs, anthropicMessage{
					Role:    role,
					Content: anthropicBlocksRaw{{Type: "text", Text: content}},
				})
			}
		}
	}

	// Build Anthropic tools if any.
	var anthropicTools []anthropicTool
	if len(tools) > 0 {
		anthropicTools = make([]anthropicTool, 0, len(tools))
		for _, td := range tools {
			anthropicTools = append(anthropicTools, anthropicTool{
				Name:        td.Name,
				Description: td.Description,
				InputSchema: td.Parameters,
			})
		}
	}

	effMax := maxTokens
	if effMax <= 0 {
		effMax = anthropicDefaultMaxTokens
	}

	req := anthropicRequest{
		Model:     model,
		MaxTokens: effMax,
		Messages:  anthropicMsgs,
		Stream:    true,
		System:    system,
	}
	if len(anthropicTools) > 0 {
		req.Tools = anthropicTools
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	url := strings.TrimRight(a.baseURL, "/") + "/v1/messages"

	return &ProtocolRequest{
		Method: http.MethodPost,
		URL:    url,
		Body:   body,
		Headers: map[string]string{
			"Content-Type":      "application/json",
			"x-api-key":         a.apiKey,
			"anthropic-version": anthropicVersion,
		},
	}, nil
}

// ParseStream reads an Anthropic SSE stream and emits StreamChunk
// values.
func (a *AnthropicAdapter) ParseStream(r io.Reader) <-chan StreamChunk {
	ch := make(chan StreamChunk, 64)

	go func() {
		defer close(ch)

		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "event: ") {
				if !scanner.Scan() {
					break
				}
				dataLine := scanner.Text()
				if !strings.HasPrefix(dataLine, "data: ") {
					continue
				}
				dataJSON := strings.TrimPrefix(dataLine, "data: ")
				eventType := strings.TrimPrefix(line, "event: ")
				a.handleStreamEvent(eventType, dataJSON, ch)
			}
		}
		if err := scanner.Err(); err != nil {
			ch <- StreamChunk{Err: err}
		}
	}()

	return ch
}

// Send executes the HTTP request and returns a stream channel.
func (a *AnthropicAdapter) Send(ctx context.Context, req *ProtocolRequest) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return nil, err
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := NewHTTPClient().Do(httpReq)
	if err != nil {
		return nil, ClassifyAPIError(a.name, err)
	}
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, ClassifyAPIError(a.name, fmt.Errorf("anthropic http %d: %s", resp.StatusCode, string(bodyBytes)))
	}

	// Start streaming; the caller reads from the response body
	// via ParseStream after this returns.
	return resp, nil
}

// handleStreamEvent processes a single Anthropic SSE event.
func (a *AnthropicAdapter) handleStreamEvent(eventType, dataJSON string, ch chan<- StreamChunk) {
	switch eventType {
	case "content_block_start":
		// Tracked if needed; deltas are self-describing.
	case "content_block_delta":
		var delta anthropicContentBlockDelta
		if err := json.Unmarshal([]byte(dataJSON), &delta); err == nil {
			switch delta.Type {
			case "text_delta":
				if delta.Text != "" {
					ch <- StreamChunk{Content: delta.Text}
				}
			case "thinking_delta":
				if delta.Thinking != "" {
					ch <- StreamChunk{Thinking: delta.Thinking}
				}
			}
		}
	case "message_stop":
		ch <- StreamChunk{Done: true}
	case "error":
		var errResp struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(dataJSON), &errResp); err == nil {
			ch <- StreamChunk{Err: fmt.Errorf("anthropic error: %s", errResp.Message)}
		}
	}
}

// anthropicRole maps our role constants to Anthropic roles.
func anthropicRole(role string) string {
	switch role {
	case RoleUser, RoleTool:
		return "user"
	case RoleAssistant:
		return "assistant"
	default:
		return "user"
	}
}

// --- Anthropic wire types ---

// anthropicBlockFromMap converts a map[string]interface{} to an
// anthropicContentBlock by JSON-encoding the map into the known
// struct shape. Extra fields (tool_use id, input, source metadata)
// that the struct doesn't have are stored in the raw form and
// serialised back via a custom marshal step.
func anthropicBlockFromMap(m map[string]interface{}) anthropicContentBlock {
	b, _ := json.Marshal(m)
	var block anthropicContentBlock
	json.Unmarshal(b, &block)
	return block
}
