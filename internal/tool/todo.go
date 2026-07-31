package tool

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
)

// TodoItem represents a single task in the todo list.
type TodoItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"` // pending, in_progress, done, cancelled
}

// todoStore holds per-session todo lists.
var (
	todoMu  sync.RWMutex
	todoMap = make(map[string][]TodoItem)
)

type SessionIDKey struct{} // exported for agent confirm check

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
	todoMu.Lock()
	defer todoMu.Unlock()
	todos = todosForStorage(todos)
	if len(todos) == 0 {
		delete(todoMap, sessionID)
	} else {
		cp := make([]TodoItem, len(todos))
		copy(cp, todos)
		todoMap[sessionID] = cp
	}
	// Persist to SQLite if a persistence hook is registered.
	if PersistTodos != nil {
		PersistTodos(sessionID, todos)
	}
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

func normalizeTodoStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "completed", "complete":
		return "done"
	case "cancelled", "canceled", "cancel":
		return "cancelled"
	case "in_progress", "in-progress", "running", "doing":
		return "in_progress"
	case "pending", "todo", "":
		return "pending"
	default:
		return "pending"
	}
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
	args.Todos = normalizeTodoItems(args.Todos)
	args.Todos = mergeTodoStatusOntoPlan(GetSessionTodos(sid), args.Todos)

	SetSessionTodos(sid, args.Todos)

	data, _ := json.Marshal(args.Todos)
	return &CallResult{Content: string(data)}, nil
}
