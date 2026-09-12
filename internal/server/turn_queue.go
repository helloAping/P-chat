package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/memory"
)

// TurnQueueItemResponse 是 GUI 使用的队列项响应。
// TurnQueueItemResponse is the GUI-facing queued turn shape.
type TurnQueueItemResponse struct {
	ID              int64               `json:"id"`
	SessionID       string              `json:"session_id"`
	Status          string              `json:"status"`
	Message         string              `json:"message"`
	ClientMsgID     int64               `json:"client_msg_id"`
	AttachmentCount int                 `json:"attachment_count"`
	Error           string              `json:"error,omitempty"`
	CreatedAt       int64               `json:"created_at"`
	UpdatedAt       int64               `json:"updated_at"`
	StartedAt       int64               `json:"started_at,omitempty"`
	FinishedAt      int64               `json:"finished_at,omitempty"`
	Payload         *SendMessageRequest `json:"payload,omitempty"`
}

// TurnQueueListResponse 是 GET /turn-queue 的响应。
// TurnQueueListResponse is the response for GET /turn-queue.
type TurnQueueListResponse struct {
	Items []TurnQueueItemResponse `json:"items"`
}

// TurnQueueItemEnvelope 是单项队列 API 的响应。
// TurnQueueItemEnvelope is the response envelope for one queue item.
type TurnQueueItemEnvelope struct {
	Item TurnQueueItemResponse `json:"item"`
}

type turnQueueFailRequest struct {
	Error string `json:"error"`
}

type turnQueueEditRequest struct {
	Message string `json:"message"`
}

func (h *Handler) turnQueueSession(c *gin.Context) (string, bool) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return "", false
	}
	id := c.Param("id")
	if _, err := h.store.GetConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return "", false
	}
	return id, true
}

// EnqueueTurnQueueItem 将一个用户回合追加到当前会话队列。
// EnqueueTurnQueueItem appends one user turn to the session queue.
func (h *Handler) EnqueueTurnQueueItem(c *gin.Context) {
	id, ok := h.turnQueueSession(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 10<<20)

	var req SendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := validateQueuedTurnRequest(req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	payload, err := json.Marshal(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("encode queue payload: %v", err)})
		return
	}
	item, err := h.store.CreateTurnQueueItem(id, payload, req.Message, req.ClientMsgID, len(req.Attachments))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("enqueue turn: %v", err)})
		return
	}
	c.JSON(http.StatusCreated, TurnQueueItemEnvelope{Item: turnQueueItemResponse(item, nil)})
}

// ListTurnQueueItems 返回当前会话未完成的队列项。
// ListTurnQueueItems returns unfinished queue items for the session.
func (h *Handler) ListTurnQueueItems(c *gin.Context) {
	id, ok := h.turnQueueSession(c)
	if !ok {
		return
	}
	if _, busy := h.sessionLocks.Load(id); !busy {
		_ = h.store.RequeueRunningTurnQueueItems(id)
	}
	items, err := h.store.ListTurnQueueItems(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("list turn queue: %v", err)})
		return
	}
	resp := make([]TurnQueueItemResponse, 0, len(items))
	for _, item := range items {
		resp = append(resp, turnQueueItemResponse(item, nil))
	}
	c.JSON(http.StatusOK, TurnQueueListResponse{Items: resp})
}

// EditTurnQueueItem 修改尚未领取的排队消息。
// EditTurnQueueItem updates the text of an unclaimed queued turn.
func (h *Handler) EditTurnQueueItem(c *gin.Context) {
	id, ok := h.turnQueueSession(c)
	if !ok {
		return
	}
	queueID, ok := parseTurnQueueID(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	var req turnQueueEditRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message is required"})
		return
	}
	item, found, err := h.store.EditTurnQueueItem(id, queueID, req.Message)
	if errors.Is(err, memory.ErrTurnQueueNotEditable) {
		c.JSON(http.StatusConflict, gin.H{"error": "turn queue item is no longer editable"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("edit turn queue item: %v", err)})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "queued turn not found"})
		return
	}
	c.JSON(http.StatusOK, TurnQueueItemEnvelope{Item: turnQueueItemResponse(item, nil)})
}

// ClaimNextTurnQueueItem 领取下一条 queued 项用于执行。
// ClaimNextTurnQueueItem claims the next queued turn for execution.
func (h *Handler) ClaimNextTurnQueueItem(c *gin.Context) {
	id, ok := h.turnQueueSession(c)
	if !ok {
		return
	}
	if _, busy := h.sessionLocks.Load(id); busy {
		c.JSON(http.StatusConflict, gin.H{"error": "a message is already being processed for this session"})
		return
	}
	activeSubagents, err := h.store.HasActiveSubagentJobs(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("check background subagent jobs: %v", err)})
		return
	}
	if activeSubagents {
		c.JSON(http.StatusConflict, gin.H{"error": "background subagent jobs are still running for this session"})
		return
	}
	item, found, err := h.store.ClaimNextTurnQueueItem(id)
	if errors.Is(err, memory.ErrTurnQueueBlocked) {
		c.JSON(http.StatusConflict, gin.H{"error": "turn queue is blocked by a failed item"})
		return
	}
	if errors.Is(err, memory.ErrTurnQueueRunning) {
		c.JSON(http.StatusConflict, gin.H{"error": "turn queue head is already running"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("claim turn queue: %v", err)})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "turn queue is empty"})
		return
	}
	var payload SendMessageRequest
	if err := json.Unmarshal([]byte(item.PayloadJSON), &payload); err != nil {
		_, _, _ = h.store.FailTurnQueueItem(id, item.ID, "queued payload is invalid")
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("decode queue payload: %v", err)})
		return
	}
	c.JSON(http.StatusOK, TurnQueueItemEnvelope{Item: turnQueueItemResponse(item, &payload)})
}

