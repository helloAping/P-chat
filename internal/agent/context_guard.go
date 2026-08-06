package agent

// context_guard.go — T1 (P0) request-time context-window convergence.
//
// 在 LLM 请求发出前强制保证 payload 落在可用上下文窗口内，且对话绝不中断。
// ensureWithinWindow is the last-resort gate that runs immediately
// before every ChatStreamCM call. The normal compaction path
// (tryAutoCompact) has already run by the time this executes; this
// function guarantees the payload that will actually be sent fits the
// usable window, converging through an escalation ladder instead of
// refusing. It never rejects a request and never asks the user to open
// a new conversation — the 2026-08 my-blog dead-loop entry point was a
// request carrying ~80万 estimated tokens being sent against a 6.4万
// window, so the gate's contract is "always send, always in-window".
//
// Convergence ladder (aligned with the Codex context-management
// mechanisms, see docs/plans/todo-auto-resume-deadloop-fixes.md §1.4):
//
//  1. Summary-replace (auto-compaction): re-run the existing
//     tryAutoCompact / Summarizer.Compress path to fold the oldest
//     messages into a summary that replaces them in the system prompt.
//  2. Head trimming: drop the oldest non-system messages via
//     truncateToFit (discard, not summarise) until the payload fits.
//  3. Forced minimal set (extreme fallback, NEVER refuses):
//     a. Truncate oversized tool results / pasted blobs (keep the head,
//        drop the tail — see truncateContentToTokens) so a single
//        giant message can no longer overflow the window.
//     b. Trim the tool list (trimToolsToFit) when the tool schemas
//        alone crowd out the window.
//     c. Minimal set: even after everything above, keep only the system
//        prompt + the newest non-system message (the latest user
//        intent), truncate their contents to fit, and send anyway.
//
// Every level that fires emits a "compact"/"guard-*" phase event so the
// frontend shows "上下文超限，已自动压缩/裁剪历史" and the user can see
// the conversation is being kept alive, not silently degraded.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/p-chat/pchat/internal/llm"
)

// contentOverheadReserve is the per-message token overhead (role/type
// wrapper + name/tool_id/mime metadata, see EstimateTokensMessages)
// reserved inside a content-truncation budget so the truncated message's
// TOTAL estimate stays within its budget. 64 tokens comfortably covers
// the metadata fields while staying tiny compared to any content we
// actually need to truncate.
const contentOverheadReserve = 64

