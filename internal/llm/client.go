package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/tool"
	openai "github.com/sashabaranov/go-openai"
)

type Message = openai.ChatCompletionMessage

type StreamChunk struct {
	Content string
	// Thinking is a reasoning / chain-of-thought delta. Only
	// populated by LLM clients that surface a separate
	// reasoning stream (DeepSeek reasoning_content, OpenAI
	// o1 reasoning tokens). Empty for models that don't
	// emit thinking. Surfaced all the way to the UI as a
	// collapsible "thinking" block (DeepSeek-style).
	Thinking string
	Done     bool
	Err      error
	TokensIn  int
	TokensOut int

	// Native tool calls (from OpenAI tool_calls field).
	// Each chunk may contain a partial tool call. Collect them via index.
	ToolCallDelta *ToolCallDelta
}

// ToolCallDelta is a single delta for one tool call.
// Multiple chunks may arrive for the same index - aggregate arguments as you go.
type ToolCallDelta struct {
	Index    int    // 0-based index of the tool call in the response
	ID       string // empty in subsequent chunks (same as first)
	Name     string // empty in subsequent chunks (same as first)
	ArgsJSON string // accumulated JSON arguments
}

// ChatOptions carries per-request sampling parameters. Zero values
// mean "use the underlying API default". When OpenAI receives
// Temperature == 0 and TopP == 0 it picks a default; MaxTokens == 0
// means "no cap".
type ChatOptions struct {
	Temperature float64
	TopP        float64
	MaxTokens   int
}

// OptionsFromConfig converts the user-facing config into ChatOptions.
// Zero-value fields in the config (e.g. unset Temperature) are passed
// through; the API applies its own defaults.
func OptionsFromConfig(cfg config.LLMConfig) ChatOptions {
	return ChatOptions{
		Temperature: cfg.Temperature,
		TopP:        cfg.TopP,
		MaxTokens:   cfg.MaxTokens,
	}
}

type ProviderInfo struct {
	Name     string
	Model    string
	Protocol string
}

type providerEntry struct {
	name      string // provider name (for error messages)
	protocol  string // "openai" or "anthropic"
	openai    *openai.Client
	anthropic *AnthropicClient
	model     string
	// apiKey + baseURL are stored alongside the SDK client so
	// the OpenAI streaming code path can build its own HTTP
	// request. We can't use the SDK's stream reader for
	// streaming because the SDK's ChatCompletionStreamChoiceDelta
	// doesn't expose `reasoning_content` (DeepSeek) — and
	// reading it requires access to the raw response bytes,
	// which the SDK consumes internally. The custom stream
	// also lets us apply the same retry / abort / context
	// semantics uniformly.
	apiKey  string
	baseURL string
}

type Client struct {
	providers map[string]*providerEntry
	default_  string

	// cfgModels is the original list of providers from config. Kept so
	// we can answer questions like "what models does provider X
	// expose?" and "what was the configured default model?".
	cfgModels []config.ProviderConfig
}

func NewClient(cfg *config.LLMConfig) (*Client, error) {
	c := &Client{
		providers: make(map[string]*providerEntry),
		default_:  cfg.Default,
		cfgModels: cfg.Providers,
	}

	if err := c.init(cfg); err != nil {
		return nil, err
	}
	return c, nil
}

// NewClientFromConfig loads the global config and returns a new LLM
// client. Convenience helper for tests and one-off tooling.
func NewClientFromConfig() (*Client, error) {
	cfg, err := config.Load("")
	if err != nil {
		return nil, err
	}
	return NewClient(&cfg.LLM)
}

func (c *Client) init(cfg *config.LLMConfig) error {

	for _, p := range cfg.Providers {
		entry := &providerEntry{
			name:     p.Name,
			protocol: p.GetProtocol(),
			model:    p.EffectiveModel(), // start with the default model
			apiKey:   p.APIKey,
			baseURL:  p.BaseURL,
		}

		switch p.GetProtocol() {
		case "anthropic":
			entry.anthropic = NewAnthropicClient(p.BaseURL, p.APIKey, p.EffectiveModel())
		default: // "openai"
			clientCfg := openai.DefaultConfig(p.APIKey)
			clientCfg.BaseURL = p.BaseURL
			entry.openai = openai.NewClientWithConfig(clientCfg)
		}

		c.providers[p.Name] = entry
	}

	if _, ok := c.providers[c.default_]; !ok {
		return fmt.Errorf("default provider %q not found", c.default_)
	}

	return nil
}

func (c *Client) ChatStream(ctx context.Context, providerName, modelName string, messages []Message) <-chan StreamChunk {
	return c.ChatStreamWithOptions(ctx, providerName, modelName, messages, nil, ChatOptions{})
}

