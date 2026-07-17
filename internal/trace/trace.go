// Package trace provides a short, human-readable correlation ID that
// flows through the entire request lifecycle (HTTP → agent → LLM →
// tool handler → log → SSE event). It is the P3-3 observability
// primitive — see docs/plans/round4-trace-and-extensibility-plan.md
// for the design rationale.
//
// # Why an 8-character hex prefix "T-"?
//
// Eight hex chars (32 bits) give 4.29 billion possible IDs. The
// birthday-collision probability is negligible at any realistic
// concurrent-stream count (<< 1 in a million for 1k concurrent
// streams). Shorter than a full UUID (32 chars) and easier to spot
// in a log line; the "T-" prefix is a visual hint that this is a
// trace id (vs. a session id "S-..." or a task id).
//
// # Why context-based, not a global variable?
//
// The trace id needs to flow through call sites that have no
// business knowing about HTTP requests — tool handlers, LLM
// streaming goroutines, the sub-agent runner, background
// goroutines launched by the agent loop. context.Context is the
// one parameter that every Go function in this codebase already
// accepts, so it's the cheapest way to thread the id without
// touching dozens of function signatures.
package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// prefix is the visual marker for a trace id. All ids start with
// "T-" so they can be told apart from session ids, task ids, or
// raw UUIDs in log lines.
const prefix = "T-"

// NewID returns a fresh trace id, e.g. "T-9f3c4a2b". Four bytes
// of crypto-random data encoded as 8 hex chars. crypto/rand
// cannot fail on Linux/macOS/Windows in practice; if it ever
// does we return a deterministic zero-id rather than panicking,
// so the rest of the request can still proceed.
func NewID() string {
	b := make([]byte, 4) // 4 bytes = 8 hex chars
	if _, err := rand.Read(b); err != nil {
		return prefix + "00000000"
	}
	return prefix + hex.EncodeToString(b)
}

// ctxKey is the unexported context key for the trace id. Using
// an unexported empty struct prevents collisions with any other
// package that might use the same key string.
type ctxKey struct{}

// WithID returns a child context carrying the given trace id.
// The id is stored under an unexported key so it cannot collide
// with other context values.
//
// Passing an empty id is a no-op (returns ctx unchanged) so
// callers don't need to guard against "is the middleware on?".
func WithID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext extracts the trace id from ctx, or returns "" if
// the ctx has none. The zero value is "" so the caller can
// always pass it through to log lines, headers, and SSE
// payloads without an extra nil-check.
func FromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}

// LogPrefix returns "[T-xxxxxxxx] " (with a trailing space) when
// ctx carries a trace id, or "" otherwise. Use it as the first
// format verb in log.Printf so every line in a request's
// lifetime is greppable by the same id:
//
//	log.Printf("%s[agent] processing turn", trace.LogPrefix(ctx))
//
// The empty-string case keeps the log line tidy when running
// outside the HTTP path (CLI REPL, tests, direct embedding).
func LogPrefix(ctx context.Context) string {
	tid := FromContext(ctx)
	if tid == "" {
		return ""
	}
	return "[" + tid + "] "
}