// ensureWithinWindow hard-guarantees that the LLM request payload (msgs +
// tools) fits the model's usable context window before it is sent, and
// NEVER refuses to send. It is applied after tryAutoCompact, so level 1
// (summary-replace) is gated by allowCompact to avoid re-entering the
// compaction path on every retry attempt — compaction advances the
// compression point and is expensive; levels 2-3 are cheap and
// idempotent, so retries re-run only those.
//
// Returns the (possibly converged) msgs and tools the caller must use
// for the request. Phase events are emitted on ch for every level that
// fires.
func (a *Agent) ensureWithinWindow(
	ctx context.Context,
	msgs []llm.ChatMessage,
	tools []llm.ToolDef,
	req ChatRequest,
	ch chan<- ChatStreamChunk,
	nextSeq func() uint64,
	roundNum, maxRounds int,
	allowCompact bool,
) ([]llm.ChatMessage, []llm.ToolDef) {
	ctxWindow := a.llm.ContextWindow(req.Provider, req.Model)
	usable := llm.UsableContextWithBuf(ctxWindow, compactBuffer(a.cfg))
	emit := func(step, msg string) {
		sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{
			Phase:    "compact",
			Step:     step,
			Message:  msg,
			Round:    roundNum,
			MaxRound: maxRounds,
		})
	}

	total := llm.EstimatePromptTokens(msgs, tools)
	if total <= usable {
		return msgs, tools
	}

	// Level 1 · summary-replace (auto-compaction). Reuse the existing
	// tryAutoCompact machinery: it estimates, compresses the oldest
	// un-summarized messages via Summarizer.Compress, backfills the
	// summary into the system prompt, and emits its own compact events.
	// Its return value ("caller should continue to the next round") is
	// deliberately ignored here — we are mid-attempt and must flow
	// through the rest of the ladder, not loop back to a new round.
	if allowCompact {
		before := total
		a.tryAutoCompact(ctx, &msgs, req, tools, ch, nextSeq, roundNum, maxRounds)
		after := llm.EstimatePromptTokens(msgs, tools)
		if after <= usable {
			return msgs, tools
		}
		if after < before {
			emit("guard-summarize", fmt.Sprintf("已总结替换早期历史（≈%d → %d tokens），仍超出窗口，继续收敛…", before, after))
		}
		total = after
	}

	// Level 2 · head trimming. Drop the oldest non-system messages
	// (discard, not summarise) until the message payload fits the budget
	// left after the tool schemas. Mirror tryAutoCompact's budget so the
	// two paths agree on what "fits".
	toolsEst := llm.EstimateTokensTools(tools)
	msgBudget := usable - toolsEst
	if msgBudget < usable/4 {
		msgBudget = usable / 4
	}
	if llm.EstimateTokensMessages(msgs) > msgBudget {
		before := len(msgs)
		truncateToFit(&msgs, msgBudget)
		msgs, _ = repairToolMessagePairs(msgs)
		if len(msgs) < before {
			emit("guard-head-trim", fmt.Sprintf("上下文超限，已裁剪历史头部（保留最近 %d 条消息，≈%d tokens）", len(msgs)-1, llm.EstimateTokensMessages(msgs)))
		}
	}

	// Level 3 · forced minimal set (extreme fallback, never refuses).
	// Each branch either shrinks the payload or hands off to the next,
	// so the loop always terminates; the final minimal set is guaranteed
	// in-window by construction.
	for llm.EstimatePromptTokens(msgs, tools) > usable {
		toolsEst := llm.EstimateTokensTools(tools)
		msgBudget := usable - toolsEst
		if msgBudget < usable/4 {
			msgBudget = usable / 4
		}

		// 3a · tool schemas alone crowd out the window (the T2
		// direction): message trimming can never help, so trim the
		// tool list first. msgBudget is floored exactly when
		// toolsEst > usable*3/4 — that is the trigger.
		if toolsEst > usable-msgBudget {
			before := len(tools)
			trimmed := trimToolsToFit(tools, usable/2)
			if len(trimmed) < before {
				tools = trimmed
				emit("guard-trim-tools", fmt.Sprintf("工具定义超限，已裁剪工具列表（%d → %d 个）", before, len(trimmed)))
				continue
			}
			if before > 0 {
				tools = nil
				emit("guard-trim-tools", "工具定义仍超限，本回合临时禁用工具")
				continue
			}
		}

		// 3b · truncate oversized message contents (keep the head).
		// Handles the single-giant-message case head-trimming cannot:
		// truncateToFit keeps the newest message even when it alone
		// overflows the window.
		before := llm.EstimateTokensMessages(msgs)
		msgBudget = usable - llm.EstimateTokensTools(tools)
		if msgBudget <= 0 {
			msgBudget = usable / 4
		}
		msgs = truncateOversizedMessages(msgs, msgBudget)
		msgs, _ = repairToolMessagePairs(msgs)
		if llm.EstimateTokensMessages(msgs) < before {
			emit("guard-truncate-content", fmt.Sprintf("单条消息过大，已截断超长内容（≈%d → %d tokens）", before, llm.EstimateTokensMessages(msgs)))
			continue
		}

		// 3c · minimal set: system + newest non-system message, both
		// truncated to fit. This is the absolute floor — the request is
		// still sent, the conversation still continues.
		msgs, tools = minimalContextSet(msgs, tools, usable)
		emit("guard-minimal", "上下文严重超限，仅保留最小上下文（system + 最新消息）发出请求")
		break
	}
	return msgs, tools
}