// ChatStreamWithTools streams a chat completion with optional tool definitions.
// When tools is non-empty, the LLM may emit native tool calls (OpenAI's
// `tool_calls` field) which are surfaced as `StreamChunk.ToolCallDelta` chunks.
func (c *Client) ChatStreamWithTools(ctx context.Context, providerName, modelName string, messages []Message, tools []openai.Tool) <-chan StreamChunk {
	return c.ChatStreamWithOptions(ctx, providerName, modelName, messages, tools, ChatOptions{})
}

// SetModel switches the active model for a provider. Pass an empty
// model to reset to the provider's default (EffectiveModel).
// Returns an error if the provider or model is unknown.
func (c *Client) SetModel(providerName, modelName string) error {
	p, ok := c.providers[providerName]
	if !ok {
		return fmt.Errorf("unknown provider %q", providerName)
	}
	if modelName == "" {
		// Reset to effective default.
		p.model = c.providerDefaultModel(providerName)
		return nil
	}
	// Look up the model in the cached list of valid model names.
	models, _ := c.ModelsFor(providerName)
	for _, m := range models {
		if m.Name == modelName {
			p.model = modelName
			return nil
		}
	}
	return fmt.Errorf("model %q not found under provider %q", modelName, providerName)
}

// providerDefaultModel returns the provider's default model by
// looking at the original config (kept in c.cfgModels).
func (c *Client) providerDefaultModel(providerName string) string {
	for _, p := range c.cfgModels {
		if p.Name == providerName {
			return p.EffectiveModel()
		}
	}
	return ""
}
// disable tool calling.
func (c *Client) ChatStreamWithOptions(ctx context.Context, providerName, modelName string, messages []Message, tools []openai.Tool, opts ChatOptions) <-chan StreamChunk {
	p, ok := c.providers[providerName]
	if !ok {
		p = c.providers[c.default_]
	}

	// Per-request model takes priority; fall back to the provider's
	// configured default. This lets multiple sessions on the same
	// provider use different models concurrently without racing on
	// the shared providerEntry.model field.
	model := modelName
	if model == "" {
		model = p.model
	}

	if p.protocol == "anthropic" {
		// Anthropic support for tools is not implemented in this branch.
		// Resolve max_tokens the same way the OpenAI branch does
		// (per-model MaxTokensOutput > global opts.MaxTokens > 0)
		// and pass it through.
		anthMax := opts.MaxTokens
		if mt := c.ModelMaxTokensOutput(p.name, model); mt > 0 {
			anthMax = mt
		}
		return p.anthropic.ChatStream(ctx, model, messages, anthMax)
	}

	// OpenAI protocol
	return c.openaiStream(ctx, p, model, messages, tools, opts)
}

