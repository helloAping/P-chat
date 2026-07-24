package server

// regen.go — regenerate-reply endpoints (P1-4 regen history).
//
//   POST /api/v1/sessions/:id/regenerate                       (Regenerate)
//   GET  /api/v1/sessions/:id/regen-replies                   (ListRegenReplies)
//   POST /api/v1/sessions/:id/regen-replies/:mid/activate      (ActivateRegenReply)
//
// Split from handler.go in T04. Behaviour unchanged.

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/trace"
)

func (h *Handler) Regenerate(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	if _, err := h.store.GetConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	var req RegenerateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.UserMessageID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_message_id must be > 0"})
		return
	}

	// Validate: the row must exist, must belong to this
	// session, and must be a user message. Without the
	// role check, a malicious / buggy client could pass
	// the id of an assistant message and the agent loop
	// would re-run with no user prompt at all.
	if err := h.store.ValidateUserMessageID(id, req.UserMessageID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// P1-4: soft-archive every existing sibling in this
	// regen group. The new assistant message (created by
	// the agent loop below) will be the new active row.
	// keepActiveID = 0 means "no row stays active" —
	// every group row is archived, and the upcoming
	// agent-loop insert becomes the lone active row.
	//
	// groupID is the string form of the user message id.
	// The agent loop reads ChatRequest.RegenGroupID and
	// stamps it on the new assistant row's
	// messages.regen_group_id column.
	groupID := strconv.FormatInt(req.UserMessageID, 10)
	if _, err := h.store.ArchiveSiblings(id, groupID, 0); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "archive siblings failed: " + err.Error()})
		return
	}

	// Resolve style / provider / model the same way
	// SendMessage does (body override > per-session >
	// config default). The RegenerateRequest body is
	// intentionally minimal — we don't let the client
	// change style / model on regen to keep the diff
	// small and avoid surprising model swaps. Future
	// iterations could accept overrides here.
	styleStr := h.sessionStyle(id)
	if styleStr == "" {
		styleStr = string(style.Tech)
	}
	if def := style.Style(h.getCfg().Style.Default); def != "" && styleStr == "" {
		styleStr = string(def)
	}
	provider := h.sessionProvider(id)
	if provider == "" {
		provider = h.getCfg().LLM.Default
	}
	model := h.sessionModel(id, provider)

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
	// Note: the user message at UserMessageID is still in
	// histMsgs (we only archived sibling assistant rows).
	// The agent loop sees it as the latest message and
	// continues from there — the user prompt drives the
	// new reply. We do NOT append a fresh user message;
	// the prompt already lives in the conversation.
	//
	// The archived siblings are excluded by the
	// is_archived = 0 filter in
	// GetChatMessagesFor -> GetChatMessagesWithMetaPage
	// so they don't leak into the LLM context. The
	// regen'd LLM gets a clean history (the user prompt
	// + every earlier turn that wasn't regenerated),
	// exactly as the user expects from a "give me a
	// different answer" action.
	msgs := buildLLMMessages(histMsgs)

	chatReq := agent.ChatRequest{
		Style:             style.Style(styleStr),
		WorkMode:          h.sessionWorkMode(id),
		Provider:          provider,
		Model:             model,
		Messages:          msgs,
		ReasoningEffort:   meta.ReasoningEffort,
		CompressedSummary: compSummary,
		SessionID:         id,
		ProjectRoot:       meta.ProjectPath,
		SkillContext:      "",
		PlanMode:          meta.PlanMode,
		PermissionLevel:   meta.PermissionLevel,
		KBBase:            meta.KnowledgeBase,
		AutoContinue:      h.sessionAutoContinue(id),
		// P1-4: the agent loop reads this and stamps the
		// new assistant row's regen_group_id, joining the
		// user message's regen group. Empty for normal
		// (non-regen) SendMessage requests.
		RegenGroupID: groupID,
		TraceID:      trace.FromContext(c.Request.Context()),
	}

	// Same session-locks pattern as SendMessage so a
	// concurrent regen + send on the same session can't
	// interleave writes.
	if _, loaded := h.sessionLocks.LoadOrStore(id, struct{}{}); loaded {
		c.JSON(http.StatusConflict, gin.H{"error": "a message is already being processed for this session"})
		return
	}
	defer h.sessionLocks.Delete(id)

	stream := h.agent.ChatStream(c.Request.Context(), chatReq)
	h.respondSSE(c, stream, id, provider, model)
}

// UserMessageSummary is the wire shape of the parent user
// message carried in the ListRegenReplies response. We
// include it so the frontend can render the bubble's
// "上一版回答" chip and the pager's user-message preview
// (e.g. `◀ 2/3 · "请帮我..." ▶`) without a second
// round-trip. content_preview is the first 80 chars of
// content — long enough to be useful, short enough to keep
// the response payload under a few KB even for sessions
// with hundreds of regen groups.
type UserMessageSummary struct {
	ID             int64  `json:"id"`
	Role           string `json:"role"`
	Content        string `json:"content"`
	ContentPreview string `json:"content_preview"`
	CreatedAt      int64  `json:"created_at"`
}

// RepliesResponse is the JSON body of
// GET /api/v1/sessions/:id/messages/:user_msg_id/replies.
// Returns the full regen group (active + archived) plus
// the user-message summary the UI needs to render the
// pager / chip without a second round-trip.
type RepliesResponse struct {
	UserMessage   UserMessageSummary `json:"user_message"`
	Replies       []MessageResponse  `json:"replies"`
	ActiveReplyID int64              `json:"active_reply_id"`
}

