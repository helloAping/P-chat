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
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
	Stream    bool               `json:"stream"`
	System    string             `json:"system,omitempty"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
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

func (c *AnthropicClient) ChatStream(ctx context.Context, messages []Message) <-chan StreamChunk {
	ch := make(chan StreamChunk, 64)

	go func() {
		defer close(ch)

		// Convert messages: separate system from user/assistant
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
					Content: msg.Content,
				})
			}
		}

		reqBody := anthropicRequest{
			Model:     c.model,
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

func (c *AnthropicClient) Chat(ctx context.Context, messages []Message) (string, error) {
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
				Content: msg.Content,
			})
		}
	}

	reqBody := anthropicRequest{
		Model:     c.model,
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
