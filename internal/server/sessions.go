package server

// sessions.go — session CRUD + archive + meta + rollback.
//
//   POST   /api/v1/sessions                       (CreateSession)
//   GET    /api/v1/sessions                       (ListSessions)
//   GET    /api/v1/sessions/archived             (ListArchived)
//   GET    /api/v1/sessions/:id                   (GetSession)
//   DELETE /api/v1/sessions/:id                   (DeleteSession)
//   DELETE /api/v1/sessions/:id/permanent         (PermanentDeleteSession)
//   POST   /api/v1/sessions/:id/clear             (ClearSessionMessages)
//   POST   /api/v1/sessions/:id/fork              (ForkSession)
//   POST   /api/v1/sessions/:id/rollback          (RollbackMessages)
//   POST   /api/v1/sessions/:id/undo-rollback     (UndoRollback)
//   PATCH  /api/v1/sessions/:id                   (RenameSession)
//   PUT    /api/v1/sessions/:id/meta              (UpdateSessionMeta)
//   POST   /api/v1/sessions/:id/archive           (ArchiveSession)
//   POST   /api/v1/sessions/:id/unarchive         (UnarchiveSession)
//
// Split from handler.go in T04. Behaviour unchanged.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
)

func (h *Handler) ListSessions(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	projectPath := c.Query("project_path")
	hasProjectParam := c.Request.URL.Query().Has("project_path")
	// Cap the list at 200 to bound the response size for users
	// with thousands of sessions. The SPA can paginate via
	// future ?before_id/limit params if it needs more.
	convs := h.store.ListConversationsLimit(200)
	out := make([]SessionResponse, 0, len(convs))
	for _, conv := range convs {
		resp := h.sessionToResponse(conv)
		if projectPath != "" {
			if resp.ProjectPath != projectPath {
				continue
			}
		} else if hasProjectParam {
			if resp.ProjectPath != "" {
				continue
			}
		}
		out = append(out, resp)
	}
	c.JSON(http.StatusOK, gin.H{"sessions": out})
}

func (h *Handler) SearchMessages(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	q := c.Query("q")
	if q == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query parameter 'q' is required"})
		return
	}
	// Cap the query length and limit. A user-supplied 10KB
	// string of `%` wildcards in a single search can be a
	// denial-of-service vector — SQLite's LIKE is O(N) and
	// pathological patterns can drive the query to take
	// seconds. Combined with the missing `limit` cap (e.g.
	// `?limit=1000000` would be honored), this is a resource
	// amplification concern.
	if len(q) > 256 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query 'q' is too long (max 256 chars)"})
		return
	}
	limit := parseIntQuery(c, "limit", 20)
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	results := h.store.SearchMessages(q, limit)
	if results == nil {
		results = []memory.SearchResult{}
	}
	c.JSON(http.StatusOK, gin.H{"results": results})
}

func (h *Handler) TokenStats(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	stats := h.store.TokenStats()
	if stats == nil {
		stats = []memory.ConversationTokenStats{}
	}
	c.JSON(http.StatusOK, gin.H{"stats": stats})
}

func (h *Handler) CreateSession(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	var req CreateSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Allow empty body for "create with defaults"
		req = CreateSessionRequest{}
	}

	id, err := h.store.NewConversation()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if req.Title != "" {
		_ = h.store.RenameConversation(id, req.Title)
	}

	// Resolve the effective provider/model for this new session.
	// Priority: request body → configured default provider →
	// that provider's default model. Validate before persisting
	// so we never end up with a session pointing at a stale
	// (deleted) provider/model.
	provider := req.Provider
	if provider == "" {
		provider = h.getCfg().LLM.Default
	}
	if !h.validProvider(provider) {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unknown provider %q", provider)})
		return
	}
	model := req.Model
	if model == "" {
		for _, p := range h.getCfg().LLM.Providers {
			if p.Name == provider {
				model = p.EffectiveModel()
				break
			}
		}
	} else if !h.validModel(provider, model) {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("model %q not found under provider %q", model, provider)})
		return
	}

	h.setSessionMeta(id, req.Style, provider, model)
	if req.WorkMode != "" {
		h.setSessionMetaWorkMode(id, req.WorkMode)
	}
	// Store project path if provided.
	if req.ProjectPath != "" {
		h.setSessionMetaProjectPath(id, req.ProjectPath)
	}

	// Re-fetch and return the full session record.
	convs := h.store.ListConversations()
	for _, cv := range convs {
		if cv.ID == id {
			c.JSON(http.StatusCreated, h.sessionToResponse(cv))
			return
		}
	}
	// Shouldn't happen, but fall back to just returning the id.
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

