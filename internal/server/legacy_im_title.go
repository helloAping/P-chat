package server

import "strings"

// legacyIMConversationResponseTitle 保留旧 IM 会话标题的读取兼容性。
// legacyIMConversationResponseTitle preserves title display for old IM sessions.
func legacyIMConversationResponseTitle(sessionID, storedTitle string) string {
	parts := strings.Split(sessionID, ":")
	if len(parts) < 2 || parts[0] != "im" {
		return storedTitle
	}
	platform := strings.TrimSpace(parts[1])
	if platform == "" {
		return storedTitle
	}
	label := legacyIMPlatformLabel(platform)
	subjectFromID := ""
	if len(parts) >= 4 {
		subjectFromID = strings.TrimSpace(parts[3])
	}
	current := strings.TrimSpace(storedTitle)
	if current != "" && !strings.HasPrefix(current, "im:") && !strings.Contains(current, "@im.") &&
		current != sessionID && current != subjectFromID && current != platform && current != label {
		return storedTitle
	}
	subject := current
	if subject == "" || subject == sessionID || strings.HasPrefix(subject, "im:") ||
		subject == platform || subject == label {
		subject = subjectFromID
	}
	subject = strings.Join(strings.Fields(strings.TrimSpace(subject)), " ")
	if at := strings.Index(subject, "@im."); at > 0 {
		subject = subject[:at]
	}
	rs := []rune(subject)
	if len(rs) > 20 {
		subject = string(rs[:16]) + "…"
	}
	if subject == "" {
		return label
	}
	return label + " · " + subject
}

func legacyIMPlatformLabel(platform string) string {
	switch strings.ToLower(platform) {
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
	default:
		return platform
	}
}
