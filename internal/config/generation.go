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

	// DefaultMediaGenerationAPIEndpoint is the editable OpenAI-compatible
	// endpoint suffix offered for a newly-created media generation model.
	// Providers with a different route can override it per model.
	DefaultMediaGenerationAPIEndpoint = "/images/generations"
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

// GenerationOperationConfig contains model endpoint suffixes and bounded
// execution settings for a media model. Endpoints are joined to the provider
// BaseURL at dispatch time.
type GenerationOperationConfig struct {
	Endpoint       string         `json:"endpoint,omitempty"`
	QueryEndpoint  string         `json:"query_endpoint,omitempty"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
	DefaultParams  map[string]any `json:"default_params,omitempty"`
}

// Normalize fills conservative execution defaults without changing URLs or
// user parameters.
func (c *GenerationOperationConfig) Normalize() {
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 600
	}
}

// MediaGenerationModelConfig declares the canonical operations exposed by a
// media model. API stores the shared/default request configuration; operation
// entries may remain empty capability markers or override endpoint/query/
// timeout/default params for a single capability when a vendor splits routes.
type MediaGenerationModelConfig struct {
	Adapter    string                                            `json:"adapter,omitempty"`
	API        *GenerationOperationConfig                        `json:"api,omitempty"`
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

// EffectiveOperationConfig returns the shared API configuration with any
// per-operation override layered on top. Empty operation values remain simple
// capability markers.
func (c *MediaGenerationModelConfig) EffectiveOperationConfig(operation GenerationOperation) GenerationOperationConfig {
	var operationConfig GenerationOperationConfig
	if c != nil {
		if c.API != nil {
			operationConfig = *c.API
		}
		if override, ok := c.Operations[operation]; ok {
			operationConfig = mergeGenerationOperationConfig(operationConfig, override)
		}
	}
	operationConfig.Normalize()
	return operationConfig
}

func mergeGenerationOperationConfig(base, override GenerationOperationConfig) GenerationOperationConfig {
	overridesEndpoint := strings.TrimSpace(override.Endpoint) != ""
	overridesQueryEndpoint := strings.TrimSpace(override.QueryEndpoint) != ""
	if overridesEndpoint {
		base.Endpoint = override.Endpoint
	}
	if overridesQueryEndpoint {
		base.QueryEndpoint = override.QueryEndpoint
	} else if overridesEndpoint {
		base.QueryEndpoint = ""
	}
	if override.TimeoutSeconds > 0 {
		base.TimeoutSeconds = override.TimeoutSeconds
	}
	if len(override.DefaultParams) > 0 {
		merged := make(map[string]any, len(base.DefaultParams)+len(override.DefaultParams))
		for key, value := range base.DefaultParams {
			merged[key] = value
		}
		for key, value := range override.DefaultParams {
			merged[key] = value
		}
		base.DefaultParams = merged
	}
	return base
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
	if model.Generation.API == nil {
		return fmt.Errorf("media generation model %q requires one shared API configuration", model.Name)
	}
	if err := validateAPIEndpointSuffix(model.Generation.API.Endpoint, "endpoint", true); err != nil {
		return fmt.Errorf("media generation model %q: %w", model.Name, err)
	}
	if err := validateAPIEndpointSuffix(model.Generation.API.QueryEndpoint, "query_endpoint", false); err != nil {
		return fmt.Errorf("media generation model %q: %w", model.Name, err)
	}
	if queryEndpoint := strings.TrimSpace(model.Generation.API.QueryEndpoint); queryEndpoint != "" &&
		!strings.Contains(queryEndpoint, "{task_id}") && !strings.Contains(queryEndpoint, "{id}") {
		return fmt.Errorf("media generation model %q: query_endpoint must contain {task_id} or {id}", model.Name)
	}
	for operation, operationConfig := range model.Generation.Operations {
		if err := validateAPIEndpointSuffix(operationConfig.Endpoint, fmt.Sprintf("operations.%s.endpoint", operation), false); err != nil {
			return fmt.Errorf("media generation model %q: %w", model.Name, err)
		}
		if err := validateAPIEndpointSuffix(operationConfig.QueryEndpoint, fmt.Sprintf("operations.%s.query_endpoint", operation), false); err != nil {
			return fmt.Errorf("media generation model %q: %w", model.Name, err)
		}
		if queryEndpoint := strings.TrimSpace(operationConfig.QueryEndpoint); queryEndpoint != "" &&
			!strings.Contains(queryEndpoint, "{task_id}") && !strings.Contains(queryEndpoint, "{id}") {
			return fmt.Errorf("media generation model %q: operations.%s.query_endpoint must contain {task_id} or {id}", model.Name, operation)
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

// NormalizeGeneration initializes maps, removes unknown capabilities, and
// applies execution defaults without deriving endpoints from vendor names.
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
			for operation, operationConfig := range model.Generation.Operations {
				if !operation.IsValid() {
					delete(model.Generation.Operations, operation)
					continue
				}
				if strings.TrimSpace(operationConfig.Endpoint) != "" {
					operationConfig.Endpoint = NormalizeAPIEndpointSuffix(operationConfig.Endpoint)
				}
				if strings.TrimSpace(operationConfig.QueryEndpoint) != "" {
					operationConfig.QueryEndpoint = NormalizeAPIEndpointSuffix(operationConfig.QueryEndpoint)
				}
				if model.Generation.API == nil || strings.TrimSpace(operationConfig.Endpoint) != "" ||
					strings.TrimSpace(operationConfig.QueryEndpoint) != "" || operationConfig.TimeoutSeconds > 0 ||
					len(operationConfig.DefaultParams) > 0 {
					operationConfig.Normalize()
					model.Generation.Operations[operation] = operationConfig
				}
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
			_, ok := model.Generation.Operations[operation]
			if !ok {
				return GenerationModelTarget{}, ModelConfig{}, GenerationOperationConfig{}, fmt.Errorf("model %s/%s does not support %s", target.Provider, target.Model, operation)
			}
			operationConfig := model.Generation.EffectiveOperationConfig(operation)
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
			operationConfig := model.Generation.EffectiveOperationConfig(operation)
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
	case "kling", "keling", "kuaishou":
		return "kling"
	case "openai", "openai_chat", "openai_responses":
		return "openai"
	case "anthropic", "anthropic_messages", "claude":
		return "anthropic"
	default:
		return strings.ToLower(strings.TrimSpace(vendor))
	}
}
