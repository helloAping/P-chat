package provider

import (
	"net/url"
	"strings"

	"github.com/p-chat/pchat/internal/config"
)

// GenerationPayloadRequest contains provider-neutral inputs for one media
// generation request. Strategy functions use it to build the vendor JSON body
// while generation.HTTPExecutor owns HTTP, polling, and asset materialization.
type GenerationPayloadRequest struct {
	Adapter       string
	Operation     config.GenerationOperation
	Model         string
	Prompt        string
	Endpoint      string
	DefaultParams map[string]any
	Options       map[string]any
	InputDataURLs []string
}

// BuildGenerationPayload builds the JSON payload for a provider media strategy.
func BuildGenerationPayload(req GenerationPayloadRequest) map[string]any {
	strategy := payloadStrategyFor(req.Adapter)
	switch req.Operation.OutputKind() {
	case config.MediaVideo:
		return strategy.BuildVideoPayload(req)
	case config.MediaAudio:
		return strategy.BuildAudioPayload(req)
	default:
		return strategy.BuildImagePayload(req)
	}
}

type mediaPayloadStrategy interface {
	BuildImagePayload(GenerationPayloadRequest) map[string]any
	BuildVideoPayload(GenerationPayloadRequest) map[string]any
	BuildAudioPayload(GenerationPayloadRequest) map[string]any
}

func payloadStrategyFor(adapter string) mediaPayloadStrategy {
	switch config.NormalizeGenerationVendor(adapter) {
	case "volcengine":
		return volcenginePayloadStrategy{}
	case "minimax":
		return minimaxPayloadStrategy{}
	case "kling":
		return klingPayloadStrategy{}
	case "openai":
		return openAIPayloadStrategy{}
	default:
		return genericPayloadStrategy{}
	}
}

type genericPayloadStrategy struct{}

func (genericPayloadStrategy) BuildImagePayload(req GenerationPayloadRequest) map[string]any {
	payload := baseGenerationPayload(req)
	return addGenericInputPayload(payload, req)
}

func (genericPayloadStrategy) BuildVideoPayload(req GenerationPayloadRequest) map[string]any {
	payload := baseGenerationPayload(req)
	return addGenericInputPayload(payload, req)
}

func (genericPayloadStrategy) BuildAudioPayload(req GenerationPayloadRequest) map[string]any {
	payload := baseGenerationPayload(req)
	return addGenericInputPayload(payload, req)
}

type openAIPayloadStrategy struct {
	genericPayloadStrategy
}

func (openAIPayloadStrategy) BuildImagePayload(req GenerationPayloadRequest) map[string]any {
	payload := baseGenerationPayload(req)
	countAsN(payload)
	return addGenericInputPayload(payload, req)
}

type minimaxPayloadStrategy struct{}

func (minimaxPayloadStrategy) BuildImagePayload(req GenerationPayloadRequest) map[string]any {
	payload := baseGenerationPayload(req)
	countAsN(payload)
	if req.Operation == config.GenerationImageToImage {
		references := make([]map[string]any, 0, len(req.InputDataURLs))
		for _, dataURL := range req.InputDataURLs {
			references = append(references, map[string]any{"type": "character", "image_file": dataURL})
		}
		payload["subject_reference"] = references
		return payload
	}
	return addGenericInputPayload(payload, req)
}

func (minimaxPayloadStrategy) BuildVideoPayload(req GenerationPayloadRequest) map[string]any {
	payload := baseGenerationPayload(req)
	if isMiniMaxH3VideoRequest(req) {
		delete(payload, "prompt")
		normalizeMiniMaxH3VideoParams(payload, req)
		payload["model"] = canonicalMiniMaxH3Model(req.Model)
		payload["content"] = miniMaxH3VideoContent(req, req.InputDataURLs)
		return payload
	}
	if req.Operation == config.GenerationImageToVideo && len(req.InputDataURLs) > 0 {
		payload["first_frame_image"] = req.InputDataURLs[0]
	}
	return payload
}

func (minimaxPayloadStrategy) BuildAudioPayload(req GenerationPayloadRequest) map[string]any {
	payload := baseGenerationPayload(req)
	if req.Operation == config.GenerationTextToSpeech {
		delete(payload, "prompt")
		payload["text"] = req.Prompt
		return payload
	}
	return addGenericInputPayload(payload, req)
}

type klingPayloadStrategy struct{}

func (klingPayloadStrategy) BuildImagePayload(req GenerationPayloadRequest) map[string]any {
	payload := baseGenerationPayload(req)
	klingModelName(payload)
	return addKlingInputPayload(payload, req)
}

func (klingPayloadStrategy) BuildVideoPayload(req GenerationPayloadRequest) map[string]any {
	payload := baseGenerationPayload(req)
	klingModelName(payload)
	return addKlingInputPayload(payload, req)
}

func (klingPayloadStrategy) BuildAudioPayload(req GenerationPayloadRequest) map[string]any {
	payload := baseGenerationPayload(req)
	klingModelName(payload)
	return addGenericInputPayload(payload, req)
}

type volcenginePayloadStrategy struct{}

func (volcenginePayloadStrategy) BuildImagePayload(req GenerationPayloadRequest) map[string]any {
	payload := baseGenerationPayload(req)
	if count, ok := payload["count"]; ok {
		delete(payload, "count")
		if numeric, ok := payloadNumericValue(count); ok && numeric > 1 {
			payload["sequential_image_generation"] = "auto"
			payload["sequential_image_generation_options"] = map[string]any{"max_images": int64(numeric)}
		}
	}
	if req.Operation == config.GenerationImageToImage {
		if len(req.InputDataURLs) == 1 {
			payload["image"] = req.InputDataURLs[0]
		} else if len(req.InputDataURLs) > 1 {
			payload["image"] = req.InputDataURLs
		}
		return payload
	}
	return addGenericInputPayload(payload, req)
}

