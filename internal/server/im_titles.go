package server

import (
	"strings"

	"github.com/p-chat/pchat/internal/im"
)

func imConversationTitle(ev im.IMEvent) string {
	return formatIMConversationTitle(imEventPlatform(ev), imConversationSubject(ev))
}

func (h *Handler) ensureIMConversationTitle(sessionID string, ev im.IMEvent, desired string) {
	if h == nil || h.store == nil || strings.TrimSpace(desired) == "" {
		return
	}
	cv, err := h.store.GetConversation(sessionID)
	if err != nil {
		return
	}
	if strings.TrimSpace(cv.Title) == strings.TrimSpace(desired) {
		return
	}
	if !shouldReplaceIMConversationTitle(cv.Title, sessionID, ev) {
		return
	}
	_ = h.store.RenameConversation(sessionID, desired)
}

func imConversationResponseTitle(sessionID, storedTitle string) string {
	platform, ok := imPlatformFromSessionID(sessionID)
	if !ok {
		return storedTitle
	}
	if !shouldReplaceIMConversationTitle(storedTitle, sessionID, im.IMEvent{Platform: platform}) {
		return storedTitle
	}
	subject := strings.TrimSpace(storedTitle)
	if subject == "" || subject == sessionID || strings.HasPrefix(subject, "im:") ||
		subject == platform || subject == imPlatformLabel(platform) {
		subject = imSubjectFromSessionID(sessionID)
	}
	return formatIMConversationTitle(platform, subject)
}

func shouldReplaceIMConversationTitle(current, sessionID string, ev im.IMEvent) bool {
	current = strings.TrimSpace(current)
	if current == "" || strings.HasPrefix(current, "im:") || strings.Contains(current, "@im.") {
		return true
	}
	platform := imEventPlatform(ev)
	rawIDs := []string{
		sessionID,
		imSubjectFromSessionID(sessionID),
		strings.TrimSpace(ev.Chat.ChatID),
		strings.TrimSpace(ev.Sender.ID),
		strings.TrimSpace(ev.Sender.DisplayName),
		platform,
		imPlatformLabel(platform),
	}
	for _, raw := range rawIDs {
		if raw != "" && current == raw {
			return true
		}
	}
	return false
}

func formatIMConversationTitle(platform, subject string) string {
	label := imPlatformLabel(platform)
	subject = shortIMIdentifier(subject)
	if subject == "" {
		return label
	}
	return label + " · " + subject
}

func imConversationSubject(ev im.IMEvent) string {
	chatType := strings.ToLower(strings.TrimSpace(ev.Chat.ChatType))
	if chatType == "private" || chatType == "p2p" {
		if name := strings.TrimSpace(ev.Sender.DisplayName); name != "" {
			return name
		}
		if sender := strings.TrimSpace(ev.Sender.ID); sender != "" {
			return sender
		}
		return strings.TrimSpace(ev.Chat.ChatID)
	}
	if chat := strings.TrimSpace(ev.Chat.ChatID); chat != "" {
		return chat
	}
	if name := strings.TrimSpace(ev.Sender.DisplayName); name != "" {
		return name
	}
	return strings.TrimSpace(ev.Sender.ID)
}

func imEventPlatform(ev im.IMEvent) string {
	if platform := strings.TrimSpace(ev.Platform); platform != "" {
		return platform
	}
	if platform := strings.TrimSpace(ev.Chat.Platform); platform != "" {
		return platform
	}
	return "im"
}

func imPlatformLabel(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "wechat":
		return "微信"
	case "wecom", "work_wechat", "enterprise_wechat":
		return "企微"
	case "feishu", "lark":
		return "飞书"
	case "telegram":
		return "Telegram"
	case "qq":
		return "QQ"
	case "":
		return "IM"
	default:
		return strings.TrimSpace(platform)
	}
}

func imPlatformFromSessionID(sessionID string) (string, bool) {
	parts := strings.Split(sessionID, ":")
	if len(parts) < 2 || parts[0] != "im" || strings.TrimSpace(parts[1]) == "" {
		return "", false
	}
	return strings.TrimSpace(parts[1]), true
}

func imSubjectFromSessionID(sessionID string) string {
	parts := strings.Split(sessionID, ":")
	if len(parts) < 4 || parts[0] != "im" {
		return ""
	}
	return strings.TrimSpace(parts[3])
}

func shortIMIdentifier(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if at := strings.Index(value, "@im."); at > 0 {
		value = value[:at]
	}
	rs := []rune(value)
	if len(rs) <= 20 {
		return value
	}
	return string(rs[:16]) + "…"
}
