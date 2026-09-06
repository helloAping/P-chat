package config

import "testing"

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

func TestDefaultGenerationEndpointPresets(t *testing.T) {
	cases := []struct {
		vendor string
		op     GenerationOperation
		want   string
	}{
		{"volcengine", GenerationTextToImage, "/images/generations"},
		{"volcengine", GenerationImageToVideo, "/contents/generations/tasks"},
		{"minimax", GenerationTextToImage, "/v1/image_generation"},
		{"minimax", GenerationTextToVideo, "/v1/video_generation"},
		{"minimax", GenerationTextToSpeech, "/v1/t2a_v2"},
	}
	for _, tc := range cases {
		if got := DefaultGenerationEndpoint(tc.vendor, tc.op); got != tc.want {
			t.Fatalf("DefaultGenerationEndpoint(%q, %q) = %q, want %q", tc.vendor, tc.op, got, tc.want)
		}
	}
	if got := DefaultGenerationEndpoint("openai", GenerationImageToImage); got != "" {
		t.Fatalf("OpenAI image-to-image requires multipart and must not use the generation preset, got %q", got)
	}
	if got := DefaultGenerationEndpoint("volcengine", GenerationVideoToVideo); got != "" {
		t.Fatalf("Volcengine video-to-video has no built-in adapter preset, got %q", got)
	}
	if got := DefaultGenerationEndpoint("minimax", GenerationVideoToVideo); got != "" {
		t.Fatalf("MiniMax video-to-video has no built-in adapter preset, got %q", got)
	}
	if got := DefaultGenerationQueryEndpoint("minimax", GenerationTextToVideo); got != "/v1/query/video_generation?task_id={task_id}" {
		t.Fatalf("MiniMax video query endpoint = %q", got)
	}
}

func TestModelAdapterDrivesEndpointPresetForCustomProvider(t *testing.T) {
	cfg := Config{LLM: LLMConfig{Providers: []ProviderConfig{{
		Name: "custom-account", Vendor: "custom", BaseURL: "https://api.minimax.io",
		Models: []ModelConfig{{
			Name: "video", Type: ModelTypeMediaGeneration,
			Generation: &MediaGenerationModelConfig{Adapter: "minimax", Operations: map[GenerationOperation]GenerationOperationConfig{
				GenerationTextToVideo: {},
			}},
		}},
	}}}, Generation: GenerationConfig{Defaults: map[GenerationOperation]GenerationModelTarget{
		GenerationTextToVideo: {Provider: "custom-account", Model: "video"},
	}}}
	_, _, operationConfig, err := cfg.ResolveGenerationTarget(GenerationTextToVideo, GenerationModelTarget{})
	if err != nil {
		t.Fatal(err)
	}
	if operationConfig.Endpoint != "/v1/video_generation" {
		t.Fatalf("adapter endpoint preset = %q", operationConfig.Endpoint)
	}
}