func (c *Client) openaiStream(ctx context.Context, p *providerEntry, model string, messages []Message, tools []openai.Tool, opts ChatOptions) <-chan StreamChunk {
	ch := make(chan StreamChunk, 64)

	go func() {
		defer close(ch)

		// Per-model overrides win over the supplied opts when set.
		// (Per-model MaxTokensOutput is non-zero → use it; otherwise
		// keep whatever the caller passed, including the global
		// LLMConfig.MaxTokens baked in by the agent.)
		if mt := c.ModelMaxTokensOutput(p.name, model); mt > 0 {
			opts.MaxTokens = mt
		}

		req := openai.ChatCompletionRequest{
			Model:    model,
			Messages: messages,
			Stream:   true,
			StreamOptions: &openai.StreamOptions{
				IncludeUsage: true,
			},
		}
		if opts.Temperature > 0 {
			req.Temperature = float32(opts.Temperature)
		}
		if opts.TopP > 0 {
			req.TopP = float32(opts.TopP)
		}
		if opts.MaxTokens > 0 {
			req.MaxTokens = opts.MaxTokens
		}
		if len(tools) > 0 {
			req.Tools = tools
		}

		// We deliberately bypass `p.openai.CreateChatCompletionStream`
		// here. The upstream SDK's `ChatCompletionStreamChoiceDelta`
		// struct has no `ReasoningContent` field, so DeepSeek-style
		// chain-of-thought deltas (and OpenAI o1 reasoning tokens)
		// are silently dropped on the floor. We can't recover them
		// after the SDK has consumed the SSE body either, so the
		// only way to surface them is to drive the HTTP request
		// ourselves and parse the raw response.
		//
		// The trade-off: we replicate ~30 lines of URL + auth +
		// SSE-parsing code, in exchange for actually seeing the
		// thinking stream. Same protocol, same auth scheme
		// (Bearer token), same JSON shape.
		body, err := json.Marshal(req)
		if err != nil {
			ch <- StreamChunk{Err: fmt.Errorf("marshal openai request: %w", err)}
			return
		}
		endpoint := strings.TrimRight(p.baseURL, "/") + "/chat/completions"
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			ch <- StreamChunk{Err: fmt.Errorf("build openai request: %w", err)}
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Cache-Control", "no-cache")
		httpReq.Header.Set("Connection", "keep-alive")
		if p.apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
		}
		httpClient := &http.Client{Timeout: 0}
		resp, err := httpClient.Do(httpReq)
		if err != nil {
			ch <- StreamChunk{Err: ClassifyAPIError(p.name, err)}
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			// Read up to 4 KB of body for the error message.
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			ch <- StreamChunk{Err: ClassifyAPIError(p.name, fmt.Errorf("openai http %d: %s", resp.StatusCode, string(errBody)))}
			return
		}

		// SSE line reader. Standard text/event-stream: events
		// separated by blank lines, each line starting with
		// "data: " followed by the JSON payload, and a final
		// "data: [DONE]" sentinel.
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				if errors.Is(err, io.EOF) {
					ch <- StreamChunk{Done: true}
					return
				}
				ch <- StreamChunk{Err: ClassifyAPIError(p.name, err)}
				return
			}
			line = bytes.TrimRight(line, "\r\n")
			if len(line) == 0 {
				continue
			}
			// Skip SSE comment lines (":") and any non-data
			// framing lines ("event:", "id:").
			if !bytes.HasPrefix(line, []byte("data: ")) {
				continue
			}
			payload := bytes.TrimPrefix(line, []byte("data: "))
			if bytes.Equal(payload, []byte("[DONE]")) {
				ch <- StreamChunk{Done: true}
				return
			}
			var r openaiStreamResponse
			if err := json.Unmarshal(payload, &r); err != nil {
				// Skip malformed lines (some providers send
				// keepalive pings or partial JSON during
				// reconnects).
				continue
			}
			for _, choice := range r.Choices {
				// Reasoning chain-of-thought (DeepSeek
				// `reasoning_content`, OpenAI o1
				// reasoning tokens). Empty for models
				// that don't surface thinking.
				if choice.Delta.ReasoningContent != "" {
					ch <- StreamChunk{Thinking: choice.Delta.ReasoningContent}
				}
				if choice.Delta.Content != "" {
					ch <- StreamChunk{Content: choice.Delta.Content}
				}
				for _, tc := range choice.Delta.ToolCalls {
					delta := &ToolCallDelta{
						Index:    tc.Index,
						ID:       tc.ID,
						Name:     tc.Function.Name,
						ArgsJSON: tc.Function.Arguments,
					}
					ch <- StreamChunk{ToolCallDelta: delta}
				}
			}
			if r.Usage != nil {
				ch <- StreamChunk{
					TokensIn:  r.Usage.PromptTokens,
					TokensOut: r.Usage.CompletionTokens,
				}
			}
		}
	}()

	return ch
}

// openaiStreamResponse is the wire shape of a single SSE
// `data:` payload from the OpenAI streaming API. We can't
// reuse the upstream SDK's `openai.ChatCompletionStreamResponse`
// because its `Delta` struct doesn't expose
// `ReasoningContent` — that field would be silently dropped
// by the JSON decoder.
type openaiStreamResponse struct {
	ID                string                         `json:"id"`
	Object            string                         `json:"object"`
	Created           int64                          `json:"created"`
	Model             string                         `json:"model"`
	Choices           []openaiStreamChoice           `json:"choices"`
	Usage             *openaiStreamUsage             `json:"usage,omitempty"`
	SystemFingerprint string                         `json:"system_fingerprint,omitempty"`
}

type openaiStreamChoice struct {
	Index        int                       `json:"index"`
	Delta        openaiStreamChoiceDelta   `json:"delta"`
	FinishReason *string                   `json:"finish_reason,omitempty"`
}

type openaiStreamChoiceDelta struct {
	Role             string                    `json:"role,omitempty"`
	Content          string                    `json:"content,omitempty"`
	// ReasoningContent is the chain-of-thought / reasoning
	// delta. Sent by DeepSeek (their `reasoning_content`
	// field) and by OpenAI o-series models (their
	// `reasoning` field, depending on the SDK version).
	// Empty for models that don't emit thinking.
	ReasoningContent string                    `json:"reasoning_content,omitempty"`
	Reasoning        string                    `json:"reasoning,omitempty"`
	FunctionCall     *openaiStreamFunctionCall `json:"function_call,omitempty"`
	ToolCalls        []openaiStreamToolCall    `json:"tool_calls,omitempty"`
}

type openaiStreamFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openaiStreamToolCall struct {
	Index    int                       `json:"index"`
	ID       string                    `json:"id"`
	Type     string                    `json:"type"`
	Function openaiStreamToolCallFunc  `json:"function"`
}

type openaiStreamToolCallFunc struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openaiStreamUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (c *Client) Chat(ctx context.Context, providerName, modelName string, messages []Message) (string, error) {
	p, ok := c.providers[providerName]
	if !ok {
		p = c.providers[c.default_]
	}

	model := modelName
	if model == "" {
		model = p.model
	}

	if p.protocol == "anthropic" {
		// Resolve max_tokens: per-model MaxTokensOutput > 0
		// (Anthropic's API requires a positive value; we don't
		// consult a global default here — the non-streaming Chat
		// entry point doesn't take ChatOptions).
		anthMax := 0
		if mt := c.ModelMaxTokensOutput(p.name, model); mt > 0 {
			anthMax = mt
		}
		return p.anthropic.Chat(ctx, model, messages, anthMax)
	}

	// OpenAI protocol
	req := openai.ChatCompletionRequest{
		Model:    model,
		Messages: messages,
	}

	// Per-model MaxTokensOutput overrides the caller's opts (and
	// therefore the global LLMConfig.MaxTokens).
	if mt := c.ModelMaxTokensOutput(p.name, model); mt > 0 {
		req.MaxTokens = mt
	}

	resp, err := p.openai.CreateChatCompletion(ctx, req)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("empty response")
	}

	return resp.Choices[0].Message.Content, nil
}

func (c *Client) ProviderNames() []string {
	names := make([]string, 0, len(c.providers))
	for name := range c.providers {
		names = append(names, name)
	}
	return names
}

func (c *Client) Providers() []ProviderInfo {
	infos := make([]ProviderInfo, 0, len(c.providers))
	for name, p := range c.providers {
		infos = append(infos, ProviderInfo{
			Name:     name,
			Model:    p.model,
			Protocol: p.protocol,
		})
	}
	return infos
}

func (c *Client) HasProvider(name string) bool {
	_, ok := c.providers[name]
	return ok
}

func (c *Client) GetModel(providerName string) string {
	if p, ok := c.providers[providerName]; ok {
		return p.model
	}
	return ""
}

func (c *Client) GetProtocol(providerName string) string {
	if p, ok := c.providers[providerName]; ok {
		return p.protocol
	}
	return ""
}

// ModelsFor returns the list of models configured under a provider.
// Returns an empty slice if the provider is unknown.
func (c *Client) ModelsFor(providerName string) ([]config.ModelConfig, bool) {
	for _, p := range c.cfgModels {
		if p.Name == providerName {
			return p.AllModels(), true
		}
	}
	return nil, false
}

// DisplayModel returns the user-facing name for the active model of
// a provider (DisplayName from config, or the model id).
func (c *Client) DisplayModel(providerName string) string {
	for _, p := range c.cfgModels {
		if p.Name == providerName {
			return p.DisplayModel()
		}
	}
	return ""
}

// ContextWindow returns the configured input context window for a
// given (provider, model) pair, or 0 if the model is unknown or
// has no MaxTokensContext set. Informational — the chat client
// does not currently truncate prompts to fit.
func (c *Client) ContextWindow(providerName, modelName string) int {
	for _, p := range c.cfgModels {
		if p.Name != providerName {
			continue
		}
		if m := p.FindModel(modelName); m != nil {
			return m.MaxTokensContext
		}
		return 0
	}
	return 0
}

// ModelMaxTokensOutput returns the per-model MaxTokensOutput
// override, or 0 if the model is unknown / unset. Callers usually
// fall back to LLMConfig.MaxTokens (the global setting) when
// this returns 0.
func (c *Client) ModelMaxTokensOutput(providerName, modelName string) int {
	for _, p := range c.cfgModels {
		if p.Name != providerName {
			continue
		}
		if m := p.FindModel(modelName); m != nil {
			return m.MaxTokensOutput
		}
		return 0
	}
	return 0
}

func (c *Client) Default() string {
	return c.default_
}

// ToolsFromRegistry converts a slice of tool.Tool into OpenAI Tool definitions.
// Parameters is expected to be a JSON schema object (or nil for empty).
func ToolsFromRegistry(tools []tool.Tool) []openai.Tool {
	out := make([]openai.Tool, 0, len(tools))
	for _, t := range tools {
		var params any
		if len(t.Parameters) > 0 {
			params = t.Parameters
		} else {
			params = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}
		out = append(out, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}
	return out
}
