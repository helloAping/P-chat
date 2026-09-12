package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
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

func TestChatPrefixSurvivesTodoUpdatesAndHistoryReload(t *testing.T) {
	store, err := memory.OpenAt(":memory:", 50)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := upgrade.SeedForTesting(store.DB()); err != nil {
		t.Fatal(err)
	}
	session, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	tool.SetSessionTodosMemory(session, []tool.TodoItem{{ID: "a", Content: "inspect", Status: "pending"}})
	defer tool.SetSessionTodosMemory(session, nil)
	requests := make(chan []json.RawMessage, 8)
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		// 比较协议消息内容；JSON 对象键序不属于模型提示词。
		// Compare wire message content; JSON object key order is not part of the model prompt.
		for i, raw := range request.Messages {
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Error(err)
				return
			}
			request.Messages[i], _ = json.Marshal(value)
		}
		requests <- request.Messages
		n := count.Add(1)
		call := func(index int, id, name, args string) map[string]any {
			return map[string]any{"index": index, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": args}}
		}
		delta := map[string]any{"content": "done", "reasoning_content": "finished"}
		if n == 1 {
			delta = map[string]any{"content": "Inspecting.", "reasoning_content": "inspect the file", "tool_calls": []any{
				call(1, "read", "read_file", `{}`), call(0, "start", "todo_write", `{"status":"in_progress"}`),
			}}
		} else if n == 2 {
			delta = map[string]any{"content": "Checked.", "reasoning_content": "mark complete", "tool_calls": []any{call(0, "finish", "todo_write", `{"status":"done"}`)}}
		}
		payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta}}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":20,\"prompt_cache_hit_tokens\":850,\"prompt_cache_miss_tokens\":150}}\n\ndata: [DONE]\n\n", payload)
	}))
	defer srv.Close()
	cfg := &config.Config{LLM: config.LLMConfig{Default: "test", Providers: []config.ProviderConfig{{Name: "test", Protocol: "openai", BaseURL: srv.URL, Model: "deepseek-flash"}}}, Limits: config.LimitsConfig{MaxRounds: 8}}
	client, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	registry.Register(tool.Tool{Name: "todo_write", Parameters: json.RawMessage(`{"type":"object"}`)}, func(_ context.Context, args json.RawMessage) (*tool.CallResult, error) {
		status := "in_progress"
		if strings.Contains(string(args), "done") {
			status = "done"
		}
		tool.SetSessionTodosMemory(session, []tool.TodoItem{{ID: "a", Content: "inspect", Status: status}})
		return &tool.CallResult{Content: string(args)}, nil
	})
	registry.Register(tool.Tool{Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`)}, func(context.Context, json.RawMessage) (*tool.CallResult, error) {
		return &tool.CallResult{Content: "file contents"}, nil
	})
	agt := New(cfg, client, nil, store, registry)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	user := llm.ChatMessage{Role: llm.RoleUser, Type: llm.TypeText, Content: "inspect", MsgType: llm.MsgTypeText, SubmitToLLM: 1}
	req := ChatRequest{Style: style.Off, Provider: "test", Model: "deepseek-flash", SessionID: session, Messages: []llm.ChatMessage{user}}
	var totalIn, totalOut int
	for chunk := range agt.ChatWithTools(ctx, req) {
		if chunk.Error != "" {
			t.Fatalf("agent error: %s", chunk.Error)
		}
		totalIn = max(totalIn, chunk.TokensIn)
		totalOut = max(totalOut, chunk.TokensOut)
	}
	if count.Load() != 3 || totalIn != 3000 || totalOut != 60 {
		t.Fatalf("requests=%d usage=%d/%d", count.Load(), totalIn, totalOut)
	}
	history := store.GetChatMessagesFor(session, 0)
	req.HistoryMessageCount = len(history)
	req.Messages = append(history, llm.ChatMessage{Role: llm.RoleUser, Type: llm.TypeText, Content: "continue", MsgType: llm.MsgTypeText, SubmitToLLM: 1})
	for chunk := range agt.ChatWithTools(ctx, req) {
		if chunk.Error != "" {
			t.Fatalf("reload error: %s", chunk.Error)
		}
	}
	if count.Load() != 4 {
		t.Fatalf("requests=%d", count.Load())
	}
	var previous []json.RawMessage
	for i := 0; i < 4; i++ {
		current := <-requests
		if len(previous) > 0 && (len(current) < len(previous) || !reflect.DeepEqual(previous, current[:len(previous)])) {
			t.Fatalf("request %d rewrote its prefix\nprevious=%s\ncurrent=%s", i+1, previous, current)
		}
		if i == 1 {
			var wire []map[string]any
			encoded, _ := json.Marshal(current)
			_ = json.Unmarshal(encoded, &wire)
			found := false
			for _, msg := range wire {
				if calls, ok := msg["tool_calls"].([]any); ok && len(calls) == 2 {
					found = true
					if calls[0].(map[string]any)["id"] != "start" || msg["reasoning_content"] != "inspect the file" {
						t.Fatalf("assistant replay = %v", msg)
					}
				}
			}
			if !found {
				t.Fatal("missing parallel tool calls")
			}
		}
		previous = current
	}
}

func TestRuntimeContextClearingAndCompaction(t *testing.T) {
	msgs := []llm.ChatMessage{{Role: llm.RoleSystem, Content: "static"}}
	appendRuntimeContext(&msgs, "skills", "skill A")
	appendRuntimeContext(&msgs, "skills", "skill B")
	if !appendRuntimeContext(&msgs, "skills", "") {
		t.Fatal("expected clear event")
	}
	if appendRuntimeContext(&msgs, "skills", "") {
		t.Fatal("repeated clear appended")
	}
	snapshots := latestRuntimeContexts(msgs)
	if len(snapshots) != 1 {
		t.Fatalf("snapshots=%d", len(snapshots))
	}
	compacted := []llm.ChatMessage{{Role: llm.RoleSystem, Content: "static + summary"}, {Role: llm.RoleUser, Content: "continue"}}
	(&Agent{}).restoreRuntimeContexts(&compacted, ChatRequest{}, snapshots)
	if len(compacted) != 3 || compacted[2].Content != snapshots[0].Content {
		t.Fatal("compaction lost current context")
	}
}
