package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
	"github.com/p-chat/pchat/internal/upgrade"
)

func TestChatWithTools_CancelDoesNotWaitForBlockedTool(t *testing.T) {
	toolStarted := make(chan struct{})
	releaseTool := make(chan struct{})
	defer close(releaseTool)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_block\",\"type\":\"function\",\"function\":{\"name\":\"blocking_tool\",\"arguments\":\"{}\"}}]}}]}\n\n")
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
	registry.Register(tool.Tool{
		Name:       "blocking_tool",
		Parameters: json.RawMessage(`{"type":"object"}`),
	}, func(context.Context, json.RawMessage) (*tool.CallResult, error) {
		close(toolStarted)
		<-releaseTool // Deliberately ignores cancellation to exercise the dispatcher.
		return &tool.CallResult{Content: "released"}, nil
	})

	agt := New(cfg, llmClient, styleMgr, store, registry)
	ctx, cancel := context.WithCancel(context.Background())
	stream := agt.ChatWithTools(ctx, ChatRequest{
		Style: style.Tech, Provider: "test", Model: "test-model", SessionID: "cancel-tool-test",
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Type: llm.TypeText, Content: "start"}},
	})
	streamClosed := make(chan struct{})
	go func() {
		for range stream {
		}
		close(streamClosed)
	}()

	select {
	case <-toolStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("blocking tool did not start")
	}
	cancel()

	select {
	case <-streamClosed:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("agent stream stayed open after cancellation while a tool ignored its context")
	}
}
