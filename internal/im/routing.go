package im

import (
	"fmt"
	"strings"

	"github.com/p-chat/pchat/internal/config"
)

// InboundPlan 是 Gateway 对一条入站消息的路由决策。
// InboundPlan is the Gateway routing decision for one inbound message.
type InboundPlan struct {
	Process      bool                    `json:"process"`
	SkipReason   string                  `json:"skip_reason,omitempty"`
	SessionID    string                  `json:"session_id,omitempty"`
	Platform     config.IMPlatformConfig `json:"platform"`
	Persona      config.IMPersona        `json:"persona"`
	AllowedTools []string                `json:"allowed_tools,omitempty"`
}

// PlanInbound 在 IM 层完成入站消息的鉴权、mention、session 和 persona 解析。
// PlanInbound resolves auth, mention, session and persona policy inside the IM layer.
func PlanInbound(cfg config.IMConfig, ev IMEvent) InboundPlan {
	cfg.Normalize()
	if !cfg.Enabled {
		return InboundPlan{Process: false, SkipReason: "im_disabled"}
	}
	platform, ok := matchingPlatform(cfg, ev.Platform, ev.Variant)
	if !ok {
		return InboundPlan{Process: false, SkipReason: "platform_not_configured"}
	}
	if !senderAllowed(ev.Platform, ev.Sender.ID, platform.AllowedSenders) {
		return InboundPlan{Process: false, SkipReason: "sender_not_allowed", Platform: platform}
	}
	if cfg.Command.RequireMentionInGroup && isGroupChat(ev.Chat.ChatType) && !mentionsBot(ev.Mentions) {
		return InboundPlan{Process: false, SkipReason: "mention_required", Platform: platform}
	}
	sessionID := BuildSessionKey(ev, cfg.Session.Scope)
	if sessionID == "" {
		return InboundPlan{Process: false, SkipReason: "missing_session_key", Platform: platform}
	}
	persona, allowedTools := ResolvePersona(cfg, ev)
	return InboundPlan{
		Process:      true,
		SessionID:    sessionID,
		Platform:     platform,
		Persona:      persona,
		AllowedTools: allowedTools,
	}
}

// ResolvePersona 返回与当前平台、聊天类型、发送者最匹配的 persona。
// ResolvePersona returns the best matching persona for the platform/chat/sender tuple.
func ResolvePersona(cfg config.IMConfig, ev IMEvent) (config.IMPersona, []string) {
	cfg.Normalize()
	platform := strings.TrimSpace(ev.Platform)
	chatType := strings.TrimSpace(ev.Chat.ChatType)
	senderID := strings.TrimSpace(ev.Sender.ID)
	keys := []string{
		fmt.Sprintf("%s:%s:%s", platform, chatType, senderID),
		fmt.Sprintf("%s:%s:*", platform, chatType),
		fmt.Sprintf("%s:*", platform),
		"default",
	}
	for _, key := range keys {
		if persona, ok := cfg.Personas[key]; ok {
			allow := persona.ToolsAllow
			if len(allow) == 0 {
				allow = cfg.ToolsAllowlistDefault
			}
			return persona, cleanStringList(allow)
		}
	}
	return config.IMPersona{}, cleanStringList(cfg.ToolsAllowlistDefault)
}

func matchingPlatform(cfg config.IMConfig, platformType, variant string) (config.IMPlatformConfig, bool) {
	for _, platform := range cfg.Platforms {
		if !platform.Enabled || platform.Type != platformType {
			continue
		}
		if variant == "" || platform.Variant == "" || platform.Variant == variant {
			return platform, true
		}
	}
	return config.IMPlatformConfig{}, false
}

func senderAllowed(platform, senderID string, allowed []string) bool {
	senderID = strings.TrimSpace(senderID)
	if len(allowed) == 0 {
		return true
	}
	for _, raw := range allowed {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		if item == "*" || item == senderID || item == platform+":"+senderID {
			return true
		}
	}
	return false
}

func isGroupChat(chatType string) bool {
	switch strings.ToLower(strings.TrimSpace(chatType)) {
	case "group", "supergroup", "channel", "guild":
		return true
	default:
		return false
	}
}

func mentionsBot(mentions []Mention) bool {
	for _, mention := range mentions {
		if mention.Bot {
			return true
		}
	}
	return false
}

func cleanStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
