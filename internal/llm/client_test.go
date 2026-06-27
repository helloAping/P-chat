package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
)

// readAll drains a StreamChunk channel into a slice. Used
// by the SSE parser tests so we can assert against the
// full event sequence.
func readAll(ch <-chan StreamChunk) []StreamChunk {
	var out []StreamChunk
	for c := range ch {
		out = append(out, c)
	}
	return out
}

// TestOpenAIStream_StandardField verifies the parser
// still handles the canonical OpenAI wire shape:
//   data: {"choices":[{"delta":{"content":"hello"}}]}
//   data: {"choices":[{"delta":{"content":" world"}}]}
//   data: [DONE]
func TestOpenAIStream_StandardField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		parts := []string{"hello", " ", "world", "!"}
		for _, p := range parts {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", p)
			if fl != nil { fl.Flush() }
		}
		// Final usage chunk with no choices.
		fmt.Fprintf(w, "data: {\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2}}\n\n")
		if fl != nil { fl.Flush() }
		fmt.Fprintf(w, "data: [DONE]\n\n")
		if fl != nil { fl.Flush() }
	}))
	defer srv.Close()

	c, err := newTestClient("openai", srv.URL)
	if err != nil { t.Fatal(err) }

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	chunks := readAll(c.ChatStream(ctx, "openai", "test-model", []Message{
		{Role: "user", Content: "hi"},
	}))
	var content strings.Builder
	for _, c := range chunks {
		if c.Err != nil { t.Fatalf("stream error: %v", c.Err) }
		if c.Content != "" { content.WriteString(c.Content) }
	}
	if got := content.String(); got != "hello world!" {
		t.Errorf("content = %q, want %q", got, "hello world!")
	}
}

// TestOpenAIStream_LegacyTextField simulates a proxy
// (api-convert.08ms.cn is one) that uses the legacy
// /v1/completions field name `text` instead of
// `content` in the chat-completions delta. The parser
// must read both and pick whichever is non-empty,
// otherwise the user's chat renders an empty assistant
// bubble even though the LLM answered.
func TestOpenAIStream_LegacyTextField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		parts := []string{"我", "是", "助手"}
		for _, p := range parts {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"text\":%q}}]}\n\n", p)
			if fl != nil { fl.Flush() }
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		if fl != nil { fl.Flush() }
	}))
	defer srv.Close()

	c, err := newTestClient("openai", srv.URL)
	if err != nil { t.Fatal(err) }

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	chunks := readAll(c.ChatStream(ctx, "openai", "test-model", []Message{
		{Role: "user", Content: "hi"},
	}))
	var content strings.Builder
	for _, c := range chunks {
		if c.Err != nil { t.Fatalf("stream error: %v", c.Err) }
		if c.Content != "" { content.WriteString(c.Content) }
	}
	if got := content.String(); got != "我是助手" {
		t.Errorf("content = %q, want %q", got, "我是助手")
	}
}

// TestOpenAIStream_ReasoningField covers OpenAI o-series
// models that emit `reasoning` instead of
// `reasoning_content`. The parser should pick either.
func TestOpenAIStream_ReasoningField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		// Three reasoning chunks, two content chunks.
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"reasoning\":\"Let me \"}}]}\n\n"); fl.Flush()
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"reasoning\":\"think.\"}}]}\n\n"); fl.Flush()
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Answer: \"}}]}\n\n"); fl.Flush()
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"42\"}}]}\n\n"); fl.Flush()
		fmt.Fprintf(w, "data: [DONE]\n\n"); fl.Flush()
	}))
	defer srv.Close()

	c, err := newTestClient("openai", srv.URL)
	if err != nil { t.Fatal(err) }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	chunks := readAll(c.ChatStream(ctx, "openai", "test-model", []Message{{Role: "user", Content: "?"}}))

	var thinking, content strings.Builder
	for _, c := range chunks {
		if c.Err != nil { t.Fatalf("stream error: %v", c.Err) }
		if c.Thinking != "" { thinking.WriteString(c.Thinking) }
		if c.Content != "" { content.WriteString(c.Content) }
	}
	if got := thinking.String(); got != "Let me think." {
		t.Errorf("thinking = %q, want %q", got, "Let me think.")
	}
	if got := content.String(); got != "Answer: 42" {
		t.Errorf("content = %q, want %q", got, "Answer: 42")
	}
}

// TestOpenAIStream_EmptyChoicesChunk verifies the
// parser doesn't crash when a chunk arrives with no
// `choices` field (e.g. a pure-usage chunk, or a
// proxy that sends a final summary chunk).
func TestOpenAIStream_EmptyChoicesChunk(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		atomic.AddInt32(&hits, 1)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"); fl.Flush()
		fmt.Fprintf(w, "data: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n"); fl.Flush()
		fmt.Fprintf(w, "data: [DONE]\n\n"); fl.Flush()
	}))
	defer srv.Close()

	c, err := newTestClient("openai", srv.URL)
	if err != nil { t.Fatal(err) }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	chunks := readAll(c.ChatStream(ctx, "openai", "test-model", []Message{{Role: "user", Content: "hi"}}))

	var last StreamChunk
	for _, c := range chunks {
		if c.Err != nil { t.Fatalf("stream error: %v", c.Err) }
		last = c
	}
	if !last.Done { t.Error("final chunk should be Done") }
	if atomic.LoadInt32(&hits) != 1 { t.Errorf("server hits = %d, want 1", hits) }
}

