package config

import (
	"strings"
	"testing"
)

func TestProviderModelAPIURLJoinsBaseURLAndModelEndpoint(t *testing.T) {
	configured := ProviderConfig{
		Protocol: "openai",
		BaseURL:  "https://ark.example/api/v3",
		Models: []ModelConfig{{
			Name: "chat", APIEndpoint: "/responses", Default: true,
		}},
	}
	if got := configured.EffectiveAPIURL(); got != "https://ark.example/api/v3/responses" {
		t.Fatalf("EffectiveAPIURL() = %q", got)
	}
	defaultOpenAI := ProviderConfig{Protocol: "openai", BaseURL: "https://proxy.example/v1"}
	if got := defaultOpenAI.EffectiveAPIURL(); got != "https://proxy.example/v1/chat/completions" {
		t.Fatalf("default OpenAI endpoint = %q", got)
	}
	defaultAnthropic := ProviderConfig{Protocol: "anthropic", BaseURL: "https://api.anthropic.com/v1"}
	if got := defaultAnthropic.EffectiveAPIURL(); got != "https://api.anthropic.com/v1/messages" {
		t.Fatalf("default Anthropic endpoint = %q", got)
	}
}

func TestModelTypeDefaultsToLLMAndEffectiveModelSkipsMediaModels(t *testing.T) {
	provider := ProviderConfig{
		Model: "legacy-chat",
		Models: []ModelConfig{
			{Name: "video-first", Type: ModelTypeMediaGeneration, Default: true},
			{Name: "chat-model"},
		},
	}

	if got := (ModelConfig{}).EffectiveType(); got != ModelTypeLLM {
		t.Fatalf("empty model type = %q, want %q", got, ModelTypeLLM)
	}
	if got := provider.EffectiveModel(); got != "chat-model" {
		t.Fatalf("effective chat model = %q, want chat-model", got)
	}
}

func TestProviderWithoutConversationalModelHasNoSyntheticBlankModel(t *testing.T) {
	provider := ProviderConfig{
		Name: "media-only", Vendor: "minimax", Model: "stale-legacy-chat",
		Models: []ModelConfig{{Name: "video-model", Type: ModelTypeMediaGeneration}},
	}
	if models := provider.AllModels(); len(models) != 1 || models[0].Name != "video-model" {
		t.Fatalf("AllModels() = %#v, want the configured media model only", models)
	}
	if model := provider.EffectiveModel(); model != "" {
		t.Fatalf("EffectiveModel() = %q, want empty", model)
	}
}

func TestResolveGenerationTargetRequiresEnabledModelCapability(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{Providers: []ProviderConfig{{
			Name: "minimax", BaseURL: "https://api.minimax.io",
			Models: []ModelConfig{{
				Name: "image-01",
				Type: ModelTypeMediaGeneration,
				Generation: &MediaGenerationModelConfig{Operations: map[GenerationOperation]GenerationOperationConfig{
					GenerationTextToImage: {Endpoint: "/v1/image_generation"},
				}},
			}},
		}}},
		Generation: GenerationConfig{Defaults: map[GenerationOperation]GenerationModelTarget{
			GenerationTextToImage: {Provider: "minimax", Model: "image-01"},
			GenerationTextToVideo: {Provider: "minimax", Model: "image-01"},
		}},
	}

	target, model, operation, err := cfg.ResolveGenerationTarget(GenerationTextToImage, GenerationModelTarget{})
	if err != nil {
		t.Fatalf("resolve text-to-image: %v", err)
	}
	if target.Provider != "minimax" || target.Model != "image-01" || model.Name != "image-01" {
		t.Fatalf("resolved target/model = %#v / %#v", target, model)
	}
	if operation.Endpoint != "/v1/image_generation" {
		t.Fatalf("operation config = %#v", operation)
	}

	if _, _, _, err := cfg.ResolveGenerationTarget(GenerationTextToVideo, GenerationModelTarget{}); err == nil {
		t.Fatal("a model without text_to_video capability must be rejected")
	}
}