func (h *Handler) GetSession(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	convs := h.store.ListConversations()
	for _, cv := range convs {
		if cv.ID == id {
			c.JSON(http.StatusOK, h.sessionToResponse(cv))
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
}

func (h *Handler) DeleteSession(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	if err := h.store.ArchiveConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	h.metaMu.Lock()
	delete(h.meta, id)
	h.metaMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"archived": id})
}

// PermanentDeleteSession DELETE /api/v1/sessions/:id/permanent
func (h *Handler) PermanentDeleteSession(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	if err := h.store.DeleteConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	h.metaMu.Lock()
	delete(h.meta, id)
	h.metaMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

// ClearSessionMessages DELETE /api/v1/sessions/:id/messages
func (h *Handler) ClearSessionMessages(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	if err := h.store.ClearMessages(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"cleared": id})
}

// RollbackMessages POST /api/v1/sessions/:id/rollback
// Deletes the message with the given id and all messages after it.
// Returns the deleted messages so the client can undo.
//
// The deleted messages are returned in the same wire shape as
// ListMessages (`MessageResponse`, with `parts` decoded from
// `metadata`) — NOT the raw `memory.Message` shape. Without
// this, the frontend's undoRollback splices the messages back
// into its in-memory array but each message has `parts =
// undefined` (only `metadata` as a raw JSON string, which the
// Message type doesn't read). MessageBubble.vue then falls back
// to plain-text rendering of `content`, silently dropping the
// thinking block, tool call cards, sub-agent cards, and
// question cards that the user had before rolling back. The
// undo "restores the messages" but visually the structural
// formatting is gone — see the bug report from 2026-07-09.
//
// buildMessageResponse filters out tool_call / tool_result /
// exec_command rows (their data is already embedded in the
// parent assistant message's `parts` array — exactly what
// the parts-driven render path consumes). `deleted_count` is
// therefore the count of items the frontend splices back via
// `msgs.splice(fromIndex, 0, ...deleted_messages)`, which
// matches the count of items it had originally removed via
// `msgs.splice(messageIndex)` (the in-memory array was loaded
// through the same filter).
func (h *Handler) RollbackMessages(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	var req struct {
		BeforeID int64 `json:"before_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.BeforeID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "before_id is required and must be > 0"})
		return
	}

	deleted, err := h.store.DeleteMessagesFrom(id, req.BeforeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Build parallel slices for buildMessageResponse (same
	// shape as the loop in ListMessages that does the same
	// transformation for a paged read).
	metas := make([]string, len(deleted))
	createds := make([]int64, len(deleted))
	rowIDs := make([]int64, len(deleted))
	seqs := make([]int64, len(deleted))
	regenGroupIDs := make([]string, len(deleted))
	isArchiveds := make([]bool, len(deleted))
	for i, m := range deleted {
		metas[i] = m.Metadata
		createds[i] = m.CreatedAt.Unix()
		rowIDs[i] = m.ID
		seqs[i] = m.Seq
		if m.RegenGroupID != nil {
			regenGroupIDs[i] = *m.RegenGroupID
		}
		isArchiveds[i] = m.IsArchived
	}

	deletedResp := make([]MessageResponse, 0, len(deleted))
	for i, m := range deleted {
		// buildMessageResponse takes llm.ChatMessage, not
		// memory.Message. Convert via a small inline helper
		// — the new-format metadata keys map 1:1 to the
		// ChatMessage fields, and the legacy multi_content
		// expansion that decodeChatMessages does is NOT
		// what we want here (one DB row should produce
		// one MessageResponse, not several).
		cm := memoryMessageToChatMessage(m)
		if resp := buildMessageResponse(cm, metas, createds, i, rowIDs[i], seqs[i], regenGroupIDs[i], isArchiveds[i]); resp != nil {
			deletedResp = append(deletedResp, *resp)
		}
	}

	// Collapse consecutive assistant rows into a single
	// message — the same merge ListMessages does on read. The
	// agent's persistAssistant writes one row per ReAct
	// round, so without this the rollback response would
	// hand back N separate "assistant" messages and the
	// frontend would render them as N separate bubbles on
	// undo. The user would then see a turn that was
	// originally one bubble split into N pieces, breaking
	// the visual continuity they had before rolling back.
	deletedResp = mergeConsecutiveAssistant(deletedResp)

	c.JSON(http.StatusOK, gin.H{
		"deleted_count":    len(deletedResp),
		"deleted_messages": deletedResp,
	})
}

// memoryMessageToChatMessage converts a memory.Message row into
// the protocol-agnostic llm.ChatMessage shape that
// buildMessageResponse consumes. Only the new-format metadata
// keys (type / name / mime_type / tool_* / tool_error) are
// mapped; legacy multi_content / tool_calls arrays are
// intentionally left un-expanded because the rollback path
// wants one row → one response (the agent's streaming path
// already expanded them into a single message's parts).
func memoryMessageToChatMessage(m memory.Message) llm.ChatMessage {
	cm := llm.ChatMessage{
		Role:        m.Role,
		Content:     m.Content,
		MsgType:     m.MsgType,
		SubmitToLLM: m.SubmitToLLM,
	}
	if m.Metadata != "" {
		var meta map[string]string
		if err := json.Unmarshal([]byte(m.Metadata), &meta); err == nil {
			cm.Name = meta["name"]
			cm.Type = meta["type"]
			cm.MimeType = meta["mime_type"]
			cm.ToolID = meta["tool_id"]
			cm.ToolName = meta["tool_name"]
			cm.ToolInput = meta["tool_input"]
			cm.ToolError = meta["tool_error"] == "true"
		}
	}
	return cm
}

// ForkSession POST /api/v1/sessions/:id/fork
// Creates a new session containing all messages up to and including
// before_id from the source session.
func (h *Handler) ForkSession(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	var req struct {
		BeforeID int64 `json:"before_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.BeforeID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "before_id is required and must be > 0"})
		return
	}

	conv, err := h.store.ForkConversation(id, req.BeforeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	srcMeta := h.ensureMetaLoaded(id)
	h.setSessionMeta(conv.ID, srcMeta.Style, srcMeta.Provider, srcMeta.Model)
	h.setSessionMetaWorkMode(conv.ID, srcMeta.WorkMode)
	if srcMeta.ProjectPath != "" {
		h.setSessionMetaProjectPath(conv.ID, srcMeta.ProjectPath)
	}

	c.JSON(http.StatusCreated, h.sessionToResponse(*conv))
}

// UndoRollback POST /api/v1/sessions/:id/rollback/undo
// Restores previously-deleted messages.
//
// The wire format is `[]MessageResponse` (same shape as
// ListMessages / RollbackMessages return), NOT
// `[]memory.Message`. The previous version of this handler
// expected `[]memory.Message`, but after the RollbackMessages
// wire-format fix the frontend's undo payload is
// `[]MessageResponse` — and parsing that into
// `[]memory.Message` fails with a 400 on `time.Time`
// (memory.Message.CreatedAt is time.Time, expects RFC3339
// string, but MessageResponse.CreatedAt is int64 Unix).
// The user-visible symptom was: rollback worked, but
// clicking 撤销 did nothing — the request 400'd before the
// frontend's msgs.splice ever ran.
//
// The conversion here is the inverse of buildMessageResponse:
// re-serialize `parts` + `thinking` + content into the
// `metadata` JSON string the store wants, and convert
// `created_at` (Unix int64) → `time.Time`. Without this,
// even if the parse succeeded the restored rows would have
// `metadata = ""` and the next reload would show the
// assistant messages as plain text (no thinking / tool /
// sub-agent cards).
func (h *Handler) UndoRollback(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	var req struct {
		Messages []MessageResponse `json:"messages"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Convert wire → storage. See the doc comment above
	// for why each field is what it is.
	restored := make([]memory.Message, 0, len(req.Messages))
	for _, r := range req.Messages {
		m := memory.Message{
			ID: r.ID,
			// Seq is preserved from the rollback's
			// deleted_messages so the restored row
			// keeps the same per-conversation
			// position it had before the rollback.
			// Without this, the new seq-based cursor
			// (migration 8) would skip the row on
			// the next page request — the original
			// (deleted) row was at seq=N, but the
			// restored row would get whatever
			// MAX(seq)+1 looks like at undo time,
			// and any cursor at seq=N would miss it.
			Seq:            r.Seq,
			ConversationID: id, // override from URL, defense-in-depth
			Role:           r.Role,
			Content:        r.Content,
			MsgType:        r.MsgType,
			// submit_to_llm is preserved from the
			// rollback's deleted_messages, NOT
			// hard-coded to 1. Pre-fix this was
			// always 1, which meant that an undone
			// thinking row (originally
			// submit_to_llm=0) was re-inserted as
			// a normal assistant row, sending its
			// chain-of-thought back into the LLM
			// context. The thinking text was the
			// exact content the user already saw —
			// the LLM would then echo it back as
			// the next "answer". The same applied
			// to tool_result rows for exec_command
			// (submit_to_llm=0) — the raw command
			// output would re-enter the LLM
			// context as if it were user content.
			// Carrying the original value through
			// the rollback/undo round-trip fixes
			// the leak.
			SubmitToLLM: r.SubmitToLLM,
			CreatedAt:   time.Unix(r.CreatedAt, 0),
			Metadata:    encodeMessageResponseMeta(r),
		}
		restored = append(restored, m)
	}

	if err := h.store.RestoreMessages(restored); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":             true,
		"restored_count": len(restored),
	})
}

// encodeMessageResponseMeta reconstructs the `metadata` JSON
// string the store wants, given a wire-shape MessageResponse.
// This is the inverse of decodePartsFromMeta: the parts
// array is serialized back to a JSON string and stored in
// `meta["parts"]` (the same double-encoding the agent uses
// when writing via snapshotStructural).
//
// We always write the v2 full-snapshot format regardless of
// what the original was: a MessageResponse built from
// decodePartsFromMeta always carries the decoded parts in
// `Parts`, including the text part that was previously
// derived from `content`. So `meta["parts"]` round-trips
// losslessly for both v1-original (now upgraded to v2) and
// v2-original messages.
//
// Note: MessageResponse has no `Thinking` field — thinking
// is always inside `Parts` as `{kind: "thinking", ...}`.
// The agent's snapshotStructural writes the whole parts
// array into meta["parts"] verbatim, and decodePartsFromMeta
// returns it as-is when the array contains text or thinking
// (the v2 fast path). Re-serializing that array here keeps
// the shape identical.
func encodeMessageResponseMeta(r MessageResponse) string {
	if len(r.Parts) == 0 {
		return ""
	}
	partsJSON, err := json.Marshal(r.Parts)
	if err != nil {
		return ""
	}
	meta := map[string]string{
		"parts": string(partsJSON),
	}
	b, _ := json.Marshal(meta)
	return string(b)
}

func (h *Handler) RenameSession(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	var req RenameSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.store.RenameConversation(id, req.Title); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"renamed": id, "title": req.Title})
}

// UpdateSessionMeta is the "change provider / model / style without
// sending a message" endpoint. Bound to PATCH /sessions/:id. The
// request body uses pointer fields so callers can send partial
// updates. The response is the refreshed SessionResponse so the
// web UI can sync the picker state in one round-trip.
//
// To stay backwards-compatible with the old PATCH behaviour
// (which only renamed a session), this handler also accepts a
// plain `{"title": "..."}` body and dispatches to the rename path.
func (h *Handler) UpdateSessionMeta(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")

	// Verify the session exists before we touch anything.
	cv, err := h.store.GetConversation(id)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Peek at the raw body. If the only key is "title", route to
	// RenameSession for backwards compat. Otherwise try the meta
	// update.
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Restore the body so the next ShouldBindJSON call still sees it.
	c.Request.Body = io.NopCloser(strings.NewReader(string(raw)))

	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Legacy rename path: only the title field is present.
	if _, hasTitle := probe["title"]; hasTitle {
		if len(probe) == 1 {
			var req RenameSessionRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			// Match the old "binding:required" semantics:
			// reject empty title explicitly so legacy callers
			// keep getting a 400 on a degenerate body.
			if strings.TrimSpace(req.Title) == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
				return
			}
			if err := h.store.RenameConversation(id, req.Title); err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			cv.Title = req.Title
			c.JSON(http.StatusOK, h.sessionToResponse(cv))
			return
		}
		// title + meta in one body: rename first, then update meta.
		var rn RenameSessionRequest
		if err := json.Unmarshal(raw, &rn); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if strings.TrimSpace(rn.Title) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
			return
		}
		if err := h.store.RenameConversation(id, rn.Title); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		cv.Title = rn.Title
	}

	var req UpdateSessionMetaRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate provider (if specified) before touching meta.
	provider := h.sessionProvider(id)
	if req.Provider != nil {
		if !h.validProvider(*req.Provider) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unknown provider %q", *req.Provider)})
			return
		}
		provider = *req.Provider
	}
	if req.Model != nil {
		if !h.validModel(provider, *req.Model) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("model %q not found under provider %q", *req.Model, provider)})
			return
		}
	}
	h.setSessionMeta(id, deref(req.Style), provider, deref(req.Model))
	if req.WorkMode != nil {
		h.setSessionMetaWorkMode(id, *req.WorkMode)
	}

	// Handle permission level separately — validate and write directly.
	if req.PermissionLevel != nil {
		level := *req.PermissionLevel
		if level != "ask" && level != "auto" && level != "full" {
			c.JSON(http.StatusBadRequest, gin.H{"error": `permission_level must be "ask", "auto", or "full"`})
			return
		}
		h.metaMu.Lock()
		m := h.meta[id]
		m.PermissionLevel = level
		h.meta[id] = m
		h.metaMu.Unlock()
		h.persistSessionMeta(id, m)
	}

	// Handle vector_store as a first-class conversation column.
	if req.VectorStore != nil {
		if h.store != nil {
			_ = h.store.SetConversationVectorStore(id, *req.VectorStore)
		}
	}

	// Handle knowledge_base
	if req.KnowledgeBase != nil {
		h.metaMu.Lock()
		m := h.meta[id]
		m.KnowledgeBase = *req.KnowledgeBase
		h.meta[id] = m
		h.metaMu.Unlock()
		h.persistSessionMeta(id, m)
	}

	// Handle auto_continue (P0-3). Pointer semantics: nil
	// means "leave the per-session setting alone" (default
	// true), non-nil means "set it to this value explicitly".
	// The next chatReq construction reads through
	// sessionAutoContinue() which applies the default.
	if req.AutoContinue != nil {
		h.metaMu.Lock()
		m := h.meta[id]
		m.AutoContinue = req.AutoContinue
		h.meta[id] = m
		h.metaMu.Unlock()
		h.persistSessionMeta(id, m)
	}

	// Re-read so the response reflects the on-disk truth.
	cv, err = h.store.GetConversation(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, h.sessionToResponse(cv))
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// --- Messages ---

func (h *Handler) ArchiveSession(c *gin.Context) {
	id := c.Param("id")
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	if err := h.store.ArchiveConversation(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// UnarchiveSession POST /api/v1/sessions/:id/unarchive
func (h *Handler) UnarchiveSession(c *gin.Context) {
	id := c.Param("id")
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	if err := h.store.UnarchiveConversation(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// If the session's project directory no longer exists (project
	// was removed), clear the project association so it shows under
	// the global view.
	meta := h.ensureMetaLoaded(id)
	if meta.ProjectPath != "" {
		if _, err := os.Stat(meta.ProjectPath); os.IsNotExist(err) {
			h.setSessionMetaProjectPath(id, "")
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ListArchived GET /api/v1/sessions/archived
func (h *Handler) ListArchived(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	convs := h.store.ListArchivedConversationsLimit(200)
	out := make([]SessionResponse, 0, len(convs))
	for _, conv := range convs {
		out = append(out, h.sessionToResponse(conv))
	}
	c.JSON(http.StatusOK, gin.H{"sessions": out})
}

// ListMCPServers GET /api/v1/mcp/servers
