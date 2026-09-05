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
	"github.com/p-chat/pchat/internal/skill"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
)

func TestChatWithTools_TaskContinuesPastTurnDeadlineOnAbortContext(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if count == 1 {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_task\",\"type\":\"function\",\"function\":{\"name\":\"task\",\"arguments\":\"{\\\"description\\\":\\\"slow inspect\\\"}\"}}]}}]}\n\n")
		} else {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"summary after task\"}}]}\n\n")
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
	var sawDeadline atomic.Bool
	registry.Register(tool.Tool{
		Name:       "task",
		Parameters: json.RawMessage(`{"type":"object"}`),
	}, func(ctx context.Context, _ json.RawMessage) (*tool.CallResult, error) {
		if _, ok := ctx.Deadline(); ok {
			sawDeadline.Store(true)
		}
		time.Sleep(700 * time.Millisecond)
		return &tool.CallResult{Content: "slow task result"}, nil
	})

	agt := New(cfg, llmClient, (*style.Manager)(nil), nil, registry)
	// 本测试只验证 task 的截止时间分离；注入空 Skill 目录以隔离用户级磁盘扫描耗时。
	// This test covers task deadline detachment only; isolate user Skill catalog I/O.
	agt.SetSkillManager(&recordingSkillManager{load: func(skill.LoadRequest) (skill.LoadedSkill, error) {
		return skill.LoadedSkill{}, nil
	}})
	turnCtx, turnCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer turnCancel()
	abortCtx, abortCancel := context.WithCancel(context.Background())
	defer abortCancel()

	var got strings.Builder
	for chunk := range agt.ChatWithTools(WithAbortContext(turnCtx, abortCtx), ChatRequest{
		Style: style.Off, Provider: "test", Model: "test-model",
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Type: llm.TypeText, Content: "run slow task"}},
	}) {
		got.WriteString(chunk.Content)
	}

	if !strings.Contains(got.String(), "summary after task") {
		t.Fatalf("final summary missing after slow task; got %q, requests=%d, sawDeadline=%v", got.String(), requests.Load(), sawDeadline.Load())
	}
	if sawDeadline.Load() {
		t.Fatal("task handler inherited the parent turn deadline")
	}
	if gotReq := requests.Load(); gotReq != 2 {
		t.Fatalf("LLM requests = %d, want 2", gotReq)
	}
}