func TestResolveGenerationTargetUsesSharedModelAPIForEveryCapability(t *testing.T) {
	sharedAPI := &GenerationOperationConfig{
		Endpoint:       "/v1/generate",
		QueryEndpoint:  "/v1/tasks/{task_id}",
		TimeoutSeconds: 90,
		DefaultParams:  map[string]any{"quality": "high"},
	}
	cfg := Config{
		LLM: LLMConfig{Providers: []ProviderConfig{{
			Name: "media", Vendor: "custom", BaseURL: "https://media.example.com",
			Models: []ModelConfig{{
				Name: "omni", Type: ModelTypeMediaGeneration,
				Generation: &MediaGenerationModelConfig{
					API: sharedAPI,
					Operations: map[GenerationOperation]GenerationOperationConfig{
						GenerationTextToVideo:  {},
						GenerationImageToVideo: {},
					},
				},
			}},
		}}},
		Generation: GenerationConfig{Defaults: map[GenerationOperation]GenerationModelTarget{
			GenerationTextToVideo:  {Provider: "media", Model: "omni"},
			GenerationImageToVideo: {Provider: "media", Model: "omni"},
		}},
	}

	for _, operation := range []GenerationOperation{GenerationTextToVideo, GenerationImageToVideo} {
		_, _, resolved, err := cfg.ResolveGenerationTarget(operation, GenerationModelTarget{})
		if err != nil {
			t.Fatalf("resolve %s: %v", operation, err)
		}
		if resolved.Endpoint != sharedAPI.Endpoint || resolved.QueryEndpoint != sharedAPI.QueryEndpoint || resolved.TimeoutSeconds != sharedAPI.TimeoutSeconds {
			t.Fatalf("resolved %s config = %#v, want shared %#v", operation, resolved, sharedAPI)
		}
		if resolved.DefaultParams["quality"] != "high" {
			t.Fatalf("resolved %s default params = %#v", operation, resolved.DefaultParams)
		}
	}
}

func TestResolveGenerationTargetMergesOperationOverrideWithSharedAPI(t *testing.T) {
	sharedAPI := &GenerationOperationConfig{
		Endpoint:       "/images/generations",
		QueryEndpoint:  "/images/tasks/{task_id}",
		TimeoutSeconds: 120,
		DefaultParams: map[string]any{
			"quality": "standard",
			"seed":    float64(1234),
		},
	}
	cfg := Config{
		LLM: LLMConfig{Providers: []ProviderConfig{{
			Name: "volcengine", ProviderID: "volcengine", BaseURL: "https://ark.example/api/v3",
			Models: []ModelConfig{{
				Name: "doubao-omni", Type: ModelTypeMediaGeneration,
				Generation: &MediaGenerationModelConfig{
					API: sharedAPI,
					Operations: map[GenerationOperation]GenerationOperationConfig{
						GenerationTextToImage: {},
						GenerationTextToVideo: {
							Endpoint:       "/contents/generations/tasks",
							QueryEndpoint:  "/contents/generations/tasks/{id}",
							TimeoutSeconds: 600,
							DefaultParams:  map[string]any{"quality": "uhd"},
						},
					},
				},
			}},
		}}},
		Generation: GenerationConfig{Defaults: map[GenerationOperation]GenerationModelTarget{
			GenerationTextToImage: {Provider: "volcengine", Model: "doubao-omni"},
			GenerationTextToVideo: {Provider: "volcengine", Model: "doubao-omni"},
		}},
	}

	_, _, imageConfig, err := cfg.ResolveGenerationTarget(GenerationTextToImage, GenerationModelTarget{})
	if err != nil {
		t.Fatalf("resolve text-to-image: %v", err)
	}
	if imageConfig.Endpoint != sharedAPI.Endpoint || imageConfig.QueryEndpoint != sharedAPI.QueryEndpoint || imageConfig.TimeoutSeconds != sharedAPI.TimeoutSeconds {
		t.Fatalf("image operation config = %#v, want shared %#v", imageConfig, sharedAPI)
	}

	_, _, videoConfig, err := cfg.ResolveGenerationTarget(GenerationTextToVideo, GenerationModelTarget{})
	if err != nil {
		t.Fatalf("resolve text-to-video: %v", err)
	}
	if videoConfig.Endpoint != "/contents/generations/tasks" || videoConfig.QueryEndpoint != "/contents/generations/tasks/{id}" || videoConfig.TimeoutSeconds != 600 {
		t.Fatalf("video operation config = %#v", videoConfig)
	}
	if videoConfig.DefaultParams["quality"] != "uhd" || videoConfig.DefaultParams["seed"] != float64(1234) {
		t.Fatalf("video default params = %#v, want shared params with override", videoConfig.DefaultParams)
	}
}

