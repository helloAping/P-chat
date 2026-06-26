package llm

import (
	"testing"

	"github.com/p-chat/pchat/internal/config"
)

func TestOptionsFromConfig(t *testing.T) {
	cfg := config.LLMConfig{
		Temperature: 0.7,
		TopP:        0.9,
		MaxTokens:   2048,
	}
	opts := OptionsFromConfig(cfg)
	if opts.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", opts.Temperature)
	}
	if opts.TopP != 0.9 {
		t.Errorf("TopP = %v, want 0.9", opts.TopP)
	}
	if opts.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %d, want 2048", opts.MaxTokens)
	}
}

func TestOptionsFromConfig_Zero(t *testing.T) {
	cfg := config.LLMConfig{}
	opts := OptionsFromConfig(cfg)
	if opts.Temperature != 0 || opts.TopP != 0 || opts.MaxTokens != 0 {
		t.Errorf("zero config should give zero options, got %+v", opts)
	}
}
