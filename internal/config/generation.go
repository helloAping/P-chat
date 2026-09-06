package config

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// ModelType separates conversational LLMs from media generation models.
// Empty values are treated as llm for backward compatibility.
type ModelType string

const (
	ModelTypeLLM             ModelType = "llm"
	ModelTypeMediaGeneration ModelType = "media_generation"
)

// IsValid reports whether the model type is supported by this build.
func (t ModelType) IsValid() bool {
	return t == ModelTypeLLM || t == ModelTypeMediaGeneration
}

// GenerationOperation is a vendor-neutral media transformation capability.
type GenerationOperation string

const (
	GenerationTextToImage  GenerationOperation = "text_to_image"
	GenerationImageToImage GenerationOperation = "image_to_image"
	GenerationTextToVideo  GenerationOperation = "text_to_video"
	GenerationImageToVideo GenerationOperation = "image_to_video"
	GenerationVideoToVideo GenerationOperation = "video_to_video"
	GenerationTextToSpeech GenerationOperation = "text_to_speech"
	GenerationTextToMusic  GenerationOperation = "text_to_music"
	GenerationTextToSound  GenerationOperation = "text_to_sound"
	GenerationAudioToAudio GenerationOperation = "audio_to_audio"
)

var generationOperations = []GenerationOperation{
	GenerationTextToImage,
	GenerationImageToImage,
	GenerationTextToVideo,
	GenerationImageToVideo,
	GenerationVideoToVideo,
	GenerationTextToSpeech,
	GenerationTextToMusic,
	GenerationTextToSound,
	GenerationAudioToAudio,
}

// AllGenerationOperations returns every canonical operation in stable UI order.
func AllGenerationOperations() []GenerationOperation {
	return append([]GenerationOperation(nil), generationOperations...)
}

// IsValid reports whether the operation is part of the canonical contract.
func (o GenerationOperation) IsValid() bool {
	for _, candidate := range generationOperations {
		if candidate == o {
			return true
		}
	}
	return false
}

// OutputKind returns the kind of asset produced by the operation.
func (o GenerationOperation) OutputKind() MediaKind {
	switch o {
	case GenerationTextToImage, GenerationImageToImage:
		return MediaImage
	case GenerationTextToVideo, GenerationImageToVideo, GenerationVideoToVideo:
		return MediaVideo
	case GenerationTextToSpeech, GenerationTextToMusic, GenerationTextToSound, GenerationAudioToAudio:
		return MediaAudio
	default:
		return ""
	}
}

// RequiredInputKind returns the required media input, or an empty kind for
// text-only generation operations.
func (o GenerationOperation) RequiredInputKind() MediaKind {
	switch o {
	case GenerationImageToImage, GenerationImageToVideo:
		return MediaImage
	case GenerationVideoToVideo:
		return MediaVideo
	case GenerationAudioToAudio:
		return MediaAudio
	default:
		return ""
	}
}