func TestResolveGenerationTargetOperationEndpointOverrideCanClearSharedQueryEndpoint(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{Providers: []ProviderConfig{{
			Name: "mixed", ProviderID: "volcengine", BaseURL: "https://ark.example/api/v3",
			Models: []ModelConfig{{
				Name: "omni", Type: ModelTypeMediaGeneration,
				Generation: &MediaGenerationModelConfig{
					API: &GenerationOperationConfig{
						Endpoint:       "/contents/generations/tasks",
						QueryEndpoint:  "/contents/generations/tasks/{id}",
						TimeoutSeconds: 600,
					},
					Operations: map[GenerationOperation]GenerationOperationConfig{
						GenerationTextToVideo: {},
						GenerationTextToImage: {Endpoint: "/images/generations", TimeoutSeconds: 120},
					},
				},
			}},
		}}},
		Generation: GenerationConfig{Defaults: map[GenerationOperation]GenerationModelTarget{
			GenerationTextToImage: {Provider: "mixed", Model: "omni"},
		}},
	}

	_, _, imageConfig, err := cfg.ResolveGenerationTarget(GenerationTextToImage, GenerationModelTarget{})
	if err != nil {
		t.Fatalf("resolve text-to-image: %v", err)
	}
	if imageConfig.Endpoint != "/images/generations" || imageConfig.QueryEndpoint != "" || imageConfig.TimeoutSeconds != 120 {
		t.Fatalf("image operation config = %#v, want image endpoint without inherited video query endpoint", imageConfig)
	}
}

func TestNormalizeGenerationKeepsSharedAPIOperationsAsCapabilityMarkers(t *testing.T) {
	cfg := Config{LLM: LLMConfig{Providers: []ProviderConfig{{
		Name: "media", Vendor: "minimax", BaseURL: "https://api.minimax.io",
		Models: []ModelConfig{{
			Name: "video", Type: ModelTypeMediaGeneration,
			Generation: &MediaGenerationModelConfig{
				API: &GenerationOperationConfig{Endpoint: "/v1/video_generation", TimeoutSeconds: 600},
				Operations: map[GenerationOperation]GenerationOperationConfig{
					GenerationTextToVideo:  {},
					GenerationImageToVideo: {},
				},
			},
		}},
	}}}}

	cfg.NormalizeGeneration()
	model := cfg.LLM.Providers[0].Models[0]
	for operation, marker := range model.Generation.Operations {
		if marker.Endpoint != "" || marker.QueryEndpoint != "" || marker.TimeoutSeconds != 0 || marker.DefaultParams != nil {
			t.Fatalf("operation %s was expanded instead of remaining a capability marker: %#v", operation, marker)
		}
	}
}

