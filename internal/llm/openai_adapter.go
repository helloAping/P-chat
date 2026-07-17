package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// OpenAIAdapter implements ProtocolAdapter for the OpenAI
// chat-completions protocol (POST /chat/completions). It converts
// ChatMessage lists into OpenAI ChatCompletionRequest JSON and
// parses the SSE stream response.
type OpenAIAdapter struct {
	baseURL string
	apiKey  string
	name    string // provider name (for log messages)
}

// NewOpenAIAdapter creates an adapter for an OpenAI-compatible
// endpoint. baseURL is the API root (e.g. https://api.openai.com).
func NewOpenAIAdapter(baseURL, apiKey, providerName string) *OpenAIAdapter {
	return &OpenAIAdapter{
		baseURL: baseURL,
		apiKey:  apiKey,
		name:    providerName,
	}
}

// Build converts ChatMessage + tools into an OpenAI
// ChatCompletionRequest and returns it as serialized JSON.
//
// Message type mapping:
//
//	text         → {role: user/assistant, content: "..."}
//	image        → {role: user, content: [{type: image_url, image_url: {url: "data:<mime>;base64,<data>"}}]}
//	tool_call    → {role: assistant, tool_calls: [{id, type: function, function: {name, arguments}}]}
//	tool_result  → {role: tool, content: "...", tool_call_id: "..."}
//	thinking     → skipped (agent-internal)
//	audio, file  → text marker
//
// Parallel tool_calls (and assistant text immediately followed by
// tool_call) are merged into a single assistant message. Emitting
// them as separate assistant messages violates the OpenAI chat
// completions schema — strict upstreams (e.g. api-convert.08ms.cn
// → Console Go) reject the request with "Upstream request failed"
// / code=invalid_request_error. This was the 2026-07-17
// regression where the model emitted 2 parallel list_files calls
// and the next round was rejected. P2-3.
func (a *OpenAIAdapter) Build(messages []ChatMessage, model string, maxTokens int, tools []ToolDef, system string, temperature float32, topP float32) (*ProtocolRequest, error) {
	openaiMsgs := make([]openai.ChatCompletionMessage, 0, len(messages)+1)

	// System prompt as first message.
	if system != "" {
		openaiMsgs = append(openaiMsgs, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: system,
		})
	}

	// Collect consecutive user messages into a single message with
	// MultiContent when an image follows a text from the same
	// role. This mirrors the old behaviour where attachments were
	// embedded in a single user message.
	var pending *openai.ChatCompletionMessage
	flushPending := func() {
		if pending == nil {
			return
		}
		openaiMsgs = append(openaiMsgs, *pending)
		pending = nil
	}

	// lastAssistantIdx points to the most recently appended
	// assistant message (or -1). Consecutive TypeToolCall entries
	// AND assistant text followed by tool_calls are folded into
	// that message, so the wire shape becomes
	//   {role:assistant, content: "...", tool_calls: [tc1, tc2]}
	// instead of three back-to-back assistant messages. Any
	// non-mergeable type (TypeToolResult, TypeImage, default,
	// user role) invalidates the pointer.
	lastAssistantIdx := -1
	lastAssistant := func() *openai.ChatCompletionMessage {
		if lastAssistantIdx < 0 || lastAssistantIdx >= len(openaiMsgs) {
			return nil
		}
		if openaiMsgs[lastAssistantIdx].Role != openai.ChatMessageRoleAssistant {
			return nil
		}
		return &openaiMsgs[lastAssistantIdx]
	}

	for i := 0; i < len(messages); i++ {
		msg := messages[i]

		switch msg.Type {
		case TypeThinking:
			continue // agent-internal only

		case TypeToolCall:
			flushPending()
			tc := openai.ToolCall{
				ID:   msg.ToolID,
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      msg.ToolName,
					Arguments: msg.ToolInput,
				},
			}
			if la := lastAssistant(); la != nil {
				// Fold into the previous assistant message so
				// parallel tool_calls land in one tool_calls
				// array. The text reply (if any) is preserved
				// on `la.Content`.
				la.ToolCalls = append(la.ToolCalls, tc)
			} else {
				openaiMsgs = append(openaiMsgs, openai.ChatCompletionMessage{
					Role:      openai.ChatMessageRoleAssistant,
					ToolCalls: []openai.ToolCall{tc},
				})
				lastAssistantIdx = len(openaiMsgs) - 1
			}

		case TypeToolResult:
			flushPending()
			lastAssistantIdx = -1
			content := msg.Content
			if msg.ToolError {
				content = fmt.Sprintf("error: %s\n工具 %s 执行失败。请分析错误原因后调整方案并重试；反复失败请告知用户。", msg.Content, msg.ToolName)
			}
			openaiMsgs = append(openaiMsgs, openai.ChatCompletionMessage{
				Role:       openaiChatRole(msg.Role),
				Content:    content,
				ToolCallID: msg.ToolID,
				Name:       msg.ToolName,
			})

		case TypeImage:
			flushPending()
			lastAssistantIdx = -1
			part := openai.ChatMessagePart{
				Type: openai.ChatMessagePartTypeImageURL,
				ImageURL: &openai.ChatMessageImageURL{
					URL: fmt.Sprintf("data:%s;base64,%s", msg.MimeType, msg.Content),
				},
			}
			if pending != nil && pending.Role == openai.ChatMessageRoleUser {
				pending.MultiContent = append(pending.MultiContent, part)
			} else {
				flushPending()
				pending = &openai.ChatCompletionMessage{
					Role:         openai.ChatMessageRoleUser,
					MultiContent: []openai.ChatMessagePart{part},
				}
			}

		case TypeText:
			role := openaiChatRole(msg.Role)
			if role == openai.ChatMessageRoleUser {
				// User text uses the existing pending path so
				// that a follow-up image can merge in.
				lastAssistantIdx = -1
				if pending != nil && pending.Role == role {
					pending.MultiContent = append(pending.MultiContent, openai.ChatMessagePart{
						Type: openai.ChatMessagePartTypeText,
						Text: msg.Content,
					})
				} else {
					flushPending()
					pending = &openai.ChatCompletionMessage{
						Role: role,
						MultiContent: []openai.ChatMessagePart{{
							Type: openai.ChatMessagePartTypeText,
							Text: msg.Content,
						}},
					}
				}
				continue
			}

			// Assistant / tool text. Tool-role text is unusual
			// but allowed by the protocol; we treat it as a
			// non-mergeable standalone entry.
			flushPending()
			if role == openai.ChatMessageRoleAssistant {
				if la := lastAssistant(); la != nil && la.Content == "" {
					// No content yet on the previous assistant
					// message — fill it. This is the
					// "text + tool_call" merge: the agent
					// produced a TypeText(assistant) first and
					// then a TypeToolCall (or vice versa) in the
					// same round, and both belong on the same
					// wire message.
					la.Content = msg.Content
					continue
				}
			}
			openaiMsgs = append(openaiMsgs, openai.ChatCompletionMessage{
				Role:    role,
				Content: msg.Content,
			})
			if role == openai.ChatMessageRoleAssistant {
				lastAssistantIdx = len(openaiMsgs) - 1
			} else {
				lastAssistantIdx = -1
			}

		default: // TypeAudio, TypeFile, or empty (plain message)
			flushPending()
			lastAssistantIdx = -1
			role := openaiChatRole(msg.Role)
			content := msg.Content
			switch msg.Type {
			case TypeAudio:
				content = fmt.Sprintf("(attached audio: %s, MIME=%s)", msg.Name, msg.MimeType)
			case TypeFile:
				content = fmt.Sprintf("(attached file: %s)", msg.Name)
			}
			openaiMsgs = append(openaiMsgs, openai.ChatCompletionMessage{
				Role:    role,
				Content: content,
			})
		}
	}
	flushPending()

	// Convert agnostic ToolDef → openai.Tool.
	openaiTools := make([]openai.Tool, 0, len(tools))
	for _, td := range tools {
		openaiTools = append(openaiTools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        td.Name,
				Description: td.Description,
				Parameters:  td.Parameters,
			},
		})
	}

	req := openai.ChatCompletionRequest{
		Model:    model,
		Messages: openaiMsgs,
		Stream:   true,
		StreamOptions: &openai.StreamOptions{
			IncludeUsage: true,
		},
	}
	if temperature > 0 {
		req.Temperature = temperature
	}
	if topP > 0 {
		req.TopP = topP
	}
	if maxTokens > 0 {
		req.MaxTokens = maxTokens
	}
	if len(openaiTools) > 0 {
		req.Tools = openaiTools
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal openai request: %w", err)
	}

	url := strings.TrimRight(a.baseURL, "/") + "/chat/completions"

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
		URL:     url,
		Body:    body,
		Headers: headers,
	}, nil
}

