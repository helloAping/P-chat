package tool

import (
	"context"
	"time"
)

// Shared tool-wait budgets. Policy TimeoutMS values in defaultToolPolicy
// are derived from these so the scheduler, handlers, and UI stay aligned.
const (
	// DefaultToolTimeout is the conservative per-call budget for unknown
	// tools and the agent's fallback when a lookup misses.
	DefaultToolTimeout = 5 * time.Minute
	// ConfirmWaitTimeout is how long a sandbox / browser confirm modal
	// waits for the user. It is independent of the per-tool execution
	// budget so a 60s write_file call does not expire while the user is
	// still looking at the dialog.
	ConfirmWaitTimeout = 5 * time.Minute
	// QuestionWaitTimeout is how long `question` waits for an answer.
	// Matches the `question` tool policy (10 min).
	QuestionWaitTimeout = 10 * time.Minute
	// WebFetchTimeout is the HTTP budget for `web_fetch` and the
	// scheduler policy for web_fetch / browser_* tools.
	WebFetchTimeout = 2 * time.Minute
	// WebSearchTimeout is the scheduler ceiling for `web_search`.
	// The search package still uses a 20s default HTTP timeout and a
	// 60s pickTimeout cap — this value must stay equal to that cap so
	// GET /tools does not advertise a longer wait than the HTTP client
	// will actually allow.
	WebSearchTimeout = 60 * time.Second
	// ReadToolTimeout covers local read / lookup tools.
	ReadToolTimeout = 60 * time.Second
	// WriteToolTimeout covers write_file / edit_file execution
	// (confirm wait is billed separately; see ConfirmWaitTimeout).
	WriteToolTimeout = 60 * time.Second
	// CheckpointToolTimeout covers todo_write / task_cancel.
	CheckpointToolTimeout = 30 * time.Second
)

type interactiveCtxKey struct{}

// WithInteractiveContext publishes the turn/abort context used for
// user-facing waits (confirm, question). Tool execution still runs
// under its own deadline; interactive waits must not share it.
func WithInteractiveContext(ctx, interactive context.Context) context.Context {
	if interactive == nil {
		return ctx
	}
	return context.WithValue(ctx, interactiveCtxKey{}, interactive)
}

// InteractiveContextFrom returns the context that should bound a
// user-facing wait. When none was published, ctx itself is used so
// tests and callers that only pass a deadline still work.
func InteractiveContextFrom(ctx context.Context) context.Context {
	if v, ok := ctx.Value(interactiveCtxKey{}).(context.Context); ok && v != nil {
		return v
	}
	return ctx
}
