package generation

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/p-chat/pchat/internal/config"
)

// NormalizeOptions validates the small vendor-neutral option surface exposed to
// models. Provider-specific or control-plane fields belong in trusted
// DefaultParams configuration and can never be supplied by a tool call.
func NormalizeOptions(operation config.GenerationOperation, options map[string]any) (map[string]any, error) {
	if len(options) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(options))
	for key, value := range options {
		key = strings.TrimSpace(key)
		var normalized any
		var err error
		switch key {
		case "seed":
			normalized, err = boundedInteger(key, value, 0, math.MaxInt32)
		case "count":
			normalized, err = boundedInteger(key, value, 1, 4)
		case "negative_prompt":
			normalized, err = boundedString(key, value, 4000)
		case "prompt_optimizer":
			normalized, err = booleanOption(key, value)
		case "aspect_ratio", "size", "resolution", "quality", "style":
			if operation.OutputKind() == config.MediaAudio {
				err = fmt.Errorf("option %q is not valid for %s", key, operation)
			} else {
				normalized, err = boundedString(key, value, 64)
			}
		case "width", "height":
			if operation.OutputKind() != config.MediaImage {
				err = fmt.Errorf("option %q is only valid for image generation", key)
			} else {
				normalized, err = boundedInteger(key, value, 64, 8192)
			}
		case "duration", "duration_seconds":
			maxDuration := int64(120)
			if operation.OutputKind() == config.MediaAudio {
				maxDuration = 600
			}
			if operation.OutputKind() == config.MediaImage {
				err = fmt.Errorf("option %q is not valid for image generation", key)
			} else {
				normalized, err = boundedInteger(key, value, 1, maxDuration)
			}
		case "voice", "voice_id":
			if operation.OutputKind() != config.MediaAudio {
				err = fmt.Errorf("option %q is only valid for audio generation", key)
			} else {
				normalized, err = boundedString(key, value, 128)
			}
		case "speed":
			normalized, err = audioNumber(operation, key, value, 0.25, 4)
		case "volume":
			normalized, err = audioNumber(operation, key, value, 0, 10)
		case "pitch":
			normalized, err = audioNumber(operation, key, value, -12, 12)
		case "sample_rate":
			if operation.OutputKind() != config.MediaAudio {
				err = fmt.Errorf("option %q is only valid for audio generation", key)
			} else {
				normalized, err = boundedInteger(key, value, 8000, 192000)
			}
		case "format":
			if operation.OutputKind() != config.MediaAudio {
				err = fmt.Errorf("option %q is only valid for audio generation", key)
			} else {
				var format string
				format, err = boundedString(key, value, 16)
				format = strings.ToLower(format)
				if err == nil && !oneOf(format, "mp3", "wav", "pcm", "flac", "aac", "ogg") {
					err = fmt.Errorf("option %q has unsupported format %q", key, format)
				}
				normalized = format
			}
		default:
			err = fmt.Errorf("option %q is not allowed; put vendor-specific parameters in the trusted model configuration", key)
		}
		if err != nil {
			return nil, err
		}
		out[key] = normalized
	}
	return out, nil
}

func boundedInteger(key string, value any, minValue, maxValue int64) (int64, error) {
	number, ok := numericValue(value)
	if !ok || math.Trunc(number) != number || number < float64(minValue) || number > float64(maxValue) {
		return 0, fmt.Errorf("option %q must be an integer from %d to %d", key, minValue, maxValue)
	}
	return int64(number), nil
}

func boundedString(key string, value any, maxRunes int) (string, error) {
	text, ok := value.(string)
	text = strings.TrimSpace(text)
	if !ok || text == "" || len([]rune(text)) > maxRunes {
		return "", fmt.Errorf("option %q must be a non-empty string of at most %d characters", key, maxRunes)
	}
	return text, nil
}

func booleanOption(key string, value any) (bool, error) {
	boolean, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("option %q must be a boolean", key)
	}
	return boolean, nil
}

func audioNumber(operation config.GenerationOperation, key string, value any, minValue, maxValue float64) (float64, error) {
	if operation.OutputKind() != config.MediaAudio {
		return 0, fmt.Errorf("option %q is only valid for audio generation", key)
	}
	number, ok := numericValue(value)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number < minValue || number > maxValue {
		return 0, fmt.Errorf("option %q must be a number from %g to %g", key, minValue, maxValue)
	}
	return number, nil
}

func numericValue(value any) (float64, bool) {
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
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	default:
		return 0, false
	}
}

func oneOf(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if value == candidate {
			return true
		}
	}
	return false
}
