package agent

// llm_retry.go — staircase retry backoff for transient upstream LLM
// errors, plus the deadline-free "abort" context that lets the retry
// phase survive the MaxTurnSeconds turn budget.
//
// Retry model: the k-th value of limits.llm_retry_backoffs is how long
// to wait before the k-th retry. Retry count = len(backoffs); total
// attempts = len(backoffs) + 1. Empty list = no retry (single attempt).
//
// Detachment: retries are bounded by the backoff list plus user
// cancellation, NOT by the MaxTurnSeconds turn deadline. The server
// passes a deadline-free abortCtx (cancelled on cancel-stream / client
// disconnect) via WithAbortContext; the retry loop runs on it so a long
// backoff sequence (up to ~19 min with the default table) is never
// truncated mid-wait. Callers that don't attach an abort context (e.g.
// direct ChatWithTools unit tests) keep the old deadline-bound behaviour
// via the ctx fallback.

import (
	"context"
	"time"

	"github.com/p-chat/pchat/internal/config"
)

// llmRetryBackoffs returns the configured per-retry backoff steps (in
// seconds), or nil when retries are disabled (empty list / no config).
func llmRetryBackoffs(cfg *config.Config) []int {
	if cfg == nil {
		return nil
	}
	return cfg.Limits.LLMRetryBackoffs
}

// retryBackoffDuration returns how long to wait before the k-th retry
// (k is 1-based). Out-of-range steps clamp to the last configured value;
// a non-positive step yields a zero (immediate) wait.
func retryBackoffDuration(backoffs []int, k int) time.Duration {
	if len(backoffs) == 0 {
		return 0
	}
	idx := k - 1
	if idx >= len(backoffs) {
		idx = len(backoffs) - 1
	}
	if backoffs[idx] < 0 {
		return 0
	}
	return time.Duration(backoffs[idx]) * time.Second
}

// abortCtxKey is the context key under which the server publishes a
// deadline-free, user-cancellable context for the LLM retry phase.
type abortCtxKey struct{}

// WithAbortContext returns a ctx carrying abort, a deadline-free context
// cancelled whenever the user stops the turn (cancel-stream) or the
// client disconnects. The agent's retry loop runs on abort so retries
// are not truncated by the MaxTurnSeconds turn deadline.
func WithAbortContext(ctx context.Context, abort context.Context) context.Context {
	if abort == nil {
		return ctx
	}
	return context.WithValue(ctx, abortCtxKey{}, abort)
}

// AbortContextFrom returns the deadline-free abort context previously
// attached with WithAbortContext, or nil when none was attached.
func AbortContextFrom(ctx context.Context) context.Context {
	if v, ok := ctx.Value(abortCtxKey{}).(context.Context); ok {
		return v
	}
	return nil
}
