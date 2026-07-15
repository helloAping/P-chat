// Tests for P3-1: per-stream monotonic sequence counter.
// sendOrDrop stamps each chunk's Seq field with a
// monotonic value (0, 1, 2, …) when a counter is provided;
// nil counter preserves the caller's Seq. Verified here as
// a pure function — no ChatStream / LLM needed.
package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestSendOrDrop_StampsMonotonicSeq verifies that calling
// sendOrDrop N times with a shared counter closure produces
// chunks with Seq 0, 1, 2, …, N-1 in order.
func TestSendOrDrop_StampsMonotonicSeq(t *testing.T) {
	ch := make(chan ChatStreamChunk, 8)
	var seq atomic.Uint64
	ctx := context.Background()
	next := func() uint64 { return seq.Add(1) - 1 }

	go func() {
		defer close(ch)
		for i := 0; i < 5; i++ {
			sendOrDrop(ctx, ch, next, ChatStreamChunk{Content: "x"})
		}
	}()

	var got []uint64
	for ev := range ch {
		got = append(got, ev.Seq)
	}
	want := []uint64{0, 1, 2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("got %d chunks, want %d (seqs=%v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("chunk %d: seq=%d, want %d", i, got[i], w)
		}
	}
}

// TestSendOrDrop_NilNextSeqPreservesSeq verifies that the
// seq-stamping is opt-in: a nil nextSeq closure leaves the
// caller's Seq unchanged. This is the path used for sub-agent
// chunks forwarded into the parent stream (the parent's
// counter would break monotonicity, and the sub-agent has
// its own counter we don't want to leak).
func TestSendOrDrop_NilNextSeqPreservesSeq(t *testing.T) {
	ch := make(chan ChatStreamChunk, 4)
	ctx := context.Background()

	go func() {
		defer close(ch)
		sendOrDrop(ctx, ch, nil, ChatStreamChunk{Content: "a", Seq: 42})
		sendOrDrop(ctx, ch, nil, ChatStreamChunk{Content: "b"})
	}()

	var got []ChatStreamChunk
	for ev := range ch {
		got = append(got, ev)
	}
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2", len(got))
	}
	if got[0].Seq != 42 {
		t.Errorf("chunk 0: seq=%d, want 42 (caller-provided, nil nextSeq must not override)", got[0].Seq)
	}
	if got[1].Seq != 0 {
		t.Errorf("chunk 1: seq=%d, want 0 (caller unset, nil nextSeq must not stamp)", got[1].Seq)
	}
}

// TestSendOrDrop_CtxCancelDropsChunk verifies the
// cancellation safety net still works with the new signature
// (the seq stamp happens BEFORE the send, but a cancelled
// ctx still drops the chunk instead of blocking).
func TestSendOrDrop_CtxCancelDropsChunk(t *testing.T) {
	ch := make(chan ChatStreamChunk, 1)
	var seq atomic.Uint64
	ctx, cancel := context.WithCancel(context.Background())
	next := func() uint64 { return seq.Add(1) - 1 }

	// Pre-fill the buffer so the next send would block.
	// IMPORTANT: do NOT start a reader on ch after this
	// point — if a reader is waiting, sendOrDrop's select
	// may pick `ch <- chunk` over `<-ctx.Done()` (both
	// are ready when the buffer is full and a reader
	// waits), which would let the chunk land. With no
	// reader and a full buffer, the send is the only
	// blocking branch; ctx.Done() is the only ready
	// branch, so the cancellation path is deterministic.
	ch <- ChatStreamChunk{Content: "buffered"}
	cancel()

	// Should NOT block (ctx is cancelled, no reader).
	done := make(chan struct{})
	go func() {
		sendOrDrop(ctx, ch, next, ChatStreamChunk{Content: "dropped"})
		close(done)
	}()

	// Wait for the call to return. We bound the wait with
	// a 1s timer so a regression that breaks the
	// cancellation path is loud.
	select {
	case <-done:
		// Good — call returned promptly via the ctx.Done
		// branch of the select.
	case <-time.After(1 * time.Second):
		t.Fatal("sendOrDrop blocked despite cancelled ctx")
	}
	// Channel still holds the pre-filled "buffered" chunk.
	// The "dropped" chunk must not have landed.
	select {
	case leftover := <-ch:
		if leftover.Content == "dropped" {
			t.Errorf("sendOrDrop sent the chunk despite cancelled ctx: content=%q", leftover.Content)
		}
	default:
		t.Errorf("expected the pre-filled 'buffered' chunk to remain, got empty channel")
	}
}
