package generation

import (
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/config"
)

func TestNormalizeOptionsRejectsProviderControlFieldsAndExcessiveCost(t *testing.T) {
	if _, err := NormalizeOptions(config.GenerationTextToVideo, map[string]any{
		"callback_url": "https://attacker.invalid/callback",
	}); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("callback_url must be rejected, got %v", err)
	}
	if _, err := NormalizeOptions(config.GenerationTextToImage, map[string]any{"count": float64(100)}); err == nil {
		t.Fatal("an excessive image count must be rejected")
	}
}

func TestNormalizeOptionsAppliesMediaSpecificBounds(t *testing.T) {
	options, err := NormalizeOptions(config.GenerationTextToSpeech, map[string]any{
		"voice_id": "calm-reader", "speed": 1.25, "sample_rate": float64(44100), "format": "WAV",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options["format"] != "wav" || options["sample_rate"] != int64(44100) {
		t.Fatalf("normalized audio options = %#v", options)
	}
	if _, err := NormalizeOptions(config.GenerationTextToImage, map[string]any{"voice_id": "reader"}); err == nil {
		t.Fatal("audio-only option must be rejected for image generation")
	}
}
