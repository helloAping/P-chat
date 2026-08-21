package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
	"github.com/p-chat/pchat/internal/upgrade"
)

func TestBuildSubagentGuardPrompt_ScopesChildAgent(t *testing.T) {
	got := buildSubagentGuardPrompt("explore", "task-123")
	for _, want := range []string{
		"Sub-Agent Execution Contract",
		"sub-agent explore",
		"Task id: task-123",
		"Stay inside the assigned sub-task",
		"Every tool call must directly advance",
		"return the best partial result",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("guard prompt missing %q:\n%s", want, got)
		}
	}
}

func TestBuildSubagentGuardPrompt_ParentChatsGetNoGuard(t *testing.T) {
	if got := buildSubagentGuardPrompt("", "task-123"); got != "" {
		t.Fatalf("parent chat guard = %q, want empty", got)
	}
}

func TestSubagentProgressReminderDue(t *testing.T) {
	cases := []struct {
		round int
		want  bool
	}{
		{round: 1, want: false},
		{round: 49, want: false},
		{round: 50, want: true},
		{round: 75, want: false},
		{round: 100, want: true},
		{round: 150, want: true},
	}
	for _, tc := range cases {
		if got := subagentProgressReminderDue(tc.round); got != tc.want {
			t.Fatalf("subagentProgressReminderDue(%d) = %v, want %v", tc.round, got, tc.want)
		}
	}
}

func TestBuildSubagentProgressReminder(t *testing.T) {
	got := buildSubagentProgressReminder(ChatRequest{
		SubagentType:   "plan",
		SubagentTaskID: "task-xyz",
	}, 50)
	for _, want := range []string{
		"round 50",
		"sub-agent plan",
		"task_id: task-xyz",
		"smallest remaining evidence/action",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress reminder missing %q:\n%s", want, got)
		}
	}
}

func TestChatWithTools_SubagentGuardAppendsToPromptOverride(t *testing.T) {
	bodyCh := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodyCh <- string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cfg := &config.Config{
		LLM: config.LLMConfig{
			Default: "test",
			Providers: []config.ProviderConfig{{
				Name:     "test",
				Protocol: "openai",
				BaseURL:  srv.URL,
				APIKey:   "test-key",
				Model:    "test-model",
			}},
		},
		Limits: config.LimitsConfig{MaxRounds: 1},
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
	agt := New(cfg, llmClient, styleMgr, store, tool.NewRegistry())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for range agt.ChatWithTools(ctx, ChatRequest{
		Style:          style.Tech,
		Provider:       "test",
		Model:          "test-model",
		SessionID:      "subagent-guard-test",
		PromptOv:       "custom prompt body",
		SubagentType:   "custom-agent",
		SubagentTaskID: "task-abc",
		Messages: []llm.ChatMessage{{
			Role:        llm.RoleUser,
			Type:        llm.TypeText,
			Content:     "inspect",
			MsgType:     llm.MsgTypeText,
			SubmitToLLM: 1,
		}},
	}) {
	}

	var body string
	select {
	case body = <-bodyCh:
	default:
		t.Fatal("mock LLM did not receive a request")
	}
	for _, want := range []string{
		"custom prompt body",
		"Sub-Agent Execution Contract",
		"sub-agent custom-agent",
		"Task id: task-abc",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("LLM request body missing %q:\n%s", want, body)
		}
	}
}
