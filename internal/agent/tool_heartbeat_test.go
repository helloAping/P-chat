package agent

import (
	"context"
	"testing"
	"time"
)

func TestWaitToolsWithHeartbeat_EmitsUntilDone(t *testing.T) {
	ctx := context.Background()
	ch := make(chan ChatStreamChunk, 8)
	toolsDone := make(chan struct{})
	var seq uint64
	nextSeq := func() uint64 {
		seq++
		return seq
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- waitToolsWithHeartbeat(ctx, toolsDone, ch, nextSeq, 15*time.Millisecond)
	}()

	var got ChatStreamChunk
	select {
	case got = <-ch:
	case <-time.After(time.Second):
		t.Fatal("expected a tool-heartbeat chunk while tools are still running")
	}
	if got.Step != "tool-heartbeat" {
		t.Fatalf("step = %q, want tool-heartbeat", got.Step)
	}
	if got.Message == "" {
		t.Fatal("heartbeat message must be non-empty so the GUI can show live status")
	}

	close(toolsDone)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("wait returned %v, want nil after toolsDone", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waitToolsWithHeartbeat did not return after toolsDone")
	}
}

func TestWaitToolsWithHeartbeat_StopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan ChatStreamChunk, 4)
	toolsDone := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- waitToolsWithHeartbeat(ctx, toolsDone, ch, nil, time.Hour)
	}()
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected ctx error on cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("waitToolsWithHeartbeat did not return after cancel")
	}
}
