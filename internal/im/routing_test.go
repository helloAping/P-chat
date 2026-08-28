package im

import (
	"testing"

	"github.com/p-chat/pchat/internal/config"
)

func TestPlanInboundRequiresMentionInGroup(t *testing.T) {
	cfg := config.DefaultIMConfig()
	cfg.Enabled = true
	cfg.Command.RequireMentionInGroup = true
	cfg.Platforms = []config.IMPlatformConfig{{
		Type:           "feishu",
		Variant:        "bot",
		Enabled:        true,
		AllowedSenders: []string{"*"},
	}}
	ev := IMEvent{
		Platform: "feishu",
		Variant:  "bot",
		Chat:     ChatRef{Platform: "feishu", Variant: "bot", ChatID: "oc_group", ChatType: "group"},
		Sender:   SenderRef{ID: "ou_user"},
		Text:     "hello",
	}

	if plan := PlanInbound(cfg, ev); plan.Process || plan.SkipReason != "mention_required" {
		t.Fatalf("plan = %+v, want skipped mention_required", plan)
	}
	ev.Mentions = []Mention{{ID: "ou_bot", Bot: true}}
	if plan := PlanInbound(cfg, ev); !plan.Process || plan.SessionID != "im:feishu:g:oc_group" {
		t.Fatalf("plan = %+v, want process with group session", plan)
	}
}

func TestPlanInboundHonorsAllowedSenders(t *testing.T) {
	cfg := config.DefaultIMConfig()
	cfg.Enabled = true
	cfg.Command.RequireMentionInGroup = false
	cfg.Platforms = []config.IMPlatformConfig{{
		Type:           "wechat",
		Variant:        "wechatbot",
		Enabled:        true,
		AllowedSenders: []string{"wechat:user-1", "user-2"},
	}}
	ev := IMEvent{
		Platform: "wechat",
		Variant:  "wechatbot",
		Chat:     ChatRef{Platform: "wechat", Variant: "wechatbot", ChatID: "room-1", ChatType: "group"},
		Sender:   SenderRef{ID: "user-3"},
		Text:     "hello",
	}
	if plan := PlanInbound(cfg, ev); plan.Process || plan.SkipReason != "sender_not_allowed" {
		t.Fatalf("plan = %+v, want sender_not_allowed", plan)
	}
	ev.Sender.ID = "user-1"
	if plan := PlanInbound(cfg, ev); !plan.Process {
		t.Fatalf("plan = %+v, want platform-qualified sender allowed", plan)
	}
}

func TestResolvePersonaMatchOrder(t *testing.T) {
	cfg := config.DefaultIMConfig()
	cfg.ToolsAllowlistDefault = []string{"read_file"}
	cfg.Personas = map[string]config.IMPersona{
		"default":          {Style: "tech", ToolsAllow: []string{"list_files"}},
		"feishu:*":         {Style: "cute", ToolsAllow: []string{"web_search"}},
		"feishu:group:*":   {Style: "guofeng", ToolsAllow: []string{"grep"}},
		"feishu:group:ou1": {Style: "tech", ToolsAllow: []string{"read_file", "grep"}},
	}
	ev := IMEvent{
		Platform: "feishu",
		Chat:     ChatRef{Platform: "feishu", ChatType: "group"},
		Sender:   SenderRef{ID: "ou1"},
	}

	persona, allow := ResolvePersona(cfg, ev)
	if persona.Style != "tech" {
		t.Fatalf("style = %q, want exact sender persona", persona.Style)
	}
	if len(allow) != 2 || allow[0] != "read_file" || allow[1] != "grep" {
		t.Fatalf("allow = %+v, want exact persona tools", allow)
	}
}
