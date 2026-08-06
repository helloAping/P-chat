package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/tool"
)

// doneChunk is the terminal chunk a turn produces when the LLM finished
// without marking every todo done — the trigger for the todo auto-resume.
func doneChunk() agent.ChatStreamChunk {
	return agent.ChatStreamChunk{Done: true, Phase: "done", Step: "done"}
}

// TestResumeTracker_NoProgressStops locks down the T3 core: auto-resumes
// with an UNCHANGED unfinished-todo set count against the limit and
// eventually stop with a user-facing notice.
func TestResumeTracker_NoProgressStops(t *testing.T) {
	tr := &resumeTracker{}
	snap := "t1,t2"
	// Baseline resume (first after a user message) is always allowed.
	if ok, notice := tr.allow(snap); !ok || notice != "" {
		t.Fatalf("baseline resume: ok=%v notice=%q, want ok=true", ok, notice)
	}
	// Unchanged snapshot: counts 1, 2 allowed, then 3 stops.
	for wantCount := 1; wantCount < maxNoProgressResumes; wantCount++ {
		if ok, notice := tr.allow(snap); !ok || notice != "" {
			t.Fatalf("no-progress resume %d: ok=%v notice=%q, want allowed", wantCount, ok, notice)
		}
	}
	ok, notice := tr.allow(snap)
	if ok {
		t.Fatal("limit reached: resume must be stopped")
	}
	if !strings.Contains(notice, "自动续跑已连续") {
		t.Fatalf("stop notice should tell the user to intervene, got %q", notice)
	}
	// After firing, the tracker resets — a later chain starts fresh.
	if ok, _ := tr.allow(snap); !ok {
		t.Fatal("tracker must reset after firing so a later chain starts fresh")
	}
}

// TestResumeTracker_ProgressResets covers the "normal task keeps going"
// half: when the unfinished-todo set changes between resumes (an item was
// done / cancelled / added), the no-progress counter resets and the chain
// continues.
func TestResumeTracker_ProgressResets(t *testing.T) {
	tr := &resumeTracker{}
	tr.allow("t1,t2") // baseline
	tr.allow("t1,t2") // count=1
	tr.allow("t1,t2") // count=2
	// Progress: t2 finished — snapshot changes, counter resets.
	if ok, _ := tr.allow("t1"); !ok {
		t.Fatal("changed snapshot must reset the no-progress counter")
	}
	// Two more unchanged resumes must still be allowed (counter restarted).
	for i := 0; i < maxNoProgressResumes-1; i++ {
		if ok, _ := tr.allow("t1"); !ok {
			t.Fatalf("resume %d after progress must be allowed", i)
		}
	}
	if ok, _ := tr.allow("t1"); ok {
		t.Fatal("counter must still hit the limit after the reset window")
	}
}

// TestPendingTodoSnapshot returns only pending/in_progress ids, sorted.
func TestPendingTodoSnapshot(t *testing.T) {
	sid := "snapshot-test"
	tool.SetSessionTodos(sid, []tool.TodoItem{
		{ID: "b", Status: "pending"},
		{ID: "a", Status: "done"}, // excluded
		{ID: "c", Status: "in_progress"},
		{ID: "d", Status: "cancelled"}, // excluded
	})
	t.Cleanup(func() { tool.SetSessionTodos(sid, nil) })
	if got := pendingTodoSnapshot(sid); got != "b,c" {
		t.Fatalf("snapshot = %q, want %q", got, "b,c")
	}
}

// newAutoResumeHandler builds a minimal Handler with a store and the
// T3 tracker map, enough to exercise respondSSE's auto-resume branches.
func newAutoResumeHandler(t *testing.T) (*Handler, *memory.Store) {
	t.Helper()
	store, err := memory.OpenAt(":memory:", 50)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &Handler{
		store:          store,
		turnCancels:    sync.Map{},
		resumeTrackers: sync.Map{},
	}, store
}

// runRespondSSE feeds one chunk into respondSSE (as one turn of the
// SendMessage loop would) and returns the result plus the SSE body.
func runRespondSSE(t *testing.T, h *Handler, chunk agent.ChatStreamChunk, retryNotice string) (turnStreamResult, string) {
	t.Helper()
	w := newStreamRecorder() // CloseNotify needed by gin's c.Stream
	ginCtx, _ := gin.CreateTestContext(w)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	ch := make(chan agent.ChatStreamChunk, 1)
	ch <- chunk
	close(ch)
	res := h.respondSSE(ginCtx, ch, "sess", "test", "m", retryNotice)
	return res, w.Body.String()
}

