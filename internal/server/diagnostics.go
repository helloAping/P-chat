package server

// diagnostics.go — HTTP endpoints backing the GUI "诊断 / 内存监控" settings
// tab. The GUI reads live memory, tunes the heartbeat / heap-dump settings,
// and downloads a heap or goroutine snapshot on demand.
//
//	GET   /api/v1/diagnostics/memory             live MemorySnapshot
//	GET   /api/v1/diagnostics/config             DiagnosticsConfig
//	PATCH /api/v1/diagnostics/config             update heartbeat / dump threshold
//	GET   /api/v1/diagnostics/snapshot/heap      download heap-<ts>.prof
//	GET   /api/v1/diagnostics/snapshot/goroutine download goroutine-<ts>.txt

import (
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/gin-gonic/gin"
)

// MemoryDiagnostics returns the current memory/goroutine snapshot.
func (h *Handler) MemoryDiagnostics(c *gin.Context) {
	if h.memMon == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory monitor not enabled"})
		return
	}
	c.JSON(http.StatusOK, h.memMon.snapshot())
}

// DiagnosticsConfigGet returns the current monitor configuration.
func (h *Handler) DiagnosticsConfigGet(c *gin.Context) {
	if h.memMon == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory monitor not enabled"})
		return
	}
	c.JSON(http.StatusOK, h.memMon.config())
}

// DiagnosticsConfigUpdate patches the monitor configuration.
func (h *Handler) DiagnosticsConfigUpdate(c *gin.Context) {
	if h.memMon == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory monitor not enabled"})
		return
	}
	var body DiagnosticsConfigUpdate
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.memMon.update(body)
	c.JSON(http.StatusOK, h.memMon.config())
}

// DiagnosticsHeapSnapshot writes a heap profile to disk and downloads it so
// the user can analyze it with `go tool pprof -top <file>`.
func (h *Handler) DiagnosticsHeapSnapshot(c *gin.Context) {
	if h.memMon == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory monitor not enabled"})
		return
	}
	path, err := h.memMon.writeHeap()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(path)))
	c.File(path)
}

// DiagnosticsGoroutineSnapshot writes a goroutine stack dump to disk and
// downloads it — the quickest way to spot a leaked tool/forwarder goroutine.
func (h *Handler) DiagnosticsGoroutineSnapshot(c *gin.Context) {
	if h.memMon == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory monitor not enabled"})
		return
	}
	path, err := h.memMon.writeGoroutine()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(path)))
	c.File(path)
}
