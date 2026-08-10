package server

// stylegen.go — 从对话生成/优化 AI 风格 的 HTTP 端点。
//
//   POST  /api/v1/stylegen          提交任务 → 202 {job_id}（后台异步跑）
//   GET   /api/v1/stylegen/:job/events  SSE 流（stage/content/thinking/result/error）
//   GET   /api/v1/stylegen/:job     JSON 状态（轮询/调试兜底）
//
// The job runs in a background goroutine owned by the in-memory
// JobManager; the POST returns immediately. Event field names mirror the
// chat SSE shape (type/content/thinking/error) so the frontend reuses its
// existing SSE parser.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/stylegen"
)

// styleGenRequest is the POST /api/v1/stylegen body.
type styleGenRequest struct {
	Mode           string `json:"mode"`            // create | optimize (empty = create)
	StyleID        string `json:"style_id"`        // optimize target style id
	Label          string `json:"label"`           // required; Chinese ok
	Requirement    string `json:"requirement"`     // user's extra requirements
	ConversationID string `json:"conversation_id"` // required; source conversation (read-only)
}

// StyleGenStart submits a stylegen job. It validates the request and
// returns 202 {job_id} immediately; the LLM work happens in the
// background and is streamed via GET /stylegen/:job/events.
func (h *Handler) StyleGenStart(c *gin.Context) {
	if h.styleGenMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":      "stylegen not available",
			"error_kind": string(stylegen.KindLLM),
		})
		return
	}

	var req styleGenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":      "invalid request body",
			"error_kind": string(stylegen.KindArgs),
		})
		return
	}

	req.Label = strings.TrimSpace(req.Label)
	req.ConversationID = strings.TrimSpace(req.ConversationID)
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "create"
	}
	if mode != "create" && mode != "optimize" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":      "mode must be create or optimize",
			"error_kind": string(stylegen.KindArgs),
		})
		return
	}
	if mode == "create" && req.Label == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":      "label is required for create",
			"error_kind": string(stylegen.KindArgs),
		})
		return
	}
	if req.ConversationID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":      "conversation_id is required (stylegen reads the current conversation)",
			"error_kind": string(stylegen.KindArgs),
		})
		return
	}
	if mode == "optimize" {
		if strings.TrimSpace(req.StyleID) == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":      "optimize requires style_id",
				"error_kind": string(stylegen.KindArgs),
			})
			return
		}
		if h.styleMgr == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error":      "style manager not available",
				"error_kind": string(stylegen.KindLLM),
			})
			return
		}
		if _, err := h.styleMgr.GetSystemPrompt(style.Style(req.StyleID)); err != nil {
			c.JSON(http.StatusNotFound, gin.H{
				"error":      fmt.Sprintf("style %q not found", req.StyleID),
				"error_kind": string(stylegen.KindNotFound),
			})
			return
		}
	}

	jobID := h.styleGenMgr.Start(stylegen.Params{
		Mode:           mode,
		StyleID:        strings.TrimSpace(req.StyleID),
		Label:          req.Label,
		Requirement:    strings.TrimSpace(req.Requirement),
		ConversationID: req.ConversationID,
	})
	c.JSON(http.StatusAccepted, gin.H{"job_id": jobID})
}

// StyleGenEvents streams a stylegen job's progress over SSE. Events:
//
//	stage   {type:"stage", stage, label}      stage transition
//	content {type:"content", content}         streamed prompt/memory delta
//	thinking{type:"thinking", thinking}       streamed reasoning delta
//	result  {type:"result", result_json}      terminal (job done)
//	error   {type:"error", error, error_kind} terminal (job failed)
//
// The channel is closed when the job reaches a terminal state; the final
// result/error event is written from the job status right before the
// stream ends.
func (h *Handler) StyleGenEvents(c *gin.Context) {
	jobID := c.Param("job")
	if h.styleGenMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":      "stylegen not available",
			"error_kind": string(stylegen.KindLLM),
		})
		return
	}
	events, ok := h.styleGenMgr.Subscribe(jobID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{
			"error":      "stylegen job not found",
			"error_kind": string(stylegen.KindNotFound),
		})
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	c.Stream(func(w io.Writer) bool {
		select {
		case <-c.Request.Context().Done():
			return false // client disconnected
		case ev, ok := <-events:
			if !ok {
				// Job finished: write the terminal result/error event
				// straight from the status, then end the stream.
				if st := h.styleGenMgr.Status(jobID); st != nil {
					writeStyleGenStatusEvent(w, st)
				}
				return false
			}
			data, err := json.Marshal(styleGenProgressPayload(ev))
			if err != nil {
				return true
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			return true
		}
	})
}

// styleGenProgressPayload maps a ProgressEvent to the SSE JSON shape.
func styleGenProgressPayload(ev stylegen.ProgressEvent) gin.H {
	switch {
	case ev.Content != "":
		return gin.H{"type": "content", "content": ev.Content}
	case ev.Thinking != "":
		return gin.H{"type": "thinking", "thinking": ev.Thinking}
	default:
		return gin.H{"type": "stage", "stage": ev.Stage, "label": ev.Label}
	}
}

// writeStyleGenStatusEvent writes a terminal result/error SSE event from
// a JobStatus.
func writeStyleGenStatusEvent(w io.Writer, st *stylegen.JobStatus) {
	var payload gin.H
	if st.Status == "error" {
		payload = gin.H{
			"type":       "error",
			"error":      st.Error,
			"error_kind": st.ErrorKind,
		}
	} else {
		payload = gin.H{"type": "result", "result_json": st.Result}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
}

// StyleGenStatus returns a pollable JSON snapshot of a job.
func (h *Handler) StyleGenStatus(c *gin.Context) {
	if h.styleGenMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":      "stylegen not available",
			"error_kind": string(stylegen.KindLLM),
		})
		return
	}
	st := h.styleGenMgr.Status(c.Param("job"))
	if st == nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error":      "stylegen job not found",
			"error_kind": string(stylegen.KindNotFound),
		})
		return
	}
	c.JSON(http.StatusOK, st)
}