// ParseStream reads an OpenAI SSE stream and emits StreamChunk
// values. It runs in the calling goroutine's thread and returns a
// channel; parsing happens asynchronously.
func (a *OpenAIAdapter) ParseStream(r io.Reader) <-chan StreamChunk {
	ch := make(chan StreamChunk, 64)

	go func() {
		defer close(ch)

		reader := bufio.NewReaderSize(r, 1<<20)
		var (
			rawChunks     int
			parseFailures int
			contentChars  int
			thinkingChars int
			choiceCount   int
		)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				if errors.Is(err, io.EOF) {
					ch <- StreamChunk{Done: true}
					log.Printf("[llm/%s] stream ended: chunks=%d content=%d thinking=%d errs=%d",
						a.name, rawChunks, contentChars, thinkingChars, parseFailures)
					return
				}
				ch <- StreamChunk{Err: err}
				return
			}
			line = bytes.TrimRight(line, "\r\n")
			if len(line) == 0 {
				continue
			}
			if !bytes.HasPrefix(line, []byte("data: ")) {
				continue
			}
			payload := bytes.TrimPrefix(line, []byte("data: "))
			if bytes.Equal(payload, []byte("[DONE]")) {
				ch <- StreamChunk{Done: true}
				return
			}
			rawChunks++
			if rawChunks <= 3 {
				log.Printf("[llm/%s] raw #%d: %s", a.name, rawChunks, string(payload))
			}

			var r openaiStreamResponse
			if err := json.Unmarshal(payload, &r); err != nil {
				parseFailures++
				continue
			}
			if proxyErr := extractProxyError(payload); proxyErr != "" {
				ch <- StreamChunk{Err: fmt.Errorf("openai proxy error: %s", proxyErr)}
				return
			}
			for _, choice := range r.Choices {
				choiceCount++
				if choice.Delta.ReasoningContent != "" {
					thinkingChars += len(choice.Delta.ReasoningContent)
					ch <- StreamChunk{Thinking: choice.Delta.ReasoningContent}
				} else if choice.Delta.Reasoning != "" {
					thinkingChars += len(choice.Delta.Reasoning)
					ch <- StreamChunk{Thinking: choice.Delta.Reasoning}
				}
				delta := choice.Delta.Content
				if delta == "" {
					delta = choice.Delta.Text
				}
				if delta != "" {
					contentChars += len(delta)
					ch <- StreamChunk{Content: delta}
				}
				for _, tc := range choice.Delta.ToolCalls {
					ch <- StreamChunk{ToolCallDelta: &ToolCallDelta{
						Index:    tc.Index,
						ID:       tc.ID,
						Name:     tc.Function.Name,
						ArgsJSON: tc.Function.Arguments,
					}}
				}
			}
			if r.Usage != nil {
				ch <- StreamChunk{
					TokensIn:  r.Usage.PromptTokens,
					TokensOut: r.Usage.CompletionTokens,
				}
			}
			if choiceCount == 0 || (contentChars == 0 && thinkingChars == 0) {
				if v, _ := extractContent(payload); v != "" {
					contentChars += len(v)
					ch <- StreamChunk{Content: v}
				}
				if v, _ := extractThinking(payload); v != "" {
					thinkingChars += len(v)
					ch <- StreamChunk{Thinking: v}
				}
			}
		}
	}()

	return ch
}

// Send executes the HTTP request and returns a stream channel.
// This is the convenience entry point: adapter.Build → HTTP POST
// → adapter.ParseStream.
func (a *OpenAIAdapter) Send(ctx context.Context, req *ProtocolRequest) (<-chan StreamChunk, error) {
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return nil, err
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	if a.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	}

	resp, err := NewHTTPClient().Do(httpReq)
	if err != nil {
		return nil, ClassifyAPIError(a.name, err)
	}
	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, ClassifyAPIError(a.name, fmt.Errorf("openai http %d: %s", resp.StatusCode, string(errBody)))
	}

	return a.ParseStream(resp.Body), nil
}

// openaiChatRole maps our role constants to the go-openai role
// constants.
func openaiChatRole(role string) string {
	switch role {
	case RoleSystem:
		return openai.ChatMessageRoleSystem
	case RoleUser:
		return openai.ChatMessageRoleUser
	case RoleAssistant:
		return openai.ChatMessageRoleAssistant
	case RoleTool:
		return openai.ChatMessageRoleTool
	default:
		return openai.ChatMessageRoleUser
	}
}
