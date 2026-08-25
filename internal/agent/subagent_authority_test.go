package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
)

func TestSubagentToolAuthorizationAllowsProjectRead(t *testing.T) {
	result, handled := subagentToolAuthorizationResult(
		ChatRequest{SubagentType: "explore", ProjectRoot: `D:\projects\app`},
		nativeToolCall{Name: "read_file", ArgsJSON: `{"path":"main.go"}`},
		tool.Tool{Name: "read_file"},
		&stubSandboxForConfirm{readDecision: tool.SandboxAllow},
	)
	if handled || result != nil {
		t.Fatalf("project read was handled=%v result=%#v, want pass-through", handled, result)
	}
}

func TestSubagentToolAuthorizationDelegatesConfirmToParent(t *testing.T) {
	result, handled := subagentToolAuthorizationResult(
		ChatRequest{SubagentType: "explore", ProjectRoot: `D:\projects\app`},
		nativeToolCall{Name: "read_file", ArgsJSON: `{"path":"D:/outside/secret.txt"}`},
		tool.Tool{Name: "read_file"},
		&stubSandboxForConfirm{readDecision: tool.SandboxConfirm},
	)
	if !handled || result == nil {
		t.Fatal("outside read was not intercepted")
	}
	if !result.IsError || result.Status != tool.CallStatusWaiting || !result.RequiresUser {
		t.Fatalf("result = %#v, want waiting error requiring parent approval", result)
	}
}

func TestSubagentToolAuthorizationBlocksSandboxBlock(t *testing.T) {
	result, handled := subagentToolAuthorizationResult(
		ChatRequest{SubagentType: "explore", ProjectRoot: `D:\projects\app`},
		nativeToolCall{Name: "read_file", ArgsJSON: `{"path":"D:/protected/key"}`},
		tool.Tool{Name: "read_file"},
		&stubSandboxForConfirm{readDecision: tool.SandboxBlock},
	)
	if !handled || result == nil {
		t.Fatal("sandbox block was not intercepted")
	}
	if result.Status != tool.CallStatusBlocked || !result.IsError {
		t.Fatalf("result = %#v, want blocked error", result)
	}
}

func TestSubagentToolAuthorizationDelegatesExecEvenWhenSandboxAllows(t *testing.T) {
	result, handled := subagentToolAuthorizationResult(
		ChatRequest{SubagentType: "explore", ProjectRoot: `D:\projects\app`},
		nativeToolCall{Name: "exec_command", ArgsJSON: `{"command":"git status --short"}`},
		tool.Tool{Name: "exec_command"},
		&stubSandboxForConfirm{execDecision: tool.SandboxAllow},
	)
	if !handled || result == nil {
		t.Fatal("exec_command was not intercepted")
	}
	if result.Status != tool.CallStatusWaiting || !result.RequiresUser {
		t.Fatalf("result = %#v, want parent approval waiting result", result)
	}
}

func TestSubagentToolAuthorizationAllowsGrepAndDocs(t *testing.T) {
	for _, name := range []string{"grep", "read_docx", "read_pdf"} {
		result, handled := subagentToolAuthorizationResult(
			ChatRequest{SubagentType: "explore", ProjectRoot: `D:\projects\app`},
			nativeToolCall{Name: name, ArgsJSON: `{"path":"docs","pattern":"TODO"}`},
			tool.Tool{Name: name},
			&stubSandboxForConfirm{readDecision: tool.SandboxAllow},
		)
		if handled || result != nil {
			t.Fatalf("%s was handled=%v result=%#v, want pass-through", name, handled, result)
		}
	}
}

func TestSubagentToolAuthorizationAllowsWebSearch(t *testing.T) {
	result, handled := subagentToolAuthorizationResult(
		ChatRequest{SubagentType: "general-purpose", ProjectRoot: `D:\projects\app`},
		nativeToolCall{Name: "web_search", ArgsJSON: `{"query":"go context timeout"}`},
		tool.Tool{Name: "web_search"},
		nil,
	)
	if handled || result != nil {
		t.Fatalf("web_search was handled=%v result=%#v, want pass-through", handled, result)
	}
}

func TestSubagentToolAuthorizationAllowsPublicWebFetch(t *testing.T) {
	cases := []string{
		`{"url":"https://example.com/docs"}`,
		`{"url":"https://example.com/api","method":"POST","body":"{}"}`,
	}
	for _, args := range cases {
		result, handled := subagentToolAuthorizationResult(
			ChatRequest{SubagentType: "general-purpose", ProjectRoot: `D:\projects\app`},
			nativeToolCall{Name: "web_fetch", ArgsJSON: args},
			tool.Tool{Name: "web_fetch"},
			nil,
		)
		if handled || result != nil {
			t.Fatalf("web_fetch %s was handled=%v result=%#v, want pass-through", args, handled, result)
		}
	}
}

func TestSubagentToolAuthorizationBlocksPrivateWebFetch(t *testing.T) {
	result, handled := subagentToolAuthorizationResult(
		ChatRequest{SubagentType: "general-purpose", ProjectRoot: `D:\projects\app`},
		nativeToolCall{Name: "web_fetch", ArgsJSON: `{"url":"https://127.0.0.1/secret"}`},
		tool.Tool{Name: "web_fetch"},
		nil,
	)
	if !handled || result == nil {
		t.Fatal("loopback web_fetch was not intercepted")
	}
	if result.Status != tool.CallStatusBlocked || !result.IsError {
		t.Fatalf("result = %#v, want blocked error", result)
	}
}

func TestChatWithToolsSubagentDoesNotExecuteWriteWithFullPermission(t *testing.T) {
	var requests atomic.Int32
	writeArgs, err := json.Marshal(map[string]string{"path": "owned.txt", "content": "nope"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 1 {
			args := strconv.Quote(string(writeArgs))
			_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_write\",\"type\":\"function\",\"function\":{\"name\":\"write_file\",\"arguments\":%s}}]}}]}\n\n", args)
		} else {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
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
	var writeCalls atomic.Int32
	registry := tool.NewRegistry()
	registry.Register(tool.Tool{
		Name:       "write_file",
		Parameters: json.RawMessage(`{"type":"object"}`),
	}, func(context.Context, json.RawMessage) (*tool.CallResult, error) {
		writeCalls.Add(1)
		return &tool.CallResult{Content: "written"}, nil
	})

	agent := New(cfg, llmClient, (*style.Manager)(nil), nil, registry)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var sawParentApproval bool
	for chunk := range agent.ChatWithTools(ctx, ChatRequest{
		Style:           style.Off,
		Provider:        "test",
		Model:           "test-model",
		SessionID:       "subagent-auth-integration",
		SubagentType:    "explore",
		PermissionLevel: tool.PermissionFull,
		Messages:        []llm.ChatMessage{{Role: llm.RoleUser, Type: llm.TypeText, Content: "write"}},
	}) {
		if chunk.ToolName == "write_file" && chunk.ToolCallStatus == string(tool.CallStatusWaiting) && chunk.ToolRequiresUser {
			sawParentApproval = true
		}
	}

	if got := writeCalls.Load(); got != 0 {
		t.Fatalf("write_file handler calls = %d, want 0", got)
	}
	if !sawParentApproval {
		t.Fatal("did not emit waiting parent-approval tool result")
	}
}
