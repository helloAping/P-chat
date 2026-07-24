package agent

// auto_continue.go — P0-3 auto-continue guard. When the agent
// loop exits because the LLM emitted zero tool calls but the
// session still has pending todos, the guard re-injects a
// user-style "未完成" prompt and re-enters the loop, up to 3
// times per turn. See docs/auto-continue.md for the user-facing
// design.
//
// Split from agent.go in T05. Behaviour unchanged.

import (
	"fmt"
	"strings"

	"github.com/p-chat/pchat/internal/tool"
)

func pickMaxStepsPrompt(lang string) string {
	if lang == "zh" {
		return MaxStepsPromptZH
	}
	return MaxStepsPromptEN
}

// MaxRoundsDefault is the safety-net per-session cap (build mode).
// At 300 rounds this is a last-resort guard against infinite loops;
// normal conversations are limited by the auto-compaction token budget,
// not by round count. When the cap fires the LLM responds with
// MaxStepsPrompt and the user can continue with a follow-up message.
const MaxRoundsDefault = 300

// MaxAutoContinue caps how many times the agent loop will
// auto-re-prompt the LLM after a no-tool-call exit when the
// todo list still has unfinished items (status pending or
// in_progress). The LLM often emits a "ready to continue"
// text block but forgets to actually invoke the next tool;
// without this guard the user has to type "继续" manually.
//
// 3 is enough to cover the common case (LLM finished a real
// tool run but the todo bookkeeping lagged one round) without
// training the LLM to rely on auto-continuation as a crutch.
// Per-session opt-out: ChatRequest.AutoContinue = false.
const MaxAutoContinue = 3

// sessionPendingTodos returns the unfinished todo items
// (status "pending" or "in_progress") for a session, plus
// their total count. The list is the same slice returned by
// the in-memory todo store, so callers can use it directly
// when formatting the auto-continue prompt.
func sessionPendingTodos(sessionID string) (count int, items []tool.TodoItem) {
	all := tool.GetSessionTodos(sessionID)
	for _, t := range all {
		if t.Status == "pending" || t.Status == "in_progress" {
			items = append(items, t)
		}
	}
	return len(items), items
}

// buildAutoContinuePrompt formats the user-style reminder
// injected when the LLM exits with no tool calls but the
// todo list has unfinished items. We send this as a user
// message rather than system because user-style messages
// are more reliably treated as actionable by current LLMs
// (system messages are often paraphrased or ignored).
func buildAutoContinuePrompt(items []tool.TodoItem) string {
	var (
		inProgress []tool.TodoItem
		pending    []tool.TodoItem
	)
	for _, t := range items {
		switch t.Status {
		case "in_progress":
			inProgress = append(inProgress, t)
		case "pending":
			pending = append(pending, t)
		}
	}
	var sb strings.Builder
	sb.WriteString("⚠ 系统检测：你刚才的回复没有调用任何工具，但 todo 列表还有未完成项。\n\n")
	if len(inProgress) > 0 {
		sb.WriteString("**进行中**:\n")
		for _, t := range inProgress {
			fmt.Fprintf(&sb, "- [%s] %s\n", t.ID, t.Content)
		}
	}
	if len(pending) > 0 {
		sb.WriteString("\n**待开始**:\n")
		for _, t := range pending {
			fmt.Fprintf(&sb, "- [%s] %s\n", t.ID, t.Content)
		}
	}
	sb.WriteString("\n请继续执行剩余任务：调用所需工具，完成后用 `todo_write` 标记 `done` 或 `cancelled`。\n")
	sb.WriteString("不要只发文本总结就停止。")
	return sb.String()
}

const (
	// Tool result caps keep the LLM context and SQLite
	// storage bounded even when a tool produces massive
	// output (e.g. systeminfo, a large log file).
	// The UI stream preview is already capped at 300
	// chars; these limits apply to what the LLM sees
	// and what gets persisted in the messages table.
	//
	// Cap choice rationale:
	//   - exec_command: keep the tail (last N chars) —
	//     stdout/stderr errors and summaries are at the
	//     end.
	//   - read_file / list_files: keep the head — the
	//     first N chars are the file/dir contents.
	//   - fallback: keep the head.
	maxToolResultExec    = 4000 // exec_command, bash
	maxToolResultRead    = 8000 // read_file
	maxToolResultDefault = 6000
)

