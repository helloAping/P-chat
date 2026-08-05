package server

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// stuckResponseWriter simulates a WebView2 renderer that has frozen: it
// accepts the connection but never reads, so Write and Flush block on a
// full socket buffer forever. Used to prove writeSSEWithTimeout bounds the
// write instead of wedging respondSSE for the rest of the turn (the
// 2026-08-04 hang: SendMessage → respondSSE → Flush → WSASend blocked for
// 14 minutes until the user cancelled).
type stuckResponseWriter struct{}

func (stuckResponseWriter) Header() http.Header         { return http.Header{} }
func (stuckResponseWriter) WriteHeader(int)             {}
func (stuckResponseWriter) Write(p []byte) (int, error) { select {} } // block forever
func (stuckResponseWriter) Flush()                      { select {} } // block forever

// TestWriteSSEWithTimeout_StuckClientBounded verifies a client that stops
// reading cannot hold the SSE handler indefinitely: writeSSEWithTimeout must
// return a timeout error well within the bound.
func TestWriteSSEWithTimeout_StuckClientBounded(t *testing.T) {
	const bound = 200 * time.Millisecond
	var w stuckResponseWriter
	ev := StreamEvent{Type: "content", Content: "x"}

	start := time.Now()
	err := writeSSEWithTimeout(w, ev, bound)
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("writeSSEWithTimeout to a stuck client = %v, want a timeout error", err)
	}
	// The goroutine backstop must return ~bound; a hung flush would make
	// this take far longer (the turn would hang until MaxTurnSeconds).
	if elapsed > 2*time.Second {
		t.Fatalf("writeSSEWithTimeout took %v, want it bounded at ~%v", elapsed, bound)
	}
}

// TestWriteSSEWithTimeout_NormalClientFlushes ensures the happy path still
// writes AND flushes the frame (the flush moved inside writeSSEWithTimeout).
func TestWriteSSEWithTimeout_NormalClientFlushes(t *testing.T) {
	got := &flushRecorder{}
	ev := StreamEvent{Type: "content", Content: "hello"}
	if err := writeSSEWithTimeout(got, ev, time.Second); err != nil {
		t.Fatalf("normal write failed: %v", err)
	}
	if !strings.Contains(got.data, "data: ") || !strings.Contains(got.data, "hello") {
		t.Fatalf("frame not written, got %q", got.data)
	}
	if !got.flushed {
		t.Fatal("flush did not run inside writeSSEWithTimeout")
	}
}

type flushRecorder struct {
	data    string
	flushed bool
}

func (r *flushRecorder) Header() http.Header         { return http.Header{} }
func (r *flushRecorder) WriteHeader(int)             {}
func (r *flushRecorder) Write(p []byte) (int, error) { r.data += string(p); return len(p), nil }
func (r *flushRecorder) Flush()                      { r.flushed = true }
