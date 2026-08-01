package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
)

func TestChatWithToolsTodoWriteDoesNotReenterCheckpointForFollowUpWork(t *testing.T) {
	const sessionID = "todo-resume-checkpoint-test"
	tool.SetSessionTodosMemory(sessionID, []tool.TodoItem{{
		ID:      "edit",
		Content: "edit the file",
		Status:  "in_progress",
	}})
	defer tool.SetSessionTodosMemory(sessionID, nil)

	var requestCount atomic.Int32
	var readCalls atomic.Int32
	var execCalls atomic.Int32
	var toolNamesByRound [3]atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		names := make([]string, 0, len(request.Tools))
		for _, item := range request.Tools {
			names = append(names, item.Function.Name)
		}
		count := requestCount.Add(1)
		switch count {
		case 1, 2, 3:
			toolNamesByRound[count-1].Store(strings.Join(names, ","))
		}

		w.Header().Set("Content-Type", "text/event-stream")
		switch count {
		case 1:
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_todo_progress\",\"type\":\"function\",\"function\":{\"name\":\"todo_write\",\"arguments\":\"{\\\"todos\\\":[{\\\"id\\\":\\\"edit\\\",\\\"content\\\":\\\"edit the file\\\",\\\"status\\\":\\\"in_progress\\\"}]}\"}}]}}]}\n\n")
		case 2:
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_read\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{}\"}}]}}]}\n\n")
		case 3:
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_exec\",\"type\":\"function\",\"function\":{\"name\":\"exec_command\",\"arguments\":\"{}\"}}]}}]}\n\n")
		case 4:
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_todo_done\",\"type\":\"function\",\"function\":{\"name\":\"todo_write\",\"arguments\":\"{\\\"todos\\\":[{\\\"id\\\":\\\"edit\\\",\\\"content\\\":\\\"edit the file\\\",\\\"status\\\":\\\"done\\\"}]}\"}}]}}]}\n\n")
		default:
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
		Limits: config.LimitsConfig{MaxRounds: 6},
	}
	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	registry.Register(tool.Tool{
		Name:       "read_file",
		Parameters: json.RawMessage(`{"type":"object"}`),
	}, func(context.Context, json.RawMessage) (*tool.CallResult, error) {
		readCalls.Add(1)
		return &tool.CallResult{Content: "file read"}, nil
	})
	registry.Register(tool.Tool{
		Name:       "exec_command",
		Parameters: json.RawMessage(`{"type":"object"}`),
	}, func(context.Context, json.RawMessage) (*tool.CallResult, error) {
		execCalls.Add(1)
		return &tool.CallResult{Content: "command ran"}, nil
	})
	registry.Register(tool.Tool{
		Name:       "todo_write",
		Parameters: json.RawMessage(`{"type":"object"}`),
	}, func(ctx context.Context, args json.RawMessage) (*tool.CallResult, error) {
		if strings.Contains(string(args), `"done"`) {
			tool.SetSessionTodosMemory(sessionID, nil)
		} else {
			tool.SetSessionTodosMemory(sessionID, []tool.TodoItem{{
				ID: "edit", Content: "edit the file", Status: "in_progress",
			}})
		}
		return &tool.CallResult{Content: string(args)}, nil
	})

	agent := New(cfg, llmClient, (*style.Manager)(nil), nil, registry)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var sawCheckpointRejection bool
	for chunk := range agent.ChatWithTools(ctx, ChatRequest{
		Style:     style.Off,
		Provider:  "test",
		Model:     "test-model",
		SessionID: sessionID,
		TodoMode:  TodoModeResume,
		Messages: []llm.ChatMessage{{
			Role:    llm.RoleUser,
			Type:    llm.TypeText,
			Content: "continue",
		}},
	}) {
		if strings.Contains(chunk.ToolError, "todo checkpoint only allows") || strings.Contains(chunk.Error, "todo checkpoint only allows") {
			sawCheckpointRejection = true
		}
	}

	if sawCheckpointRejection {
		t.Fatal("resume turn rejected write_file before the work tool could run")
	}
	if got := readCalls.Load(); got != 1 {
		t.Fatalf("read_file calls = %d, want 1", got)
	}
	if got := execCalls.Load(); got != 1 {
		t.Fatalf("exec_command calls = %d, want 1", got)
	}
	for round, names := range toolNamesByRound {
		got := names.Load()
		if got == nil || !strings.Contains(got.(string), "read_file") || !strings.Contains(got.(string), "exec_command") {
			t.Fatalf("round %d tools = %v, want normal work tools after todo_write", round+1, got)
		}
	}
}

