package provider

import (
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/config"
)

func TestBuildGenerationPayloadUsesProviderSpecificPromptShapes(t *testing.T) {
	volc := BuildGenerationPayload(GenerationPayloadRequest{
		Adapter:   "volcengine",
		Operation: config.GenerationTextToVideo,
		Model:     "model",
		Prompt:    "move slowly",
	})
	if _, exists := volc["prompt"]; exists {
		t.Fatalf("Volcengine video payload must not contain top-level prompt: %#v", volc)
	}
	content, ok := volc["content"].([]map[string]any)
	if !ok || len(content) != 1 || content[0]["text"] != "move slowly" {
		t.Fatalf("Volcengine video content = %#v", volc["content"])
	}

	minimax := BuildGenerationPayload(GenerationPayloadRequest{
		Adapter:   "minimax",
		Operation: config.GenerationTextToSpeech,
		Model:     "model",
		Prompt:    "你好",
	})
	if minimax["text"] != "你好" {
		t.Fatalf("MiniMax TTS text = %#v", minimax)
	}
	if _, exists := minimax["prompt"]; exists {
		t.Fatalf("MiniMax TTS payload must not contain prompt: %#v", minimax)
	}
}

func TestBuildGenerationPayloadSupportsMiniMaxAndVolcengineImages(t *testing.T) {
	minimaxImage := BuildGenerationPayload(GenerationPayloadRequest{
		Adapter:       "minimax",
		Operation:     config.GenerationImageToImage,
		Model:         "model",
		Prompt:        "keep the character",
		Options:       map[string]any{"count": int64(2)},
		InputDataURLs: []string{"data:image/png;base64,cmVmZXJlbmNl"},
	})
	references, ok := minimaxImage["subject_reference"].([]map[string]any)
	if !ok || len(references) != 1 {
		t.Fatalf("MiniMax image references = %#v", minimaxImage)
	}
	imageFile, _ := references[0]["image_file"].(string)
	if !strings.HasPrefix(imageFile, "data:image/png;base64,") || minimaxImage["n"] != int64(2) {
		t.Fatalf("MiniMax image payload = %#v", minimaxImage)
	}

	volcImage := BuildGenerationPayload(GenerationPayloadRequest{
		Adapter:       "volcengine",
		Operation:     config.GenerationImageToImage,
		Model:         "model",
		Prompt:        "blend references",
		InputDataURLs: []string{"data:image/png;base64,b25l", "data:image/png;base64,dHdv"},
	})
	if images, ok := volcImage["image"].([]string); !ok || len(images) != 2 {
		t.Fatalf("Volcengine multi-image payload = %#v", volcImage)
	}

	klingVideo := BuildGenerationPayload(GenerationPayloadRequest{
		Adapter:       "kling",
		Operation:     config.GenerationImageToVideo,
		Model:         "kling-v1-6",
		Prompt:        "make it move",
		InputDataURLs: []string{"data:image/png;base64,Zmlyc3Q="},
	})
	if klingVideo["model_name"] != "kling-v1-6" {
		t.Fatalf("Kling model_name = %#v", klingVideo)
	}
	if _, exists := klingVideo["model"]; exists {
		t.Fatalf("Kling payload must not contain generic model field: %#v", klingVideo)
	}
	if klingVideo["image"] != "Zmlyc3Q=" {
		t.Fatalf("Kling image payload should use raw base64: %#v", klingVideo)
	}
}

func TestBuildGenerationPayloadUsesMiniMaxH3ContentArray(t *testing.T) {
	payload := BuildGenerationPayload(GenerationPayloadRequest{
		Adapter:       "minimax",
		Operation:     config.GenerationImageToVideo,
		Model:         "minimax-h3",
		Prompt:        "make it move",
		Endpoint:      "/v2/video_generation",
		Options:       map[string]any{"aspect_ratio": "16:9", "duration_seconds": int64(5)},
		InputDataURLs: []string{"data:image/png;base64,Zmlyc3Q="},
	})
	if payload["model"] != "MiniMax-H3" {
		t.Fatalf("MiniMax H3 model alias was not canonicalized: %#v", payload["model"])
	}
	if _, exists := payload["prompt"]; exists {
		t.Fatalf("MiniMax H3 payload should not contain prompt: %#v", payload)
	}
	if payload["ratio"] != "16:9" {
		t.Fatalf("MiniMax H3 ratio = %#v", payload["ratio"])
	}
	content, ok := payload["content"].([]map[string]any)
	if !ok || len(content) != 2 {
		t.Fatalf("MiniMax H3 content = %#v", payload["content"])
	}
	if image, ok := content[1]["image_url"].(map[string]any); !ok || image["url"] == "" || content[1]["role"] != "first_frame" {
		t.Fatalf("MiniMax H3 first frame content = %#v", content[1])
	}
}

func TestBuildGenerationPayloadKeepsLegacyMiniMaxVideoShape(t *testing.T) {
	payload := BuildGenerationPayload(GenerationPayloadRequest{
		Adapter:       "minimax",
		Operation:     config.GenerationImageToVideo,
		Model:         "video-01",
		Prompt:        "make it move",
		Endpoint:      "/v1/video_generation",
		InputDataURLs: []string{"data:image/png;base64,Zmlyc3Q="},
	})
	if payload["prompt"] != "make it move" {
		t.Fatalf("legacy MiniMax video prompt was changed: %#v", payload)
	}
	if _, exists := payload["content"]; exists {
		t.Fatalf("legacy MiniMax video payload should not use H3 content array: %#v", payload)
	}
	if firstFrame, _ := payload["first_frame_image"].(string); !strings.HasPrefix(firstFrame, "data:image/png;base64,") {
		t.Fatalf("legacy MiniMax video first frame = %#v", payload)
	}
}
