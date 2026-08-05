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
	"time"

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

// CumToolErrMax is the cumulative tool-failure breaker threshold in
// ChatWithTools. After this many total tool failures across a turn
// (each a different failing command), the agent injects a "stop and
// summarise" system message instead of letting the loop spin — the
// same-tool and stuck-loop guards only catch REPEATED failures, not
// the whack-a-mole case where the model tries a fresh failing command
// every round (e.g. `find` on Windows). Exported so tests can assert
// the breaker fires within a bounded number of rounds.
const CumToolErrMax = 8

// MaxStreamBytesPerRound limits the combined text, thinking, and tool-call
// argument deltas accepted from one upstream LLM round. 正常模型输出远小于 1 MiB；
// this guard prevents a broken or looping SSE provider from growing the server
// heap without bound before cancellation reaches the transport.
const MaxStreamBytesPerRound = 1 << 20

// RoundStreamStallTimeout is the per-attempt ceiling on receiving NO chunk
// at all from an upstream LLM stream. The LLM client already runs its own
// idle watchdog (default 120s, internal/llm/client.go) — but that watchdog
// resets on ANY transport byte, so a proxy that pads a dead upstream with
// SSE keep-alive lines can keep it "alive" indefinitely. Without this
// backstop the round's select would block until the turn deadline
// (MaxTurnSeconds, default 3600s) and the UI would show a permanently
// spinning tool / sub-agent card.
//
// 3 minutes sits safely above the default 120s client-side idle watchdog
// (so the client's own recovery — which knows the provider — runs first and
// yields a properly-classified API error), while still bounding a genuinely
// silent stream well before the turn deadline. Fires on NO chunk of any
// kind (content/thinking/tool/error/done), independent of keep-alive bytes.
//
// This is the default; limits.round_stream_stall_timeout (seconds)
// overrides it. 0 in config keeps this default.
const RoundStreamStallTimeout = 3 * time.Minute

// MaxToolResultFullBytes caps how much of a tool result is shipped
// to the frontend as ToolResultFull. Results larger than this are
// truncated to the display preview; the frontend fetches the full
// body on demand via the tool-result endpoint. 32 KiB keeps the
// common cases (file reads, command output, todo JSON) inline while
// bounding what the Vue reactive store can be asked to hold.
const MaxToolResultFullBytes = 32 << 10

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

// resetAutoContinueCount clears the no-tool streak after a tool round makes
// progress. Keeping this as a small pure helper makes the resume invariant
// explicit and testable: the cap applies to consecutive no-progress rounds,
// not to the whole user turn.
func resetAutoContinueCount(count int, roundToolSucceeded bool) int {
	if roundToolSucceeded {
		return 0
	}
	return count
}

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

// HasPendingTodos reports whether the session currently has unfinished
// todo items (pending or in_progress). Exported so the server can decide
// whether an interrupted turn should auto-resume: when real work is in
// flight, the turn is re-run with a "继续完成<任务>" nudge instead of
// forcing the user to type "继续" by hand.
func HasPendingTodos(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	count, _ := sessionPendingTodos(sessionID)
	return count > 0
}

// BuildTurnTimeoutResumePrompt is the MaxTurnSeconds-deadline variant of
// BuildAutoResumePrompt. Kept exported for backwards compatibility.
func BuildTurnTimeoutResumePrompt(sessionID string) string {
	return BuildAutoResumePrompt(sessionID, "超出最长执行时间")
}

// BuildAutoResumePrompt returns the user-style "继续" nudge the server
// injects as the only new message when an interrupted turn is auto-resumed
// — the semantic equivalent of the user manually typing "继续".
//
// Unlike BuildTurnTimeoutResumePrompt (specific to the MaxTurnSeconds
// deadline), this covers every interrupt path: LLM upstream errors after
// retries are exhausted, LLM stream stalls, tool hangs, deadline
// termination. When the session has active todos (pending / in_progress),
// the nudge names the first unfinished task ("继续完成“<任务名>”") and
// anchors the model to the whole todo list: re-check the in_progress item
// first, then continue the pending ones, keeping the mandatory todo_write
// contract (original IDs, only status updates, no new list, no skipping) —
// the model may only summarise once every todo reaches a terminal state.
// When there are no todos, it degrades to a plain "继续" with no contract.
//
// reason is a short Chinese label for what interrupted the turn, e.g.
// "超出最长执行时间" or "上游长时间无响应". Exported because
// internal/server needs it to build the retry ChatRequest.
func BuildAutoResumePrompt(sessionID, reason string) string {
	_, items := sessionPendingTodos(sessionID)
	if reason == "" {
		reason = "中断"
	}
	var sb strings.Builder
	// 任务名取第一个未完成项（进行中优先），让提示语直接点名要续跑的任务。
	// Name the first unfinished task so the nudge says exactly what to
	// continue instead of a generic "继续".
	taskName := ""
	for _, status := range []string{"in_progress", "pending"} {
		for _, t := range items {
			if t.Status == status {
				taskName = t.Content
				break
			}
		}
		if taskName != "" {
			break
		}
	}
	if taskName != "" {
		fmt.Fprintf(&sb, "⏱ 上一回合因%s被中断，请继续完成“%s”…\n\n", reason, taskName)
	} else {
		fmt.Fprintf(&sb, "⏱ 上一回合因%s被自动终止，请像收到“继续”一样接着完成当前任务。\n\n", reason)
	}
	if len(items) == 0 {
		sb.WriteString("当前没有待办任务。请继续执行上一回合未完成的工作，需要时用工具推进，完成后给出总结。")
		return sb.String()
	}
	// 进行中项排前，确保续跑不跳过被中断的工作（与 todo guard 排序一致）。
	// Order in_progress items first so a resume cannot skip interrupted
	// work (mirrors the todo guard ordering).
	sorted := make([]tool.TodoItem, 0, len(items))
	for _, status := range []string{"in_progress", "pending"} {
		for _, t := range items {
			if t.Status == status {
				sorted = append(sorted, t)
			}
		}
	}
	sb.WriteString("当前待办状态（先处理进行中的项）：\n")
	for _, t := range sorted {
		status := "待开始"
		if t.Status == "in_progress" {
			status = "进行中"
		}
		fmt.Fprintf(&sb, "- [%s] (%s) %s\n", t.ID, status, t.Content)
	}
	sb.WriteString("\n请按上面的 todo 列表继续执行，而不是重新开始整个任务：\n")
	sb.WriteString("1. 先复核并继续 in_progress 项（不要重复已完成的工作），再处理 pending 项；不得跳过、重建或改名原 todo。\n")
	sb.WriteString("2. 每完成一项，立即用原 ID 调用 `todo_write` 标记 `done`（无法完成标记 `cancelled`）；只更新 status，不要创建新的 todo_list。\n")
	sb.WriteString("3. 仅当全部 todo 均为 `done` 或 `cancelled` 时，才给出最终总结；在此之前不要只发文本总结就停止。")
	return sb.String()
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
	sb.WriteString("必须沿用上面列出的原 todo ID 和内容，只更新 status，不要创建新的 todo_list。\n")
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
