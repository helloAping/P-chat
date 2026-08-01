package agent

import (
	"fmt"
	"strings"

	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/tool"
)

// TodoMode controls how a new message interacts with an existing todo plan.
// The zero value is treated as TodoModeAuto for compatibility with CLI and
// older API clients.
type TodoMode string

const (
	TodoModeAuto   TodoMode = "auto"
	TodoModeResume TodoMode = "resume"
	TodoModeClear  TodoMode = "clear"
)

// MaxTodoCheckpointAttempts bounds the number of todo-only review rounds
// after ordinary work has run without a matching todo_write call. Keeping the
// limit local to a stream prevents a forgotten update from creating an
// unbounded ReAct loop or retaining per-session state in a global map.
const MaxTodoCheckpointAttempts = 3

const (
	todoGuardStart = "\n\n[PCHAT_TODO_GUARD]\n"
	todoGuardEnd   = "\n[/PCHAT_TODO_GUARD]"
)

// NormalizeTodoMode converts an API value to one of the supported modes.
// Unknown and empty values intentionally fall back to auto so old clients
// continue to work without accidentally clearing a user's plan.
func NormalizeTodoMode(mode string) TodoMode {
	switch TodoMode(strings.ToLower(strings.TrimSpace(mode))) {
	case TodoModeResume:
		return TodoModeResume
	case TodoModeClear:
		return TodoModeClear
	default:
		return TodoModeAuto
	}
}

func todoModeFromRequest(mode TodoMode) TodoMode {
	return NormalizeTodoMode(string(mode))
}

func todoGuardActive(mode TodoMode, items []tool.TodoItem) bool {
	return todoModeFromRequest(mode) == TodoModeResume || len(items) > 0
}

func unfinishedTodos(sessionID string) []tool.TodoItem {
	all := tool.GetSessionTodos(sessionID)
	if len(all) == 0 {
		return nil
	}
	items := make([]tool.TodoItem, 0, len(all))
	for _, item := range all {
		if item.Status == "pending" || item.Status == "in_progress" {
			items = append(items, item)
		}
	}
	return items
}

func todoItemsHaveActiveWork(items []tool.TodoItem) bool {
	for _, item := range items {
		if item.Status == "pending" || item.Status == "in_progress" {
			return true
		}
	}
	return false
}

// buildTodoGuardPrompt creates the dynamic instruction block used on every
// LLM round. It is deliberately bounded by the validated todo list and always
// puts in_progress items first so resume requests cannot skip interrupted work.
func buildTodoGuardPrompt(mode TodoMode, items []tool.TodoItem, checkpoint bool) string {
	var b strings.Builder
	if todoModeFromRequest(mode) == TodoModeResume {
		b.WriteString("恢复模式：这是对中断任务的继续处理。必须先复核原 todo 中的 in_progress 项，再处理新消息；不得跳过原任务直接开始新任务。\n")
	}
	if checkpoint {
		b.WriteString("状态检查回合：上一轮执行了工作但没有确认 todo 状态。当前只允许检查并调用 todo_write（或等待 question 的回答），不得继续执行普通工具。\n")
	}
	b.WriteString("Todo 约束：只要列表存在 pending 或 in_progress，就不能把当前回合当作完成。每个任务开始时设为 in_progress，完成后立即使用原 ID 调用 todo_write 标记 done；无法完成时标记 cancelled。不得重建、改名或跳过原 todo。\n")
	if len(items) == 0 {
		b.WriteString("当前没有活动 todo。若恢复请求中的旧任务已完成，请保持终态并正常回应。")
		return b.String()
	}
	b.WriteString("当前活动 todo（按优先级处理）：\n")
	for _, status := range []string{"in_progress", "pending"} {
		for _, item := range items {
			if item.Status == status {
				fmt.Fprintf(&b, "- [%s] (%s) %s\n", item.ID, item.Status, item.Content)
			}
		}
	}
	b.WriteString("每次完成一个任务后先更新 todo，再继续下一个任务或总结。")
	return b.String()
}

// upsertTodoGuard replaces one marked block in the first system message. It
// never appends per-round messages, which keeps context growth bounded even
// when auto-continue or checkpoint rounds are used. The block also survives
// auto-compaction because compaction preserves the first system message.
func upsertTodoGuard(msgs *[]llm.ChatMessage, mode TodoMode, items []tool.TodoItem, checkpoint bool) {
	if msgs == nil || len(*msgs) == 0 {
		return
	}
	msg := &(*msgs)[0]
	block := todoGuardStart + buildTodoGuardPrompt(mode, items, checkpoint) + todoGuardEnd
	content := msg.Content
	start := strings.Index(content, todoGuardStart)
	if start >= 0 {
		endOffset := strings.Index(content[start+len(todoGuardStart):], todoGuardEnd)
		if endOffset >= 0 {
			end := start + len(todoGuardStart) + endOffset + len(todoGuardEnd)
			msg.Content = content[:start] + block + content[end:]
			return
		}
		// A malformed/truncated marker should not accumulate another copy.
		msg.Content = content[:start] + block
		return
	}
	msg.Content += block
}

func todoOnlyToolDefs(defs []llm.ToolDef) []llm.ToolDef {
	if len(defs) == 0 {
		return nil
	}
	out := make([]llm.ToolDef, 0, 2)
	for _, def := range defs {
		if isTodoCheckpointTool(def.Name) {
			out = append(out, def)
		}
	}
	return out
}

func isTodoCheckpointTool(name string) bool {
	return name == "todo_write" || name == "question"
}
