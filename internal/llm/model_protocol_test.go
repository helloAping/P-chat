package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/p-chat/pchat/internal/config"
)

func TestChatCMUsesPerModelProtocolOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/chat/completions":
			if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
				t.Errorf("OpenAI Authorization = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{"content": "openai"}}},
			})
		case "/messages":
			if got := r.Header.Get("x-api-key"); got != "test-key" {
				t.Errorf("Anthropic x-api-key = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "anthropic"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(&config.LLMConfig{
		Default: "mixed",
		Providers: []config.ProviderConfig{{
			Name: "mixed", Protocol: config.ProtocolOpenAIChat, BaseURL: server.URL, APIKey: "test-key",
			Models: []config.ModelConfig{
				{Name: "gpt", Default: true},
				{Name: "claude", Protocol: config.ProtocolAnthropicMessages},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	message := []ChatMessage{{Role: RoleUser, Type: TypeText, Content: "hello"}}
	if got, err := client.ChatCM(context.Background(), "mixed", "gpt", message, ChatOptions{}); err != nil || got != "openai" {
		t.Fatalf("inherited protocol result = %q, err=%v", got, err)
	}
	if got, err := client.ChatCM(context.Background(), "mixed", "claude", message, ChatOptions{}); err != nil || got != "anthropic" {
		t.Fatalf("overridden protocol result = %q, err=%v", got, err)
	}
}

func TestChatStreamCMUsesPerModelProtocolOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Errorf("request path = %q, want /messages", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("Anthropic x-api-key = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"streamed\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	client, err := NewClient(&config.LLMConfig{
		Default: "mixed",
		Providers: []config.ProviderConfig{{
			Name: "mixed", Protocol: config.ProtocolOpenAIChat, BaseURL: server.URL, APIKey: "test-key",
			Models: []config.ModelConfig{
				{Name: "gpt", Default: true},
				{Name: "claude", Protocol: config.ProtocolAnthropicMessages},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	chunks := readAll(client.ChatStreamCM(context.Background(), "mixed", "claude", []ChatMessage{{Role: RoleUser, Type: TypeText, Content: "hello"}}, nil, ChatOptions{}))
	var content string
	for _, chunk := range chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		content += chunk.Content
	}
	if content != "streamed" {
		t.Fatalf("streamed content = %q", content)
	}
}

func TestModelProtocolConfigPersistsOverrideAndClear(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	initial := `{"llm":{"providers":[{"name":"mixed","protocol":"openai_chat","base_url":"https://example.com/v1","api_key":"k","models":[{"name":"chat","api_endpoint":"/chat/completions"}]}]}}`
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	override := config.ProtocolAnthropicMessages
	updated, err := config.UpdateModelProtocolAt(configPath, "mixed", "chat", config.ModelConfig{}, &override, false)
	if err != nil {
		t.Fatal(err)
	}
	model := updated.FindModel("chat")
	if model.Protocol != config.ProtocolAnthropicMessages || model.APIEndpoint != "/messages" {
		t.Fatalf("stored override = %#v", model)
	}

	inherit := ""
	updated, err = config.UpdateModelProtocolAt(configPath, "mixed", "chat", config.ModelConfig{}, &inherit, false)
	if err != nil {
		t.Fatal(err)
	}
	model = updated.FindModel("chat")
	if model.Protocol != "" || model.APIEndpoint != "/chat/completions" {
		t.Fatalf("cleared override = %#v", model)
	}
}
