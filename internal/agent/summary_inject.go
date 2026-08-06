package agent

// summary_inject.go — T2: bounded, non-duplicating compressed-summary
// injection into the system prompt.
//
// 根因（2026-08-05 my-blog 死循环，T-32964d67）：
// tryAutoCompact 每次压缩成功回填时，都把 CompressedSummaryFor()（全部
// 265 条 summary 的拼接 ≈54KB）整体追加到 system 消息：
//
//	newMsgs[0].Content += "\n\n[前文摘要]\n" + compSum
//
// 而 system 消息在 turn 开始时已注入过一次（agent.go:1220）。由于每次
// 压缩后估算值仍超窗（summary 重复累积 + 新工具结果），下一轮又压缩，
// 又追加整份 compSum → system 单调膨胀（观测：每轮 +86KB，est 从 5.2万
// 涨到 80万），且 backfill 后 hist 为空 → 请求只有 system 一条
// （messages=1），LLM 完全"失忆"→ 反复重试同一失败命令 → 死循环。
//
// 修复：摘要注入改为"剥离旧块 → 追加新块"（appendSummaryInjection），
// 任何时刻 system 里至多一份摘要；并用 MaxSummaryPromptTokens 限制注入
// 总量，防止 summaries 表无限增长时 system 无限膨胀。配套修复（memory
// 包）：最新几条消息永不参与总结，保证 LLM 始终能看到最近的真实上下文
// （见 Summarizer.Compress 的 summaryProtectedNewest）。

import "strings"

const (
	// summaryBlockStart / summaryBlockEnd bracket the injected summary
	// section. The end marker makes stripping unambiguous even when the
	// summary text itself contains blank lines or the todo-guard block
	// follows (upsertTodoGuard appends/replaces its own marked block and
	// never touches the summary section).
	summaryBlockStart = "\n\n[前文摘要]\n"
	summaryBlockEnd   = "\n[前文摘要结束]\n"
)

// MaxSummaryPromptTokens caps how many tokens of accumulated summaries
// are injected into the system prompt. The summaries table grows without
// bound on a very long session; the prompt must not. 8k tokens ≈ 30KB is
// comfortably below the auto-compact headroom (20k) while still carrying
// the gist of everything summarized so far.
const MaxSummaryPromptTokens = 8000

// appendSummaryInjection appends a bounded compressed summary to the
// system prompt, first removing any previously injected summary block so
// the summary can never accumulate multiple copies — the T2 root cause.
// Returns system unchanged when compSum is empty.
func appendSummaryInjection(system, compSum string) string {
	system = stripSummaryInjection(system)
	if compSum == "" {
		return system
	}
	capped := truncateContentToTokens(compSum, MaxSummaryPromptTokens)
	return system + summaryBlockStart + capped + summaryBlockEnd
}

// stripSummaryInjection removes any previously injected summary block
// (both the bracketed format and a legacy unbracketed trailing block)
// from the system prompt. Used so a fresh summary REPLACES the old one
// instead of accumulating alongside it.
func stripSummaryInjection(system string) string {
	start := strings.Index(system, summaryBlockStart)
	if start < 0 {
		return system
	}
	rest := system[start+len(summaryBlockStart):]
	if end := strings.Index(rest, summaryBlockEnd); end >= 0 {
		return system[:start] + rest[end+len(summaryBlockEnd):]
	}
	// Legacy (pre-T2) format: no end marker — strip to the end of the
	// string. Sections appended after the summary (skill context, style
	// memory, todo guard) would be lost, but this path only runs against
	// messages assembled by the old binary, which is not a live concern
	// after the upgrade.
	return system[:start]
}
