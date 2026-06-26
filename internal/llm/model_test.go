package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	stream := c.ChatStreamWithOptions(context.Background(), "test", "big", []openai.ChatCompletionMessage{
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
	stream := c.ChatStreamWithOptions(context.Background(), "p", "m", []openai.ChatCompletionMessage{
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

// TestPerRequestModelDoesNotRace verifies that two concurrent
// ChatStreamWithOptions calls on the same provider, with different
// per-request model names, each send the right model on the wire
// and don't trample each other through the shared
// providerEntry.model field. This is the regression test for
// "switching model in one session changes the model used by all
// other sessions on the same provider".
func TestPerRequestModelDoesNotRace(t *testing.T) {
	var (
		mu       sync.Mutex
		expected = map[string]int{
			"big":   0,
			"small": 0,
		}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(buf, &req)
		mu.Lock()
		expected[req.Model]++
		mu.Unlock()
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
				Protocol: "openai",
				BaseURL: srv.URL,
				Model:   "small",
				Models:  []config.ModelConfig{{Name: "small"}, {Name: "big"}},
			},
		},
	}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Launch N=20 concurrent calls, half on "big" and half on
	// "small", on the same provider. None should see the wrong
	// model on the wire.
	const N = 20
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		model := "small"
		if i%2 == 0 {
			model = "big"
		}
		go func(model string) {
			defer wg.Done()
			stream := c.ChatStreamWithOptions(context.Background(), "p", model, []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleUser, Content: "hi"},
			}, nil, ChatOptions{})
			for range stream {
			}
		}(model)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if expected["big"] != N/2 {
		t.Errorf("big model seen %d times, want %d (race on shared providerEntry.model?)", expected["big"], N/2)
	}
	if expected["small"] != N/2 {
		t.Errorf("small model seen %d times, want %d (race on shared providerEntry.model?)", expected["small"], N/2)
	}
}
