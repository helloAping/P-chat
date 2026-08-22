package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/memory"
)

type subagentJobCanceller interface {
	Cancel(id string) bool
}

type subagentJobEventSource interface {
	Subscribe(sessionID string) (<-chan memory.SubagentJobEvent, func())
	Publish(ev memory.SubagentJobEvent)
}

// SetSubagentJobCanceller wires process-local async subagent cancellation
// into the HTTP API. The durable job table remains the source of truth.
func (h *Handler) SetSubagentJobCanceller(c subagentJobCanceller) {
	h.subagentJobs = c
}

// SetSubagentJobEvents wires process-local async subagent job notifications.
func (h *Handler) SetSubagentJobEvents(source subagentJobEventSource) {
	h.subagentJobEvents = source
}

// ListSubagentJobs returns recent async subagent jobs for a session.
func (h *Handler) ListSubagentJobs(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "job store is not configured"})
		return
	}
	sessionID := c.Param("id")
	limit := parseIntQuery(c, "limit", 20)
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	jobs, err := h.store.ListSubagentJobs(sessionID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"jobs": jobs})
}

// GetSubagentJob returns one async subagent job scoped to the session.
func (h *Handler) GetSubagentJob(c *gin.Context) {
	job, ok := h.loadSubagentJob(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, job)
}

// CancelSubagentJob cancels an async subagent job by task_id.
func (h *Handler) CancelSubagentJob(c *gin.Context) {
	job, ok := h.loadSubagentJob(c)
	if !ok {
		return
	}
	if isSubagentJobTerminal(job.Status) {
		c.JSON(http.StatusOK, gin.H{
			"job":            job,
			"cancelled_live": false,
		})
		return
	}
	cancelledLive := false
	if h.subagentJobs != nil {
		cancelledLive = h.subagentJobs.Cancel(job.ID)
	}
	if err := h.store.CancelSubagentJob(job.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	updated, ok, err := h.store.GetSubagentJob(job.SessionID, job.TaskID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "subagent job not found"})
		return
	}
	if h.subagentJobEvents != nil {
		h.subagentJobEvents.Publish(memory.SubagentJobEvent{Type: "updated", Job: &updated})
	}
	c.JSON(http.StatusOK, gin.H{
		"job":            updated,
		"cancelled_live": cancelledLive,
	})
}

// SubagentJobEvents streams async subagent job updates for a session.
func (h *Handler) SubagentJobEvents(c *gin.Context) {
	if h.subagentJobEvents == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subagent job events are not configured"})
		return
	}
	sessionID := c.Param("id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session id is required"})
		return
	}
	events, unsubscribe := h.subagentJobEvents.Subscribe(sessionID)
	defer unsubscribe()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	c.Stream(func(w io.Writer) bool {
		select {
		case <-c.Request.Context().Done():
			return false
		case ev, ok := <-events:
			if !ok {
				return false
			}
			writeSubagentJobSSE(w, ev)
			return true
		case <-ticker.C:
			writeSubagentJobSSE(w, memory.SubagentJobEvent{Type: "heartbeat"})
			return true
		}
	})
}

func writeSubagentJobSSE(w io.Writer, ev memory.SubagentJobEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
}

func (h *Handler) loadSubagentJob(c *gin.Context) (memory.SubagentJob, bool) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "job store is not configured"})
		return memory.SubagentJob{}, false
	}
	sessionID := c.Param("id")
	taskID := c.Param("task_id")
	job, ok, err := h.store.GetSubagentJob(sessionID, taskID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return memory.SubagentJob{}, false
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "subagent job not found"})
		return memory.SubagentJob{}, false
	}
	return job, true
}

func isSubagentJobTerminal(status string) bool {
	switch status {
	case memory.SubagentJobSucceeded, memory.SubagentJobFailed, memory.SubagentJobCancelled:
		return true
	default:
		return false
	}
}
