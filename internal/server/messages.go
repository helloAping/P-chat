package server

// messages.go — the user-facing message endpoint that drives the
// chat loop:
//
//   POST /api/v1/sessions/:id/messages            (SendMessage)
//
// The actual SSE frame emission lives in respondSSE; the chunk
// → wire mapping lives in stream_adapter.go (T04 sibling).
// loadUserMessageSummary is the regen-reply input helper.
//
// Split from handler.go in T04. Behaviour unchanged.

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/trace"
)

func (h *Handler) SendMessage(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	if _, err := h.store.GetConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	// Cap the request body so a malicious client cannot OOM the
	// server by posting a multi-GB JSON. 10 MiB is generous: a
	// 1 MiB message + a few inline base64 image attachments
	// easily fits; anything larger should be sent as a /upload
	// reference, not inlined.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 10<<20)

	var req SendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Defensive: also cap the parsed Message field in case the
	// body limit is bypassed by a proxy.
	const maxMessageLen = 1 << 20 // 1 MiB
	if len(req.Message) > maxMessageLen {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": fmt.Sprintf("message too long: %d bytes (max %d); split into multiple turns or attach a file reference", len(req.Message), maxMessageLen),
		})
		return
	}
	if len(req.Attachments) > 16 {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("too many attachments: %d (max 16)", len(req.Attachments))})
		return
	}
	if req.ClientMsgID < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "client_msg_id must be a positive integer"})
		return
	}

	// Serialise all state changes for a session, including the idempotency
	// lookup below. A retry can arrive after the previous stream completed but
	// before the caller observed its final SSE frame; it must never start a
	// second model run for the same client-minted user row.
	if _, loaded := h.sessionLocks.LoadOrStore(id, struct{}{}); loaded {
		c.JSON(http.StatusConflict, gin.H{"error": "a message is already being processed for this session"})
		return
	}
	defer h.sessionLocks.Delete(id)

	if req.ClientMsgID > 0 {
		identity, found, err := h.store.FindMessageIdentity(req.ClientMsgID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("check client message id: %v", err)})
			return
		}
		if found {
			if identity.ConversationID == id && identity.Role == llm.RoleUser && identity.Content == req.Message {
				c.JSON(http.StatusConflict, gin.H{
					"error": "this client message has already been accepted; recover the existing reply instead of resending it",
					"code":  "duplicate_client_message",
				})
				return
			}
			c.JSON(http.StatusConflict, gin.H{
				"error": "client_msg_id is already bound to a different message",
				"code":  "client_message_id_conflict",
			})
			return
		}
	}

	// Resolve style: body override → per-session override →
	// configured default → built-in "tech" fallback. The
	// per-session lookup is the one piece that was missing —
	// without it, switching the picker never took effect on the
	// next message because the body always omits the style.
	s, _ := style.ParseStyle(req.Style)
	if s == "" {
		s = style.Style(h.sessionStyle(id))
	}
	if s == "" {
		if def := style.Style(h.getCfg().Style.Default); def != "" {
			s = def
		} else {
			s = style.Tech
		}
	}

	// Resolve provider: body override → per-session override →
	// configured default. Validate before mutating anything.
	provider := h.sessionProvider(id)
	if req.Provider != "" {
		if !h.validProvider(req.Provider) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unknown provider %q", req.Provider)})
			return
		}
		provider = req.Provider
	}

	// Resolve model: body override → per-session override → that
	// provider's EffectiveModel.
	model := h.sessionModel(id, provider)
	if req.Model != "" {
		if !h.validModel(provider, req.Model) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("model %q not found under provider %q", req.Model, provider)})
			return
		}
		model = req.Model
	}

	workMode := h.sessionWorkMode(id)
	if req.WorkMode != "" {
		workMode = config.WorkMode(req.WorkMode).Normalize()
	}

	// Persist whichever fields the caller actually changed. The
	// setSessionMeta helper is a no-op when nothing differs, so
	// sending an empty body is fine.
	h.setSessionMeta(id, string(s), provider, model)
	if req.WorkMode != "" {
		h.setSessionMetaWorkMode(id, string(workMode))
	}

	// Hydrate the durable plan before the agent builds its prompt. A
	// resume request must see the interrupted in_progress item even after
	// a server restart; clear is idempotent because the UI may have already
	// called DELETE /todos during preflight.
	todoMode := agent.NormalizeTodoMode(req.TodoMode)
	h.hydrateSessionTodos(id)
	if todoMode == agent.TodoModeClear {
		if err := h.clearSessionTodos(id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	// Build messages: history after last compression + new user message.
	// Messages older than the compressed range are replaced by the
	// CompressedSummary field on the ChatRequest. All reads go through
	// the per-session variants so concurrent SendMessage calls on
	// different sessions don't race.
	meta := h.ensureMetaLoaded(id)
	histMsgs, compSummary := h.loadHistoryForSend(c.Request.Context(), id, provider, model)
	msgs := buildLLMMessages(histMsgs)
	historyMessageCount := len(msgs)
	msgs = append(msgs, llm.ChatMessage{
		Role:        llm.RoleUser,
		Type:        llm.TypeText,
		Content:     req.Message,
		MsgType:     llm.MsgTypeText,
		SubmitToLLM: 1,
	})

	chatReq := agent.ChatRequest{
		Style:               s,
		WorkMode:            workMode,
		Provider:            provider,
		Model:               model,
		Messages:            msgs,
		HistoryMessageCount: historyMessageCount,
		Attachments:         req.Attachments,
		// Forward the frontend's client-minted row id. The
		// agent uses it as the explicit SQLite row id for
		// this turn's user message, so rollback/regen
		// have a valid target even when the LLM call
		// fails before the SSE `done` event lands. See
		// the comment on SendMessageRequest.ClientMsgID.
		ClientMsgID:       req.ClientMsgID,
		ReasoningEffort:   meta.ReasoningEffort,
		CompressedSummary: compSummary,
		SessionID:         id,
		ProjectRoot:       meta.ProjectPath,
		SkillContext:      req.SkillContext,
		PlanMode:          meta.PlanMode,
		PermissionLevel:   meta.PermissionLevel,
		KBBase:            meta.KnowledgeBase,
		AutoContinue:      h.sessionAutoContinue(id),
		TodoLongRunMode:   h.sessionTodoLongRunMode(id),
		TodoMode:          todoMode,
		// P3-3: copy the trace id off the request context
		// so the agent loop can stamp every emitted chunk
		// without re-reading ctx. The traceIDMiddleware on
		// the server has already minted one (or adopted
		// the client-supplied one) and put it under
		// trace.ctxKey.
		TraceID: trace.FromContext(c.Request.Context()),
	}

	// Hard wall-clock cap for the whole turn. Backstop for hangs that
	// escape the per-tool / LLM-stream timeouts: an exec_command whose
	// orphaned grandchild holds the output pipes, or an SSE write
	// blocked against a dead client connection. When the deadline
	// fires, ChatWithTools' ctx.Done() path emits an error event and
	// the session lock is released, so the user is never stuck on a
	// permanently "busy" session.
	turnCtx := c.Request.Context()
	var cancel context.CancelFunc
	if maxSec := h.getCfg().Limits.MaxTurnSeconds; maxSec > 0 {
		turnCtx, cancel = context.WithTimeout(turnCtx, time.Duration(maxSec)*time.Second)
		// Replace the request context so respondSSE can observe the
		// deadline error and emit a terminal error frame when the
		// stream closes without a done event.
		c.Request = c.Request.WithContext(turnCtx)
	} else {
		// No hard cap configured: still create a cancelable ctx so
		// respondSSE's write-timeout path and POST /cancel-stream
		// can abort a stuck turn instead of blocking forever.
		turnCtx, cancel = context.WithCancel(turnCtx)
	}
	defer cancel()
	unregister := h.registerTurnCancel(id, cancel)
	defer unregister()

	stream := h.agent.ChatStream(turnCtx, chatReq)
	h.respondSSE(c, stream, id, provider, model)
}

// turnCancelRegistration is the cleanup handle returned by
// registerTurnCancel. Caller must invoke it when the stream ends.
type turnCancelRegistration func()

// registerTurnCancel publishes the turn's cancel func so
// respondSSE's write-timeout path and POST /cancel-stream can
// abort it from outside. Returns a cleanup func the caller defers;
// a stale cancel for a finished turn is a harmless no-op, but the
// entry should be removed once the stream completes so the map
// doesn't grow. Shared by SendMessage and Regenerate (both feed
// respondSSE).
func (h *Handler) registerTurnCancel(id string, cancel context.CancelFunc) turnCancelRegistration {
	h.turnCancels.Store(id, cancel)
	return func() {
		h.turnCancels.Delete(id)
	}
}

// CancelStream aborts the in-flight SendMessage turn for a session.
// POST /api/v1/sessions/:id/cancel-stream. The frontend calls this
// after an AbortController fires so the server-side agent loop
// exits promptly and the session lock releases — without it, a
// client that stops reading but keeps the TCP connection open
// (frozen WebView2 renderer) would hold the turn until the
// MaxTurnSeconds backstop.
//
// Idempotent: cancelling a turn that already finished (or never
// started) is a no-op. Always returns 200 so the client never
// sees an error for a best-effort abort.
func (h *Handler) CancelStream(c *gin.Context) {
	id := c.Param("id")
	if v, ok := h.turnCancels.Load(id); ok {
		if f, ok := v.(context.CancelFunc); ok {
			log.Printf("[cancel-stream] cancelling turn for session %s", id)
			f()
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ToolResultResponse is the body of GET
// /sessions/:id/messages/:msg_id/tool-result/:tool_id.
type ToolResultResponse struct {
	ToolID   string `json:"tool_id"`
	ToolName string `json:"tool_name,omitempty"`
	Content  string `json:"content"`
	// Truncated echoes the byte length of the untruncated result
	// so the frontend can label the affordance.
	Bytes int `json:"bytes"`
}

// GetToolResult returns the full (untruncated) body of a tool
// result whose SSE event was truncated server-side (tool results >
// MaxToolResultFullBytes ship only a display preview; the full body
// is parked in the agent's bounded cache and fetched here on
// demand). This keeps multi-MB outputs out of the Vue reactive
// store while still letting the user read them.
//
// GET /api/v1/sessions/:id/messages/:user_msg_id/tool-result/:tool_id
func (h *Handler) GetToolResult(c *gin.Context) {
	id := c.Param("id")
	toolID := c.Param("tool_id")
	content, ok := agent.LookupTruncatedResult(id, toolID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "tool result not found or expired"})
		return
	}
	c.JSON(http.StatusOK, ToolResultResponse{
		ToolID:  toolID,
		Content: content,
		Bytes:   len(content),
	})
}

func (h *Handler) loadHistoryForSend(ctx context.Context, id, provider, model string) ([]llm.ChatMessage, string) {
	_ = h.store.Flush()
	lastComp := h.store.LastCompressedIDFor(id)
	contextCap := h.contextMessageLimit(provider, model)
	if contextCap <= 0 {
		contextCap = 50
	}

	if h.store.CountChatMessages(id) > contextCap && h.summarizer != nil {
		if _, _, err := h.summarizer.Compress(ctx, id); err != nil {
			log.Printf("%s[history] pre-send compact failed: %v", trace.LogPrefix(ctx), err)
		}
		lastComp = h.store.LastCompressedIDFor(id)
	}

	var histMsgs []llm.ChatMessage
	var compSummary string
	if lastComp > 0 {
		histMsgs, _, _ = h.store.GetChatMessagesAfterIDFor(id, contextCap, lastComp)
		// Carry the pre-summarized history so the agent can inject
		// "[前文摘要]" into the system prompt — the LLM must know
		// what the compressed-away history contained. Dropping this
		// (a regression in an earlier refactor) made every post-
		// compaction turn blind to the summarized context.
		compSummary = h.store.CompressedSummaryFor(id)
	} else {
		limit := 0
		if h.store.CountChatMessages(id) > contextCap {
			limit = contextCap
		}
		histMsgs = h.store.GetChatMessagesFor(id, limit)
	}
	// Media rows persisted as "upl://<id>" references must come
	// back as base64 for the LLM request; the disk read replaces
	// the old SQLite read and keeps the model's vision context
	// intact. Best-effort: a missing file degrades to a text
	// marker instead of breaking the turn.
	histMsgs = resolveHistoryUploads(histMsgs, h.attachResolver)
	return histMsgs, compSummary
}

// respondSSE writes a chat stream to the response. Used
// by both SendMessage and the P1-3 Regenerate endpoint —
// both paths produce an `agent.ChatStreamChunk` channel
// and need the same SSE envelope (data + id, flush
// per-frame, done-handling). Keep this the only place
// that knows how an internal chunk becomes wire bytes.

// writeSSEWithTimeout writes one SSE frame under a hard write
// deadline. A WebView2 renderer frozen by memory pressure stops
// reading the response body but keeps the TCP connection open —
// net/http only cancels the request ctx when the connection
// *closes*, so a stuck client would otherwise keep the turn alive
// until MaxTurnSeconds. The deadline makes the write fail fast,
// respondSSE cancels the turn, and the agent loop exits via
// ctx.Done().
func writeSSEWithTimeout(w http.ResponseWriter, ev StreamEvent, timeout time.Duration) error {
	// http.NewResponseController unwraps gin's writer down to the
	// underlying net.Conn and sets a write deadline there.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Now().Add(timeout)); err == nil {
		return writeSSEFrame(w, ev)
	}
	// No deadline support (e.g. a test buffer): goroutine + timer.
	// The caller stops writing after the first failure, so the
	// abandoned goroutine is bounded to one write.
	type result struct{ err error }
	done := make(chan result, 1)
	go func() {
		done <- result{err: writeSSEFrame(w, ev)}
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case r := <-done:
		return r.err
	case <-t.C:
		return fmt.Errorf("sse write timed out after %s (client not reading)", timeout)
	}
}

func (h *Handler) respondSSE(c *gin.Context, stream <-chan agent.ChatStreamChunk, sessionID, provider, model string) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Header("X-Session-ID", sessionID)
	c.Header("X-Provider", provider)
	c.Header("X-Model", model)
	// P3-3: echo the trace id from the request context so
	// curl users can grep server-debug.log with the same
	// id they see in the response. The middleware already
	// set this header on the *outgoing* response, but a
	// second set here is harmless and self-documenting —
	// any future caller of respondSSE that bypassed the
	// middleware (e.g. a unit test) still gets the right
	// header.
	if tid := trace.FromContext(c.Request.Context()); tid != "" {
		c.Header("X-Trace-Id", tid)
	}
	c.Writer.Flush()

	// Track whether a terminal frame (done / error with Done=true)
	// was emitted. When the stream closes without one — e.g. the
	// turn's hard timeout fired and ChatWithTools' ctx.Done() paths
	// returned silently — emit a terminal error frame so the client
	// can distinguish "interrupted" from "completed" instead of
	// treating a truncation as success.
	terminalEmitted := false
	// sseWriteTimeout bounds one frame write. When the client stops
	// reading (frozen renderer), the write hangs; the timeout cancels
	// the turn via turnCtx so the agent loop exits and the session
	// lock releases instead of blocking until MaxTurnSeconds.
	const sseWriteTimeout = 10 * time.Second
	// cancelTurn aborts the in-flight turn. Resolved from
	// c.Request.Context()'s cancel — SendMessage registered it in
	// h.turnCancels; we look it up here so Regenerate (which also
	// calls respondSSE) works too.
	cancelTurn := func() {
		if v, ok := h.turnCancels.Load(sessionID); ok {
			if f, ok := v.(context.CancelFunc); ok {
				f()
			}
		}
	}
	c.Stream(func(w io.Writer) bool {
		// gin passes c.Writer (an http.ResponseWriter) here; cast
		// back so writeSSEWithTimeout can reach the net.Conn.
		rw, isRW := w.(http.ResponseWriter)
		if !isRW {
			rw = c.Writer
		}
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[sse] panic in stream writer: %v", r)
			}
		}()
		chunk, ok := <-stream
		if !ok {
			// Stream closed without a done event. If the turn was
			// interrupted by its hard deadline, surface that to the
			// client as a terminal error frame.
			if !terminalEmitted {
				terminalEmitted = true
				if err := c.Request.Context().Err(); err != nil {
					ev := streamEventFromChunk(agent.ChatStreamChunk{
						Error:    fmt.Sprintf("回合超出最长执行时间被终止: %v", err),
						ErrorKind: "turn_timeout",
						Done:     true,
					}, provider, model, streamDoneIDs{})
					if werr := writeSSEWithTimeout(rw, ev, sseWriteTimeout); werr == nil {
						if fl, ok := c.Writer.(http.Flusher); ok {
							fl.Flush()
						}
					}
				}
			}
			return false
		}
		var ids streamDoneIDs
		if chunk.Done && h.store != nil {
			ids.userMessageID = h.store.GetLastUserMessageID(sessionID)
			ids.lastMessageID = h.store.GetLastMessageID(sessionID)
		}
		ev := streamEventFromChunk(chunk, provider, model, ids)
		if chunk.Done {
			terminalEmitted = true
		}
		if ev.Type == "question" {
			log.Printf("[sse] writing question event (%d bytes json)", len(ev.QuestionJSON))
		}
		if err := writeSSEWithTimeout(rw, ev, sseWriteTimeout); err != nil {
			log.Printf("%s[sse] write failed (client stopped reading): %v — cancelling turn", trace.LogPrefix(c.Request.Context()), err)
			cancelTurn()
			return false
		}
		if fl, ok := c.Writer.(http.Flusher); ok {
			fl.Flush()
		}
		return !chunk.Done
	})
}

