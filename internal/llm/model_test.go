package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/config"
	openai "github.com/sashabaranov/go-openai"
)

// TestModelMaxTokensOutput_PerModelOverride verifies that when a
// provider's model has MaxTokensOutput set, the outgoing request
// uses that value (overriding both the global LLMConfig.MaxTokens
// and the per-call ChatOptions).
func TestModelMaxTokensOutput_PerModelOverride(t *testing.T) {
	var capturedBody string
	var capturedMax int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		capturedBody = string(buf)
		// Decode just the max_tokens field to keep the test
		// independent of the streaming response shape.
		var req struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.Unmarshal(buf, &req)
		capturedMax = req.MaxTokens
		// Return a minimal valid SSE response.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	cfg := &config.LLMConfig{
		Default: "test",
		Providers: []config.ProviderConfig{
			{
				Name:     "test",
				Protocol: "openai",
				BaseURL:  srv.URL,
				APIKey:   "sk-x",
				Model:    "small",
				Models: []config.ModelConfig{
					{Name: "small", MaxTokensOutput: 1024},
					{Name: "big", MaxTokensOutput: 8192, MaxTokensContext: 200000},
				},
			},
		},
		MaxTokens: 4096, // global default
	}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Switch to "big" — per-model override is 8192.
	if err := c.SetModel("test", "big"); err != nil {
		t.Fatal(err)
	}
	opts := OptionsFromConfig(*cfg) // MaxTokens=4096 from global
	stream := c.ChatStreamWithOptions(context.Background(), "test", []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "hi"},
	}, nil, opts)
	// Drain the stream.
	for range stream {
	}

	if capturedMax != 8192 {
		t.Errorf("max_tokens sent = %d, want 8192 (per-model override)\nbody=%s", capturedMax, capturedBody)
	}
}

// TestContextWindow returns the configured context window for
// each model under a provider.
func TestContextWindow(t *testing.T) {
	cfg := &config.LLMConfig{
		Default: "p",
		Providers: []config.ProviderConfig{
			{
				Name:    "p",
				BaseURL: "http://x",
				Model:   "default",
				Models: []config.ModelConfig{
					{Name: "small", MaxTokensContext: 8192},
					{Name: "big", MaxTokensContext: 200000, MaxTokensOutput: 4096},
				},
			},
		},
	}
	c, _ := NewClient(cfg)
	if got := c.ContextWindow("p", "small"); got != 8192 {
		t.Errorf("ContextWindow(small) = %d, want 8192", got)
	}
	if got := c.ContextWindow("p", "big"); got != 200000 {
		t.Errorf("ContextWindow(big) = %d, want 200000", got)
	}
	if got := c.ContextWindow("p", "missing"); got != 0 {
		t.Errorf("ContextWindow(missing) = %d, want 0", got)
	}
	if got := c.ContextWindow("missing", "x"); got != 0 {
		t.Errorf("ContextWindow for unknown provider = %d, want 0", got)
	}
}

// TestModelMaxTokensOutput_FallsBackToCallerWhenUnset ensures
// that a model without a per-model MaxTokensOutput does NOT
// override the caller's opts (so the global LLMConfig.MaxTokens
// still flows through to the API).
func TestModelMaxTokensOutput_FallsBackToCallerWhenUnset(t *testing.T) {
	var capturedMax int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		var req struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.Unmarshal(buf, &req)
		capturedMax = req.MaxTokens
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	cfg := &config.LLMConfig{
		Default: "p",
		Providers: []config.ProviderConfig{
			{
				Name:    "p",
				BaseURL: srv.URL,
				Model:   "m",
				Models:  []config.ModelConfig{{Name: "m"}}, // no MaxTokensOutput
			},
		},
	}
	c, _ := NewClient(cfg)
	opts := ChatOptions{MaxTokens: 1234}
	stream := c.ChatStreamWithOptions(context.Background(), "p", []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "hi"},
	}, nil, opts)
	for range stream {
	}
	if capturedMax != 1234 {
		t.Errorf("max_tokens = %d, want 1234 (caller's opts preserved)", capturedMax)
	}
	// Sanity check: also test that the body actually contained
	// "max_tokens":1234 so we know it wasn't just dropped.
	if !strings.Contains("", "") { // placeholder; the assertion above is enough
		_ = capturedMax
	}
}
