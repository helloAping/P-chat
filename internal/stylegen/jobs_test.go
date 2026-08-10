package stylegen

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
)

// contextTimeout returns a cancellable context with a deadline, mirroring
// the real CLI/server usage.
func contextTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// jobTestDeps builds isolated deps (with a conversation) for JobManager
// tests, reusing the scaffolding from generate_test.go.
func jobTestDeps(t *testing.T, llmClient LLMClient) (Deps, string) {
	t.Helper()
	store, err := memory.OpenAt(filepath.Join(t.TempDir(), "test.db"), 100)
	if err != nil {
		t.Fatalf("memory.OpenAt: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	styleMgr, err := style.NewManager(store.DB())
	if err != nil {
		t.Fatalf("style.NewManager: %v", err)
	}
	seedStyles(t, store.DB())
	convID, err := store.NewConversation()
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	store.AddChatMessageTo(convID, textMsg(llm.RoleUser, "帮我写一个排序算法"))
	_ = store.Flush()
	return Deps{
		Store:    store,
		StyleMgr: styleMgr,
		LLM:      llmClient,
		Provider: "test-provider",
		MaxChars: 100000,
	}, convID
}

func TestJobManager_SubscribeReceivesEventsAndCloses(t *testing.T) {
	deps, convID := jobTestDeps(t, llmText(`{"id":"job-style","prompt":"# 任务风格","memory":""}`))
	jm := NewJobManager(deps)
	defer jm.Stop()

	jobID := jm.Start(Params{Mode: "create", Label: "任务风格", ConversationID: convID})
	if jobID == "" {
		t.Fatal("Start returned empty id")
	}

	ch, ok := jm.Subscribe(jobID)
	if !ok {
		t.Fatal("Subscribe returned ok=false for running job")
	}

	var stages []string
	var sawResult bool
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case ev, open := <-ch:
			if !open {
				break loop
			}
			if ev.Stage == "done" {
				sawResult = true
			}
			if ev.Content == "" && ev.Thinking == "" && ev.Stage != "" {
				stages = append(stages, ev.Stage)
			}
		case <-deadline:
			t.Fatal("timeout waiting for job events")
		}
	}

	if !sawResult {
		t.Errorf("terminal 'done' event missing; stages=%v", stages)
	}
	if st := jm.Status(jobID); st == nil || st.Status != "done" {
		t.Fatalf("status = %+v, want done", st)
	}
	if st := jm.Status(jobID); st.Result == nil || st.Result.ID != "job-style" {
		t.Fatalf("result = %+v, want id job-style", st)
	}
}

func TestJobManager_ErrorPropagates(t *testing.T) {
	deps, convID := jobTestDeps(t, &fakeLLM{err: context.DeadlineExceeded})
	jm := NewJobManager(deps)
	defer jm.Stop()

	jobID := jm.Start(Params{Mode: "create", Label: "x", ConversationID: convID})

	ctx, cancel := contextTimeout(5 * time.Second)
	defer cancel()
	st := jm.Wait(ctx, jobID)
	if st == nil {
		t.Fatal("Wait timed out")
	}
	if st.Status != "error" {
		t.Fatalf("status = %q, want error", st.Status)
	}
	if st.ErrorKind != string(KindLLM) {
		t.Errorf("error_kind = %q, want E_LLM", st.ErrorKind)
	}
	if st.Error == "" {
		t.Error("error message empty")
	}
}

func TestJobManager_SubscribeUnknownJob(t *testing.T) {
	deps, _ := jobTestDeps(t, llmText("{}"))
	jm := NewJobManager(deps)
	defer jm.Stop()
	if _, ok := jm.Subscribe("nope"); ok {
		t.Fatal("Subscribe should fail for unknown job")
	}
	if st := jm.Status("nope"); st != nil {
		t.Fatal("Status should return nil for unknown job")
	}
}

func TestJobManager_SubscribeFinishedJobYieldsTerminalEvent(t *testing.T) {
	deps, convID := jobTestDeps(t, llmText(`{"id":"finished","prompt":"# F","memory":""}`))
	jm := NewJobManager(deps)
	defer jm.Stop()

	jobID := jm.Start(Params{Mode: "create", Label: "F", ConversationID: convID})
	ctx, cancel := contextTimeout(5 * time.Second)
	st := jm.Wait(ctx, jobID)
	cancel()
	if st == nil || st.Status != "done" {
		t.Fatalf("job did not finish: %+v", st)
	}

	ch, ok := jm.Subscribe(jobID)
	if !ok {
		t.Fatal("Subscribe failed for finished job")
	}
	ev, open := <-ch
	if !open {
		t.Fatal("finished-job channel should yield one event then close")
	}
	if ev.Stage != "done" {
		t.Errorf("terminal event stage = %q, want done", ev.Stage)
	}
	if _, stillOpen := <-ch; stillOpen {
		t.Error("channel should be closed after terminal event")
	}
}

func TestJobManager_TTLSweeps(t *testing.T) {
	deps, convID := jobTestDeps(t, llmText(`{"id":"swept","prompt":"# S","memory":""}`))
	jm := NewJobManager(deps)
	jm.ttl = 50 * time.Millisecond // shrink TTL for the test
	defer jm.Stop()

	jobID := jm.Start(Params{Mode: "create", Label: "S", ConversationID: convID})
	ctx, cancel := contextTimeout(5 * time.Second)
	st := jm.Wait(ctx, jobID)
	cancel()
	if st == nil || st.Status != "done" {
		t.Fatalf("job did not finish: %+v", st)
	}

	jm.sweep() // manual sweep with past deadline
	if st := jm.Status(jobID); st != nil {
		t.Errorf("job should have been swept, still present: %+v", st)
	}
}

func TestJobManager_StageTracking(t *testing.T) {
	deps, convID := jobTestDeps(t, llmText(`{"id":"stages","prompt":"# P","memory":""}`))
	jm := NewJobManager(deps)
	defer jm.Stop()

	jobID := jm.Start(Params{Mode: "create", Label: "P", ConversationID: convID})
	ctx, cancel := contextTimeout(5 * time.Second)
	st := jm.Wait(ctx, jobID)
	cancel()
	if st == nil {
		t.Fatal("Wait timed out")
	}
	seen := map[string]bool{}
	for _, s := range st.Stages {
		seen[s.Stage] = s.Done
	}
	for _, want := range []string{"reading", "analyzing", "generating", "saving"} {
		if !seen[want] {
			t.Errorf("stage %q missing from status: %+v", want, st.Stages)
		}
	}
}

func TestJobManager_ConcurrentStart(t *testing.T) {
	deps, convID := jobTestDeps(t, llmText(`{"id":"conc","prompt":"# C","memory":""}`))
	jm := NewJobManager(deps)
	defer jm.Stop()

	done := make(chan string, 10)
	for i := 0; i < 10; i++ {
		go func(n int) {
			id := jm.Start(Params{Mode: "create", Label: "C", ConversationID: convID})
			ctx, cancel := contextTimeout(10 * time.Second)
			jm.Wait(ctx, id)
			cancel()
			done <- id
		}(i)
	}
	seen := map[string]bool{}
	for i := 0; i < 10; i++ {
		id := <-done
		if seen[id] {
			t.Fatalf("duplicate job id %q", id)
		}
		seen[id] = true
	}
	if len(seen) != 10 {
		t.Fatalf("expected 10 distinct jobs, got %d", len(seen))
	}
}