// chunkToEvent maps an internal ChatStreamChunk to a public
// StreamEvent the API exposes. provider/model are stamped on
// every event so the client can show a small "produced by" badge
// on the assistant message even when the model is unknown to the
// chunk itself.
//
// ★ chunkToEvent 是服务端 ChatStreamChunk → 前端 StreamEvent 的映射器。
// 映射规则（按优先级，更具体的匹配优先）：
//  1. question JSON 非空     → type:"question"   (问题模态框)
//  2. tool_confirm JSON 非空 → type:"tool_confirm" (沙箱确认)
//  3. Error 非空             → type:"error"       (LLM 错误)
//  4. Done == true           → type:"done"        (★ 终止 SSE)
//  5. ToolName 非空          → type:"tool"        (工具调用结果)
//  6. Thinking 非空          → type:"thinking"    (思考增量)
//  7. Content 非空           → type:"content"     (文本增量)
//  8. ContentRewrite 非空    → type:"content_rewrite" (后处理文本重写)
//  9. ThinkingRewrite 非空   → type:"thinking_rewrite"
//  10. Phase 非空             → type:"phase"       (子代理开始/结束 + 系统状态)
//  11. 其他                   → type:"phase"       (心跳)
//
// Sub_agent 字段在所有分支中无条件拷贝，确保子代理的 content/thinking/tool/phase
// 事件全部带有 sub_agent=true 标记，前端能正确路由到嵌套 SubAgentCard。
//
// 修改指南 → docs/modules/server.md
// RegenerateRequest is the body of POST
// /api/v1/sessions/:id/regenerate. The user_message_id
// is the SQLite row id of the user message whose
// assistant reply should be re-produced. The handler
// physically deletes every message with id > user_message_id
// in the same conversation, then re-runs the agent loop
// from scratch (the user message itself stays).
//
// Why physical delete (not soft-mark): the chat store
// reads back the conversation as a single source of
// truth. Soft marks would surface as "ghost" rows in
// the assistant message list until something explicitly
// reaped them, and the per-session meta's
// last_message_id pointer would also need to roll back
// to the right value. The existing rollback code path
// already does the same physical delete + meta rewrite
// (DeleteMessagesFrom), and we don't put the result on
// the undo stack because regen is a normal flow, not
// a destructive op.
type RegenerateRequest struct {
	UserMessageID int64 `json:"user_message_id" binding:"required"`
}

