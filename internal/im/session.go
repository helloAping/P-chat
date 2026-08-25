package im

import (
	"fmt"
	"strings"
)

// BuildSessionKey 将规范化 IM 事件映射为稳定的 P-Chat session id。
// BuildSessionKey maps a normalized IM event to a stable P-Chat session id.
func BuildSessionKey(ev IMEvent, scope string) string {
	platform := strings.TrimSpace(ev.Platform)
	if platform == "" {
		platform = strings.TrimSpace(ev.Chat.Platform)
	}
	if platform == "" {
		return ""
	}
	scope = strings.TrimSpace(scope)
	switch scope {
	case "per_sender", "per_chat", "per_thread":
	default:
		scope = "per_thread"
	}

	chatID := strings.TrimSpace(ev.Chat.ChatID)
	senderID := strings.TrimSpace(ev.Sender.ID)
	threadID := strings.TrimSpace(ev.Chat.ThreadID)
	chatType := strings.ToLower(strings.TrimSpace(ev.Chat.ChatType))

	switch scope {
	case "per_sender":
		if senderID == "" {
			return ""
		}
		return fmt.Sprintf("im:%s:u:%s", platform, senderID)
	case "per_chat":
		if chatType == "private" || chatType == "p2p" {
			if senderID == "" {
				return ""
			}
			return fmt.Sprintf("im:%s:u:%s", platform, senderID)
		}
		if chatID == "" {
			return ""
		}
		return fmt.Sprintf("im:%s:g:%s", platform, chatID)
	default:
		if chatType == "private" || chatType == "p2p" {
			if senderID == "" {
				return ""
			}
			return fmt.Sprintf("im:%s:u:%s", platform, senderID)
		}
		if chatID == "" {
			return ""
		}
		if threadID != "" {
			return fmt.Sprintf("im:%s:g:%s:t:%s", platform, chatID, threadID)
		}
		return fmt.Sprintf("im:%s:g:%s", platform, chatID)
	}
}