// CompleteTurnQueueItem 标记队列项完成。
// CompleteTurnQueueItem marks one queued turn as done.
func (h *Handler) CompleteTurnQueueItem(c *gin.Context) {
	h.finishTurnQueueItem(c, memory.TurnQueueStatusDone)
}

// FailTurnQueueItem 标记队列项失败。
// FailTurnQueueItem marks one queued turn as failed.
func (h *Handler) FailTurnQueueItem(c *gin.Context) {
	id, ok := h.turnQueueSession(c)
	if !ok {
		return
	}
	queueID, ok := parseTurnQueueID(c)
	if !ok {
		return
	}
	var req turnQueueFailRequest
	_ = c.ShouldBindJSON(&req)
	if req.Error == "" {
		req.Error = "queued turn failed"
	}
	item, found, err := h.store.FailTurnQueueItem(id, queueID, req.Error)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("fail turn queue item: %v", err)})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "turn queue item not found"})
		return
	}
	c.JSON(http.StatusOK, TurnQueueItemEnvelope{Item: turnQueueItemResponse(item, nil)})
}

// RetryTurnQueueItem 将失败项重新放回队列。
// RetryTurnQueueItem moves a failed item back to queued.
func (h *Handler) RetryTurnQueueItem(c *gin.Context) {
	id, ok := h.turnQueueSession(c)
	if !ok {
		return
	}
	queueID, ok := parseTurnQueueID(c)
	if !ok {
		return
	}
	item, found, err := h.store.RequeueFailedTurnQueueItem(id, queueID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("retry turn queue item: %v", err)})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "failed turn queue item not found"})
		return
	}
	c.JSON(http.StatusOK, TurnQueueItemEnvelope{Item: turnQueueItemResponse(item, nil)})
}

// DeleteTurnQueueItem 取消尚未执行的队列项。
// DeleteTurnQueueItem cancels a queued or failed item.
func (h *Handler) DeleteTurnQueueItem(c *gin.Context) {
	id, ok := h.turnQueueSession(c)
	if !ok {
		return
	}
	queueID, ok := parseTurnQueueID(c)
	if !ok {
		return
	}
	item, found, err := h.store.CancelTurnQueueItem(id, queueID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("delete turn queue item: %v", err)})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "turn queue item not found"})
		return
	}
	c.JSON(http.StatusOK, TurnQueueItemEnvelope{Item: turnQueueItemResponse(item, nil)})
}

// ClearTurnQueue 清空当前会话待执行队列。
// ClearTurnQueue cancels all queued or failed items for one session.
func (h *Handler) ClearTurnQueue(c *gin.Context) {
	id, ok := h.turnQueueSession(c)
	if !ok {
		return
	}
	count, err := h.store.ClearTurnQueue(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("clear turn queue: %v", err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "cleared": count})
}

func (h *Handler) finishTurnQueueItem(c *gin.Context, status string) {
	id, ok := h.turnQueueSession(c)
	if !ok {
		return
	}
	queueID, ok := parseTurnQueueID(c)
	if !ok {
		return
	}
	var (
		item  memory.TurnQueueItem
		found bool
		err   error
	)
	switch status {
	case memory.TurnQueueStatusDone:
		item, found, err = h.store.CompleteTurnQueueItem(id, queueID)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported queue status"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("finish turn queue item: %v", err)})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "turn queue item not found"})
		return
	}
	c.JSON(http.StatusOK, TurnQueueItemEnvelope{Item: turnQueueItemResponse(item, nil)})
}

func validateQueuedTurnRequest(req SendMessageRequest) error {
	const maxMessageLen = 1 << 20
	if req.Message == "" {
		return fmt.Errorf("message is required")
	}
	if len(req.Message) > maxMessageLen {
		return fmt.Errorf("message too long: %d bytes (max %d)", len(req.Message), maxMessageLen)
	}
	if len(req.Attachments) > 16 {
		return fmt.Errorf("too many attachments: %d (max 16)", len(req.Attachments))
	}
	if req.TurnModePolicy != "" {
		if _, ok := agent.ParseTurnModePolicy(req.TurnModePolicy); !ok {
			return fmt.Errorf(`turn_mode_policy must be "auto", "plan", or "build"`)
		}
	}
	if req.ClientMsgID <= 0 {
		return fmt.Errorf("client_msg_id is required")
	}
	return nil
}

func parseTurnQueueID(c *gin.Context) (int64, bool) {
	raw := c.Param("queue_id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid queue_id"})
		return 0, false
	}
	return id, true
}

func turnQueueItemResponse(item memory.TurnQueueItem, payload *SendMessageRequest) TurnQueueItemResponse {
	return TurnQueueItemResponse{
		ID:              item.ID,
		SessionID:       item.SessionID,
		Status:          item.Status,
		Message:         item.Message,
		ClientMsgID:     item.ClientMsgID,
		AttachmentCount: item.AttachmentCount,
		Error:           item.Error,
		CreatedAt:       unixOrZero(item.CreatedAt),
		UpdatedAt:       unixOrZero(item.UpdatedAt),
		StartedAt:       unixOrZero(item.StartedAt),
		FinishedAt:      unixOrZero(item.FinishedAt),
		Payload:         payload,
	}
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
