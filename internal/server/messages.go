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
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"

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

	// Serialise concurrent SendMessage calls on the same session
	// so message writes don't interleave and corrupt history.
	if _, loaded := h.sessionLocks.LoadOrStore(id, struct{}{}); loaded {
		c.JSON(http.StatusConflict, gin.H{"error": "a message is already being processed for this session"})
		return
	}
	defer h.sessionLocks.Delete(id)

	// Build messages: history after last compression + new user message.
	// Messages older than the compressed range are replaced by the
	// CompressedSummary field on the ChatRequest. All reads go through
	// the per-session variants so concurrent SendMessage calls on
	// different sessions don't race.
	meta := h.ensureMetaLoaded(id)
	lastComp := h.store.LastCompressedIDFor(id)
	var histMsgs []llm.ChatMessage
	var compSummary string
	if lastComp > 0 {
		histMsgs, _, _ = h.store.GetChatMessagesAfterIDFor(id, 0, lastComp)
		compSummary = h.store.CompressedSummaryFor(id)
	} else {
		histMsgs = h.store.GetChatMessagesFor(id, 0)
	}
	msgs := buildLLMMessages(histMsgs)
	msgs = append(msgs, llm.ChatMessage{
		Role:        llm.RoleUser,
		Type:        llm.TypeText,
		Content:     req.Message,
		MsgType:     llm.MsgTypeText,
		SubmitToLLM: 1,
	})

	chatReq := agent.ChatRequest{
		Style:       s,
		WorkMode:    workMode,
		Provider:    provider,
		Model:       model,
		Messages:    msgs,
		Attachments: req.Attachments,
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
		// P3-3: copy the trace id off the request context
		// so the agent loop can stamp every emitted chunk
		// without re-reading ctx. The traceIDMiddleware on
		// the server has already minted one (or adopted
		// the client-supplied one) and put it under
		// trace.ctxKey.
		TraceID: trace.FromContext(c.Request.Context()),
	}

	stream := h.agent.ChatStream(c.Request.Context(), chatReq)
	h.respondSSE(c, stream, id, provider, model)
}

// respondSSE writes a chat stream to the response. Used
// by both SendMessage and the P1-3 Regenerate endpoint —
// both paths produce an `agent.ChatStreamChunk` channel
// and need the same SSE envelope (data + id, flush
// per-frame, done-handling). Keep this the only place
// that knows how an internal chunk becomes wire bytes.

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

	c.Stream(func(w io.Writer) bool {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[sse] panic in stream writer: %v", r)
			}
		}()
		chunk, ok := <-stream
		if !ok {
			return false
		}
		var ids streamDoneIDs
		if chunk.Done && h.store != nil {
			ids.userMessageID = h.store.GetLastUserMessageID(sessionID)
			ids.lastMessageID = h.store.GetLastMessageID(sessionID)
		}
		ev := streamEventFromChunk(chunk, provider, model, ids)
		if ev.Type == "question" {
			log.Printf("[sse] writing question event (%d bytes json)", len(ev.QuestionJSON))
		}
		if err := writeSSEFrame(w, ev); err != nil {
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
//   1. question JSON 非空     → type:"question"   (问题模态框)
//   2. tool_confirm JSON 非空 → type:"tool_confirm" (沙箱确认)
//   3. Error 非空             → type:"error"       (LLM 错误)
//   4. Done == true           → type:"done"        (★ 终止 SSE)
//   5. ToolName 非空          → type:"tool"        (工具调用结果)
//   6. Thinking 非空          → type:"thinking"    (思考增量)
//   7. Content 非空           → type:"content"     (文本增量)
//   8. ContentRewrite 非空    → type:"content_rewrite" (后处理文本重写)
//   9. ThinkingRewrite 非空   → type:"thinking_rewrite"
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