func (volcenginePayloadStrategy) BuildVideoPayload(req GenerationPayloadRequest) map[string]any {
	payload := baseGenerationPayload(req)
	content := []map[string]any{{"type": "text", "text": req.Prompt}}
	for _, dataURL := range req.InputDataURLs {
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURL}})
	}
	delete(payload, "prompt")
	payload["content"] = content
	return payload
}

func (volcenginePayloadStrategy) BuildAudioPayload(req GenerationPayloadRequest) map[string]any {
	payload := baseGenerationPayload(req)
	return addGenericInputPayload(payload, req)
}

func baseGenerationPayload(req GenerationPayloadRequest) map[string]any {
	payload := clonePayloadMap(req.DefaultParams)
	for key, value := range req.Options {
		payload[key] = value
	}
	if duration, ok := payload["duration_seconds"]; ok {
		if _, exists := payload["duration"]; !exists {
			payload["duration"] = duration
		}
		delete(payload, "duration_seconds")
	}
	payload["model"] = req.Model
	payload["prompt"] = req.Prompt
	return payload
}

func addGenericInputPayload(payload map[string]any, req GenerationPayloadRequest) map[string]any {
	if len(req.InputDataURLs) == 0 {
		return payload
	}
	field := string(req.Operation.RequiredInputKind())
	if len(req.InputDataURLs) == 1 {
		payload[field] = req.InputDataURLs[0]
	} else {
		payload[field+"s"] = req.InputDataURLs
	}
	return payload
}

func klingModelName(payload map[string]any) {
	if model, ok := payload["model"]; ok {
		payload["model_name"] = model
		delete(payload, "model")
	}
}

func addKlingInputPayload(payload map[string]any, req GenerationPayloadRequest) map[string]any {
	if image := maybeFirstInput(req, config.MediaImage); image != "" {
		payload["image"] = stripDataURLHeader(image)
	}
	if video := maybeFirstInput(req, config.MediaVideo); video != "" {
		payload["video"] = stripDataURLHeader(video)
	}
	return payload
}

func maybeFirstInput(req GenerationPayloadRequest, kind config.MediaKind) string {
	if req.Operation.RequiredInputKind() != kind || len(req.InputDataURLs) == 0 {
		return ""
	}
	return req.InputDataURLs[0]
}

func stripDataURLHeader(value string) string {
	const marker = ";base64,"
	if index := strings.Index(value, marker); index >= 0 {
		return value[index+len(marker):]
	}
	return value
}

func countAsN(payload map[string]any) {
	if count, ok := payload["count"]; ok {
		payload["n"] = count
		delete(payload, "count")
	}
}

func isMiniMaxH3VideoRequest(req GenerationPayloadRequest) bool {
	if req.Operation.OutputKind() != config.MediaVideo {
		return false
	}
	if canonical := canonicalMiniMaxH3Model(req.Model); canonical == "MiniMax-H3" || canonical == "MiniMax-H3-Max" {
		return true
	}
	endpoint := strings.ToLower(strings.TrimSpace(req.Endpoint))
	if parsed, err := url.Parse(endpoint); err == nil && parsed.Path != "" {
		endpoint = parsed.Path
	}
	return strings.HasSuffix(strings.TrimRight(endpoint, "/"), "/v2/video_generation")
}

func canonicalMiniMaxH3Model(model string) string {
	trimmed := strings.TrimSpace(model)
	normalized := strings.ToLower(strings.NewReplacer("_", "-", " ", "-", ".", "-").Replace(trimmed))
	switch normalized {
	case "minimax-h3", "minimaxh3":
		return "MiniMax-H3"
	case "minimax-h3-max", "minimax-h3max", "minimaxh3-max", "minimaxh3max":
		return "MiniMax-H3-Max"
	default:
		return trimmed
	}
}

func normalizeMiniMaxH3VideoParams(payload map[string]any, req GenerationPayloadRequest) {
	if aspectRatio, ok := payload["aspect_ratio"]; ok {
		if _, exists := payload["ratio"]; !exists {
			payload["ratio"] = aspectRatio
		}
		delete(payload, "aspect_ratio")
	}
	if _, exists := payload["duration"]; !exists {
		payload["duration"] = int64(5)
	}
	if _, exists := payload["resolution"]; !exists {
		payload["resolution"] = "768P"
	}
	if _, exists := payload["ratio"]; !exists {
		if req.Operation == config.GenerationTextToVideo {
			payload["ratio"] = "16:9"
		} else {
			payload["ratio"] = "adaptive"
		}
	}
}

func miniMaxH3VideoContent(req GenerationPayloadRequest, dataURLs []string) []map[string]any {
	content := []map[string]any{{"type": "text", "text": req.Prompt}}
	for _, dataURL := range dataURLs {
		switch req.Operation.RequiredInputKind() {
		case config.MediaImage:
			content = append(content, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": dataURL},
				"role":      "first_frame",
			})
		case config.MediaVideo:
			content = append(content, map[string]any{
				"type":      "video_url",
				"video_url": map[string]any{"url": dataURL},
				"role":      "reference_video",
			})
		case config.MediaAudio:
			content = append(content, map[string]any{
				"type":      "audio_url",
				"audio_url": map[string]any{"url": dataURL},
				"role":      "reference_audio",
			})
		}
	}
	return content
}

func clonePayloadMap(source map[string]any) map[string]any {
	out := make(map[string]any, len(source)+4)
	for key, value := range source {
		out[key] = value
	}
	return out
}

func payloadNumericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	default:
		return 0, false
	}
}
