package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// TodoItem represents a single task in the todo list.
type TodoItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"` // pending, in_progress, done, cancelled
}

const (
	maxTodoItems        = 100
	maxTodoIDBytes      = 128
	maxTodoContentBytes = 4096
	maxTodoListBytes    = 256 << 10
)

// todoStore holds per-session todo lists.
var (
	todoMu  sync.RWMutex
	todoMap = make(map[string][]TodoItem)
)

type SessionIDKey struct{} // exported for agent confirm check

// TodoPersister is an optional request-scoped persistence callback. It keeps
// todo writes tied to the current store without a global session map.
type TodoPersister func(sessionID string, todos []TodoItem) error

type todoPersisterKey struct{}

// WithTodoPersister attaches a request-scoped todo persistence callback.
func WithTodoPersister(ctx context.Context, persist TodoPersister) context.Context {
	if persist == nil {
		return ctx
	}
	return context.WithValue(ctx, todoPersisterKey{}, persist)
}

func todoPersisterFromCtx(ctx context.Context) TodoPersister {
	if p, ok := ctx.Value(todoPersisterKey{}).(TodoPersister); ok {
		return p
	}
	return nil
}

// WithSessionID attaches a session ID to the context so tool
// handlers can access per-session state.
func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, SessionIDKey{}, id)
}

// PermissionLevelKey is the context key for per-session permission level.
type PermissionLevelKey struct{}

// Permission levels for the sandbox confirm flow.
const (
	PermissionAsk  = "ask"  // always show confirm modal
	PermissionAuto = "auto" // auto-approve, skip confirm modal
	PermissionFull = "full" // skip all sandbox checks
)

// WithPermissionLevel attaches a permission level to the context.
func WithPermissionLevel(ctx context.Context, level string) context.Context {
	return context.WithValue(ctx, PermissionLevelKey{}, level)
}

// GetSessionTodos returns the current todo list for a session.
func GetSessionTodos(sessionID string) []TodoItem {
	todoMu.RLock()
	defer todoMu.RUnlock()
	todos := todoMap[sessionID]
	if todos == nil {
		return []TodoItem{}
	}
	out := make([]TodoItem, len(todos))
	copy(out, todos)
	return out
}

// SetSessionTodos replaces the todo list for a session.
func SetSessionTodos(sessionID string, todos []TodoItem) {
	stored := setSessionTodosMemory(sessionID, todos)
	// Persist outside the mutex. The persistence hook may perform a
	// synchronous SQLite transaction and must never block readers or
	// re-enter todo state while the in-memory lock is held.
	if PersistTodos != nil {
		PersistTodos(sessionID, stored)
	}
}

// SetSessionTodosMemory replaces only the process-local todo cache. Callers
// that already persisted the list (for example, a server hydrate or clear
// operation) should use this to avoid a duplicate global-hook transaction.
func SetSessionTodosMemory(sessionID string, todos []TodoItem) {
	setSessionTodosMemory(sessionID, todos)
}

// setSessionTodosMemory updates only the process-local cache and returns the
// normalized copy that should be persisted. Request-scoped agent writes use
// this after their persistence callback succeeds, avoiding a second SQLite
// transaction through the legacy global hook.
func setSessionTodosMemory(sessionID string, todos []TodoItem) []TodoItem {
	stored := todosForStorage(todos)
	todoMu.Lock()
	if len(stored) == 0 {
		delete(todoMap, sessionID)
	} else {
		cp := make([]TodoItem, len(stored))
		copy(cp, stored)
		todoMap[sessionID] = cp
	}
	todoMu.Unlock()
	return stored
}

func normalizeTodoItems(todos []TodoItem) []TodoItem {
	if len(todos) == 0 {
		return nil
	}
	out := make([]TodoItem, 0, len(todos))
	for _, t := range todos {
		t.Status = normalizeTodoStatus(t.Status)
		out = append(out, t)
	}
	return out
}

func todosForStorage(todos []TodoItem) []TodoItem {
	out := normalizeTodoItems(todos)
	if len(out) == 0 {
		return nil
	}
	allTerminal := true
	for _, t := range out {
		if t.Status != "done" && t.Status != "cancelled" {
			allTerminal = false
			break
		}
	}
	if allTerminal {
		return nil
	}
	return out
}

func hasActiveTodos(todos []TodoItem) bool {
	for _, t := range todos {
		if t.Status == "pending" || t.Status == "in_progress" {
			return true
		}
	}
	return false
}

