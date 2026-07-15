package server_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// TestTraceID_GeneratedWhenMissing covers the default path:
// the client doesn't send an X-Trace-Id header, so the
// traceIDMiddleware mints a fresh 8-char hex id with the
// "T-" prefix. The same id is reflected back on the
// response header so curl users can copy it.
func TestTraceID_GeneratedWhenMissing(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	tid := resp.Header.Get("X-Trace-Id")
	if tid == "" {
		t.Fatal("X-Trace-Id header missing")
	}
	matched, err := regexp.MatchString(`^T-[0-9a-f]{8}$`, tid)
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Errorf("X-Trace-Id = %q, want T-xxxxxxxx (8 hex)", tid)
	}
}

// TestTraceID_HeaderPassthrough covers the correlation path:
// when the client supplies an X-Trace-Id, the server adopts
// it as-is (rather than minting a new one) and echoes it
// back. This lets the browser / Wails runtime tag its own
// logs with the same id the server stamps on outbound
// requests and tool handler log lines.
func TestTraceID_HeaderPassthrough(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	const want = "T-abc12345"
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Trace-Id", want)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := resp.Header.Get("X-Trace-Id")
	if got != want {
		t.Errorf("X-Trace-Id = %q, want %q (server should adopt client-supplied id)", got, want)
	}
}

// TestTraceID_DistinctPerRequest ensures the middleware
// doesn't accidentally share a trace id between requests.
// Each request should get its own id when the client
// doesn't pin one.
func TestTraceID_DistinctPerRequest(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		resp, err := http.Get(srv.URL + "/api/v1/health")
		if err != nil {
			t.Fatal(err)
		}
		tid := resp.Header.Get("X-Trace-Id")
		resp.Body.Close()
		if tid == "" {
			t.Fatalf("iter %d: X-Trace-Id missing", i)
		}
		if seen[tid] {
			t.Errorf("iter %d: duplicate trace id %q across requests", i, tid)
		}
		seen[tid] = true
	}
}

// TestTraceID_AllowsHeaderInCORS preflights checks the
// CORS preflight lists X-Trace-Id as an allowed request
// header. Without this, the browser would block a
// cross-origin fetch that includes X-Trace-Id.
func TestTraceID_AllowsHeaderInCORS(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodOptions, srv.URL+"/api/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", srv.URL)
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "X-Trace-Id,Content-Type")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	allowed := resp.Header.Get("Access-Control-Allow-Headers")
	if !strings.Contains(allowed, "X-Trace-Id") {
		t.Errorf("Access-Control-Allow-Headers = %q, want to include X-Trace-Id", allowed)
	}
}
