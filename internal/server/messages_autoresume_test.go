package server

// Auto-resume tests for respondSSE: when a turn ends NORMALLY (a real
// done chunk with no error) but the session still has unfinished todos,
// respondSSE must signal turnStreamRetry instead of emitting the done
// frame — SendMessage re-runs the loop with a "继续完成<任务> / 检查并
// 更新 todo" nudge, so the user never has to type "继续" while tracked
// work is still open.

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/tool"
)

func TestRespondSSE_NormalDoneWithPendingTodosResumes(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store
	convID, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	// A task is still in progress → the turn must NOT end here.
	tool.SetSessionTodos(convID, []tool.TodoItem{
		{ID: "t1", Content: "排查剩余测试失败", Status: "in_progress"},
	})
	t.Cleanup(func() { tool.SetSessionTodos(convID, nil) })

	w := newStreamRecorder()
	c, _ := gin.CreateTestContext(w)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Request = httptest.NewRequest("POST", "/api/v1/sessions/"+convID+"/messages", nil).WithContext(ctx)

	stream := make(chan agent.ChatStreamChunk, 4)
	stream <- agent.ChatStreamChunk{Phase: "llm", Content: "final answer", Done: true}
	close(stream)

	res := s.handler.respondSSE(c, stream, convID, "cs", "doubao-seed-2.0-lite", "⏱ 自动重试（第 1/2 次）")

	if res != turnStreamRetry {
		t.Fatalf("respondSSE = %v, want turnStreamRetry (todo still pending)", res)
	}
	body := w.Body.String()
	// The notice about auto-continuing must have been written...
	if !strings.Contains(body, "todo-auto-continue") {
		t.Errorf("response missing auto-continue notice; got: %s", body)
	}
	// ...and the done frame must NOT have been written, or the client
	// would close the stream and miss SendMessage's resumed run.
	if strings.Contains(body, `"type":"done"`) || strings.Contains(body, "type\\\":\\\"done") {
		t.Errorf("response emitted a done frame despite pending todos: %s", body)
	}
}

func TestRespondSSE_NormalDoneNoTodosEndsNormally(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store
	convID, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	// All todos done/cancelled (or none) → normal end.
	tool.SetSessionTodos(convID, []tool.TodoItem{
		{ID: "t1", Content: "done task", Status: "done"},
	})
	t.Cleanup(func() { tool.SetSessionTodos(convID, nil) })

	w := newStreamRecorder()
	c, _ := gin.CreateTestContext(w)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Request = httptest.NewRequest("POST", "/api/v1/sessions/"+convID+"/messages", nil).WithContext(ctx)

	stream := make(chan agent.ChatStreamChunk, 4)
	stream <- agent.ChatStreamChunk{Phase: "llm", Content: "all done", Done: true}
	close(stream)

	res := s.handler.respondSSE(c, stream, convID, "cs", "doubao-seed-2.0-lite", "⏱ 自动重试（第 1/2 次）")

	if res != turnStreamEnded {
		t.Fatalf("respondSSE = %v, want turnStreamEnded when all todos are terminal", res)
	}
	if !strings.Contains(w.Body.String(), `"type":"done"`) {
		t.Errorf("response should emit a done frame for a finished plan; got: %s", w.Body.String())
	}
}

func TestRespondSSE_NormalDonePendingTodosNoBudgetEndsNormally(t *testing.T) {
	// retryNotice empty (e.g. Regenerate, or budget exhausted) must NOT
	// auto-resume even with pending todos.
	s, _ := newTestServer(t)
	store := s.store
	convID, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	tool.SetSessionTodos(convID, []tool.TodoItem{
		{ID: "t1", Content: "pending task", Status: "pending"},
	})
	t.Cleanup(func() { tool.SetSessionTodos(convID, nil) })

	w := newStreamRecorder()
	c, _ := gin.CreateTestContext(w)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Request = httptest.NewRequest("POST", "/api/v1/sessions/"+convID+"/messages", nil).WithContext(ctx)

	stream := make(chan agent.ChatStreamChunk, 4)
	stream <- agent.ChatStreamChunk{Phase: "llm", Content: "final", Done: true}
	close(stream)

	res := s.handler.respondSSE(c, stream, convID, "cs", "doubao-seed-2.0-lite", "")

	if res != turnStreamEnded {
		t.Fatalf("respondSSE = %v, want turnStreamEnded with no retry budget", res)
	}
}
