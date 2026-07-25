package main

import (
	"testing"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/im"
)

func TestRegisterIMAdaptersRegistersFeishuBot(t *testing.T) {
	cfg := config.DefaultIMConfig()
	cfg.Platforms = []config.IMPlatformConfig{
		{Type: "feishu", Variant: "bot", Enabled: true},
	}
	gateway := im.NewGateway(cfg)

	registerIMAdapters(gateway, cfg)

	result := gateway.TestConnection("feishu", "bot")
	if !result.OK || result.Status != "registered" {
		t.Fatalf("test result = %+v, want registered ok", result)
	}
}

func TestRegisterIMAdaptersRegistersFeishuBotEvenBeforeConfigured(t *testing.T) {
	cfg := config.DefaultIMConfig()
	gateway := im.NewGateway(cfg)

	registerIMAdapters(gateway, cfg)

	result := gateway.TestConnection("feishu", "bot")
	if result.Status != "not_configured" {
		t.Fatalf("test result = %+v, want not_configured until config adds platform", result)
	}

	cfg.Platforms = []config.IMPlatformConfig{{Type: "feishu", Variant: "bot", Enabled: true}}
	gateway.UpdateConfig(cfg)
	result = gateway.TestConnection("feishu", "bot")
	if !result.OK || result.Status != "registered" {
		t.Fatalf("test result after config = %+v, want registered ok", result)
	}
}
