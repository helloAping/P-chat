package server

// message_helpers.go — read-only message endpoints + render helpers.
//
// Endpoints:
//   GET  /api/v1/sessions/:id/messages          (ListMessages)
//
// Plus the cross-cutting helpers that turn stored llm.ChatMessage
// rows into the wire MessageResponse shape, the snapshot/context
// inspector endpoints, and the SSE frame-line parsing helpers used
// by the message endpoints.
//
// Split from handler.go in T04. Behaviour unchanged.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
)

func (h *Handler) ListMessages(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	// Use the per-session query — do NOT mutate the global
	// currentID here. Two concurrent ListMessages on different
	// sessions would otherwise race.
	if _, err := h.store.GetConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	// Pagination:
	//   ?before_seq=N — return only messages with seq < N
	//                    (preferred; survives rollback/undo
	//                     because seq is per-conversation and
	//                     never reused)
	//   ?before_id=N  — return only messages with id < N
	//                    (legacy cursor; kept for back-compat
	//                     with older clients. BUG: id is
	//                     AUTOINCREMENT, so a rollback+undo
	//                     leaves the page with new ids and
	//                     the cursor becomes stale. Use
	//                     before_seq when possible.)
	//   ?limit=K      — cap the result at K rows
	//
	// When neither `before_*` is set the response is the
	// full (limited) history, which is the right thing for
	// the first switch into a session the frontend has
	// never seen (e.g. after app reload).
	beforeSeq := parseInt64Query(c, "before_seq", 0)
	beforeID := parseInt64Query(c, "before_id", 0)
	if beforeSeq == 0 && beforeID != 0 {
		// Legacy client sent only before_id. The cursor is
		// still the id-based one, so we keep using the id
		// SQL filter and the id-based has_more check.
		// Newer clients send before_seq and we prefer that.
	}
	queryLimit := parseIntQuery(c, "limit", 0)

	meta := h.ensureMetaLoaded(id)
	contextCap := h.contextMessageLimit(meta.Provider, meta.Model)

	// Effective limit: explicit query param wins; otherwise
	// the context-window cap (keeps the same default the old
	// un-paged endpoint used).
	limit := queryLimit
	if limit <= 0 || limit > contextCap {
		limit = contextCap
	}

	// We dispatch to the seq- or id-based query based on
	// which cursor the client sent. Newer clients send
	// `before_seq` and get the seq-aware filter
	// (stable across rollback+undo); older clients send
	// `before_id` and get the id-aware filter. When
	// neither is set, both code paths issue the
	// unfiltered "give me the latest N rows" query.
	var (
		msgs          []llm.ChatMessage
		metas         []string
		createds      []int64
		rowIDs        []int64
		seqs          []int64
		regenGroupIDs []string
		isArchiveds   []bool
	)
	if beforeSeq > 0 {
		msgs, metas, createds, rowIDs, seqs, regenGroupIDs, isArchiveds = h.store.GetChatMessagesWithMetaPageBySeq(id, beforeSeq, limit)
	} else {
		msgs, metas, createds, rowIDs, seqs, regenGroupIDs, isArchiveds = h.store.GetChatMessagesWithMetaPage(id, beforeID, limit)
	}
	out := make([]MessageResponse, 0, len(msgs))
	// rowIDs[i] is the SQLite row id for msgs[i]. We pair them
	// in buildMessageResponse so the client can use the lowest
	// row id as the `before_id` cursor for the next page.
	// rowIDs is in DESC order (matches the row order), so
	// rowIDs[len-1] is the smallest id (oldest returned row).
	for i, m := range msgs {
		resp := buildMessageResponse(m, metas, createds, i, rowIDs[i], seqs[i], regenGroupIDs[i], isArchiveds[i])
		if resp != nil {
			out = append(out, *resp)
		}
	}

	var (
		hasMore   = false
		oldestID  int64
		oldestSeq int64
	)
	if len(rowIDs) > 0 {
		// `rowIDs` is parallel to `msgs` and is oldest-first
		// (we reversed the SQL DESC order), so rowIDs[0] is
		// the smallest id in this page (the "oldest"
		// message) and rowIDs[len-1] is the largest (the
		// "newest" message). The pagination cursor is
		// "fetch the next older page", so we report the
		// smallest id here; the client's next request is
		// `?before_id=<oldestID>` and the SQL predicate
		// `id < ?` then naturally targets everything
		// strictly older than that row. The previous code
		// returned rowIDs[len-1] (the newest id) which
		// caused every page to overlap with the previous
		// one by all-but-one row and `has_more` to stay
		// `true` forever — the infinite-scroll history
		// loader would re-fetch the same messages and
		// append them to the in-memory list, producing
		// visible duplicate bubbles (see handler
		// CursorTest for the regression lock).
		oldestID = rowIDs[0]
		oldestSeq = seqs[0]
		// `has_more` = "is there at least one row older
		// than the oldest one we returned?". Cheap: a single
		// indexed COUNT/EXISTS query. We use the seq-based
		// check when possible (matches the new cursor) and
		// fall back to the id-based one for legacy clients.
		if beforeSeq > 0 {
			hasMore = h.store.HasOlderMessagesBySeq(id, oldestSeq)
		} else {
			hasMore = h.store.HasOlderMessages(id, oldestID)
		}
	}

	// Question/answer persistence was simplified: instead of
	// separate msg_type=6 rows (which required a pairing pass
	// here), the question tool now lives as a `kind:"question"`
	// part inside the assistant message's persisted parts
	// snapshot. Reload reads parts straight from
	// meta["parts"] (see decodePartsFromMeta); no pairing
	// step is needed. Question parts round-trip through the
	// question tool's tool_call/tool_result row (msg_type=4),
	// which buildMessageResponse already filters out below.

	// Merge user attachment rows back into the text row from the same
	// turn. Live streaming renders text+uploads as one local message;
	// storage keeps them as separate rows so uploads can stay typed.
	out = mergeUserAttachmentRows(out)

	// Merge consecutive assistant messages that belong to the
	// same user turn. During live streaming the frontend
	// accumulates all ReAct-round outputs into a single message
	// bubble; this merge reconstructs that same single-bubble
	// view on reload so the user doesn't see each round as an
	// independent message.
	out = mergeConsecutiveAssistant(out)

	c.JSON(http.StatusOK, gin.H{
		"messages":   out,
		"has_more":   hasMore,
		"oldest_id":  oldestID,
		"oldest_seq": oldestSeq,
	})
}

