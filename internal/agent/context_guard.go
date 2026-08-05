package agent

// context_guard.go — T1 (P0) hard context-window guard.
//
// 在 LLM 请求发出前强制保证 payload 落在可用上下文窗口内，否则拒绝发送。
// ensureWithinWindow is the last-resort check that runs immediately
// before every ChatStreamCM call. The normal compaction path
// (tryAutoCompact) has already run by the time this executes; this
// function only guarantees the payload that will actually be sent fits
// the usable window, force-truncating when possible and refusing with a
// clear error otherwise — it never silently sends an over-window
// request (the entry point of the 2026-08 my-blog dead-loop, where a
// request carrying ~80万 estimated tokens was still sent against a 6.4万
// window).

import (
	"fmt"

	"github.com/p-chat/pchat/internal/llm"
)

// ensureWithinWindow hard-guarantees that the LLM request payload (msgs +
// tools) fits the model's usable context window before it is sent. It is
// applied after tryAutoCompact, so it must not re-run compression (that
// path is bounded above and re-entering it risks the 2026-08-05
// auto-compact dead-loop). The decision ladder:
//
//  1. Tool schemas alone already reach the usable window — truncating
//     messages can never help, refuse immediately (the T2 direction:
//     tools are a fixed per-request cost that no amount of history
//     trimming can reclaim).
//  2. Total in-window — no-op.
//  3. Total over-window — force-truncate the oldest non-system messages
//     via truncateToFit, then repair tool pairing.
//  4. Still over-window (a single oversized message, or the system
//     message itself) — refuse with an explicit error.
//
// The caller must use the returned slice for the request: when truncation
// fires the slice is replaced in place and returned.
func (a *Agent) ensureWithinWindow(msgs []llm.ChatMessage, tools []llm.ToolDef, req ChatRequest) ([]llm.ChatMessage, error) {
	ctxWindow := a.llm.ContextWindow(req.Provider, req.Model)
	usable := llm.UsableContextWithBuf(ctxWindow, compactBuffer(a.cfg))

	// Tools are a fixed per-request cost; if they alone fill the window,
	// no message trimming can ever make this request fit.
	toolsEst := llm.EstimateTokensTools(tools)
	if toolsEst >= usable {
		return msgs, fmt.Errorf(
			"工具定义（≈%d tokens）已超过可用上下文窗口（≈%d tokens），历史消息无论如何压缩都无法发出请求。请减少启用的工具（或改用更大上下文窗口的模型）后重试。",
			toolsEst, usable)
	}

	total := llm.EstimatePromptTokens(msgs, tools)
	if total <= usable {
		return msgs, nil
	}

	// Force-truncate: drop the oldest non-system messages until the
	// estimated payload fits. Mirror the budget used by tryAutoCompact's
	// post-compact truncation so the two paths agree on what "fits".
	msgBudget := usable - toolsEst
	if msgBudget < usable/4 {
		msgBudget = usable / 4
	}
	truncateToFit(&msgs, msgBudget)
	repaired, _ := repairToolMessagePairs(msgs)
	msgs = repaired

	// truncateToFit keeps the newest message even when it alone overflows
	// the budget, and never drops the system message — so a single
	// oversized message (or an oversized system prompt) can still leave
	// the payload over-window. That is the refuse branch: do not send.
	if after := llm.EstimatePromptTokens(msgs, tools); after > usable {
		return msgs, fmt.Errorf(
			"上下文超限（≈%d / 可用 %d tokens），且单条消息过大无法截断，无法继续。请开启新对话或清理历史后重试。",
			after, usable)
	}
	return msgs, nil
}
