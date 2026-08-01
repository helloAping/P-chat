package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
)

func TestChatWithToolsSerializesMutatingToolCalls(t *testing.T) {
	var requests atomic.Int32
	var running atomic.Int32
	var overlapped atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if count == 1 {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_a\",\"type\":\"function\",\"function\":{\"name\":\"write_file\",\"arguments\":\"{\\\"id\\\":\\\"a\\\"}\"}},{\"index\":1,\"id\":\"call_b\",\"type\":\"function\",\"function\":{\"name\":\"write_file\",\"arguments\":\"{\\\"id\\\":\\\"b\\\"}\"}}]}}]}\n\n")
		} else {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cfg := &config.Config{
		LLM: config.LLMConfig{
			Default: "test",
			Providers: []config.ProviderConfig{{
				Name: "test", Protocol: "openai", BaseURL: srv.URL,
				APIKey: "test-key", Model: "test-model",
			}},
		},
		Limits: config.LimitsConfig{MaxRounds: 2},
	}
	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	registry.Register(tool.Tool{
		Name:       "write_file",
		Parameters: json.RawMessage(`{"type":"object"}`),
	}, func(context.Context, json.RawMessage) (*tool.CallResult, error) {
		if running.Add(1) > 1 {
			overlapped.Store(true)
		}
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
		return &tool.CallResult{
			Content:      "written",
			Summary:      "Updated test file",
			ChangedPaths: []string{"test.txt"},
			NextAction:   "verify",
		}, nil
	})

	agent := New(cfg, llmClient, (*style.Manager)(nil), nil, registry)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var structuredResultSeen bool
	for chunk := range agent.ChatWithTools(ctx, ChatRequest{
		Style:    style.Off,
		Provider: "test",
		Model:    "test-model",
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Type: llm.TypeText, Content: "write both"}},
	}) {
		if chunk.ToolName == "write_file" && chunk.ToolCallStatus == "ok" && chunk.ToolSummary == "Updated test file" && len(chunk.ToolChangedPaths) == 1 && chunk.ToolChangedPaths[0] == "test.txt" && chunk.ToolNextAction == "verify" {
			structuredResultSeen = true
		}
	}

	if overlapped.Load() {
		t.Fatal("mutating write_file calls overlapped")
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("LLM requests = %d, want 2", got)
	}
	if !structuredResultSeen {
		t.Fatal("agent did not emit the structured tool result")
	}
}