func mergeTodoStatusOntoPlan(existing, incoming []TodoItem) []TodoItem {
	if len(existing) == 0 || !hasActiveTodos(existing) {
		return incoming
	}
	byID := make(map[string]TodoItem, len(incoming))
	byContent := make(map[string]TodoItem, len(incoming))
	for _, t := range incoming {
		if t.ID != "" {
			byID[t.ID] = t
		}
		if t.Content != "" {
			byContent[t.Content] = t
		}
	}
	out := make([]TodoItem, len(existing))
	for i, t := range existing {
		out[i] = t
		if next, ok := byID[t.ID]; ok {
			out[i].Status = next.Status
			continue
		}
		if next, ok := byContent[t.Content]; ok {
			out[i].Status = next.Status
		}
	}
	return out
}

func normalizeTodoStatusStrict(status string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "completed", "complete":
		return "done", true
	case "cancelled", "canceled", "cancel":
		return "cancelled", true
	case "in_progress", "in-progress", "running", "doing":
		return "in_progress", true
	case "pending", "todo", "":
		return "pending", true
	default:
		return "", false
	}
}

func validateTodoWrite(existing, incoming []TodoItem) error {
	if len(incoming) > maxTodoItems {
		return fmt.Errorf("todo list is too large: %d items (max %d)", len(incoming), maxTodoItems)
	}
	seen := make(map[string]struct{}, len(incoming))
	inProgress := 0
	totalBytes := 0
	for i := range incoming {
		item := &incoming[i]
		item.ID = strings.TrimSpace(item.ID)
		item.Content = strings.TrimSpace(item.Content)
		if item.ID == "" {
			return fmt.Errorf("todo %d has an empty id", i+1)
		}
		if item.Content == "" {
			return fmt.Errorf("todo %q has empty content", item.ID)
		}
		if len(item.ID) > maxTodoIDBytes {
			return fmt.Errorf("todo %q has an id longer than %d bytes", item.ID, maxTodoIDBytes)
		}
		if len(item.Content) > maxTodoContentBytes {
			return fmt.Errorf("todo %q has content longer than %d bytes", item.ID, maxTodoContentBytes)
		}
		totalBytes += len(item.ID) + len(item.Content)
		if totalBytes > maxTodoListBytes {
			return fmt.Errorf("todo list exceeds %d bytes", maxTodoListBytes)
		}
		status, ok := normalizeTodoStatusStrict(item.Status)
		if !ok {
			return fmt.Errorf("todo %q has invalid status %q", item.ID, item.Status)
		}
		item.Status = status
		if _, ok := seen[item.ID]; ok {
			return fmt.Errorf("duplicate todo id %q", item.ID)
		}
		seen[item.ID] = struct{}{}
		if item.Status == "in_progress" {
			inProgress++
		}
	}
	if inProgress > 1 {
		return fmt.Errorf("at most one todo may be in_progress")
	}
	// Terminal items cannot be reopened. Active plans are merged below, so
	// this check only needs to reject a direct regression of a known ID.
	for _, old := range existing {
		if old.Status != "done" && old.Status != "cancelled" {
			continue
		}
		for _, next := range incoming {
			if next.ID == old.ID && next.Status != old.Status && next.Status != "done" && next.Status != "cancelled" {
				return fmt.Errorf("terminal todo %q cannot return to %s", old.ID, next.Status)
			}
		}
	}
	return nil
}

func normalizeTodoStatus(status string) string {
	if normalized, ok := normalizeTodoStatusStrict(status); ok {
		return normalized
	}
	return "pending"
}

// PersistTodos is set by the server to persist todo lists to
// SQLite for durability across restarts. The tool package
// itself doesn't depend on the memory package.
var PersistTodos func(sessionID string, todos []TodoItem)

type todoWriteArgs struct {
	Todos []TodoItem `json:"todos"`
}

func handleTodoWrite(ctx context.Context, argsRaw json.RawMessage) (*CallResult, error) {
	var args todoWriteArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return &CallResult{Content: "参数错误: " + err.Error(), IsError: true}, nil
	}

	sid, _ := ctx.Value(SessionIDKey{}).(string)
	if err := validateTodoWrite(GetSessionTodos(sid), args.Todos); err != nil {
		return &CallResult{Content: "todo_write rejected: " + err.Error(), IsError: true}, nil
	}
	args.Todos = normalizeTodoItems(args.Todos)
	args.Todos = mergeTodoStatusOntoPlan(GetSessionTodos(sid), args.Todos)

	if persist := todoPersisterFromCtx(ctx); persist != nil {
		stored := todosForStorage(args.Todos)
		if err := persist(sid, stored); err != nil {
			return &CallResult{Content: "todo_write persistence failed: " + err.Error(), IsError: true}, nil
		}
		setSessionTodosMemory(sid, stored)
	} else {
		SetSessionTodos(sid, args.Todos)
	}

	data, _ := json.Marshal(args.Todos)
	return &CallResult{Content: string(data)}, nil
}