// truncateContentToTokens shrinks s, keeping the head, so that its
// estimate (plus contentOverheadReserve) fits maxTokens. Binary-search
// the longest rune-safe byte prefix; the result always ends on a UTF-8
// rune boundary so the wire payload stays valid. Returns a short marker
// when even one rune would overflow.
func truncateContentToTokens(s string, maxTokens int) string {
	if maxTokens <= 0 {
		return "[内容已省略：上下文窗口超限]"
	}
	budget := maxTokens - contentOverheadReserve
	if budget < 16 {
		budget = 16
	}
	if llm.EstimateTokens(s) <= budget || s == "" {
		return s
	}
	lo, hi := 0, len(s)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if llm.EstimateTokensBytes([]byte(s[:mid])) <= budget {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	// Step back to a rune boundary so the emitted prefix is valid UTF-8
	// (s[:lo] could otherwise split a multi-byte CJK rune).
	for lo > 0 && !utf8.RuneStart(s[lo]) {
		lo--
	}
	if lo == 0 {
		return "[内容已省略：上下文窗口超限]"
	}
	return s[:lo] + "\n…[内容已截断：超出上下文预算]"
}

// truncateOversizedMessages shrinks oversized non-system message contents
// (keeping the head) until the whole message payload estimate fits budget.
// It is the level-3b fallback for the single-giant-message case that
// truncateToFit cannot fix (truncateToFit always keeps the newest message,
// even when it alone overflows). Media rows (MimeType set) are replaced
// with a short text marker instead of truncated — cutting base64 mid-stream
// would corrupt the image/file wire format and get the request rejected.
func truncateOversizedMessages(msgs []llm.ChatMessage, budget int) []llm.ChatMessage {
	out := append([]llm.ChatMessage(nil), msgs...)
	if len(out) <= 1 || llm.EstimateTokensMessages(out) <= budget {
		return out
	}
	n := 0
	for _, m := range out {
		if m.Role != llm.RoleSystem {
			n++
		}
	}
	if n == 0 {
		return out
	}
	perMsgCap := budget / n
	if perMsgCap < 64 {
		perMsgCap = 64
	}
	for i := 1; i < len(out); i++ {
		m := &out[i]
		if m.Role == llm.RoleSystem {
			continue
		}
		if m.MimeType != "" {
			// Binary attachment: a truncated base64 body is invalid on the
			// wire — degrade to a text marker (keep role/type structure).
			m.Content = "[附件已省略：上下文窗口超限]"
			m.MimeType = ""
			continue
		}
		if est := llm.EstimateTokensMessages([]llm.ChatMessage{*m}); est > perMsgCap {
			m.Content = truncateContentToTokens(m.Content, perMsgCap)
		}
	}
	// Tighten until the payload fits (bounded: perMsgCap ≥ 16, halving
	// each pass, so at most O(log budget) passes).
	for llm.EstimateTokensMessages(out) > budget && perMsgCap > 16 {
		perMsgCap /= 2
		for i := 1; i < len(out); i++ {
			m := &out[i]
			if m.Role == llm.RoleSystem || m.MimeType != "" {
				continue
			}
			if est := llm.EstimateTokensMessages([]llm.ChatMessage{*m}); est > perMsgCap {
				m.Content = truncateContentToTokens(m.Content, perMsgCap)
			}
		}
	}
	return out
}

// trimToolsToFit reduces the tool list so its schema estimate fits the
// budget. Essential bookkeeping tools (todo_write, question) are kept as
// long as possible; the rest are kept smallest-first so the largest
// number of tools survive the cut. This is the "裁剪工具列表" level of
// the convergence ladder, for the case where EstimateTokensTools alone
// approaches the window (T2 suspect #1).
func trimToolsToFit(tools []llm.ToolDef, budget int) []llm.ToolDef {
	if budget <= 0 {
		return nil
	}
	type sizedTool struct {
		def llm.ToolDef
		est int
	}
	var essentials, rest []sizedTool
	for _, t := range tools {
		est := llm.EstimateTokens(t.Name) + llm.EstimateTokens(t.Description) + llm.EstimateTokensBytes(t.Parameters) + 24
		s := sizedTool{def: t, est: est}
		if t.Name == "todo_write" || t.Name == "question" {
			essentials = append(essentials, s)
		} else {
			rest = append(rest, s)
		}
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].est < rest[j].est })
	out := make([]llm.ToolDef, 0, len(tools))
	acc := 0
	consider := append(essentials, rest...)
	for _, s := range consider {
		if acc+s.est > budget {
			continue
		}
		out = append(out, s.def)
		acc += s.est
	}
	return out
}

// minimalContextSet is the absolute floor of the convergence ladder: the
// system prompt plus the newest non-system message (the latest user
// intent), with both contents truncated so the pair — plus the (trimmed)
// tools — fits the usable window. The request is always sent; the
// conversation always continues. This is the "兜底最小集" the plan
// mandates for even the most pathological over-window payload.
func minimalContextSet(msgs []llm.ChatMessage, tools []llm.ToolDef, usable int) ([]llm.ChatMessage, []llm.ToolDef) {
	if len(msgs) == 0 {
		return msgs, tools
	}
	sys := msgs[0]
	var latest llm.ChatMessage
	found := false
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != llm.RoleSystem {
			latest = msgs[i]
			found = true
			break
		}
	}
	out := []llm.ChatMessage{sys}
	if found {
		out = append(out, latest)
	}

	// Trim tools first so they cannot crowd out the message pair: keep
	// tools only while they leave at least half the window for messages.
	for llm.EstimateTokensTools(tools) > usable/2 {
		tools = trimToolsToFit(tools, usable/4)
		if len(tools) == 0 {
			break
		}
	}
	msgBudget := usable - llm.EstimateTokensTools(tools)
	if msgBudget < 0 {
		msgBudget = 0
	}
	if est := llm.EstimateTokens(out[0].Content); est > msgBudget {
		out[0].Content = truncateContentToTokens(out[0].Content, msgBudget)
	}
	if len(out) == 2 {
		rem := msgBudget - llm.EstimateTokens(out[0].Content)
		if rem < 0 {
			rem = 0
		}
		if est := llm.EstimateTokens(out[1].Content); est > rem {
			out[1].Content = truncateContentToTokens(out[1].Content, rem)
		}
	}
	return out, tools
}

// guardSentinel is a test helper marker reused by context_guard_test.go to
// assert that the convergence events are actually emitted (kept in this
// file so the test never depends on a spelling that drifts).
var guardStepPrefixes = []string{"guard-summarize", "guard-head-trim", "guard-truncate-content", "guard-trim-tools", "guard-minimal"}

// isGuardStep reports whether step is one of the guard phase-event steps.
func isGuardStep(step string) bool {
	for _, p := range guardStepPrefixes {
		if step == p {
			return true
		}
	}
	return strings.HasPrefix(step, "guard-")
}
