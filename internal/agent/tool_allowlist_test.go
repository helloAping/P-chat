package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
	"github.com/p-chat/pchat/internal/upgrade"
)

func TestChatWithToolsAllowedToolsBlocksHiddenToolCalls(t *testing.T) {
	var called atomic.Bool
	var advertised atomic.Value
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requests.Add(1)
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		names := make([]string, 0, len(request.Tools))
		for _, item := range request.Tools {
			names = append(names, item.Function.Name)
		}
		if count == 1 {
			advertised.Store(strings.Join(names, ","))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if count == 1 {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_danger\",\"type\":\"function\",\"function\":{\"name\":\"dangerous_tool\",\"arguments\":\"{}\"}}]}}]}\n\n")
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
				Name: "test", Protocol: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test-model",
			}},
		},
		Limits: config.LimitsConfig{MaxRounds: 2},
	}
	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.OpenAt(":memory:", 50)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := upgrade.SeedForTesting(store.DB()); err != nil {
		t.Fatal(err)
	}
	styleMgr, err := style.NewManager(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	registry.RegisterForTest(tool.Tool{Name: "read_file", Description: "read"})
	registry.Register(tool.Tool{Name: "dangerous_tool", Description: "danger"}, func(context.Context, json.RawMessage) (*tool.CallResult, error) {
		called.Store(true)
		return &tool.CallResult{Content: "danger executed"}, nil
	})

	agt := New(cfg, llmClient, styleMgr, store, registry)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var sawBlocked bool
	for chunk := range agt.ChatWithTools(ctx, ChatRequest{
		Style:        style.Tech,
		Provider:     "test",
		Model:        "test-model",
		SessionID:    "im-allowlist-test",
		AllowedTools: []string{"read_file"},
		Messages:     []llm.ChatMessage{{Role: llm.RoleUser, Type: llm.TypeText, Content: "run danger"}},
	}) {
		if chunk.ToolName == "dangerous_tool" && chunk.ToolCallStatus == string(tool.CallStatusBlocked) && strings.Contains(chunk.ToolError, "当前会话关闭") {
			sawBlocked = true
		}
	}

	if called.Load() {
		t.Fatal("hidden dangerous_tool was executed")
	}
	if !sawBlocked {
		t.Fatal("hidden tool call was not reported as blocked")
	}
	if got, _ := advertised.Load().(string); got != "read_file" {
		t.Fatalf("advertised tools = %q, want read_file only", got)
	}
}

func TestChatWithToolsAllowedToolsAcceptsHiddenCompatibilityAlias(t *testing.T) {
	var advertised atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		names := make([]string, 0, len(request.Tools))
		for _, item := range request.Tools {
			names = append(names, item.Function.Name)
		}
		advertised.Store(strings.Join(names, ","))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cfg := &config.Config{
		LLM: config.LLMConfig{Default: "test", Providers: []config.ProviderConfig{{
			Name: "test", Protocol: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test-model",
		}}},
		Limits: config.LimitsConfig{MaxRounds: 2},
	}
	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	registry.RegisterForTest(tool.Tool{Name: "canonical_tool", Description: "canonical capability"})
	registry.RegisterAlias("legacy_tool", "canonical_tool")

	agt := New(cfg, llmClient, (*style.Manager)(nil), nil, registry)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for range agt.ChatWithTools(ctx, ChatRequest{
		Style:        style.Off,
		Provider:     "test",
		Model:        "test-model",
		AllowedTools: []string{"legacy_tool"},
		Messages:     []llm.ChatMessage{{Role: llm.RoleUser, Type: llm.TypeText, Content: "inspect image"}},
	}) {
	}

	if got, _ := advertised.Load().(string); got != "canonical_tool" {
		t.Fatalf("advertised tools = %q, want canonical_tool", got)
	}
}

func TestChatWithToolsDispatchesHiddenCompatibilityAlias(t *testing.T) {
	var requests atomic.Int32
	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if count == 1 {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_legacy\",\"type\":\"function\",\"function\":{\"name\":\"legacy_tool\",\"arguments\":\"{}\"}}]}}]}\n\n")
		} else {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cfg := &config.Config{
		LLM: config.LLMConfig{Default: "test", Providers: []config.ProviderConfig{{
			Name: "test", Protocol: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test-model",
		}}},
		Limits: config.LimitsConfig{MaxRounds: 2},
	}
	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	registry.Register(tool.Tool{Name: "canonical_tool", Description: "canonical capability"}, func(context.Context, json.RawMessage) (*tool.CallResult, error) {
		called.Store(true)
		return &tool.CallResult{Content: "legacy accepted"}, nil
	})
	registry.RegisterAlias("legacy_tool", "canonical_tool")

	agt := New(cfg, llmClient, (*style.Manager)(nil), nil, registry)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for range agt.ChatWithTools(ctx, ChatRequest{
		Style:    style.Off,
		Provider: "test",
		Model:    "test-model",
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Type: llm.TypeText, Content: "use legacy call"}},
	}) {
	}

	if !called.Load() {
		t.Fatal("hidden compatibility alias was not dispatched to its canonical handler")
	}
}