func TestChatWithToolsChecksTodoWhenWorkPhaseEnds(t *testing.T) {
	const sessionID = "todo-phase-end-checkpoint-test"
	tool.SetSessionTodosMemory(sessionID, []tool.TodoItem{{
		ID: "edit", Content: "edit the file", Status: "in_progress",
	}})
	defer tool.SetSessionTodosMemory(sessionID, nil)

	var requestCount atomic.Int32
	var toolsByRequest [3]atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		names := make([]string, 0, len(request.Tools))
		for _, item := range request.Tools {
			names = append(names, item.Function.Name)
		}
		count := requestCount.Add(1)
		if count <= int32(len(toolsByRequest)) {
			toolsByRequest[count-1].Store(strings.Join(names, ","))
		}

		w.Header().Set("Content-Type", "text/event-stream")
		switch count {
		case 1:
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_read\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{}\"}}]}}]}\n\n")
		case 2:
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"the edit is complete\"}}]}\n\n")
		case 3:
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_todo_done\",\"type\":\"function\",\"function\":{\"name\":\"todo_write\",\"arguments\":\"{\\\"todos\\\":[{\\\"id\\\":\\\"edit\\\",\\\"content\\\":\\\"edit the file\\\",\\\"status\\\":\\\"done\\\"}]}\"}}]}}]}\n\n")
		default:
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cfg := &config.Config{
		LLM: config.LLMConfig{Default: "test", Providers: []config.ProviderConfig{{
			Name: "test", Protocol: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test-model",
		}}},
		Limits: config.LimitsConfig{MaxRounds: 6},
	}
	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	registry.Register(tool.Tool{Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`)}, func(context.Context, json.RawMessage) (*tool.CallResult, error) {
		return &tool.CallResult{Content: "file read"}, nil
	})
	registry.Register(tool.Tool{Name: "todo_write", Parameters: json.RawMessage(`{"type":"object"}`)}, func(context.Context, json.RawMessage) (*tool.CallResult, error) {
		tool.SetSessionTodosMemory(sessionID, nil)
		return &tool.CallResult{Content: "updated"}, nil
	})
	registry.Register(tool.Tool{Name: "question", Parameters: json.RawMessage(`{"type":"object"}`)}, func(context.Context, json.RawMessage) (*tool.CallResult, error) {
		return &tool.CallResult{Content: "unused"}, nil
	})

	a := New(cfg, llmClient, (*style.Manager)(nil), nil, registry)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for range a.ChatWithTools(ctx, ChatRequest{
		Style: style.Off, Provider: "test", Model: "test-model", SessionID: sessionID,
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Type: llm.TypeText, Content: "continue"}},
	}) {
	}

	if got := toolsByRequest[1].Load(); got == nil || !strings.Contains(got.(string), "read_file") {
		t.Fatalf("request 2 tools = %v, want normal work tools before the model ends its work phase", got)
	}
	if got := toolsByRequest[2].Load(); got == nil || !strings.Contains(got.(string), "todo_write") || !strings.Contains(got.(string), "question") || strings.Contains(got.(string), "read_file") {
		t.Fatalf("request 3 tools = %v, want todo-only checkpoint after the model ended its work phase", got)
	}
}

func TestAdaptiveTodoLongRunBypassesRoundCapWhilePlanIsActive(t *testing.T) {
	const sessionID = "todo-long-run-round-cap-test"
	tool.SetSessionTodosMemory(sessionID, []tool.TodoItem{{
		ID: "work", Content: "finish work", Status: "in_progress",
	}})
	defer tool.SetSessionTodosMemory(sessionID, nil)

	var requestCount atomic.Int32
	var workCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		switch requestCount.Load() {
		case 1:
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_work\",\"type\":\"function\",\"function\":{\"name\":\"write_file\",\"arguments\":\"{}\"}}]}}]}\n\n")
		case 2:
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_todo\",\"type\":\"function\",\"function\":{\"name\":\"todo_write\",\"arguments\":\"{\\\"todos\\\":[{\\\"id\\\":\\\"work\\\",\\\"content\\\":\\\"finish work\\\",\\\"status\\\":\\\"done\\\"}]}\"}}]}}]}\n\n")
		default:
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cfg := &config.Config{
		LLM: config.LLMConfig{Default: "test", Providers: []config.ProviderConfig{{
			Name: "test", Protocol: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test-model",
		}}},
		Limits: config.LimitsConfig{MaxRounds: 1, TodoLongRunMode: config.TodoLongRunAdaptive},
	}
	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	registry.Register(tool.Tool{Name: "write_file", Parameters: json.RawMessage(`{"type":"object"}`)}, func(context.Context, json.RawMessage) (*tool.CallResult, error) {
		workCalls.Add(1)
		return &tool.CallResult{Content: "written"}, nil
	})
	registry.Register(tool.Tool{Name: "todo_write", Parameters: json.RawMessage(`{"type":"object"}`)}, func(context.Context, json.RawMessage) (*tool.CallResult, error) {
		tool.SetSessionTodosMemory(sessionID, nil)
		return &tool.CallResult{Content: "updated"}, nil
	})

	a := New(cfg, llmClient, (*style.Manager)(nil), nil, registry)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for range a.ChatWithTools(ctx, ChatRequest{
		Style: style.Off, Provider: "test", Model: "test-model", SessionID: sessionID,
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Type: llm.TypeText, Content: "continue"}},
	}) {
	}

	if got := workCalls.Load(); got != 1 {
		t.Fatalf("work calls = %d, want 1; adaptive mode should bypass the one-round cap", got)
	}
	if got := requestCount.Load(); got < 3 {
		t.Fatalf("LLM requests = %d, want work + checkpoint + final summary", got)
	}
}
