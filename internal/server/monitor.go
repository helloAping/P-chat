package server

// monitor.go — runtime diagnostics for pchat-server: a /debug/pprof endpoint,
// a periodic memory/goroutine heartbeat in server-debug.log, an automatic
// heap-profile dump when the Go heap crosses a threshold, and HTTP endpoints
// the GUI uses to display current memory, tune the heartbeat/dump settings,
// and download a heap / goroutine snapshot on demand.
//
// Startup defaults come from env vars (so the feature works out of the box on
// the affected device without touching config.yaml); the GUI can then override
// the heartbeat interval and heap-dump threshold at runtime via the
// /api/v1/diagnostics/config endpoint.
//
//	PC_PPROF             "0" disables the /debug/pprof endpoint (default on)
//	PC_MEM_HEARTBEAT_SEC seconds between heartbeat log lines (default 30, 0 = off)
//	PC_HEAP_DUMP_MB      auto-dump heap profile when heap exceeds this MB
//	                     (default 1024, 0 = off)

import (
	"context"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof/* on http.DefaultServeMux at init
	"os"
	"path/filepath"
	"runtime"
	runtimepprof "runtime/pprof"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/paths"
)

// registerPprof exposes Go's pprof profiling endpoints on the gin router at
// /debug/pprof/* (heap, goroutine, allocs, profile, ...). net/http/pprof
// registers its handlers on http.DefaultServeMux when imported, so we delegate
// the whole subtree to it.
func registerPprof(r *gin.Engine) {
	if os.Getenv("PC_PPROF") == "0" {
		return
	}
	serve := func(w http.ResponseWriter, req *http.Request) {
		http.DefaultServeMux.ServeHTTP(w, req)
	}
	r.GET("/debug/pprof", gin.WrapF(serve))
	r.Any("/debug/pprof/*any", gin.WrapF(serve))
}

// MemorySnapshot is the live memory state returned to the GUI.
type MemorySnapshot struct {
	HeapAllocMB int64  `json:"heap_alloc_mb"`
	HeapSysMB   int64  `json:"heap_sys_mb"`
	HeapObjects uint64 `json:"heap_objects"`
	NumGC       uint32 `json:"num_gc"`
	Goroutines  int    `json:"goroutines"`
	RSSMB       int64  `json:"rss_mb"`
}

// DiagnosticsConfig is the mutable monitor configuration the GUI reads.
type DiagnosticsConfig struct {
	MemHeartbeatSec int    `json:"mem_heartbeat_sec"`
	HeapDumpMB      int    `json:"heap_dump_mb"`
	PprofEnabled    bool   `json:"pprof_enabled"`
	HeapDumpDir     string `json:"heap_dump_dir"`
}

// DiagnosticsConfigUpdate is the PATCH body; nil fields are left unchanged.
type DiagnosticsConfigUpdate struct {
	MemHeartbeatSec *int `json:"mem_heartbeat_sec,omitempty"`
	HeapDumpMB      *int `json:"heap_dump_mb,omitempty"`
}

// memoryMonitor owns the heartbeat / auto-dump state. Its settings are
// readable and writable at runtime (the GUI PATCHes /diagnostics/config).
type memoryMonitor struct {
	mu           sync.RWMutex
	heartbeatSec int
	heapDumpMB   int
	heapDumpDir  string

	// lastBeat / lastDump are only touched by the run loop goroutine.
	lastBeat time.Time
	lastDump time.Time
}

func newMemoryMonitor() *memoryMonitor {
	return &memoryMonitor{
		heartbeatSec: envInt("PC_MEM_HEARTBEAT_SEC", 30),
		heapDumpMB:   envInt("PC_HEAP_DUMP_MB", 1024),
		heapDumpDir:  paths.GlobalDir(),
	}
}

func (m *memoryMonitor) snapshot() MemorySnapshot {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return MemorySnapshot{
		HeapAllocMB: int64(ms.HeapAlloc >> 20),
		HeapSysMB:   int64(ms.HeapSys >> 20),
		HeapObjects: ms.HeapObjects,
		NumGC:       ms.NumGC,
		Goroutines:  runtime.NumGoroutine(),
		RSSMB:       processRSSMB(),
	}
}

func (m *memoryMonitor) config() DiagnosticsConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return DiagnosticsConfig{
		MemHeartbeatSec: m.heartbeatSec,
		HeapDumpMB:      m.heapDumpMB,
		PprofEnabled:    os.Getenv("PC_PPROF") != "0",
		HeapDumpDir:     m.heapDumpDir,
	}
}

func (m *memoryMonitor) update(u DiagnosticsConfigUpdate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u.MemHeartbeatSec != nil && *u.MemHeartbeatSec >= 0 {
		m.heartbeatSec = *u.MemHeartbeatSec
	}
	if u.HeapDumpMB != nil && *u.HeapDumpMB >= 0 {
		m.heapDumpMB = *u.HeapDumpMB
	}
}

// run ticks every second and (a) logs a heartbeat line when the interval has
// elapsed and (b) auto-dumps a heap profile once the heap crosses the
// threshold. Reading the settings under RLock each tick makes GUI updates take
// effect without restarting the loop.
func (m *memoryMonitor) run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		m.mu.RLock()
		sec, dumpMB, dir := m.heartbeatSec, m.heapDumpMB, m.heapDumpDir
		m.mu.RUnlock()

		if sec > 0 && time.Since(m.lastBeat) >= time.Duration(sec)*time.Second {
			m.lastBeat = time.Now()
			s := m.snapshot()
			log.Printf("[monitor] heap_alloc=%dMB heap_sys=%dMB objects=%d num_gc=%d goroutines=%d rss=%dMB",
				s.HeapAllocMB, s.HeapSysMB, s.HeapObjects, s.NumGC, s.Goroutines, s.RSSMB)
		}
		if dumpMB > 0 && m.snapshot().HeapAllocMB >= int64(dumpMB) && time.Since(m.lastDump) > time.Minute {
			m.lastDump = time.Now()
			if _, err := m.writeHeap(); err != nil {
				log.Printf("[monitor] auto heap dump failed: %v", err)
			}
		}
		_ = dir
	}
}

// writeHeap writes a runtime heap profile under the monitor dir and returns
// the file path.
func (m *memoryMonitor) writeHeap() (string, error) {
	m.mu.RLock()
	dir := m.heapDumpDir
	m.mu.RUnlock()
	name := filepath.Join(dir, fmt.Sprintf("heap-%s.prof", time.Now().Format("20060102-150405")))
	if err := writeProfileFile(name, func(f *os.File) error { return runtimepprof.WriteHeapProfile(f) }); err != nil {
		return "", err
	}
	log.Printf("[monitor] heap dump -> %s (heap_alloc=%dMB). Analyze: go tool pprof -top %s",
		name, m.snapshot().HeapAllocMB, name)
	return name, nil
}

// writeGoroutine writes a full goroutine stack dump under the monitor dir and
// returns the file path. The text dump shows every goroutine's stack — the
// quickest way to spot a leaked tool/forwarder goroutine.
func (m *memoryMonitor) writeGoroutine() (string, error) {
	m.mu.RLock()
	dir := m.heapDumpDir
	m.mu.RUnlock()
	name := filepath.Join(dir, fmt.Sprintf("goroutine-%s.txt", time.Now().Format("20060102-150405")))
	if err := writeProfileFile(name, func(f *os.File) error {
		p := runtimepprof.Lookup("goroutine")
		if p == nil {
			return fmt.Errorf("goroutine profile unavailable")
		}
		return p.WriteTo(f, 2) // 2 = full stack traces, not just counts
	}); err != nil {
		return "", err
	}
	log.Printf("[monitor] goroutine dump -> %s (goroutines=%d)", name, runtime.NumGoroutine())
	return name, nil
}

func writeProfileFile(name string, write func(*os.File) error) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	f, err := os.Create(name)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	defer f.Close()
	if err := write(f); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
