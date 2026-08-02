package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestRegisterPprof verifies the /debug/pprof endpoints are reachable when
// enabled (default), so the user can capture a live heap/goroutine profile
// while the server's memory is spiking.
func TestRegisterPprof(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PC_PPROF", "1")
	r := gin.New()
	registerPprof(r)

	for _, path := range []string{
		"/debug/pprof",
		"/debug/pprof/",
		"/debug/pprof/heap",
		"/debug/pprof/goroutine",
		"/debug/pprof/allocs",
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		r.ServeHTTP(w, req)
		// The bare /debug/pprof path 301-redirects to /debug/pprof/ — that is
		// net/http/pprof's standard behaviour and browsers follow it.
		ok := w.Code == http.StatusOK
		if path == "/debug/pprof" {
			ok = ok || w.Code == http.StatusMovedPermanently
		}
		if !ok {
			t.Errorf("GET %s = %d, want 200 (body head: %.120s)", path, w.Code, w.Body.String())
		}
	}
}

// TestRegisterPprof_Disabled verifies PC_PPROF=0 turns the endpoint off.
func TestRegisterPprof_Disabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PC_PPROF", "0")
	r := gin.New()
	registerPprof(r)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Error("pprof should be disabled when PC_PPROF=0")
	}
}

func TestEnvInt(t *testing.T) {
	t.Setenv("PC_MEM_HEARTBEAT_SEC", "7")
	if got := envInt("PC_MEM_HEARTBEAT_SEC", 30); got != 7 {
		t.Errorf("envInt = %d, want 7", got)
	}
	if got := envInt("PC_MEM_HEARTBEAT_SEC_UNSET_XYZ", 30); got != 30 {
		t.Errorf("envInt default = %d, want 30", got)
	}
	t.Setenv("PC_MEM_HEARTBEAT_SEC", "not-a-number")
	if got := envInt("PC_MEM_HEARTBEAT_SEC", 30); got != 30 {
		t.Errorf("envInt fallback on bad value = %d, want 30", got)
	}
}

// TestMemoryMonitor_ConfigUpdate verifies the runtime-editable monitor config:
// defaults from env, update via the struct, and reads reflect the change.
func TestMemoryMonitor_ConfigUpdate(t *testing.T) {
	t.Setenv("PC_MEM_HEARTBEAT_SEC", "11")
	t.Setenv("PC_HEAP_DUMP_MB", "22")
	t.Setenv("PC_PPROF", "1")
	m := newMemoryMonitor()
	c := m.config()
	if c.MemHeartbeatSec != 11 || c.HeapDumpMB != 22 {
		t.Fatalf("config = %+v, want heartbeat=11 dump=22", c)
	}
	if !c.PprofEnabled {
		t.Error("pprof should be enabled when PC_PPROF != 0")
	}

	sec, mb := 60, 4096
	m.update(DiagnosticsConfigUpdate{MemHeartbeatSec: &sec, HeapDumpMB: &mb})
	c = m.config()
	if c.MemHeartbeatSec != 60 || c.HeapDumpMB != 4096 {
		t.Errorf("after update config = %+v, want heartbeat=60 dump=4096", c)
	}

	// Snapshot returns sane values.
	s := m.snapshot()
	if s.HeapAllocMB < 0 || s.Goroutines < 1 {
		t.Errorf("snapshot = %+v, want heap>=0 and goroutines>=1", s)
	}
}

// TestDiagnosticsEndpoints exercises the /diagnostics/* handlers end to end via
// a gin router: memory read, config read/update, and both snapshot downloads.
func TestDiagnosticsEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mm := newMemoryMonitor()
	mm.heapDumpDir = t.TempDir() // don't write into the user's ~/.p-chat during tests
	h := &Handler{memMon: mm}
	r := gin.New()
	r.GET("/diagnostics/memory", h.MemoryDiagnostics)
	r.GET("/diagnostics/config", h.DiagnosticsConfigGet)
	r.PATCH("/diagnostics/config", h.DiagnosticsConfigUpdate)
	r.GET("/diagnostics/snapshot/heap", h.DiagnosticsHeapSnapshot)
	r.GET("/diagnostics/snapshot/goroutine", h.DiagnosticsGoroutineSnapshot)

	do := func(method, path string, body string) *httptest.ResponseRecorder {
		var req *http.Request
		if body != "" {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
		} else {
			req = httptest.NewRequest(method, path, nil)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	w := do("GET", "/diagnostics/memory", "")
	if w.Code != http.StatusOK {
		t.Fatalf("memory = %d: %s", w.Code, w.Body.String())
	}
	var snap MemorySnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil {
		t.Fatalf("memory decode: %v", err)
	}
	if snap.Goroutines < 1 {
		t.Errorf("goroutines = %d, want >= 1", snap.Goroutines)
	}

	w = do("GET", "/diagnostics/config", "")
	if w.Code != http.StatusOK {
		t.Fatalf("config get = %d", w.Code)
	}

	w = do("PATCH", "/diagnostics/config", `{"mem_heartbeat_sec": 15, "heap_dump_mb": 2048}`)
	if w.Code != http.StatusOK {
		t.Fatalf("config patch = %d: %s", w.Code, w.Body.String())
	}
	var cfg DiagnosticsConfig
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("config decode: %v", err)
	}
	if cfg.MemHeartbeatSec != 15 || cfg.HeapDumpMB != 2048 {
		t.Errorf("config after patch = %+v, want heartbeat=15 dump=2048", cfg)
	}

	for _, path := range []string{"/diagnostics/snapshot/heap", "/diagnostics/snapshot/goroutine"} {
		w = do("GET", path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", path, w.Code, w.Body.String())
		}
		if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
			t.Errorf("%s missing attachment header, got %q", path, cd)
		}
		if w.Body.Len() == 0 {
			t.Errorf("%s returned empty body", path)
		}
	}
}

// TestDiagnosticsEndpoints_Disabled verifies handlers 503 when no monitor.
func TestDiagnosticsEndpoints_Disabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{} // memMon nil
	r := gin.New()
	r.GET("/diagnostics/memory", h.MemoryDiagnostics)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/diagnostics/memory", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("memory with nil monitor = %d, want 503", w.Code)
	}
}