func mergeUserAttachmentRows(msgs []MessageResponse) []MessageResponse {
	if len(msgs) <= 1 {
		return msgs
	}
	merged := make([]MessageResponse, 0, len(msgs))
	for _, msg := range msgs {
		if len(merged) > 0 && shouldMergeUserAttachmentRow(merged[len(merged)-1], msg) {
			prev := &merged[len(merged)-1]
			prev.Attachments = append(prev.Attachments, msg.Attachments...)
			if strings.TrimSpace(prev.Content) == "" && strings.TrimSpace(msg.Content) != "" {
				prev.Content = msg.Content
			}
			continue
		}
		merged = append(merged, msg)
	}
	return merged
}

func shouldMergeUserAttachmentRow(prev, curr MessageResponse) bool {
	if prev.Role != "user" || curr.Role != "user" {
		return false
	}
	if len(curr.Attachments) == 0 {
		return false
	}
	return strings.TrimSpace(curr.Content) == ""
}

// SnapshotRecovery (P0-1) returns the delta of assistant
// messages with seq > after_seq, oldest first, with the
// full metadata blob (carrying the persisted parts[]).
// The frontend uses this after a dropped SSE stream to
// rebuild the trailing assistant message that was being
// streamed when the network failed.
//
// Why a separate endpoint instead of ListMessages? Two
// reasons:
//   1. ListMessages returns the full history with
//      pagination cursors; the recovery flow wants a
//      "delta since cursor" pattern and the response
//      shape is much simpler (no has_more / oldest_*).
//   2. ListMessages applies mergeConsecutiveAssistant +
//      buildMessageResponse decoding, which is the wrong
//      shape for a partial stream. The recovery flow
//      just wants the raw row data so it can decide how
//      to merge it with whatever parts the local Pinia
//      store already accumulated.
//
// Query:
//   ?after_seq=N   — return rows with seq > N (default 0)
//
// Response:
//   { "messages": [MessageResponse...],
//     "next_seq":  <highest seq returned, 0 if none> }

func (h *Handler) SnapshotRecovery(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	if _, err := h.store.GetConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	afterSeq := parseInt64Query(c, "after_seq", 0)
	if afterSeq < 0 {
		afterSeq = 0
	}

	msgs, metas, createds, seqs := h.store.GetAssistantMessagesAfterSeq(id, afterSeq)

	out := make([]MessageResponse, 0, len(msgs))
	for i, m := range msgs {
		// We pass rowID=0 (we don't have it from this
		// query) — the recovery flow doesn't need the
		// SQLite row id, only the seq cursor. The created
		// time comes from createds[i] (parallel to msgs).
		resp := buildMessageResponse(m, metas, createds, i, 0, seqs[i], "", false)
		if resp != nil {
			out = append(out, *resp)
		}
	}

	nextSeq := int64(0)
	if len(seqs) > 0 {
		nextSeq = seqs[len(seqs)-1]
	}

	c.JSON(http.StatusOK, gin.H{
		"messages": out,
		"next_seq": nextSeq,
	})
}

