package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestHandleTodoWriteNormalizesStatusAliases(t *testing.T) {
	sessionID := "todo-normalize-test"
	t.Cleanup(func() { SetSessionTodos(sessionID, nil) })

	result, err := handleTodoWrite(
		WithSessionID(context.Background(), sessionID),
		json.RawMessage(`{"todos":[{"id":"a","content":"A","status":"completed"},{"id":"b","content":"B","status":"doing"}]}`),
	)
	if err != nil {
		t.Fatalf("handleTodoWrite returned error: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("result = %+v, want success", result)
	}

	var returned []TodoItem
	if err := json.Unmarshal([]byte(result.Content), &returned); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if returned[0].Status != "done" || returned[1].Status != "in_progress" {
		t.Fatalf("returned statuses = %q/%q, want done/in_progress", returned[0].Status, returned[1].Status)
	}

	stored := GetSessionTodos(sessionID)
	if len(stored) != 2 {
		t.Fatalf("stored len = %d, want 2", len(stored))
	}
	if stored[0].Status != "done" || stored[1].Status != "in_progress" {
		t.Fatalf("stored statuses = %q/%q, want done/in_progress", stored[0].Status, stored[1].Status)
	}
}

func TestHandleTodoWriteKeepsActivePlanWhenModelWritesNewList(t *testing.T) {
	sessionID := "todo-terminal-test"
	SetSessionTodos(sessionID, []TodoItem{{ID: "old", Content: "old", Status: "pending"}})
	t.Cleanup(func() { SetSessionTodos(sessionID, nil) })

	result, err := handleTodoWrite(
		WithSessionID(context.Background(), sessionID),
		json.RawMessage(`{"todos":[{"id":"a","content":"A","status":"completed"},{"id":"b","content":"B","status":"canceled"}]}`),
	)
	if err != nil {
		t.Fatalf("handleTodoWrite returned error: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("result = %+v, want success", result)
	}

	var returned []TodoItem
	if err := json.Unmarshal([]byte(result.Content), &returned); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(returned) != 1 || returned[0].ID != "old" || returned[0].Status != "pending" {
		t.Fatalf("returned todos = %+v, want original active plan", returned)
	}
	stored := GetSessionTodos(sessionID)
	if len(stored) != 1 || stored[0].ID != "old" || stored[0].Status != "pending" {
		t.Fatalf("stored todos = %+v, want original active plan", stored)
	}
}

func TestHandleTodoWriteUpdatesExistingPlanStatuses(t *testing.T) {
	sessionID := "todo-status-merge-test"
	SetSessionTodos(sessionID, []TodoItem{
		{ID: "a", Content: "A", Status: "in_progress"},
		{ID: "b", Content: "B", Status: "pending"},
	})
	t.Cleanup(func() { SetSessionTodos(sessionID, nil) })

	result, err := handleTodoWrite(
		WithSessionID(context.Background(), sessionID),
		json.RawMessage(`{"todos":[{"id":"a","content":"changed by model","status":"completed"},{"id":"x","content":"B","status":"doing"}]}`),
	)
	if err != nil {
		t.Fatalf("handleTodoWrite returned error: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("result = %+v, want success", result)
	}

	var returned []TodoItem
	if err := json.Unmarshal([]byte(result.Content), &returned); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(returned) != 2 {
		t.Fatalf("returned len = %d, want 2", len(returned))
	}
	if returned[0].ID != "a" || returned[0].Content != "A" || returned[0].Status != "done" {
		t.Fatalf("returned[0] = %+v, want original content with done status", returned[0])
	}
	if returned[1].ID != "b" || returned[1].Content != "B" || returned[1].Status != "in_progress" {
		t.Fatalf("returned[1] = %+v, want original content with in_progress status", returned[1])
	}
}

func TestHandleTodoWriteClearsStoreWhenExistingPlanAllTerminal(t *testing.T) {
	sessionID := "todo-existing-terminal-test"
	SetSessionTodos(sessionID, []TodoItem{
		{ID: "a", Content: "A", Status: "in_progress"},
		{ID: "b", Content: "B", Status: "pending"},
	})
	t.Cleanup(func() { SetSessionTodos(sessionID, nil) })

	result, err := handleTodoWrite(
		WithSessionID(context.Background(), sessionID),
		json.RawMessage(`{"todos":[{"id":"a","content":"A","status":"completed"},{"id":"b","content":"B","status":"canceled"}]}`),
	)
	if err != nil {
		t.Fatalf("handleTodoWrite returned error: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("result = %+v, want success", result)
	}

	var returned []TodoItem
	if err := json.Unmarshal([]byte(result.Content), &returned); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(returned) != 2 || returned[0].Status != "done" || returned[1].Status != "cancelled" {
		t.Fatalf("returned todos = %+v, want terminal statuses", returned)
	}
	if got := GetSessionTodos(sessionID); len(got) != 0 {
		t.Fatalf("stored len = %d, want cleared store", len(got))
	}
}

func TestHandleTodoWriteRejectsInvalidItems(t *testing.T) {
	longContent := strings.Repeat("x", maxTodoContentBytes+1)
	tests := []struct {
		name string
		body string
	}{
		{name: "empty id", body: `{"todos":[{"id":" ","content":"work","status":"pending"}]}`},
		{name: "empty content", body: `{"todos":[{"id":"a","content":" ","status":"pending"}]}`},
		{name: "invalid status", body: `{"todos":[{"id":"a","content":"work","status":"later"}]}`},
		{name: "duplicate id", body: `{"todos":[{"id":"a","content":"one","status":"pending"},{"id":"a","content":"two","status":"pending"}]}`},
		{name: "multiple active", body: `{"todos":[{"id":"a","content":"one","status":"in_progress"},{"id":"b","content":"two","status":"in_progress"}]}`},
		{name: "content too long", body: `{"todos":[{"id":"a","content":"` + longContent + `","status":"pending"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionID := "todo-invalid-" + tt.name
			t.Cleanup(func() { SetSessionTodos(sessionID, nil) })
			result, err := handleTodoWrite(WithSessionID(context.Background(), sessionID), json.RawMessage(tt.body))
			if err != nil {
				t.Fatalf("handleTodoWrite returned error: %v", err)
			}
			if result == nil || !result.IsError {
				t.Fatalf("result = %+v, want validation error", result)
			}
			if got := GetSessionTodos(sessionID); len(got) != 0 {
				t.Fatalf("invalid write changed state: %#v", got)
			}
		})
	}
}

func TestHandleTodoWriteRejectsTerminalRegression(t *testing.T) {
	sessionID := "todo-terminal-regression-test"
	SetSessionTodos(sessionID, []TodoItem{
		{ID: "a", Content: "done work", Status: "done"},
		{ID: "b", Content: "remaining work", Status: "pending"},
	})
	t.Cleanup(func() { SetSessionTodos(sessionID, nil) })

	result, err := handleTodoWrite(
		WithSessionID(context.Background(), sessionID),
		json.RawMessage(`{"todos":[{"id":"a","content":"done work","status":"pending"},{"id":"b","content":"remaining work","status":"pending"}]}`),
	)
	if err != nil {
		t.Fatalf("handleTodoWrite returned error: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("result = %+v, want terminal regression error", result)
	}
	got := GetSessionTodos(sessionID)
	if len(got) != 2 || got[0].Status != "done" {
		t.Fatalf("terminal regression changed state: %#v", got)
	}
}

func TestHandleTodoWriteUsesRequestPersisterOnce(t *testing.T) {
	sessionID := "todo-request-persister-test"
	t.Cleanup(func() { SetSessionTodos(sessionID, nil) })

	previousGlobal := PersistTodos
	defer func() { PersistTodos = previousGlobal }()
	globalCalls := 0
	PersistTodos = func(string, []TodoItem) { globalCalls++ }

	requestCalls := 0
	ctx := WithTodoPersister(
		WithSessionID(context.Background(), sessionID),
		func(gotSessionID string, todos []TodoItem) error {
			requestCalls++
			if gotSessionID != sessionID || len(todos) != 1 || todos[0].ID != "a" {
				t.Fatalf("unexpected persistence payload: session=%q todos=%#v", gotSessionID, todos)
			}
			return nil
		},
	)

	result, err := handleTodoWrite(ctx, json.RawMessage(`{"todos":[{"id":"a","content":"work","status":"pending"}]}`))
	if err != nil {
		t.Fatalf("handleTodoWrite returned error: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("result = %+v, want success", result)
	}
	if requestCalls != 1 {
		t.Fatalf("request persister calls = %d, want 1", requestCalls)
	}
	if globalCalls != 0 {
		t.Fatalf("global persister calls = %d, want 0", globalCalls)
	}
}
