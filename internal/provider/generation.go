package provider

import (
	"strings"

	"github.com/p-chat/pchat/internal/config"
)

// GenerationAdapterRequest describes the provider strategy context needed to
// choose a media generation JSON dialect.
type GenerationAdapterRequest struct {
	ProviderID        string
	Protocol          string
	ConfiguredAdapter string
}

// GenerationAdapter returns the default media-generation adapter for a
// provider strategy. Model-level adapter overrides are honored first; otherwise
// official provider strategies choose their own dialect and Custom follows the
// selected protocol family.
func GenerationAdapter(req GenerationAdapterRequest) string {
	configured := config.NormalizeGenerationVendor(req.ConfiguredAdapter)
	if configured != "" && configured != "openai" && configured != "anthropic" {
		return configured
	}

	switch strings.ToLower(strings.TrimSpace(req.ProviderID)) {
	case "volcengine":
		return "volcengine"
	case "minimax":
		return "minimax"
	case "kling", "keling":
		return "kling"
	case "openai", "anthropic", "deepseek", "mimo", "zhipu", "gemini", "kimi", "custom":
		if configured != "" {
			return configured
		}
		return ProtocolGenerationAdapter(req.Protocol)
	default:
		if configured != "" {
			return configured
		}
		return ""
	}
}

// ProtocolGenerationAdapter maps the selected LLM protocol family to a generic
// media adapter. It is primarily used by Custom and OpenAI-compatible presets.
func ProtocolGenerationAdapter(protocol string) string {
	if config.ProtocolIsAnthropic(protocol) {
		return "anthropic"
	}
	return "openai"
}
