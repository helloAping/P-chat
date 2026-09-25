package config

import "testing"

func TestCapabilitiesSupportsInputModality(t *testing.T) {
	modalities := []MediaKind{MediaImage, MediaAudio}
	caps := Capabilities{InputModalities: &modalities}

	if !caps.SupportsInput(MediaImage) {
		t.Fatal("image input should be supported")
	}
	if !caps.SupportsInput(MediaAudio) {
		t.Fatal("audio input should be supported")
	}
	if caps.SupportsInput(MediaVideo) {
		t.Fatal("video input should not be supported")
	}
}

func TestCapabilitiesExplicitEmptyModalitiesDisableLegacyHeuristics(t *testing.T) {
	empty := []MediaKind{}
	caps := Capabilities{InputModalities: &empty, SupportsVision: true, SupportsAudio: true}
	if caps.SupportsInput(MediaImage) || caps.SupportsInput(MediaAudio) {
		t.Fatal("an explicit empty capability list must disable media input")
	}
}

func TestCapabilitiesSupportsInputUsesLegacyFlags(t *testing.T) {
	caps := Capabilities{SupportsVision: true, SupportsAudio: true}

	if !caps.SupportsInput(MediaImage) || !caps.SupportsInput(MediaAudio) {
		t.Fatal("legacy vision/audio flags should remain compatible")
	}
}

func TestRecognitionConfigNormalizesLegacyVisionRoute(t *testing.T) {
	cfg := Config{
		Vision: VisionRecognitionConfig{
			Enabled:        true,
			Provider:       "vision-provider",
			Model:          "vision-model",
			TimeoutSeconds: 45,
			MaxImageBytes:  1234,
		},
	}

	cfg.NormalizeRecognition()
	route, ok := cfg.Recognition.Route(MediaImage)
	if !ok {
		t.Fatal("legacy vision config should produce an image route")
	}
	if route.Provider != "vision-provider" || route.Model != "vision-model" {
		t.Fatalf("route = %#v", route)
	}
	if route.TimeoutSeconds != 45 || route.MaxBytes != 1234 {
		t.Fatalf("route limits = %#v", route)
	}
}

func TestRecognitionRouteRequiresCompleteEnabledConfiguration(t *testing.T) {
	cfg := RecognitionConfig{Routes: map[MediaKind]RecognitionRoute{
		MediaImage: {Enabled: true, Provider: "p", Model: "m"},
		MediaVideo: {Enabled: true, Provider: "p"},
		MediaAudio: {Provider: "p", Model: "m"},
	}}

	if _, ok := cfg.Route(MediaImage); !ok {
		t.Fatal("complete image route should be available")
	}
	if _, ok := cfg.Route(MediaVideo); ok {
		t.Fatal("route without model must be unavailable")
	}
	if _, ok := cfg.Route(MediaAudio); ok {
		t.Fatal("disabled route must be unavailable")
	}
}
