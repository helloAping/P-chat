package config

import (
	"path/filepath"
	"testing"
)

func TestModelProtocolOverridesProviderProtocolForDefaultEndpoint(t *testing.T) {
	provider := ProviderConfig{
		Protocol: ProtocolOpenAIChat,
		BaseURL:  "https://example.com/v1",
		Models: []ModelConfig{
			{Name: "inherited"},
			{Name: "claude", Protocol: ProtocolAnthropicMessages},
		},
	}

	if got := provider.Models[0].EffectiveProtocol(provider.GetProtocol()); got != ProtocolOpenAIChat {
		t.Fatalf("inherited protocol = %q, want %q", got, ProtocolOpenAIChat)
	}
	if got := provider.Models[1].EffectiveProtocol(provider.GetProtocol()); got != ProtocolAnthropicMessages {
		t.Fatalf("overridden protocol = %q, want %q", got, ProtocolAnthropicMessages)
	}
	if got := provider.ModelAPIURL("inherited"); got != "https://example.com/v1/chat/completions" {
		t.Fatalf("inherited model URL = %q", got)
	}
	if got := provider.ModelAPIURL("claude"); got != "https://example.com/v1/messages" {
		t.Fatalf("overridden model URL = %q", got)
	}
}

func TestAddModelNormalizesAndValidatesProtocolOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	configPath := filepath.Join(dir, ".p-chat", "config.json")
	if err := osWriteFile(configPath, `{"llm":{"providers":[{"name":"p","protocol":"openai_chat","base_url":"https://example.com/v1","api_key":"k"}]}}`); err != nil {
		t.Fatal(err)
	}

	updated, err := AddModelAt(configPath, "p", ModelConfig{Name: "claude", Protocol: ProtocolAnthropic})
	if err != nil {
		t.Fatal(err)
	}
	model := updated.FindModel("claude")
	if model == nil {
		t.Fatal("added model not found")
	}
	if model.Protocol != ProtocolAnthropicMessages {
		t.Fatalf("stored protocol = %q, want normalized %q", model.Protocol, ProtocolAnthropicMessages)
	}
	if model.APIEndpoint != "/messages" {
		t.Fatalf("api_endpoint = %q, want /messages", model.APIEndpoint)
	}

	if _, err := AddModelAt(configPath, "p", ModelConfig{Name: "bad", Protocol: "gopher"}); err == nil {
		t.Fatal("invalid model protocol should be rejected")
	}
}

func TestUpdateModelProtocolOverrideCanBeCleared(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	configPath := filepath.Join(dir, ".p-chat", "config.json")
	if err := osWriteFile(configPath, `{"llm":{"providers":[{"name":"p","protocol":"openai_chat","base_url":"https://example.com/v1","api_key":"k","models":[{"name":"mixed","api_endpoint":"/chat/completions"}]}]}}`); err != nil {
		t.Fatal(err)
	}

	override := ProtocolAnthropicMessages
	updated, err := UpdateModelProtocolAt(configPath, "p", "mixed", ModelConfig{}, &override, false)
	if err != nil {
		t.Fatal(err)
	}
	model := updated.FindModel("mixed")
	if model.Protocol != ProtocolAnthropicMessages || model.APIEndpoint != "/messages" {
		t.Fatalf("overridden model = %#v", model)
	}

	inherit := ""
	updated, err = UpdateModelProtocolAt(configPath, "p", "mixed", ModelConfig{}, &inherit, false)
	if err != nil {
		t.Fatal(err)
	}
	model = updated.FindModel("mixed")
	if model.Protocol != "" || model.APIEndpoint != "/chat/completions" {
		t.Fatalf("cleared model override = %#v", model)
	}
}

func TestProviderProtocolChangeOnlyRewritesInheritedModelEndpoints(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	configPath := filepath.Join(dir, ".p-chat", "config.json")
	initial := `{"llm":{"providers":[{"name":"p","protocol":"openai_chat","base_url":"https://example.com/v1","api_key":"k","models":[{"name":"inherited","api_endpoint":"/chat/completions"},{"name":"claude","protocol":"anthropic_messages","api_endpoint":"/messages"}]}]}}`
	if err := osWriteFile(configPath, initial); err != nil {
		t.Fatal(err)
	}

	updated, err := UpdateProviderAt(configPath, "p", ProviderPatch{Protocol: ProtocolOpenAIResponses})
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.FindModel("inherited").APIEndpoint; got != "/responses" {
		t.Fatalf("inherited endpoint = %q, want /responses", got)
	}
	if model := updated.FindModel("claude"); model.Protocol != ProtocolAnthropicMessages || model.APIEndpoint != "/messages" {
		t.Fatalf("explicit override was rewritten: %#v", model)
	}
}