// Regenerate re-runs the agent loop for the assistant
// reply of a given user message. The user message itself
// is preserved; every existing assistant sibling in the
// regen group is soft-archived (P1-4) instead of hard-
// deleted (P1-3), and the new reply becomes the active
// row. See RegenerateRequest for the rationale.

func (h *Handler) loadUserMessageSummary(convID string, msgID int64) (*UserMessageSummary, error) {
	_ = h.store.Flush()
	// Single-row read. We don't need a paging method —
	// loadUserMessageSummary is called only on demand
	// (the first time the user hovers a paginated bubble
	// or paginates), not on every list.
	var (
		role    string
		content string
		created int64
	)
	err := h.store.DB().QueryRow(
		`SELECT role, content, created_at FROM messages
		 WHERE conversation_id = ? AND id = ?`,
		convID, msgID,
	).Scan(&role, &content, &created)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("message id %d not found in session %s", msgID, convID)
	}
	if err != nil {
		return nil, err
	}
	if role != "user" {
		return nil, fmt.Errorf("message id %d has role %q, want \"user\"", msgID, role)
	}
	preview := content
	if len(preview) > userMessagePreviewLength {
		preview = preview[:userMessagePreviewLength] + "…"
	}
	return &UserMessageSummary{
		ID:             msgID,
		Role:           role,
		Content:        content,
		ContentPreview: preview,
		CreatedAt:      created,
	}, nil
}

// ActivateRegenReplyRequest is the body of
// POST /api/v1/sessions/:id/messages/:reply_id/activate.
// user_message_id is required so the handler can
// compute the regen group_id and validate the reply
// actually belongs to that group (a malicious /
// buggy client could otherwise activate any reply
// from any conversation).
type ActivateRegenReplyRequest struct {
	UserMessageID int64 `json:"user_message_id" binding:"required"`
}

// ActivateRegenReply makes :reply_id the active reply in
// its regen group, archiving every other sibling. The
// caller is the frontend's ◀ N/M ▶ pager when the user
// picks a different historical reply to view.
//
// On success, returns the new full sibling set (in the
// same shape as ListRegenReplies) so the frontend can
// re-render the bubble + pager in one round-trip.
//
// Errors:
//   - 404: session / reply / user message not found
//   - 400: reply_id / user_message_id invalid, or the
//     reply isn't in the user message's regen group
//   - 503: store unavailable