// TestRespondSSE_NoProgressAutoResumeStops is the T3 integration
// acceptance: a mock turn that keeps finishing without updating todos
// must auto-resume a bounded number of times, then stop with a notice
// and a terminal done frame — instead of looping forever.
func TestRespondSSE_NoProgressAutoResumeStops(t *testing.T) {
	h, _ := newAutoResumeHandler(t)
	sid := "sess"
	tool.SetSessionTodos(sid, []tool.TodoItem{
		{ID: "t1", Content: "a", Status: "pending"},
		{ID: "t2", Content: "b", Status: "pending"},
	})
	t.Cleanup(func() { tool.SetSessionTodos(sid, nil) })

	// The first three turns auto-resume (baseline + counts 1..2)...
	for turn := 1; turn <= 1+maxNoProgressResumes-1; turn++ {
		res, body := runRespondSSE(t, h, doneChunk(), "⏱ retry budget remains")
		if res != turnStreamRetry {
			t.Fatalf("turn %d: result = %v, want turnStreamRetry; body=%s", turn, res, body)
		}
		if !strings.Contains(body, "todo-auto-continue") {
			t.Fatalf("turn %d: missing todo-auto-continue notice; body=%s", turn, body)
		}
	}
	// ...then the no-progress limit stops the chain: terminal done frame
	// plus a user-facing notice, and the result is NOT retry.
	res, body := runRespondSSE(t, h, doneChunk(), "⏱ retry budget remains")
	if res != turnStreamEnded {
		t.Fatalf("limit turn: result = %v, want turnStreamEnded", res)
	}
	if !strings.Contains(body, "自动续跑已连续") {
		t.Fatalf("limit turn: missing auto-resume-stopped notice; body=%s", body)
	}
	if !strings.Contains(body, `"done"`) && !strings.Contains(body, "type") {
		t.Fatalf("limit turn: missing terminal done frame; body=%s", body)
	}
}

// TestRespondSSE_TodoProgressKeepsResuming covers the "normal task keeps
// going" requirement: when the todo set CHANGES between resumes (an item
// got done), the no-progress counter resets and the chain survives longer
// than the no-progress limit would otherwise allow.
func TestRespondSSE_TodoProgressKeepsResuming(t *testing.T) {
	h, _ := newAutoResumeHandler(t)
	sid := "sess"
	tool.SetSessionTodos(sid, []tool.TodoItem{
		{ID: "t1", Content: "a", Status: "pending"},
		{ID: "t2", Content: "b", Status: "pending"},
	})
	t.Cleanup(func() { tool.SetSessionTodos(sid, nil) })

	// Turn 1: baseline.
	if res, _ := runRespondSSE(t, h, doneChunk(), "⏱"); res != turnStreamRetry {
		t.Fatalf("turn 1: want retry, got %v", res)
	}
	// Turn 2: baseline count 1.
	if res, _ := runRespondSSE(t, h, doneChunk(), "⏱"); res != turnStreamRetry {
		t.Fatalf("turn 2: want retry, got %v", res)
	}
	// Progress: t1 done before turn 3 — counter resets.
	tool.SetSessionTodos(sid, []tool.TodoItem{
		{ID: "t1", Content: "a", Status: "done"},
		{ID: "t2", Content: "b", Status: "pending"},
	})
	// Turn 3: snapshot changed → allowed; turn 4..5 unchanged → counts 1..2.
	for turn := 3; turn <= 5; turn++ {
		if res, _ := runRespondSSE(t, h, doneChunk(), "⏱"); res != turnStreamRetry {
			t.Fatalf("turn %d after progress: want retry, got %v", turn, res)
		}
	}
	// Turn 6: limit hit again.
	if res, _ := runRespondSSE(t, h, doneChunk(), "⏱"); res != turnStreamEnded {
		t.Fatalf("turn 6: want turnStreamEnded, got %v", res)
	}
}

// TestRespondSSE_UserMessageResetsTracker is the recovery requirement: a
// genuine user message (which SendMessage handles by deleting the
// session's tracker entry) resets the no-progress budget, so a manual
// continuation is never blocked by the stale count.
func TestRespondSSE_UserMessageResetsTracker(t *testing.T) {
	h, _ := newAutoResumeHandler(t)
	sid := "sess"
	tool.SetSessionTodos(sid, []tool.TodoItem{{ID: "t1", Content: "a", Status: "pending"}})
	t.Cleanup(func() { tool.SetSessionTodos(sid, nil) })

	// Burn the whole budget: baseline + 2 counted resumes, then stopped.
	for i := 0; i < maxNoProgressResumes; i++ {
		if res, _ := runRespondSSE(t, h, doneChunk(), "⏱"); res != turnStreamRetry {
			t.Fatalf("resume %d: want retry, got %v", i, res)
		}
	}
	if res, _ := runRespondSSE(t, h, doneChunk(), "⏱"); res != turnStreamEnded {
		t.Fatal("budget must be exhausted before the reset")
	}

	// A user message arrives → SendMessage deletes the tracker.
	h.resumeTrackers.Delete(sid)
	if res, _ := runRespondSSE(t, h, doneChunk(), "⏱"); res != turnStreamRetry {
		t.Fatal("after a user message the auto-resume budget must start fresh")
	}
}