// TestOpenAIStream_HttpError verifies the parser
// surfaces non-2xx responses as a classified error
// instead of silently returning an empty stream.
func TestOpenAIStream_HttpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"rate limited","code":"rate_limit_exceeded"}}`)
	}))
	defer srv.Close()

	c, err := newTestClient("openai", srv.URL)
	if err != nil { t.Fatal(err) }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	chunks := readAll(c.ChatStream(ctx, "openai", "test-model", []Message{{Role: "user", Content: "hi"}}))

	if len(chunks) != 1 { t.Fatalf("got %d chunks, want 1", len(chunks)) }
	if chunks[0].Err == nil { t.Error("expected an error chunk") }
}

// newTestClient builds a minimal Client wired to a
// single OpenAI-compatible provider. Used by the
// parser tests above; bypasses the global config so
// they can run with arbitrary mock servers.
func newTestClient(name, baseURL string) (*Client, error) {
	c := &Client{
		providers: map[string]*providerEntry{
			name: {
				name:     name,
				protocol: "openai",
				model:    "test-model",
				apiKey:   "test-key",
				baseURL:  baseURL,
			},
		},
		default_: name,
		cfgModels: []config.ProviderConfig{{
			Name:     name,
			Protocol: "openai",
			BaseURL:  baseURL,
			APIKey:   "test-key",
			Model:    "test-model",
		}},
	}
	return c, nil
}

// TestExtractContent_FieldNames covers the walker's
// ability to find content in proxies that use any of
// the known candidate field names at any depth. This
// is the regression test for the api-convert.08ms.cn
// proxy that emits a wire shape the standard
// openaiStreamResponse struct can't decode into
// `choices[].delta.content`.
func TestExtractContent_FieldNames(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "standard_delta_content",
			payload: `{"choices":[{"delta":{"content":"hello"}}]}`,
			want:    "hello",
		},
		{
			name:    "legacy_delta_text",
			payload: `{"choices":[{"delta":{"text":"hello"}}]}`,
			want:    "hello",
		},
		{
			name:    "wrapped_envelope",
			payload: `{"data":{"choices":[{"delta":{"content":"hello"}}]}}`,
			want:    "hello",
		},
		{
			name:    "chatml_message_content",
			payload: `{"choices":[{"message":{"content":"hello"}}]}`,
			want:    "hello",
		},
		{
			name:    "top_level_content",
			payload: `{"content":"hello"}`,
			want:    "hello",
		},
		{
			name:    "output_text_field",
			payload: `{"choices":[{"delta":{"output_text":"hello"}}]}`,
			want:    "hello",
		},
		{
			name:    "no_match",
			payload: `{"choices":[{"delta":{"role":"assistant"}}]}`,
			want:    "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := extractContent([]byte(tc.payload))
			if got != tc.want {
				t.Errorf("extractContent(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestExtractThinking_FieldNames(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "deepseek_reasoning_content",
			payload: `{"choices":[{"delta":{"reasoning_content":"thinking..."}}]}`,
			want:    "thinking...",
		},
		{
			name:    "openai_reasoning",
			payload: `{"choices":[{"delta":{"reasoning":"thinking..."}}]}`,
			want:    "thinking...",
		},
		{
			name:    "thoughts_field",
			payload: `{"choices":[{"delta":{"thoughts":"thinking..."}}]}`,
			want:    "thinking...",
		},
		{
			name:    "no_thinking",
			payload: `{"choices":[{"delta":{"content":"hi"}}]}`,
			want:    "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := extractThinking([]byte(tc.payload))
			if got != tc.want {
				t.Errorf("extractThinking(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestOpenAIStream_WrappedEnvelope is the
// end-to-end version of the proxy-envelope test
// (vs. the unit-level extractContent test above).
// The mock server returns a wrapped envelope that
// the standard openaiStreamResponse struct can't
// decode into choices[].delta.content. The walker
// must recover the text and emit it as a Content
// chunk so the user's chat isn't empty.
func TestOpenAIStream_WrappedEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		fmt.Fprintf(w, "data: {\"data\":{\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}}\n\n"); fl.Flush()
		fmt.Fprintf(w, "data: {\"data\":{\"choices\":[{\"delta\":{\"content\":\" world\"}}]}}\n\n"); fl.Flush()
		fmt.Fprintf(w, "data: [DONE]\n\n"); fl.Flush()
	}))
	defer srv.Close()

	c, err := newTestClient("openai", srv.URL)
	if err != nil { t.Fatal(err) }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	chunks := readAll(c.ChatStream(ctx, "openai", "test-model", []Message{{Role: "user", Content: "hi"}}))

	var content strings.Builder
	for _, c := range chunks {
		if c.Err != nil { t.Fatalf("stream error: %v", c.Err) }
		if c.Content != "" { content.WriteString(c.Content) }
	}
	if got := content.String(); got != "hello world" {
		t.Errorf("content = %q, want %q (walker should have recovered it from the wrapped envelope)", got, "hello world")
	}
}