// userMessagePreviewLength is the truncation length for
// UserMessageSummary.ContentPreview. Long enough to be
// meaningful ("请帮我写一个 todo 工具..."), short enough
// to keep the response small. 80 chars is the same
// limit the project's docs use for the assistant's
// tool-call args preview.
const userMessagePreviewLength = 80

// ListRegenReplies returns the full sibling set for the
// regen group rooted at :user_msg_id, oldest-first by
// SQLite id. The response includes a UserMessageSummary
// so the frontend has enough context to render the
// bubble's ◀ N/M ▶ pager + the "上一版回答" chip's
// user-message preview without a second round-trip.
//
// Errors:
//   - 404: session or user message not found
//   - 400: user_msg_id is not a valid integer
//   - 200 + replies: []  when the user message exists
//     but has no regen history (legacy single-shot row,
//     regen_group_id is NULL on the only assistant).
//     The frontend treats this as "no pager" — the
//     bubble stays in the single-reply shape.

func (h *Handler) ListRegenReplies(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	convID := c.Param("id")
	if _, err := h.store.GetConversation(convID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	userMsgID, err := strconv.ParseInt(c.Param("user_msg_id"), 10, 64)
	if err != nil || userMsgID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_msg_id must be a positive integer"})
		return
	}

	// Load the user message summary. If it's missing or
	// not a user message, return 404 — the endpoint is
	// only meaningful for actual user messages.
	summary, err := h.loadUserMessageSummary(convID, userMsgID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	// Load the full sibling set via the store helper.
	// regen_group_id is the string form of the user
	// message id (the same convention ArchiveSiblings
	// and ActivateSibling use).
	groupID := strconv.FormatInt(userMsgID, 10)
	_, metas, createds, ids, seqs, isArchiveds, regenGroupIDs := h.store.ListSiblings(convID, groupID)

	// Build the parallel responses. ListSiblings is
	// oldest-first by id; the frontend can render
	// ◀ older ... N/M ... newer ▶ with active_idx
	// = replies.findIndex(!is_archived).
	out := make([]MessageResponse, 0, len(ids))
	var activeID int64
	for i := range ids {
		cm := llm.ChatMessage{
			Role:    "assistant",
			Content: "",
		}
		// decode via buildMessageResponse so the wire
		// shape matches ListMessages exactly (parts
		// decoded, attachments reconstructed, etc.).
		resp := buildMessageResponse(cm, metas, createds, i, ids[i], seqs[i], regenGroupIDs[i], isArchiveds[i])
		if resp == nil {
			continue
		}
		out = append(out, *resp)
		if !isArchiveds[i] {
			activeID = ids[i]
		}
	}

	c.JSON(http.StatusOK, RepliesResponse{
		UserMessage:   *summary,
		Replies:       out,
		ActiveReplyID: activeID,
	})
}

// loadUserMessageSummary reads one user message and shapes
// it as UserMessageSummary. Returns an error when the row
// doesn't exist, isn't in the conversation, or isn't a
// user role — the caller maps the error to 404.

func (h *Handler) ActivateRegenReply(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	convID := c.Param("id")
	if _, err := h.store.GetConversation(convID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	replyID, err := strconv.ParseInt(c.Param("reply_id"), 10, 64)
	if err != nil || replyID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "reply_id must be a positive integer"})
		return
	}
	var req ActivateRegenReplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.UserMessageID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_message_id must be > 0"})
		return
	}

	// Compute the group_id and delegate the activation
	// to the store. The store verifies the reply belongs
	// to this group / conversation and returns a
	// descriptive error on mismatch (mapped to 400 by
	// the handler below).
	groupID := strconv.FormatInt(req.UserMessageID, 10)
	if err := h.store.ActivateSibling(convID, groupID, replyID); err != nil {
		// Validation errors (group / conversation
		// mismatch) get 400. The store's error
		// messages already name the offending id, so we
		// pass them through verbatim — the client
		// can surface them in the toast.
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Re-list the sibling set so the client gets the
	// fresh active_reply_id + every reply's current
	// is_archived flag in one round-trip. The user
	// doesn't need a second fetch to update the pager.
	summary, err := h.loadUserMessageSummary(convID, req.UserMessageID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	_, metas, createds, ids, seqs, isArchiveds, regenGroupIDs := h.store.ListSiblings(convID, groupID)
	out := make([]MessageResponse, 0, len(ids))
	var activeID int64
	for i := range ids {
		cm := llm.ChatMessage{Role: "assistant"}
		resp := buildMessageResponse(cm, metas, createds, i, ids[i], seqs[i], regenGroupIDs[i], isArchiveds[i])
		if resp == nil {
			continue
		}
		out = append(out, *resp)
		if !isArchiveds[i] {
			activeID = ids[i]
		}
	}

	c.JSON(http.StatusOK, RepliesResponse{
		UserMessage:   *summary,
		Replies:       out,
		ActiveReplyID: activeID,
	})
}

// sessionToResponse converts a memory.Conversation into the API
// representation. The per-session provider/model/style are read
// from the meta cache (lazily re-hydrated from
// conversations.metadata). When the session has no override, the
// server's default provider + EffectiveModel is reported so the
// client always sees a complete picker state.