// GenerationOperationConfig contains the vendor endpoint override and bounded
// execution settings for one model capability. Endpoint may be absolute or
// relative to the provider BaseURL.
type GenerationOperationConfig struct {
	Endpoint       string         `json:"endpoint,omitempty"`
	QueryEndpoint  string         `json:"query_endpoint,omitempty"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
	DefaultParams  map[string]any `json:"default_params,omitempty"`
}

// Normalize fills conservative defaults without changing user parameters.
func (c *GenerationOperationConfig) Normalize(vendor string, operation GenerationOperation) {
	if strings.TrimSpace(c.Endpoint) == "" {
		c.Endpoint = DefaultGenerationEndpoint(vendor, operation)
	}
	if strings.TrimSpace(c.QueryEndpoint) == "" {
		c.QueryEndpoint = DefaultGenerationQueryEndpoint(vendor, operation)
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 600
	}
}

// MediaGenerationModelConfig maps canonical operations to vendor endpoints.
type MediaGenerationModelConfig struct {
	Adapter    string                                            `json:"adapter,omitempty"`
	Operations map[GenerationOperation]GenerationOperationConfig `json:"operations,omitempty"`
}

// Supports reports whether this model explicitly exposes the operation.
func (c *MediaGenerationModelConfig) Supports(operation GenerationOperation) bool {
	if c == nil || !operation.IsValid() {
		return false
	}
	_, ok := c.Operations[operation]
	return ok
}

// SupportedOperations returns the model capabilities in stable order.
func (c *MediaGenerationModelConfig) SupportedOperations() []GenerationOperation {
	if c == nil {
		return nil
	}
	out := make([]GenerationOperation, 0, len(c.Operations))
	for _, operation := range generationOperations {
		if _, ok := c.Operations[operation]; ok {
			out = append(out, operation)
		}
	}
	return out
}

// GenerationModelTarget selects one media model without duplicating provider
// credentials in application or session configuration.
type GenerationModelTarget struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// Valid reports whether both target coordinates are present.
func (t GenerationModelTarget) Valid() bool {
	return strings.TrimSpace(t.Provider) != "" && strings.TrimSpace(t.Model) != ""
}

// GenerationConfig stores application-level defaults. Session switches remain
// independent and default off.
type GenerationConfig struct {
	Defaults       map[GenerationOperation]GenerationModelTarget `json:"defaults,omitempty"`
	RequireConfirm bool                                          `json:"require_confirm,omitempty"`
}

// ValidateGenerationModel validates the media-specific portion of a model
// before it is persisted through provider CRUD APIs.
func ValidateGenerationModel(model ModelConfig) error {
	typeName := model.EffectiveType()
	if !typeName.IsValid() {
		return fmt.Errorf("invalid model type %q", model.Type)
	}
	if typeName == ModelTypeLLM {
		if model.Generation != nil && len(model.Generation.Operations) > 0 {
			return fmt.Errorf("llm model %q cannot declare media generation operations", model.Name)
		}
		return nil
	}
	if model.Default {
		return fmt.Errorf("media generation model %q cannot be the provider chat default", model.Name)
	}
	if model.Generation == nil || len(model.Generation.Operations) == 0 {
		return fmt.Errorf("media generation model %q requires at least one operation", model.Name)
	}
	for operation := range model.Generation.Operations {
		if !operation.IsValid() {
			return fmt.Errorf("unsupported generation operation %q", operation)
		}
	}
	return nil
}

// GenerationDefaultReferenced reports whether an application default points
// at a provider/model pair.
func (c *Config) GenerationDefaultReferenced(provider, model string) (GenerationOperation, bool) {
	if c == nil {
		return "", false
	}
	for operation, target := range c.Generation.Defaults {
		if target.Provider == provider && (model == "" || target.Model == model) {
			return operation, true
		}
	}
	return "", false
}

// NormalizeGeneration initializes maps and fills endpoint presets for known
// vendors. Custom endpoint values always win.
func (c *Config) NormalizeGeneration() {
	if c.Generation.Defaults == nil {
		c.Generation.Defaults = make(map[GenerationOperation]GenerationModelTarget)
	}
	for providerIndex := range c.LLM.Providers {
		provider := &c.LLM.Providers[providerIndex]
		for modelIndex := range provider.Models {
			model := &provider.Models[modelIndex]
			if model.EffectiveType() != ModelTypeMediaGeneration || model.Generation == nil {
				continue
			}
			if model.Generation.Operations == nil {
				model.Generation.Operations = make(map[GenerationOperation]GenerationOperationConfig)
			}
			if strings.TrimSpace(model.Generation.Adapter) == "" {
				model.Generation.Adapter = NormalizeGenerationVendor(provider.Vendor)
			}
			adapter := NormalizeGenerationVendor(model.Generation.Adapter)
			for operation, operationConfig := range model.Generation.Operations {
				if !operation.IsValid() {
					delete(model.Generation.Operations, operation)
					continue
				}
				operationConfig.Normalize(adapter, operation)
				model.Generation.Operations[operation] = operationConfig
			}
		}
	}
}

// ResolveGenerationTarget resolves a session override followed by the app
// default, then verifies the selected model capability.
func (c *Config) ResolveGenerationTarget(operation GenerationOperation, override GenerationModelTarget) (GenerationModelTarget, ModelConfig, GenerationOperationConfig, error) {
	if c == nil {
		return GenerationModelTarget{}, ModelConfig{}, GenerationOperationConfig{}, fmt.Errorf("generation config is unavailable")
	}
	if !operation.IsValid() {
		return GenerationModelTarget{}, ModelConfig{}, GenerationOperationConfig{}, fmt.Errorf("unsupported generation operation %q", operation)
	}
	target := override
	if !target.Valid() {
		target = c.Generation.Defaults[operation]
	}
	if !target.Valid() {
		return GenerationModelTarget{}, ModelConfig{}, GenerationOperationConfig{}, fmt.Errorf("no generation model is configured for %s", operation)
	}
	for _, provider := range c.LLM.Providers {
		if provider.Name != target.Provider {
			continue
		}
		for _, model := range provider.Models {
			if model.Name != target.Model {
				continue
			}
			if model.EffectiveType() != ModelTypeMediaGeneration || model.Generation == nil {
				return GenerationModelTarget{}, ModelConfig{}, GenerationOperationConfig{}, fmt.Errorf("model %s/%s is not a media generation model", target.Provider, target.Model)
			}
			operationConfig, ok := model.Generation.Operations[operation]
			if !ok {
				return GenerationModelTarget{}, ModelConfig{}, GenerationOperationConfig{}, fmt.Errorf("model %s/%s does not support %s", target.Provider, target.Model, operation)
			}
			adapter := model.Generation.Adapter
			if strings.TrimSpace(adapter) == "" {
				adapter = provider.Vendor
			}
			operationConfig.Normalize(adapter, operation)
			if !generationEndpointUsable(provider.BaseURL, operationConfig.Endpoint) {
				return GenerationModelTarget{}, ModelConfig{}, GenerationOperationConfig{}, fmt.Errorf("model %s/%s has no usable endpoint for %s", target.Provider, target.Model, operation)
			}
			return target, model, operationConfig, nil
		}
		return GenerationModelTarget{}, ModelConfig{}, GenerationOperationConfig{}, fmt.Errorf("generation model %s/%s was not found", target.Provider, target.Model)
	}
	return GenerationModelTarget{}, ModelConfig{}, GenerationOperationConfig{}, fmt.Errorf("generation provider %q was not found", target.Provider)
}

func generationEndpointUsable(baseURL, endpoint string) bool {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return false
	}
	if parsed, err := url.Parse(endpoint); err == nil && parsed.IsAbs() {
		return parsed.Scheme == "http" || parsed.Scheme == "https"
	}
	base, err := url.Parse(strings.TrimSpace(baseURL))
	return err == nil && base.IsAbs() && (base.Scheme == "http" || base.Scheme == "https")
}

// GenerationModelsFor returns all media models supporting the operation.
func (c *Config) GenerationModelsFor(operation GenerationOperation) []GenerationModelTarget {
	if c == nil || !operation.IsValid() {
		return nil
	}
	out := make([]GenerationModelTarget, 0)
	for _, provider := range c.LLM.Providers {
		for _, model := range provider.Models {
			if model.EffectiveType() != ModelTypeMediaGeneration || !model.Generation.Supports(operation) {
				continue
			}
			operationConfig := model.Generation.Operations[operation]
			adapter := model.Generation.Adapter
			if strings.TrimSpace(adapter) == "" {
				adapter = provider.Vendor
			}
			operationConfig.Normalize(adapter, operation)
			if !generationEndpointUsable(provider.BaseURL, operationConfig.Endpoint) {
				continue
			}
			out = append(out, GenerationModelTarget{Provider: provider.Name, Model: model.Name})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider == out[j].Provider {
			return out[i].Model < out[j].Model
		}
		return out[i].Provider < out[j].Provider
	})
	return out
}

// NormalizeGenerationVendor maps common provider aliases to adapter ids.
func NormalizeGenerationVendor(vendor string) string {
	switch strings.ToLower(strings.TrimSpace(vendor)) {
	case "volcengine", "volcano", "doubao", "ark", "volcengine_ark":
		return "volcengine"
	case "minimax", "hailuo":
		return "minimax"
	case "openai":
		return "openai"
	default:
		return strings.ToLower(strings.TrimSpace(vendor))
	}
}

// DefaultGenerationBaseURL returns an editable provider URL preset.
func DefaultGenerationBaseURL(vendor string) string {
	switch NormalizeGenerationVendor(vendor) {
	case "volcengine":
		return "https://ark.cn-beijing.volces.com/api/v3"
	case "minimax":
		return "https://api.minimax.io"
	case "openai":
		return "https://api.openai.com/v1"
	default:
		return ""
	}
}

// DefaultGenerationEndpoint returns the create endpoint for a known vendor.
func DefaultGenerationEndpoint(vendor string, operation GenerationOperation) string {
	switch NormalizeGenerationVendor(vendor) {
	case "volcengine":
		switch operation {
		case GenerationTextToImage, GenerationImageToImage:
			return "/images/generations"
		case GenerationTextToVideo, GenerationImageToVideo:
			return "/contents/generations/tasks"
		}
	case "minimax":
		switch operation {
		case GenerationTextToImage, GenerationImageToImage:
			return "/v1/image_generation"
		case GenerationTextToVideo, GenerationImageToVideo:
			return "/v1/video_generation"
		case GenerationTextToSpeech:
			return "/v1/t2a_v2"
		}
	case "openai":
		if operation == GenerationTextToImage {
			return "/images/generations"
		}
	}
	return ""
}

// DefaultGenerationQueryEndpoint returns the async status endpoint template.
func DefaultGenerationQueryEndpoint(vendor string, operation GenerationOperation) string {
	if operation != GenerationTextToVideo && operation != GenerationImageToVideo {
		return ""
	}
	switch NormalizeGenerationVendor(vendor) {
	case "volcengine":
		return "/contents/generations/tasks/{task_id}"
	case "minimax":
		return "/v1/query/video_generation?task_id={task_id}"
	default:
		return ""
	}
}