func TestResolveGenerationTargetRejectsMissingCustomEndpoint(t *testing.T) {
	cfg := Config{LLM: LLMConfig{Providers: []ProviderConfig{{
		Name: "custom", Vendor: "custom", Models: []ModelConfig{{
			Name: "video", Type: ModelTypeMediaGeneration,
			Generation: &MediaGenerationModelConfig{Operations: map[GenerationOperation]GenerationOperationConfig{
				GenerationTextToVideo: {},
			}},
		}},
	}}}, Generation: GenerationConfig{Defaults: map[GenerationOperation]GenerationModelTarget{
		GenerationTextToVideo: {Provider: "custom", Model: "video"},
	}}}
	if _, _, _, err := cfg.ResolveGenerationTarget(GenerationTextToVideo, GenerationModelTarget{}); err == nil {
		t.Fatal("custom generation model without base URL or absolute endpoint must be unavailable")
	}
	if models := cfg.GenerationModelsFor(GenerationTextToVideo); len(models) != 0 {
		t.Fatalf("unusable model must not be listed as a selectable candidate: %#v", models)
	}
}

func TestValidateGenerationModelRequiresRelativeSharedEndpoint(t *testing.T) {
	model := ModelConfig{
		Name: "video", Type: ModelTypeMediaGeneration,
		Generation: &MediaGenerationModelConfig{
			API:        &GenerationOperationConfig{Endpoint: "/v1/video_generation"},
			Operations: map[GenerationOperation]GenerationOperationConfig{GenerationTextToVideo: {}},
		},
	}
	if err := ValidateGenerationModel(model); err != nil {
		t.Fatalf("relative generation endpoints rejected: %v", err)
	}
	model.Generation.API.Endpoint = "https://media.example/v3/generate"
	model.Generation.API.QueryEndpoint = "https://media.example/v3/tasks/{task_id}"
	if err := ValidateGenerationModel(model); err == nil {
		t.Fatal("absolute generation endpoints must be rejected for new configuration")
	}

	model.Generation.API.Endpoint = "/v3/contents/generations/tasks"
	model.Generation.API.QueryEndpoint = "/v3/contents/generations/tasks/static"
	if err := ValidateGenerationModel(model); err == nil || !strings.Contains(err.Error(), "{task_id} or {id}") {
		t.Fatalf("query endpoint without a task placeholder should be rejected, got %v", err)
	}
	model.Generation.API.QueryEndpoint = "/v3/contents/generations/tasks/{id}"
	if err := ValidateGenerationModel(model); err != nil {
		t.Fatalf("documented {id} task placeholder rejected: %v", err)
	}

	model.Generation.Operations[GenerationTextToVideo] = GenerationOperationConfig{
		Endpoint:      "https://media.example/v3/absolute",
		QueryEndpoint: "/v3/contents/generations/tasks/{id}",
	}
	if err := ValidateGenerationModel(model); err == nil {
		t.Fatal("absolute per-operation endpoint must be rejected for new configuration")
	}
	model.Generation.Operations[GenerationTextToVideo] = GenerationOperationConfig{
		Endpoint:      "/v3/contents/generations/tasks",
		QueryEndpoint: "/v3/contents/generations/tasks/static",
	}
	if err := ValidateGenerationModel(model); err == nil || !strings.Contains(err.Error(), "operations.text_to_video.query_endpoint") {
		t.Fatalf("per-operation query endpoint without placeholder should be rejected, got %v", err)
	}
}

func TestNormalizeMediaGenerationModelFillsEditableDefaultEndpoint(t *testing.T) {
	model := ModelConfig{
		Name: "image", Type: ModelTypeMediaGeneration,
		Generation: &MediaGenerationModelConfig{
			API:        &GenerationOperationConfig{},
			Operations: map[GenerationOperation]GenerationOperationConfig{GenerationTextToImage: {}},
		},
	}
	if err := normalizeModelAPIConfig("openai", &model); err != nil {
		t.Fatalf("normalize media model: %v", err)
	}
	if got := model.Generation.API.Endpoint; got != DefaultMediaGenerationAPIEndpoint {
		t.Fatalf("media endpoint = %q, want %q", got, DefaultMediaGenerationAPIEndpoint)
	}
}
