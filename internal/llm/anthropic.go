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

const (
	anthropicVersion = "2023-06-01"
	anthropicDefaultBaseURL = "https://api.anthropic.com"
)

type AnthropicClient struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

func NewAnthropicClient(baseURL, apiKey, model string) *AnthropicClient {
	if baseURL == "" {
		baseURL = anthropicDefaultBaseURL
	}
	return &AnthropicClient{
		baseURL:    baseURL,
		apiKey:     apiKey,
		model:      model,
		httpClient: &http.Client{},
	}
}

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content anthropicBlocksRaw `json:"content"`
}

// anthropicBlocksRaw is the wire format of an Anthropic message
// content field. Anthropic accepts a plain string for single-text
// messages and a JSON array of content blocks for multi-part
// (text + image / document / tool_use). We always send the
// array form here — it's accepted by every modern Claude model
// and lets the agent layer stay protocol-agnostic when it
// builds the message list.
type anthropicBlocksRaw []anthropicContentBlock

func (b anthropicBlocksRaw) MarshalJSON() ([]byte, error) {
	if len(b) == 0 {
		return []byte(`""`), nil
	}
	if len(b) == 1 && b[0].Type == "text" {
		// Single text block → emit as a plain string for
		// compatibility with older Claude models.
		return json.Marshal(b[0].Text)
	}
	return json.Marshal([]anthropicContentBlock(b))
}

