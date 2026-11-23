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
	"net"
	"net/http"
	"path/filepath"
	"strings"

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
	if !allowDiagnosticsSnapshot(c) {
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
	if !allowDiagnosticsSnapshot(c) {
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

// allowDiagnosticsSnapshot 在 SEC-01 管理级鉴权完全接线前提供默认关闭、仅本机的双闸门。
// allowDiagnosticsSnapshot provides default-off and loopback-only gates until SEC-01 management auth is fully wired.
func allowDiagnosticsSnapshot(c *gin.Context) bool {
	if !diagnosticsSnapshotsEnabled() {
		c.JSON(http.StatusForbidden, gin.H{
			"error":      "diagnostics snapshot downloads are disabled; set PC_DIAGNOSTICS_SNAPSHOTS=1 before server startup to enable them on loopback",
			"error_kind": "diagnostics_snapshot_disabled",
		})
		return false
	}
	if !isLoopbackRemoteAddr(c.Request.RemoteAddr) {
		c.JSON(http.StatusForbidden, gin.H{
			"error":      "diagnostics snapshot downloads are restricted to loopback clients",
			"error_kind": "diagnostics_snapshot_local_only",
		})
		return false
	}
	return true
}

// isLoopbackRemoteAddr 仅信任服务端提供的 TCP 对端地址，绝不信任可伪造的转发头。
// isLoopbackRemoteAddr only trusts the TCP peer address supplied by the server.
func isLoopbackRemoteAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(strings.TrimSpace(host), "[]"))
	return ip != nil && ip.IsLoopback()
}