// ContextMessage is one entry in the P2-3 inspector's
// per-message breakdown. Distinct from MessageResponse
// because the inspector only needs a small subset of
// fields (role + tokens + preview + is_tool_result);
// shipping the full MessageResponse (with its
// possibly-large parts[] JSON) would balloon the
// payload and slow down the drawer's render.
type ContextMessage struct {
	// Role is "user" / "assistant" / "tool" /
	// "system". The frontend uses this to colour-
	// code the row.
	Role string `json:"role"`
	// Tokens is the estimate for this single message
	// (content + small per-message overhead). Rounded
	// for display, but the underlying computation is
	// the same `llm.EstimateTokens` the agent uses to
	// decide when to compress.
	Tokens int `json:"tokens"`
	// Preview is a 100-char head of the message text
	// for the UI list. Tool results truncate more
	// aggressively (40 chars) because the front of a
	// tool result is often a JSON header.
	Preview string `json:"preview"`
	// IsToolResult is true for role="tool" rows AND
	// for assistant message parts that came from a
	// tool (sub-agent text often describes a tool's
	// output). The drawer uses this to render a
	// distinct "tool" background.
	IsToolResult bool `json:"is_tool_result"`
	// IsCompressed is true for the synthetic summary
	// message that tryAutoCompact wrote to the system
	// prompt (replaces a chunk of older history). The
	// drawer shows a small "已压缩" badge on these so
	// the user understands why the message list is
	// shorter than they remember.
	IsCompressed bool `json:"is_compressed,omitempty"`
}

