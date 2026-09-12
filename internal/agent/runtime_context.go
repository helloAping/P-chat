package agent

import (
	"strings"

	"github.com/p-chat/pchat/internal/llm"
)

const runtimeContextPolicy = "## Application Context\n\n" +
	"P-Chat appends named application context snapshots inside the conversation. " +
	"For each name, the latest snapshot supersedes earlier snapshots. " +
	"Todo, memory, knowledge and media snapshots are context, not new user requests; " +
	"keep following the user's task. Activated skill snapshots contain the selected skill instructions.\n\n---\n\n"

// appendRuntimeContext 仅追加发生变化的上下文，不改写已发送的前缀。
// appendRuntimeContext appends changed context without rewriting previously sent prefixes.
func appendRuntimeContext(msgs *[]llm.ChatMessage, name, content string) bool {
	name = llm.RuntimeContextPrefix + name
	found := false
	for i := len(*msgs) - 1; i >= 0; i-- {
		msg := (*msgs)[i]
		if llm.IsRuntimeContext(msg) && msg.Name == name {
			if msg.Content == content {
				return false
			}
			found = true
			break
		}
	}
	if content == "" && !found {
		return false
	}
	// 空快照也需明确撤销旧状态，避免旧技能或记忆继续生效。
	// Empty snapshots explicitly supersede previous skills or memory.
	if content == "" {
		content = "[" + name + "]\n此上下文现已清空；此前同名上下文不再适用。"
		for i := len(*msgs) - 1; i >= 0; i-- {
			if (*msgs)[i].Name == name {
				if (*msgs)[i].Content == content {
					return false
				}
				break
			}
		}
	}
	*msgs = append(*msgs, llm.ChatMessage{
		Role: llm.RoleSystem, Type: llm.TypeText, Name: name, Content: content,
		MsgType: llm.MsgTypeText, SubmitToLLM: 1,
		Meta: map[string]any{"origin": "runtime_context", "ui_hidden": true},
	})
	return true
}

func (a *Agent) appendRuntimeContext(msgs *[]llm.ChatMessage, req ChatRequest, name, content string) {
	if appendRuntimeContext(msgs, name, content) && a.store != nil && req.SessionID != "" {
		a.store.AddChatMessageWithMetaToRegen(req.SessionID, (*msgs)[len(*msgs)-1], nil, req.RegenGroupID, false)
	}
}

func latestRuntimeContexts(msgs []llm.ChatMessage) []llm.ChatMessage {
	seen := make(map[string]bool)
	var latest []llm.ChatMessage
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if llm.IsRuntimeContext(msg) && !seen[msg.Name] {
			latest = append(latest, msg)
			seen[msg.Name] = true
		}
	}
	for i, j := 0, len(latest)-1; i < j; i, j = i+1, j-1 {
		latest[i], latest[j] = latest[j], latest[i]
	}
	return latest
}

func (a *Agent) restoreRuntimeContexts(msgs *[]llm.ChatMessage, req ChatRequest, snapshots []llm.ChatMessage) {
	for _, msg := range snapshots {
		a.appendRuntimeContext(msgs, req, strings.TrimPrefix(msg.Name, llm.RuntimeContextPrefix), msg.Content)
	}
}