type anthropicContentBlock struct {
	Type   string                   `json:"type"`
	Text   string                   `json:"text,omitempty"`
	Source *anthropicContentSource  `json:"source,omitempty"`
	// Tool use fields
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// Tool result fields
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// anthropicContentSource is the per-block source field. Used by
// image and document blocks.
type anthropicContentSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
	Stream    bool               `json:"stream"`
	System    string             `json:"system,omitempty"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicResponse struct {
	ID      string                 `json:"id"`
	Type    string                 `json:"type"`
	Role    string                 `json:"role"`
	Content []anthropicContentBlock `json:"content"`
	Model   string                 `json:"model"`
	StopReason string             `json:"stop_reason"`
	Usage   anthropicUsage         `json:"usage"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Stream event types
type anthropicStreamEvent struct {
	Type    string          `json:"type"`
	Message json.RawMessage `json:"message,omitempty"`
	Index   int             `json:"index,omitempty"`
	Delta   json.RawMessage `json:"delta,omitempty"`
	ContentBlock json.RawMessage `json:"content_block,omitempty"`
}

type anthropicContentBlockDelta struct {
	Type     string `json:"type"`
	Index    int    `json:"index,omitempty"`
	Delta    struct {
		Type     string `json:"type"`
		Text     string `json:"text,omitempty"`
		Thinking string `json:"thinking,omitempty"`
		// Anthropic tool_use deltas: streamed JSON input.
		PartialJSON string `json:"partial_json,omitempty"`
	} `json:"delta"`
}

// anthropicContentBlockStart tells us what KIND of content
// block the server is starting, so we know how to interpret
// the subsequent deltas. The Index field lets us correlate
// later deltas back to this block (Anthropic streams blocks
// in parallel and a single response can have many
// interleaved text / thinking / tool_use blocks).
type anthropicContentBlockStart struct {
	Type     string `json:"type"`
	Index    int    `json:"index"`
	Text     string `json:"text"`
	Thinking string `json:"thinking,omitempty"`
}

type anthropicMessageDelta struct {
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// anthropicDefaultMaxTokens is the fallback used when no per-call
// or per-model max_tokens is provided. Anthropic's API requires
// max_tokens on every request, so we can't simply omit it.
const anthropicDefaultMaxTokens = 4096

func (c *AnthropicClient) ChatStream(ctx context.Context, modelName string, messages []Message, maxTokens int) <-chan StreamChunk {
	ch := make(chan StreamChunk, 64)

	go func() {
		defer close(ch)

		// Convert messages: separate system from user/assistant,
		// and translate OpenAI-shaped MultiContent (text + image_url)
		// into Anthropic content blocks. A message that has no
		// MultiContent falls back to a single text block; a message
		// that has MultiContent becomes a list of typed blocks.
		var systemMsg string
		var anthropicMsgs []anthropicMessage

		for _, msg := range messages {
			switch msg.Role {
			case "system":
				if systemMsg != "" {
					systemMsg += "\n\n" + msg.Content
				} else {
					systemMsg = msg.Content
				}
			default:
				anthropicMsgs = append(anthropicMsgs, anthropicMessage{
					Role:    msg.Role,
					Content: openAIToAnthropicContent(msg),
				})
			}
		}

		// Per-request model takes priority; fall back to the
		// default the client was constructed with.
		model := modelName
		if model == "" {
			model = c.model
		}

		// Resolve max_tokens: explicit per-call override wins,
		// then the model's MaxTokensOutput, then 4096 (Anthropic
		// requires a positive value; 0 is not accepted).
		effectiveMax := maxTokens
		if effectiveMax <= 0 {
			effectiveMax = anthropicDefaultMaxTokens
		}

		reqBody := anthropicRequest{
			Model:     model,
			MaxTokens: effectiveMax,
			Messages:  anthropicMsgs,
			Stream:    true,
			System:    systemMsg,
		}

		body, err := json.Marshal(reqBody)
		if err != nil {
			ch <- StreamChunk{Err: err}
			return
		}

		url := strings.TrimRight(c.baseURL, "/") + "/v1/messages"
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		if err != nil {
			ch <- StreamChunk{Err: err}
			return
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", c.apiKey)
		req.Header.Set("anthropic-version", anthropicVersion)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			ch <- StreamChunk{Err: err}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			ch <- StreamChunk{Err: fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, string(bodyBytes))}
			return
		}

		// Use bufio.Reader.ReadBytes('\n') with a 1 MB initial
		// buffer so reasoning/long-content SSE payloads don't get
		// truncated by bufio.MaxScanTokenSize (64 KiB). See
		// anthropic_adapter.go for the primary fix; this is the
		// same logic kept in sync for the legacy non-stream
		// adapter used by Chat().
		reader := bufio.NewReaderSize(resp.Body, 1<<20)
		var eventType string
		for {
			line, err := readSSELine(reader)
			if err != nil {
				if err != io.EOF {
					ch <- StreamChunk{Err: err}
				}
				return
			}
			if line == "" {
				eventType = ""
				continue
			}
			if strings.HasPrefix(line, "event: ") {
				eventType = strings.TrimPrefix(line, "event: ")
				continue
			}
			if strings.HasPrefix(line, "data: ") {
				dataJSON := strings.TrimPrefix(line, "data: ")
				c.handleStreamEvent(eventType, dataJSON, ch)
			}
		}
	}()

	return ch
}

func (c *AnthropicClient) handleStreamEvent(eventType, dataJSON string, ch chan<- StreamChunk) {
	switch eventType {
	case "content_block_start":
		// Track the kind of content block by index. Anthropic
		// streams text, thinking, and tool_use blocks in
		// parallel; each `content_block_delta` references
		// the block by its index, so we need to know what
		// kind of block each delta is for.
		//
		// We only need this for blocks that affect the next
		// delta's routing: thinking blocks carry "thinking"
		// field, text blocks carry "text", and tool_use
		// blocks carry an "id" + "name" (we don't surface
		// native tool_use as deltas — we let the agent
		// re-aggregate).
		var start anthropicContentBlockStart
		if err := json.Unmarshal([]byte(dataJSON), &start); err == nil {
			// We don't actually need to store this — the
			// delta's `Type` field ("text_delta" vs
			// "thinking_delta") is self-describing. But we
			// could log or surface it later if needed.
		}
	case "content_block_delta":
		var delta anthropicContentBlockDelta
		if err := json.Unmarshal([]byte(dataJSON), &delta); err == nil {
			switch delta.Delta.Type {
			case "text_delta":
				if delta.Delta.Text != "" {
					ch <- StreamChunk{Content: delta.Delta.Text}
				}
			case "thinking_delta":
				// Anthropic extended-thinking chain-of-
				// thought. We only ever see this if the
				// upstream model was configured to think
				// — we never enable it ourselves.
				if delta.Delta.Thinking != "" {
					ch <- StreamChunk{Thinking: delta.Delta.Thinking}
				}
			}
		}
	case "message_delta":
		var delta anthropicMessageDelta
		if err := json.Unmarshal([]byte(dataJSON), &delta); err == nil {
			// Anthropic sends the usage in message_delta
			// (which arrives AFTER message_stop in some
			// implementations). To avoid racing the
			// message_stop's Done=true (which closes the
			// consumer loop and discards the token count),
			// we include Done=true here as well. The
			// agent loop is idempotent on Done and just
			// updates the max.
			ch <- StreamChunk{
				TokensOut: delta.Usage.OutputTokens,
				Done:      true,
			}
		}
	case "message_stop":
		// If we got message_stop without a preceding
		// message_delta (e.g. the proxy collapsed them),
		// still emit Done so the consumer doesn't hang.
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

func (c *AnthropicClient) Chat(ctx context.Context, modelName string, messages []Message, maxTokens int) (string, error) {
	var systemMsg string
	var anthropicMsgs []anthropicMessage

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			if systemMsg != "" {
				systemMsg += "\n\n" + msg.Content
			} else {
				systemMsg = msg.Content
			}
		default:
			anthropicMsgs = append(anthropicMsgs, anthropicMessage{
				Role:    msg.Role,
				Content: openAIToAnthropicContent(msg),
			})
		}
	}

	model := modelName
	if model == "" {
		model = c.model
	}

	// Resolve max_tokens (see ChatStream for the same rule).
	effectiveMax := maxTokens
	if effectiveMax <= 0 {
		effectiveMax = anthropicDefaultMaxTokens
	}

	reqBody := anthropicRequest{
		Model:     model,
		MaxTokens: effectiveMax,
		Messages:  anthropicMsgs,
		Stream:    false,
		System:    systemMsg,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	url := strings.TrimRight(c.baseURL, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	if len(result.Content) == 0 {
		return "", fmt.Errorf("empty response from anthropic")
	}

	return result.Content[0].Text, nil
}

// openAIToAnthropicContent converts a single OpenAI-shaped
// ChatCompletionMessage into Anthropic content blocks. The
// message may carry a plain text Content, a MultiContent array
// of text + image_url parts, or both — in any case we emit the
// right Anthropic-side shape:
//
//   - Plain text only: a single text block. (MarshalJSON then
//     downgrades the array to a JSON string for older models.)
//   - Mixed: a list of text + image blocks. Image blocks carry
//     a base64 source with the data URL decomposed into
//     media_type + raw data.
//
// We deliberately don't translate OpenAI's "input_audio" part
// because the agent layer doesn't synthesize one — audio
// attachments fall through as text markers (see
// internal/agent/attachment.go).
func openAIToAnthropicContent(msg Message) anthropicBlocksRaw {
	if len(msg.MultiContent) == 0 {
		// Plain text path. The empty-block case (no MultiContent
		// and no Content) emits an empty string so the request
		// stays well-formed for both legacy and modern models.
		return anthropicBlocksRaw{{Type: "text", Text: msg.Content}}
	}

	out := make(anthropicBlocksRaw, 0, len(msg.MultiContent))
	for _, p := range msg.MultiContent {
		switch p.Type {
		case "text":
			out = append(out, anthropicContentBlock{Type: "text", Text: p.Text})
		case "image_url":
			if p.ImageURL == nil {
				continue
			}
			mime, data, ok := splitDataURL(p.ImageURL.URL)
			if !ok {
				// Not a data: URL (could be a remote https://
				// URL). Anthropic also accepts URL sources
				// for vision, so forward it as-is.
				out = append(out, anthropicContentBlock{
					Type:   "image",
					Source: &anthropicContentSource{Type: "url", URL: p.ImageURL.URL},
				})
				continue
			}
			out = append(out, anthropicContentBlock{
				Type: "image",
				Source: &anthropicContentSource{
					Type:      "base64",
					MediaType: mime,
					Data:      data,
				},
			})
		default:
			// Unknown / unsupported part type (e.g. input_audio).
			// Don't drop silently — keep the model aware of
			// what was attached by emitting a text marker.
			out = append(out, anthropicContentBlock{
				Type: "text",
				Text: fmt.Sprintf("(unsupported content part: type=%s)", p.Type),
			})
		}
	}
	return out
}

// splitDataURL parses "data:<mime>;base64,<data>" into its
// components. Returns ok=false if the input isn't a data URL
// at all (callers should fall back to a URL source).
func splitDataURL(s string) (mime string, data string, ok bool) {
	const prefix = "data:"
	if len(s) < len(prefix) || s[:len(prefix)] != prefix {
		return "", "", false
	}
	rest := s[len(prefix):]
	// Find the ";base64," marker; anything before is the mime
	// type, anything after is the base64 payload.
	const sep = ";base64,"
	idx := -1
	if len(rest) >= len(sep) && rest[len(rest)-len(sep):] == sep {
		// trailing ";base64,"
		idx = len(rest) - len(sep)
	} else {
		// find ";base64," anywhere
		for i := 0; i+len(sep) <= len(rest); i++ {
			if rest[i:i+len(sep)] == sep {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return "", "", false
	}
	return rest[:idx], rest[idx+len(sep):], true
}