// ContextInspectorResponse is the wire shape of
// GET /api/v1/sessions/:id/context. Returns a quick
// snapshot of the current conversation's footprint so
// the chat UI can render the "上下文" tab/drawer.
type ContextInspectorResponse struct {
	SessionID       string `json:"session_id"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	ContextWindow   int    `json:"context_window"`
	EstimatedTokens int    `json:"estimated_tokens"`
	UsableTokens    int    `json:"usable_tokens"`
	// UtilizationPct is estimated_tokens / usable_tokens * 100 —
	// the denominator the auto-compact logic actually triggers on.
	UtilizationPct float64 `json:"utilization_pct"`
	// ContextWindowPct is estimated_tokens / context_window * 100 —
	// the denominator the user sees as "how full the model's window
	// is" (e.g. 200K / 1M = 20%). Both pcts are returned so the UI
	// can show either without a second pass.
	ContextWindowPct  float64          `json:"context_window_pct"`
	CompressedSummary string           `json:"compressed_summary,omitempty"`
	Messages          []ContextMessage `json:"messages"`
}

// ContextInspector (P2-3) returns a quick breakdown
// of the conversation footprint: per-message token
// estimate, total vs context window, compressed
// summary (if any). The response is small enough to
// load on every session switch — the messages list
// is bounded by the same contextMessageLimit that
// ListMessages uses, so a 200-message session
// produces a ~10 KB response (no full parts[] payload).
//
// Token counts are estimates (see internal/llm/
// token_count.go). The display labels every number
// as "估算" so users don't get confused when an exact
// tokenizer (had we shipped one) disagrees by ±20%.

func (h *Handler) ContextInspector(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	if _, err := h.store.GetConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	meta := h.ensureMetaLoaded(id)
	provider := meta.Provider
	if provider == "" {
		provider = h.getCfg().LLM.Default
	}
	model := meta.Model
	if model == "" {
		for _, p := range h.getCfg().LLM.Providers {
			if p.Name == provider {
				model = p.EffectiveModel()
				break
			}
		}
	}

	// Build the LLM-bound message list the same way
	// SendMessage does: history after the compressed
	// range + the synthetic CompressedSummary. The
	// token count we report is what the LLM actually
	// sees on the next turn.
	lastComp := h.store.LastCompressedIDFor(id)
	var histMsgs []llm.ChatMessage
	var compSummary string
	if lastComp > 0 {
		histMsgs, _, _ = h.store.GetChatMessagesAfterIDFor(id, 0, lastComp)
		compSummary = h.store.CompressedSummaryFor(id)
	} else {
		histMsgs = h.store.GetChatMessagesFor(id, 0)
	}
	boundMsgs := buildLLMMessages(histMsgs)
	if compSummary != "" {
		// Mirror how the system prompt pre-pends the
		// summary — the bound message list does NOT
		// include it directly (it's prepended via the
		// system prompt), but it does consume tokens
		// from the context window. Add it to the
		// estimate so the inspector reports a number
		// consistent with the actual LLM-bound payload.
		boundMsgs = append(boundMsgs, llm.ChatMessage{
			Role:    "system",
			Content: "[compressed summary]\n" + compSummary,
		})
	}

	// Per-message breakdown for the UI list. We use
	// the RAW (un-bound) messages here so each row
	// corresponds to a single database row the user
	// can mentally map. The token count uses the
	// same single-message estimator as the agent, so
	// media rows are bounded multimodal parts rather
	// than raw base64 text.
	const previewMax = 100
	out := make([]ContextMessage, 0, len(histMsgs))
	for _, m := range histMsgs {
		preview := m.Content
		if len(preview) > previewMax {
			preview = preview[:previewMax] + "…"
		}
		// Tool rows that are pure JSON get a tighter
		// preview — the first 40 chars of a tool
		// result is usually {"ok":true,"data":...}
		// which is more noise than signal at the
		// top of the list. (We still keep the full
		// token estimate below.)
		if m.Role == "tool" {
			if len(m.Content) > 40 {
				preview = m.Content[:40] + "…"
			}
		}
		out = append(out, ContextMessage{
			Role:         m.Role,
			Tokens:       llm.EstimateMessageTokens(m),
			Preview:      preview,
			IsToolResult: m.Role == "tool",
		})
	}

	// Total estimate matches what the agent uses
	// internally to decide when to compact, so the
	// drawer's utilisation bar lines up with the
	// agent's "context_warn" phase events.
	totalEstimate := llm.EstimateTokensMessages(boundMsgs)
	cw := h.agent.LLM().ContextWindow(provider, model)
	if cw <= 0 {
		cw = llm.DefaultContextWindow
	}
	usable := llm.UsableContextWithBuf(cw, llm.AutoCompactBuffer)
	utilPct := 0.0
	if usable > 0 {
		utilPct = float64(totalEstimate) / float64(usable) * 100.0
		if utilPct > 999.9 {
			utilPct = 999.9
		}
	}
	// ContextWindowPct uses the FULL window as the denominator —
	// the number the user reads as "how full the model is" (200K/1M).
	// It stays distinct from utilPct (usable-based, the auto-compact
	// trigger) so the UI can render both honest numbers.
	cwPct := 0.0
	if cw > 0 {
		cwPct = float64(totalEstimate) / float64(cw) * 100.0
		if cwPct > 999.9 {
			cwPct = 999.9
		}
	}

	c.JSON(http.StatusOK, ContextInspectorResponse{
		SessionID:         id,
		Provider:          provider,
		Model:             model,
		ContextWindow:     cw,
		EstimatedTokens:   totalEstimate,
		UsableTokens:      usable,
		UtilizationPct:    utilPct,
		ContextWindowPct:  cwPct,
		CompressedSummary: compSummary,
		Messages:          out,
	})
}

// mergeConsecutiveAssistant collapses runs of consecutive
// assistant messages into a single message. In the database,
// each ReAct round produces its own assistant row (plus
// intermediate tool_call / tool_result rows which are filtered
// out by buildMessageResponse). After filtering, consecutive
// assistant messages belong to the same user turn and should
// render as one bubble — matching what the user saw during
// live streaming.

func mergeConsecutiveAssistant(msgs []MessageResponse) []MessageResponse {
	if len(msgs) <= 1 {
		return msgs
	}
	merged := make([]MessageResponse, 0, len(msgs))
	i := 0
	for i < len(msgs) {
		msg := msgs[i]
		if msg.Role != "assistant" || i+1 >= len(msgs) || msgs[i+1].Role != "assistant" {
			merged = append(merged, msg)
			i++
			continue
		}
		run := []MessageResponse{msg}
		i++
		for i < len(msgs) && msgs[i].Role == "assistant" {
			run = append(run, msgs[i])
			i++
		}
		merged = append(merged, mergeAssistantRun(run))
	}
	return merged
}

// mergeAssistantRun merges a slice of consecutive assistant
// messages (from the same user turn) into a single message.
// Each message's parts are appended in round order, preserving
// the interleaved sequence (text → tool → sub_agent → text → …)
// that the frontend's parts-driven render path expects. Without
// this the relaoded conversation loses the round structure and
// shows all text first, then all tools below.

func mergeAssistantRun(run []MessageResponse) MessageResponse {
	if len(run) == 1 {
		return run[0]
	}
	base := run[0]
	var parts []MessagePart

	for _, m := range run {
		parts = append(parts, m.Parts...)
	}

	base.Parts = reorderAsyncSubAgentsAfterTaskTools(parts)
	// `base.Content` is intentionally NOT regenerated from
	// parts. The MessageBubble component (frontend/src/components/
	// MessageBubble.vue) renders assistant messages off
	// `message.parts` exclusively, so the joined-Content
	// projection was dead — and it would have been wrong
	// anyway: multi-round text joined with "\n" doesn't
	// match the live-streaming view (where each round's
	// text is its own part with its own styling), so
	// recomputing Content here would diverge from the
	// user-visible streaming bubble.
	return base
}

func reorderAsyncSubAgentsAfterTaskTools(parts []MessagePart) []MessagePart {
	if len(parts) <= 1 {
		return parts
	}

	rest := make([]MessagePart, 0, len(parts))
	asyncSubs := make([]MessagePart, 0)
	for _, p := range parts {
		if isAsyncSubAgentPart(p) {
			asyncSubs = append(asyncSubs, p)
			continue
		}
		rest = append(rest, p)
	}
	if len(asyncSubs) == 0 {
		return parts
	}

	insertAfter := -1
	taskOrder := make([]string, 0, len(asyncSubs))
	seen := make(map[string]struct{})
	for i, p := range rest {
		if !isTaskToolPart(p) {
			continue
		}
		insertAfter = i
		for _, key := range taskToolOrderKeys(p) {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			taskOrder = append(taskOrder, key)
		}
	}
	if insertAfter < 0 {
		return parts
	}

	orderedSubs := orderAsyncSubAgents(asyncSubs, taskOrder)
	out := make([]MessagePart, 0, len(parts))
	out = append(out, rest[:insertAfter+1]...)
	out = append(out, orderedSubs...)
	out = append(out, rest[insertAfter+1:]...)
	return out
}

func isAsyncSubAgentPart(p MessagePart) bool {
	return p.Kind == "sub_agent" && p.RunMode == "async"
}

func isTaskToolPart(p MessagePart) bool {
	return p.Kind == "tool" && p.Name == "task"
}

func taskToolOrderKeys(p MessagePart) []string {
	keys := make([]string, 0, 3)
	for _, key := range []string{
		taskIDFromJSON(p.Args),
		taskIDFromText(p.Result),
		taskDescriptionFromJSON(p.Args),
	} {
		key = normalizeTaskOrderKey(key)
		if key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func orderAsyncSubAgents(parts []MessagePart, taskOrder []string) []MessagePart {
	if len(parts) <= 1 || len(taskOrder) == 0 {
		return parts
	}
	order := make(map[string]int, len(taskOrder))
	for i, key := range taskOrder {
		if _, ok := order[key]; !ok {
			order[key] = i
		}
	}
	type rankedPart struct {
		part  MessagePart
		rank  int
		index int
	}
	ranked := make([]rankedPart, 0, len(parts))
	for i, p := range parts {
		rank := len(taskOrder) + i
		for _, key := range []string{p.TaskID, p.Task} {
			if n, ok := order[normalizeTaskOrderKey(key)]; ok {
				rank = n
				break
			}
		}
		ranked = append(ranked, rankedPart{part: p, rank: rank, index: i})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].rank != ranked[j].rank {
			return ranked[i].rank < ranked[j].rank
		}
		return ranked[i].index < ranked[j].index
	})
	out := make([]MessagePart, 0, len(parts))
	for _, item := range ranked {
		out = append(out, item.part)
	}
	return out
}

func taskIDFromJSON(raw string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	if v, ok := payload["task_id"].(string); ok {
		return v
	}
	if v, ok := payload["taskId"].(string); ok {
		return v
	}
	return ""
}

func taskDescriptionFromJSON(raw string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	if v, ok := payload["description"].(string); ok {
		return v
	}
	return ""
}

func taskIDFromText(s string) string {
	const marker = "task_id="
	i := strings.Index(s, marker)
	if i < 0 {
		return ""
	}
	rest := s[i+len(marker):]
	for j, r := range rest {
		if r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return rest[:j]
		}
	}
	return rest
}

func normalizeTaskOrderKey(s string) string {
	return strings.TrimSpace(s)
}

// buildMessageResponse shapes one ChatMessage row into the
// public MessageResponse. Returns nil for rows the frontend
// shouldn't render (tool call / tool result rows are
// reconstructed into the assistant message's Parts; attachment
// rows are surfaced as Attachments). rowID is the SQLite row
// id, propagated so the client can use it as the
// `before_id` cursor for the next page request. seq is the
// per-conversation logical position, propagated for the new
// seq-based cursor (stable across rollback+undo).
//
// regenGroupID / isArchived are the P1-4 regen-history
// fields, propagated as-is so the frontend can drive the
// ◀ N/M ▶ pager and the "上一版回答" chip. The main
// ListMessages SQL filters archived rows out, so the values
// here are always (empty, false) on that path; the
// /replies endpoint (a future addition in P1-4 commit 2)
// surfaces the full sibling set including the
// (groupID, true) rows.

func buildMessageResponse(m llm.ChatMessage, metas []string, createds []int64, i int, rowID int64, seq int64, regenGroupID string, isArchived bool) *MessageResponse {
	if i < len(metas) && isUIHiddenMessageMeta(metas[i]) {
		return nil
	}
	if isLegacyInternalResumeMessage(m) {
		return nil
	}
	created := time.Now().Unix()
	if i < len(createds) && createds[i] != 0 {
		created = createds[i]
	}
	resp := MessageResponse{
		ID:           rowID,
		Seq:          seq,
		Role:         m.Role,
		MsgType:      m.MsgType,
		Content:      m.Content,
		CreatedAt:    created,
		Name:         m.Name,
		SubmitToLLM:  m.SubmitToLLM,
		RegenGroupID: regenGroupID,
		IsArchived:   isArchived,
	}

	// Attachment messages (image / audio / video / file): compute a data
	// URL for the frontend. The frontend's MessageBubble
	// distinguishes them by the `kind` field and the wire
	// `type` (image_url / audio_url / video_url). We keep
	// the same structure for all three so the storage
	// path (raw base64 in messages.content) is identical.
	//
	// Rows persisted from an uploaded file carry "upl://<id>"
	// instead of base64; surface those as a relative
	// /api/v1/uploads/<id> URL so the browser fetches the
	// bytes on demand instead of shipping them through the
	// messages payload. Legacy base64 rows fall through to
	// the data: URL path unchanged.
	if isAttachmentType(m.Type) && m.Content != "" {
		mime := m.MimeType
		if mime == "" {
			mime = defaultMIMEForType(m.Type)
		}
		if uploadID, ok := uploadIDFromContent(m.Content); ok {
			resp.Attachments = append(resp.Attachments, AttachmentPart{
				Type: typeURLFor(m.Type),
				URL:  "/api/v1/uploads/" + uploadID,
				Name: m.Name,
				Kind: kindFor(m.Type),
				MIME: mime,
			})
		} else {
			dataURL := "data:" + mime + ";base64," + m.Content
			resp.Attachments = append(resp.Attachments, AttachmentPart{
				Type: typeURLFor(m.Type),
				URL:  dataURL,
				Name: m.Name,
				Kind: kindFor(m.Type),
				MIME: mime,
			})
		}
	}

	// Tool call metadata.
	if m.ToolID != "" {
		resp.ToolCallID = m.ToolID
	}

	// Note: command extraction (msg_type=5 / exec_command output)
	// USED to set resp.Name here, but the row is dropped by the
	// MsgTypeTool/MsgTypeCommand filter at the bottom of this
	// function, so the assignment was dead code. The frontend's
	// ExecOutputCard now reads the command from the persisted
	// tool part's `args` field (see MessageBubble.vue routing for
	// tool kind:tool parts), so the reload path is correct
	// without any post-filter mutation of resp.Name.

	// Restore assistant parts from metadata.
	if i < len(metas) && metas[i] != "" {
		if parts := decodePartsFromMeta(metas[i], m.Content); len(parts) > 0 {
			resp.Parts = parts
		}
	}
	// Attachment messages carry their payload as URLs in
	// Attachments; clear Content so the frontend doesn't
	// render the raw base64 string as text.
	if isAttachmentType(m.Type) {
		resp.Content = ""
	}
	// Tool call / result messages are embedded in the
	// main assistant message's Parts — the separate rows
	// are only for DB reconstruction and cause blank
	// bubbles if returned to the frontend.
	// Command messages (msg_type=5) are also filtered:
	// exec_command results already appear in the parts
	// as ToolCallCard entries — returning them as
	// independent bubbles would duplicate the display.
	if m.MsgType == llm.MsgTypeTool || m.MsgType == llm.MsgTypeCommand {
		return nil
	}

	return &resp
}

func isUIHiddenMessageMeta(raw string) bool {
	if raw == "" {
		return false
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return false
	}
	switch v := meta["ui_hidden"].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}

func isLegacyInternalResumeMessage(m llm.ChatMessage) bool {
	if m.Role != llm.RoleUser || m.MsgType != llm.MsgTypeText {
		return false
	}
	content := strings.TrimSpace(m.Content)
	return strings.HasPrefix(content, "⏱ 上一回合因") ||
		strings.HasPrefix(content, "⚠ 系统检测：你刚才的回复没有调用任何工具")
}

// parseInt64Query returns the int64 value of a query string
// parameter, or `def` if the parameter is missing or invalid.

func parseInt64Query(c *gin.Context, key string, def int64) int64 {
	raw := c.Query(key)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return def
	}
	return v
}

// parseIntQuery returns the int value of a query string
// parameter, or `def` if the parameter is missing or invalid.

func parseIntQuery(c *gin.Context, key string, def int) int {
	raw := c.Query(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

// isMediaType reports whether t is a binary media type the
// frontend can render inline (image / audio / video). All three
// share the same storage path: raw base64 in messages.content,
// surfaced to the UI as a data URL on the MessageAttachment.

func isMediaType(t string) bool {
	return t == llm.TypeImage || t == llm.TypeAudio || t == llm.TypeVideo
}

// isAttachmentType 判断消息是否作为独立附件行持久化。
// isAttachmentType reports whether t is a standalone attachment row.
func isAttachmentType(t string) bool {
	return isMediaType(t) || t == llm.TypeFile
}

// uplRefPrefix marks a media row whose bytes live on disk in
// ~/.p-chat/uploads instead of base64 in messages.content. The
// content column stores "upl://<uploadID>"; the frontend fetches
// the file via GET /api/v1/uploads/<id> on demand.
const uplRefPrefix = "upl://"

// uploadIDFromContent extracts the upload id from a media row's
// content. Returns (id, true) when content is an upl:// reference,
// (_, false) for legacy base64 payloads.
func uploadIDFromContent(content string) (string, bool) {
	if !strings.HasPrefix(content, uplRefPrefix) {
		return "", false
	}
	return strings.TrimPrefix(content, uplRefPrefix), true
}

// resolveHistoryUploads replaces "upl://<id>" attachment rows with the
// base64 bytes re-read from ~/.p-chat/uploads. The agent then decides whether
// to send native media or expose the session-scoped attachment reader.
// Missing files degrade to a text marker rather than dropping the
// turn. The display path (ListMessages) is untouched — it resolves
// references to /api/v1/uploads/<id> URLs for on-demand loading.
func resolveHistoryUploads(msgs []llm.ChatMessage, r *agent.DiskAttachmentResolver) []llm.ChatMessage {
	if r == nil {
		return msgs
	}
	for i, m := range msgs {
		id, ok := uploadIDFromContent(m.Content)
		if !ok {
			continue
		}
		path, _ := r.Resolve(agent.Attachment{ID: id})
		var data []byte
		if path != "" {
			if read, err := os.ReadFile(path); err == nil {
				data = read
			}
		}
		if data == nil {
			msgs[i] = llm.ChatMessage{
				Role:        m.Role,
				Type:        llm.TypeText,
				Content:     fmt.Sprintf("(attached file %s — file not found on server)", m.Name),
				MsgType:     llm.MsgTypeText,
				SubmitToLLM: 1,
			}
			continue
		}
		if len(data) == 0 {
			msgs[i] = llm.ChatMessage{
				Role:        m.Role,
				Type:        llm.TypeText,
				Content:     fmt.Sprintf("(attached file %s — upload file is empty)", m.Name),
				MsgType:     llm.MsgTypeText,
				SubmitToLLM: 1,
			}
			continue
		}
		m.Content = base64.StdEncoding.EncodeToString(data)
		m.UploadID = id
		msgs[i] = m
	}
	return msgs
}

// defaultMIMEForType picks a sane MIME for a media row whose
// metadata didn't capture one (defensive; the upload pipeline
// always sets it). Bumping a video without a recorded MIME
// down to video/mp4 lets the <video> element at least try
// to play it.

func defaultMIMEForType(t string) string {
	switch t {
	case llm.TypeImage:
		return "image/png"
	case llm.TypeAudio:
		return "audio/mpeg"
	case llm.TypeVideo:
		return "video/mp4"
	}
	return "application/octet-stream"
}

// typeURLFor maps a media Type to the wire name the frontend
// uses on MessageAttachment. We keep the OpenAI-style *_url
// suffix for symmetry with the existing image path; the
// MessageBubble component dispatches on this string.

func typeURLFor(t string) string {
	switch t {
	case llm.TypeImage:
		return "image_url"
	case llm.TypeAudio:
		return "audio_url"
	case llm.TypeVideo:
		return "video_url"
	case llm.TypeFile:
		return "text"
	}
	return ""
}

// kindFor maps a media Type to the lower-level "kind" the
// frontend uses for its MessageAttachment.kind (drives the
// chip icon / fallback render). Same vocabulary as the
// uploadKind constants on the way in.

func kindFor(t string) string {
	switch t {
	case llm.TypeImage:
		return "image"
	case llm.TypeAudio:
		return "audio"
	case llm.TypeVideo:
		return "video"
	}
	return "file"
}

// mimeFromDataURL extracts the MIME type from a data URL
// ("data:<mime>;base64,..."). Returns "" if the URL is not a
// data URL.

func mimeFromDataURL(u string) string {
	if !strings.HasPrefix(u, "data:") {
		return ""
	}
	rest := strings.TrimPrefix(u, "data:")
	if i := strings.Index(rest, ";"); i >= 0 {
		return rest[:i]
	}
	return ""
}

// inferTextPartMeta pulls a filename / kind hint out of a text
// part produced by the agent's attachment expansion. The agent
// prefixes file dumps with "--- <name> ---"; we surface that as
// the part's display name.

func inferTextPartMeta(s string) (name, kind, mime string) {
	s = strings.TrimSpace(s)
	const marker = "--- "
	if strings.HasPrefix(s, marker) {
		rest := strings.TrimPrefix(s, marker)
		if i := strings.Index(rest, " ---"); i >= 0 {
			return rest[:i], "text", "text/plain"
		}
	}
	return "", "text", "text/plain"
}

// buildLLMMessages 将会话历史整理为 LLM 消息，并为历史图片保留安全的工具引用。
// buildLLMMessages turns a session's stored history into the
// message slice fed to the LLM. Two responsibilities:
//
//  1. Filter out display-only rows (SubmitToLLM == 0): the
//     system prompt, thinking blocks, raw exec_command output,
//     etc. — anything the user sees in the chat but the LLM
//     doesn't need in its context. Historical image rows with
//     upload_id are the exception: the agent needs their
//     references so it can replace them with safe placeholders
//     or media_recognize tool hints before the LLM request.
//
//  2. Rewrite `task` tool results from role=tool to role=user.
//     The `task` tool is the sub-agent system entry point.
//     The PARENT agent issues the task call (so the parent
//     sees its own tool_call), but the sub-agent's response
//     arrives as a tool_result on the PARENT's message
//     stream. From the parent's LLM perspective, the
//     tool_call id never appears in the LLM context — only
//     the result does — and a bare `role: tool` orphan
//     would be rejected by some providers. The historical
//     workaround (commit 51039e2) flipped ALL tool_results
//     to `role: user`, but that's wrong for the main
//     tool loop: read_file / write_file / exec_command /
//     todo_write / question results all need to stay as
//     `role: tool` so the LLM can match them against the
//     tool_call that produced them.
//
//     The fix scopes the rewrite to `task` results only.
//     `m.ToolName` is populated on the result row when the
//     agent writes the tool_result message (see
//     internal/agent/agent.go toolMsg construction).

func buildLLMMessages(histMsgs []llm.ChatMessage) []llm.ChatMessage {
	msgs := make([]llm.ChatMessage, 0, len(histMsgs)+1)
	for _, m := range histMsgs {
		if m.SubmitToLLM == 0 && !isHistoricalAttachmentReference(m) {
			continue
		}
		if m.MsgType == llm.MsgTypeTool && m.Role == llm.RoleTool && m.ToolName == "task" {
			m.Role = llm.RoleUser
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func isHistoricalAttachmentReference(m llm.ChatMessage) bool {
	return isAttachmentType(m.Type) && strings.TrimSpace(m.UploadID) != ""
}

// decodePartsFromMeta pulls the assistant message's `parts`
// (thinking + text + tool + sub-agent) out of the raw
// metadata string and the message content.
//
// Storage format (two versions, auto-detected):
//
//   New (v2) — meta["parts"] is a full snapshot of the parts
//   accumulator, including text and thinking in stream order.
//   When the JSON contains any text or thinking part it is
//   returned as-is — the interleaved order the user saw during
//   streaming is preserved exactly.
//
//   Old (v1) — meta["parts"] contains only structural parts
//   (tool + sub_agent). Thinking is stored as a raw string in
//   meta["thinking"]; text comes from the `content` column.
//   The rebuild appends thinking first, then structural parts,
//   then a trailing text part from `content`.
//
// Returns nil when the row has no parts — that's the legacy
// path where the assistant message is just plain text, and the
// UI's `parts.length > 0` check falls through to the markdown
// fallback in MessageBubble. The decode is best-effort:
// malformed JSON produces nil so the UI degrades to plain text
// rather than 500-ing.

func decodePartsFromMeta(meta string, content string) []MessagePart {
	if meta == "" {
		return nil
	}
	var blob map[string]string
	if err := json.Unmarshal([]byte(meta), &blob); err != nil {
		return nil
	}

	// 1. Try the new (v2) full-snapshot format first.
	//    When meta["parts"] already contains text or thinking
	//    parts, the array is self-contained and needs no
	//    rebuilding.
	if raw, ok := blob["parts"]; ok && raw != "" {
		var full []MessagePart
		if err := json.Unmarshal([]byte(raw), &full); err == nil && hasTextOrThinking(full) {
			return full
		}
	}

	// 2. Old (v1) format: rebuild from separate fields.
	var parts []MessagePart

	// Thinking — stored as raw string (no double-encode).
	if t, ok := blob["thinking"]; ok && t != "" {
		parts = append(parts, MessagePart{Kind: "thinking", Text: t})
	}

	// Structural parts (tool + sub_agent) — JSON blob.
	if raw, ok := blob["parts"]; ok && raw != "" {
		var structural []MessagePart
		if err := json.Unmarshal([]byte(raw), &structural); err == nil {
			parts = append(parts, structural...)
		}
	}

	// Text — from content (not stored in meta).
	parts = append(parts, MessagePart{Kind: "text", Text: content})

	if len(parts) == 0 {
		return nil
	}
	return parts
}

// hasTextOrThinking reports whether a parts array contains at
// least one part whose kind is "text" or "thinking". Used to
// distinguish the new full-snapshot format (which includes
// these kinds inline) from the old structural-only format
// (which only stores tool / sub_agent parts).

func hasTextOrThinking(parts []MessagePart) bool {
	for _, p := range parts {
		if p.Kind == "text" || p.Kind == "thinking" {
			return true
		}
		if len(p.Parts) > 0 && hasTextOrThinking(p.Parts) {
			return true
		}
	}
	return false
}

// SendMessage is the main streaming endpoint. It accepts a user
// message, appends it to the session's history, and streams the
// assistant's response back as Server-Sent Events.
//
// The request may optionally carry `provider` and/or `model` to
// override the per-session defaults. Overrides are validated
// against the configured providers and models, then written back
// to the session meta so the next message in this session keeps
// using the new model.

func (h *Handler) sessionToResponse(cv memory.Conversation) SessionResponse {
	m := h.ensureMetaLoaded(cv.ID)
	provider := m.Provider
	if provider == "" {
		provider = h.getCfg().LLM.Default
	}
	model := h.sessionModel(cv.ID, provider)
	return SessionResponse{
		ID:                             cv.ID,
		Title:                          imConversationResponseTitle(cv.ID, cv.Title),
		Provider:                       provider,
		Model:                          model,
		Style:                          m.Style,
		WorkMode:                       string(h.sessionWorkMode(cv.ID)),
		ProjectPath:                    m.ProjectPath,
		PlanMode:                       m.PlanMode,
		PermissionLevel:                m.PermissionLevel,
		ReasoningEffort:                m.ReasoningEffort,
		VectorStore:                    cv.VectorStore,
		KnowledgeBase:                  m.KnowledgeBase,
		AutoContinue:                   h.sessionAutoContinue(cv.ID),
		TodoLongRunMode:                string(h.sessionTodoLongRunMode(cv.ID)),
		UseImageRecognition:            m.UseImageRecognition,
		EnabledRecognitionCapabilities: append([]config.MediaKind(nil), m.RecognitionCapabilities...),
		EnabledGenerationOperations:    append([]config.GenerationOperation(nil), m.GenerationOperations...),
		SubAgentModelEnabled:           m.SubAgentModelEnabled,
		SubAgentProvider:               m.SubAgentProvider,
		SubAgentModel:                  m.SubAgentModel,
		CreatedAt:                      cv.CreatedAt.Unix(),
		UpdatedAt:                      cv.UpdatedAt.Unix(),
	}
}

// ParseLimit returns the value of the `?limit=N` query parameter, or
// the default if absent / invalid. Exposed for the new
// GET /sessions/:id/messages?limit=20 endpoint.

func ParseLimit(c *gin.Context, def int) int {
	s := c.Query("limit")
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// SetSummarizer wires the summarizer for compress support.
