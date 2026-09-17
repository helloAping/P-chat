package provider

import "testing"

func TestPresetsIncludesCustomResponsesDefaults(t *testing.T) {
	preset, ok := Get("custom")
	if !ok {
		t.Fatal("custom preset not found")
	}
	if preset.ID != "custom" {
		t.Fatalf("preset id = %q", preset.ID)
	}
	var responses *ProtocolPreset
	for i := range preset.Protocols {
		if preset.Protocols[i].ID == ProtocolOpenAIResponses {
			responses = &preset.Protocols[i]
			break
		}
	}
	if responses == nil {
		t.Fatal("custom OpenAI Responses protocol not found")
	}
	if responses.EndpointDefaults.OpenAIResponses != "/responses" {
		t.Fatalf("OpenAIResponses endpoint = %q", responses.EndpointDefaults.OpenAIResponses)
	}
	if responses.EndpointDefaults.ImageGeneration != "/responses" {
		t.Fatalf("ImageGeneration endpoint = %q", responses.EndpointDefaults.ImageGeneration)
	}
}

func TestPresetsIncludeProviderVariants(t *testing.T) {
	volcengine, ok := Get("volcengine")
	if !ok {
		t.Fatal("volcengine preset not found")
	}
	var coding *StrategyVariant
	for i := range volcengine.Variants {
		if volcengine.Variants[i].ID == "coding_plan" {
			coding = &volcengine.Variants[i]
			break
		}
	}
	if coding == nil {
		t.Fatalf("volcengine variants = %#v", volcengine.Variants)
	}
	if got := coding.DefaultBaseURLs[ProtocolOpenAIChat]; got != "https://ark.cn-beijing.volces.com/api/coding/v3" {
		t.Fatalf("coding OpenAI base URL = %q", got)
	}
	if got := coding.DefaultBaseURLs[ProtocolAnthropicMessages]; got != "https://ark.cn-beijing.volces.com/api/coding" {
		t.Fatalf("coding Anthropic base URL = %q", got)
	}

	minimax, ok := Get("minimax")
	if !ok {
		t.Fatal("minimax preset not found")
	}
	if minimax.DefaultVariant != "global" || len(minimax.Variants) < 2 {
		t.Fatalf("minimax variants = %#v", minimax.Variants)
	}

	mimo, ok := Get("mimo")
	if !ok {
		t.Fatal("mimo preset not found")
	}
	if mimo.DefaultVariant != "payg" || len(mimo.Variants) != 2 {
		t.Fatalf("mimo variants = %#v", mimo.Variants)
	}
	if got := mimo.Variants[0].DefaultBaseURLs[ProtocolOpenAIChat]; got != "https://api.xiaomimimo.com/v1" {
		t.Fatalf("mimo OpenAI base URL = %q", got)
	}
	if got := mimo.Variants[1].DefaultBaseURLs[ProtocolAnthropicMessages]; got != "https://token-plan-cn.xiaomimimo.com/anthropic" {
		t.Fatalf("mimo Token Plan Anthropic base URL = %q", got)
	}
}

func TestPresetsIncludeMediaEndpointDefaults(t *testing.T) {
	minimax, ok := Get("minimax")
	if !ok {
		t.Fatal("minimax preset not found")
	}
	if got := minimax.Protocols[0].EndpointDefaults.VideoGeneration; got != "/v2/video_generation" {
		t.Fatalf("minimax video endpoint = %q", got)
	}
	if got := minimax.Protocols[0].EndpointDefaults.AudioGeneration; got != "/v1/t2a_v2" {
		t.Fatalf("minimax audio endpoint = %q", got)
	}

	volcengine, ok := Get("volcengine")
	if !ok {
		t.Fatal("volcengine preset not found")
	}
	if got := volcengine.Protocols[0].EndpointDefaults.VideoGeneration; got != "/contents/generations/tasks" {
		t.Fatalf("volcengine video endpoint = %q", got)
	}
	if got := volcengine.Protocols[0].EndpointDefaults.VideoTaskQuery; got != "/contents/generations/tasks/{task_id}" {
		t.Fatalf("volcengine video query endpoint = %q", got)
	}

	kling, ok := Get("kling")
	if !ok {
		t.Fatal("kling preset not found")
	}
	if got := kling.Protocols[0].EndpointDefaults.VideoGeneration; got != "/v1/videos/text2video" {
		t.Fatalf("kling video endpoint = %q", got)
	}
	if got := kling.Protocols[0].EndpointDefaults.VideoTaskQuery; got != "/v1/videos/text2video/{task_id}" {
		t.Fatalf("kling video query endpoint = %q", got)
	}
}

func TestGenerationAdapterUsesProviderStrategyBeforeProtocol(t *testing.T) {
	cases := []struct {
		name string
		req  GenerationAdapterRequest
		want string
	}{
		{
			name: "volcengine openai protocol",
			req:  GenerationAdapterRequest{ProviderID: "volcengine", Protocol: string(ProtocolOpenAIChat), ConfiguredAdapter: "openai"},
			want: "volcengine",
		},
		{
			name: "minimax openai protocol",
			req:  GenerationAdapterRequest{ProviderID: "minimax", Protocol: string(ProtocolOpenAIChat), ConfiguredAdapter: "openai"},
			want: "minimax",
		},
		{
			name: "kling openai protocol",
			req:  GenerationAdapterRequest{ProviderID: "kling", Protocol: string(ProtocolOpenAIChat), ConfiguredAdapter: "openai"},
			want: "kling",
		},
		{
			name: "custom anthropic protocol",
			req:  GenerationAdapterRequest{ProviderID: "custom", Protocol: string(ProtocolAnthropicMessages)},
			want: "anthropic",
		},
		{
			name: "explicit adapter override",
			req:  GenerationAdapterRequest{ProviderID: "volcengine", Protocol: string(ProtocolOpenAIChat), ConfiguredAdapter: "minimax"},
			want: "minimax",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GenerationAdapter(tc.req); got != tc.want {
				t.Fatalf("GenerationAdapter() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPresetsReturnsDefensiveCopy(t *testing.T) {
	first := Presets()
	if len(first) == 0 || len(first[0].Protocols) == 0 {
		t.Fatal("presets unexpectedly empty")
	}
	first[0].ID = "mutated"
	first[0].Protocols[0].DisplayName = "mutated"
	if len(first[0].ModelList.Fallback) > 0 {
		first[0].ModelList.Fallback[0].ID = "mutated"
	}
	volcengine, ok := Get("volcengine")
	if !ok {
		t.Fatal("volcengine preset not found")
	}
	volcengine.Variants[1].DefaultBaseURLs[ProtocolOpenAIChat] = "mutated"

	second := Presets()
	if second[0].ID == "mutated" {
		t.Fatal("preset id was mutated through returned slice")
	}
	if second[0].Protocols[0].DisplayName == "mutated" {
		t.Fatal("protocol preset was mutated through returned slice")
	}
	volcengineAgain, ok := Get("volcengine")
	if !ok {
		t.Fatal("volcengine preset not found")
	}
	if got := volcengineAgain.Variants[1].DefaultBaseURLs[ProtocolOpenAIChat]; got == "mutated" {
		t.Fatal("variant base URL was mutated through returned preset")
	}
}
