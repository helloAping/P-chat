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
	Type   string                  `json:"type"`
	Text   string                  `json:"text,omitempty"`
	Source *anthropicContentSource  `json:"source,omitempty"`
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
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicMessageDelta struct {
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (c *AnthropicClient) ChatStream(ctx context.Context, modelName string, messages []Message) <-chan StreamChunk {
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

		reqBody := anthropicRequest{
			Model:     model,
			MaxTokens: 4096,
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

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}

			// SSE format: "event: xxx\ndata: xxx"
			if strings.HasPrefix(line, "event: ") {
				// Read the next data line
				if !scanner.Scan() {
					break
				}
				dataLine := scanner.Text()
				if !strings.HasPrefix(dataLine, "data: ") {
					continue
				}
				dataJSON := strings.TrimPrefix(dataLine, "data: ")

				eventType := strings.TrimPrefix(line, "event: ")
				c.handleStreamEvent(eventType, dataJSON, ch)
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- StreamChunk{Err: err}
			return
		}
	}()

	return ch
}

func (c *AnthropicClient) handleStreamEvent(eventType, dataJSON string, ch chan<- StreamChunk) {
	switch eventType {
	case "content_block_delta":
		var delta anthropicContentBlockDelta
		if err := json.Unmarshal([]byte(dataJSON), &delta); err == nil {
			if delta.Type == "text_delta" && delta.Text != "" {
				ch <- StreamChunk{Content: delta.Text}
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

func (c *AnthropicClient) Chat(ctx context.Context, modelName string, messages []Message) (string, error) {
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

	reqBody := anthropicRequest{
		Model:     model,
		MaxTokens: 4096,
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
