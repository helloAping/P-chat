package tool

import (
	"context"
	"encoding/json"
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

	SetSessionTodos(sid, args.Todos)

	data, _ := json.Marshal(args.Todos)
	return &CallResult{Content: string(data)}, nil
}
