// Package provider 描述设置页使用的供应商策略预设。
// Package provider describes provider strategy presets used by the settings UI.
//
// LLM 与媒体生成运行时仍由 llm/generation 执行，供应商差异通过这里的策略元数据进入分发。
// LLM and media generation still execute in llm/generation; provider-specific
// differences enter dispatch through this strategy metadata.
package provider

import "strings"

// ProtocolID 是新设置页保存的稳定请求协议标识。
// ProtocolID is the stable wire-protocol identifier stored by new settings UI.
type ProtocolID string

const (
	// ProtocolOpenAIChat 表示 OpenAI-compatible Chat Completions 协议。
	// ProtocolOpenAIChat is the OpenAI-compatible Chat Completions protocol.
	ProtocolOpenAIChat ProtocolID = "openai_chat"
	// ProtocolOpenAIResponses 表示 OpenAI Responses 协议。
	// ProtocolOpenAIResponses is the OpenAI Responses protocol.
	ProtocolOpenAIResponses ProtocolID = "openai_responses"
	// ProtocolAnthropicMessages 表示 Anthropic Messages 协议。
	// ProtocolAnthropicMessages is the Anthropic Messages protocol.
	ProtocolAnthropicMessages ProtocolID = "anthropic_messages"
)

// Capability 是预设中展示的供应商粗粒度能力。
// Capability is a coarse provider capability shown in presets.
type Capability string

const (
	CapabilityChat            Capability = "chat"
	CapabilityModels          Capability = "models"
	CapabilityImageGeneration Capability = "image_generation"
	CapabilityVideoGeneration Capability = "video_generation"
	CapabilityAudioGeneration Capability = "audio_generation"
)

// EndpointDefaults 是策略提供的默认 API 端点后缀；空值表示暂不声明该能力。
// EndpointDefaults are strategy-provided endpoint suffixes. Empty values mean
// the preset does not claim that capability yet.
type EndpointDefaults struct {
	ModelList         string `json:"model_list,omitempty"`
	OpenAIChat        string `json:"openai_chat,omitempty"`
	OpenAIResponses   string `json:"openai_responses,omitempty"`
	AnthropicMessages string `json:"anthropic_messages,omitempty"`
	ImageGeneration   string `json:"image_generation,omitempty"`
	VideoGeneration   string `json:"video_generation,omitempty"`
	AudioGeneration   string `json:"audio_generation,omitempty"`
	ImageTaskQuery    string `json:"image_task_query,omitempty"`
	VideoTaskQuery    string `json:"video_task_query,omitempty"`
	AudioTaskQuery    string `json:"audio_task_query,omitempty"`
}

// ProtocolPreset 描述一个供应商如何使用一种 LLM 请求协议。
// ProtocolPreset describes how one provider speaks one LLM protocol.
type ProtocolPreset struct {
	ID               ProtocolID       `json:"id"`
	DisplayName      string           `json:"display_name"`
	DefaultBaseURL   string           `json:"default_base_url,omitempty"`
	EndpointDefaults EndpointDefaults `json:"endpoint_defaults"`
	DisabledReason   string           `json:"disabled_reason,omitempty"`
}

// StrategyVariant 区分同一供应商下的 API 产品或套餐。
// StrategyVariant separates API products or plans under one provider.
type StrategyVariant struct {
	ID              string                `json:"id"`
	DisplayName     string                `json:"display_name"`
	Description     string                `json:"description,omitempty"`
	DefaultBaseURLs map[ProtocolID]string `json:"default_base_urls,omitempty"`
}

// ModelPreset 是策略在无法远程列出模型时提供的静态候选。
// ModelPreset is a static model candidate used when remote listing is
// unavailable or not supported by a provider strategy.
type ModelPreset struct {
	ID              string       `json:"id"`
	DisplayName     string       `json:"display_name,omitempty"`
	OwnedBy         string       `json:"owned_by,omitempty"`
	DefaultEndpoint string       `json:"default_endpoint,omitempty"`
	Capabilities    []Capability `json:"capabilities,omitempty"`
}

// ModelListPreset 告诉 UI 模型获取是远程、静态还是手动优先。
// ModelListPreset tells the UI whether model discovery is remote, static, or
// manual-first for a provider strategy.
type ModelListPreset struct {
	Mode            string        `json:"mode"`
	DefaultEndpoint string        `json:"default_endpoint,omitempty"`
	Fallback        []ModelPreset `json:"fallback,omitempty"`
}

// ProviderPreset 是 GET /provider-presets 的稳定响应结构。
// ProviderPreset is the stable response shape for GET /provider-presets.
type ProviderPreset struct {
	ID             string            `json:"id"`
	DisplayName    string            `json:"display_name"`
	Description    string            `json:"description,omitempty"`
	DefaultVariant string            `json:"default_variant,omitempty"`
	Variants       []StrategyVariant `json:"variants,omitempty"`
	Protocols      []ProtocolPreset  `json:"protocols"`
	Capabilities   []Capability      `json:"capabilities,omitempty"`
	ModelList      ModelListPreset   `json:"model_list"`
}

var defaultPresets = []ProviderPreset{
	{
		ID:          "custom",
		DisplayName: "Custom",
		Description: "自定义供应商；Base URL 由用户填写，端点按协议默认带入。",
		Protocols: []ProtocolPreset{
			{
				ID:          ProtocolOpenAIChat,
				DisplayName: "OpenAI Chat",
				EndpointDefaults: EndpointDefaults{
					ModelList:       "/models",
					OpenAIChat:      "/chat/completions",
					ImageGeneration: "/images/generations",
					VideoGeneration: "/videos/generations",
					AudioGeneration: "/audio/speech",
				},
			},
			{
				ID:          ProtocolOpenAIResponses,
				DisplayName: "OpenAI Responses",
				EndpointDefaults: EndpointDefaults{
					ModelList:       "/models",
					OpenAIResponses: "/responses",
					ImageGeneration: "/responses",
					VideoGeneration: "/responses",
					AudioGeneration: "/responses",
					ImageTaskQuery:  "/responses/{task_id}",
					VideoTaskQuery:  "/responses/{task_id}",
					AudioTaskQuery:  "/responses/{task_id}",
				},
			},
			{
				ID:          ProtocolAnthropicMessages,
				DisplayName: "Anthropic Messages",
				EndpointDefaults: EndpointDefaults{
					ModelList:         "/models",
					AnthropicMessages: "/messages",
					ImageGeneration:   "/messages",
					VideoGeneration:   "/messages",
					AudioGeneration:   "/messages",
				},
			},
		},
		Capabilities: []Capability{
			CapabilityChat,
			CapabilityModels,
			CapabilityImageGeneration,
			CapabilityVideoGeneration,
			CapabilityAudioGeneration,
		},
		ModelList: ModelListPreset{Mode: "remote_then_manual", DefaultEndpoint: "/models"},
	},
	{
		ID:          "openai",
		DisplayName: "OpenAI",
		Description: "OpenAI 官方 API；首批先声明对话协议与模型列表。",
		Protocols: []ProtocolPreset{
			{
				ID:             ProtocolOpenAIChat,
				DisplayName:    "OpenAI Chat",
				DefaultBaseURL: "https://api.openai.com/v1",
				EndpointDefaults: EndpointDefaults{
					ModelList:  "/models",
					OpenAIChat: "/chat/completions",
				},
			},
			{
				ID:             ProtocolOpenAIResponses,
				DisplayName:    "OpenAI Responses",
				DefaultBaseURL: "https://api.openai.com/v1",
				EndpointDefaults: EndpointDefaults{
					ModelList:       "/models",
					OpenAIResponses: "/responses",
				},
			},
		},
		Capabilities: []Capability{CapabilityChat, CapabilityModels},
		ModelList:    ModelListPreset{Mode: "remote", DefaultEndpoint: "/models"},
	},
	{
		ID:          "anthropic",
		DisplayName: "Claude",
		Description: "Anthropic Claude 官方 Messages API。",
		Protocols: []ProtocolPreset{
			{
				ID:             ProtocolAnthropicMessages,
				DisplayName:    "Anthropic Messages",
				DefaultBaseURL: "https://api.anthropic.com/v1",
				EndpointDefaults: EndpointDefaults{
					ModelList:         "/models",
					AnthropicMessages: "/messages",
				},
			},
		},
		Capabilities: []Capability{CapabilityChat, CapabilityModels},
		ModelList:    ModelListPreset{Mode: "remote", DefaultEndpoint: "/models"},
	},
	{
		ID:          "deepseek",
		DisplayName: "DeepSeek",
		Description: "DeepSeek 官方 OpenAI / Anthropic 兼容 API。",
		Protocols: []ProtocolPreset{
			{
				ID:             ProtocolOpenAIChat,
				DisplayName:    "OpenAI Chat",
				DefaultBaseURL: "https://api.deepseek.com",
				EndpointDefaults: EndpointDefaults{
					ModelList:  "/models",
					OpenAIChat: "/chat/completions",
				},
			},
			{
				ID:             ProtocolAnthropicMessages,
				DisplayName:    "Anthropic Messages",
				DefaultBaseURL: "https://api.deepseek.com/anthropic/v1",
				EndpointDefaults: EndpointDefaults{
					ModelList:         "/models",
					AnthropicMessages: "/messages",
				},
			},
		},
		Capabilities: []Capability{CapabilityChat, CapabilityModels},
		ModelList: ModelListPreset{
			Mode:            "remote_then_static",
			DefaultEndpoint: "/models",
			Fallback: []ModelPreset{
				{ID: "deepseek-v4-flash", DisplayName: "DeepSeek V4 Flash", OwnedBy: "deepseek", Capabilities: []Capability{CapabilityChat}},
				{ID: "deepseek-v4-pro", DisplayName: "DeepSeek V4 Pro", OwnedBy: "deepseek", Capabilities: []Capability{CapabilityChat}},
				{ID: "deepseek-v4-flash-vision-exp", DisplayName: "DeepSeek V4 Flash Vision", OwnedBy: "deepseek", Capabilities: []Capability{CapabilityChat}},
			},
		},
	},
	{
		ID:             "minimax",
		DisplayName:    "MiniMax",
		Description:    "MiniMax OpenAI 兼容文本 API；区域由策略变体区分。",
		DefaultVariant: "global",
		Variants: []StrategyVariant{
			{
				ID:          "global",
				DisplayName: "Global",
				Description: "国际站 Pay-as-you-go API。",
				DefaultBaseURLs: map[ProtocolID]string{
					ProtocolOpenAIChat: "https://api.minimax.io/v1",
				},
			},
			{
				ID:          "cn",
				DisplayName: "中国站",
				Description: "中国站 MiniMax API。",
				DefaultBaseURLs: map[ProtocolID]string{
					ProtocolOpenAIChat: "https://api.minimaxi.com/v1",
				},
			},
		},
		Protocols: []ProtocolPreset{
			{
				ID:          ProtocolOpenAIChat,
				DisplayName: "OpenAI Chat",
				EndpointDefaults: EndpointDefaults{
					ModelList:       "/models",
					OpenAIChat:      "/chat/completions",
					ImageGeneration: "/v1/image_generation",
					VideoGeneration: "/v2/video_generation",
					AudioGeneration: "/v1/t2a_v2",
					VideoTaskQuery:  "/v2/video_generation/{task_id}",
				},
			},
		},
		Capabilities: []Capability{CapabilityChat, CapabilityModels, CapabilityVideoGeneration, CapabilityAudioGeneration, CapabilityImageGeneration},
		ModelList: ModelListPreset{
			Mode:            "remote_then_static",
			DefaultEndpoint: "/models",
			Fallback: []ModelPreset{
				{ID: "MiniMax-M2.7", DisplayName: "MiniMax M2.7", OwnedBy: "minimax", Capabilities: []Capability{CapabilityChat}},
				{ID: "MiniMax-M2.7-highspeed", DisplayName: "MiniMax M2.7 Highspeed", OwnedBy: "minimax", Capabilities: []Capability{CapabilityChat}},
				{ID: "MiniMax-M2.5", DisplayName: "MiniMax M2.5", OwnedBy: "minimax", Capabilities: []Capability{CapabilityChat}},
			},
		},
	},
	{
		ID:             "zhipu",
		DisplayName:    "智谱 GLM",
		Description:    "智谱 BigModel OpenAI 兼容 API；普通 API 与 Coding Plan 使用独立入口。",
		DefaultVariant: "api",
		Variants: []StrategyVariant{
			{
				ID:          "api",
				DisplayName: "普通 API",
				DefaultBaseURLs: map[ProtocolID]string{
					ProtocolOpenAIChat: "https://open.bigmodel.cn/api/paas/v4",
				},
			},
			{
				ID:          "coding_plan",
				DisplayName: "Coding Plan",
				Description: "GLM Coding 专用入口。",
				DefaultBaseURLs: map[ProtocolID]string{
					ProtocolOpenAIChat: "https://open.bigmodel.cn/api/coding/paas/v4",
				},
			},
		},
		Protocols: []ProtocolPreset{
			{
				ID:          ProtocolOpenAIChat,
				DisplayName: "OpenAI Chat",
				EndpointDefaults: EndpointDefaults{
					ModelList:  "/models",
					OpenAIChat: "/chat/completions",
				},
			},
		},
		Capabilities: []Capability{CapabilityChat, CapabilityModels},
		ModelList: ModelListPreset{
			Mode:            "remote_then_static",
			DefaultEndpoint: "/models",
			Fallback: []ModelPreset{
				{ID: "glm-5.2", DisplayName: "GLM 5.2", OwnedBy: "zhipu", Capabilities: []Capability{CapabilityChat}},
			},
		},
	},
	{
		ID:             "mimo",
		DisplayName:    "MiMo",
		Description:    "小米 MiMo 官方 OpenAI / Anthropic 兼容 API；普通 API 与 Token Plan 使用独立入口。",
		DefaultVariant: "payg",
		Variants: []StrategyVariant{
			{
				ID:          "payg",
				DisplayName: "普通 API",
				Description: "按量计费 API Key。",
				DefaultBaseURLs: map[ProtocolID]string{
					ProtocolOpenAIChat:        "https://api.xiaomimimo.com/v1",
					ProtocolAnthropicMessages: "https://api.xiaomimimo.com/anthropic",
				},
			},
			{
				ID:          "token_plan",
				DisplayName: "Token Plan",
				Description: "订阅套餐专属入口；不要与普通 API Key 混用。",
				DefaultBaseURLs: map[ProtocolID]string{
					ProtocolOpenAIChat:        "https://token-plan-cn.xiaomimimo.com/v1",
					ProtocolAnthropicMessages: "https://token-plan-cn.xiaomimimo.com/anthropic",
				},
			},
		},
		Protocols: []ProtocolPreset{
			{
				ID:          ProtocolOpenAIChat,
				DisplayName: "OpenAI Chat",
				EndpointDefaults: EndpointDefaults{
					ModelList:  "/models",
					OpenAIChat: "/chat/completions",
				},
			},
			{
				ID:          ProtocolAnthropicMessages,
				DisplayName: "Anthropic Messages",
				EndpointDefaults: EndpointDefaults{
					ModelList:         "/models",
					AnthropicMessages: "/messages",
				},
			},
		},
		Capabilities: []Capability{CapabilityChat, CapabilityModels},
		ModelList: ModelListPreset{
			Mode:            "remote_then_static",
			DefaultEndpoint: "/models",
			Fallback: []ModelPreset{
				{ID: "mimo-v2.5-pro", DisplayName: "MiMo V2.5 Pro", OwnedBy: "mimo", Capabilities: []Capability{CapabilityChat}},
				{ID: "mimo-v2.5", DisplayName: "MiMo V2.5", OwnedBy: "mimo", Capabilities: []Capability{CapabilityChat}},
				{ID: "mimo-v2-pro", DisplayName: "MiMo V2 Pro", OwnedBy: "mimo", Capabilities: []Capability{CapabilityChat}},
				{ID: "mimo-v2-omni", DisplayName: "MiMo V2 Omni", OwnedBy: "mimo", Capabilities: []Capability{CapabilityChat}},
			},
		},
	},
	{
		ID:             "volcengine",
		DisplayName:    "火山方舟",
		Description:    "火山方舟普通 API、Coding Plan、Agent Plan；不同套餐入口互相独立。",
		DefaultVariant: "api",
		Variants: []StrategyVariant{
			{
				ID:          "api",
				DisplayName: "普通 API",
				Description: "方舟通用模型调用入口。",
				DefaultBaseURLs: map[ProtocolID]string{
					ProtocolOpenAIChat:      "https://ark.cn-beijing.volces.com/api/v3",
					ProtocolOpenAIResponses: "https://ark.cn-beijing.volces.com/api/v3",
				},
			},
			{
				ID:          "coding_plan",
				DisplayName: "Coding Plan",
				Description: "Coding Plan 专属入口；不要与普通 API Key 混用。",
				DefaultBaseURLs: map[ProtocolID]string{
					ProtocolOpenAIChat:        "https://ark.cn-beijing.volces.com/api/coding/v3",
					ProtocolOpenAIResponses:   "https://ark.cn-beijing.volces.com/api/coding/v3",
					ProtocolAnthropicMessages: "https://ark.cn-beijing.volces.com/api/coding",
				},
			},
			{
				ID:          "agent_plan",
				DisplayName: "Agent Plan",
				Description: "Agent Plan 专属 OpenAI 兼容入口。",
				DefaultBaseURLs: map[ProtocolID]string{
					ProtocolOpenAIChat:      "https://ark.cn-beijing.volces.com/api/plan/v3",
					ProtocolOpenAIResponses: "https://ark.cn-beijing.volces.com/api/plan/v3",
				},
			},
		},
		Protocols: []ProtocolPreset{
			{
				ID:          ProtocolOpenAIChat,
				DisplayName: "OpenAI Chat",
				EndpointDefaults: EndpointDefaults{
					ModelList:       "/models",
					OpenAIChat:      "/chat/completions",
					ImageGeneration: "/images/generations",
					VideoGeneration: "/contents/generations/tasks",
					VideoTaskQuery:  "/contents/generations/tasks/{task_id}",
				},
			},
			{
				ID:          ProtocolOpenAIResponses,
				DisplayName: "OpenAI Responses",
				EndpointDefaults: EndpointDefaults{
					ModelList:       "/models",
					OpenAIResponses: "/responses",
					ImageGeneration: "/images/generations",
					VideoGeneration: "/contents/generations/tasks",
					VideoTaskQuery:  "/contents/generations/tasks/{task_id}",
				},
			},
			{
				ID:          ProtocolAnthropicMessages,
				DisplayName: "Anthropic Messages",
				EndpointDefaults: EndpointDefaults{
					ModelList:         "/models",
					AnthropicMessages: "/messages",
				},
			},
		},
		Capabilities: []Capability{CapabilityChat, CapabilityModels, CapabilityImageGeneration, CapabilityVideoGeneration},
		ModelList: ModelListPreset{
			Mode:            "remote_then_static",
			DefaultEndpoint: "/models",
			Fallback: []ModelPreset{
				{ID: "doubao-seed-2-0-lite-260215", DisplayName: "Doubao Seed 2.0 Lite", OwnedBy: "volcengine", Capabilities: []Capability{CapabilityChat}},
				{ID: "ark-code-latest", DisplayName: "Ark Code Latest", OwnedBy: "volcengine", Capabilities: []Capability{CapabilityChat}},
			},
		},
	},
	{
		ID:          "gemini",
		DisplayName: "Gemini",
		Description: "Google Gemini OpenAI-compatible endpoint。",
		Protocols: []ProtocolPreset{
			{
				ID:             ProtocolOpenAIChat,
				DisplayName:    "OpenAI Chat",
				DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
				EndpointDefaults: EndpointDefaults{
					ModelList:  "/models",
					OpenAIChat: "/chat/completions",
				},
			},
		},
		Capabilities: []Capability{CapabilityChat, CapabilityModels},
		ModelList:    ModelListPreset{Mode: "remote", DefaultEndpoint: "/models"},
	},
	{
		ID:             "kling",
		DisplayName:    "Kling 可灵",
		Description:    "Kling 官方媒体生成 API；API Key 字段填写官方 Bearer/JWT token。",
		DefaultVariant: "global",
		Variants: []StrategyVariant{
			{
				ID:          "global",
				DisplayName: "Global",
				Description: "Kling 国际站 API。",
				DefaultBaseURLs: map[ProtocolID]string{
					ProtocolOpenAIChat: "https://api-singapore.klingai.com",
				},
			},
		},
		Protocols: []ProtocolPreset{
			{
				ID:          ProtocolOpenAIChat,
				DisplayName: "媒体任务 API",
				EndpointDefaults: EndpointDefaults{
					ImageGeneration: "/v1/images/generations",
					VideoGeneration: "/v1/videos/text2video",
					ImageTaskQuery:  "/v1/images/generations/{task_id}",
					VideoTaskQuery:  "/v1/videos/text2video/{task_id}",
				},
			},
		},
		Capabilities: []Capability{CapabilityImageGeneration, CapabilityVideoGeneration},
		ModelList: ModelListPreset{
			Mode: "static_then_manual",
			Fallback: []ModelPreset{
				{ID: "kling-v1", DisplayName: "Kling v1", OwnedBy: "kling", Capabilities: []Capability{CapabilityVideoGeneration}},
				{ID: "kling-v1-5", DisplayName: "Kling v1.5", OwnedBy: "kling", Capabilities: []Capability{CapabilityVideoGeneration}},
				{ID: "kling-v1-6", DisplayName: "Kling v1.6", OwnedBy: "kling", Capabilities: []Capability{CapabilityVideoGeneration}},
			},
		},
	},
	{
		ID:             "kimi",
		DisplayName:    "Kimi",
		Description:    "Moonshot Kimi OpenAI 兼容 API；国际站与中国站 API Key 体系不同。",
		DefaultVariant: "global",
		Variants: []StrategyVariant{
			{
				ID:          "global",
				DisplayName: "Global",
				DefaultBaseURLs: map[ProtocolID]string{
					ProtocolOpenAIChat: "https://api.moonshot.ai/v1",
				},
			},
			{
				ID:          "cn",
				DisplayName: "中国站",
				DefaultBaseURLs: map[ProtocolID]string{
					ProtocolOpenAIChat: "https://api.moonshot.cn/v1",
				},
			},
		},
		Protocols: []ProtocolPreset{
			{
				ID:          ProtocolOpenAIChat,
				DisplayName: "OpenAI Chat",
				EndpointDefaults: EndpointDefaults{
					ModelList:  "/models",
					OpenAIChat: "/chat/completions",
				},
			},
		},
		Capabilities: []Capability{CapabilityChat, CapabilityModels},
		ModelList:    ModelListPreset{Mode: "remote", DefaultEndpoint: "/models"},
	},
}

// Presets 返回已注册供应商预设的防御性副本。
// Presets returns a defensive copy of the registered provider presets.
func Presets() []ProviderPreset {
	out := make([]ProviderPreset, len(defaultPresets))
	for i := range defaultPresets {
		out[i] = clonePreset(defaultPresets[i])
	}
	return out
}

// Get 按 id 返回一个供应商预设。
// Get returns one provider preset by id.
func Get(id string) (ProviderPreset, bool) {
	id = strings.TrimSpace(strings.ToLower(id))
	for _, preset := range defaultPresets {
		if preset.ID == id {
			return clonePreset(preset), true
		}
	}
	return ProviderPreset{}, false
}

func clonePreset(p ProviderPreset) ProviderPreset {
	if p.Variants != nil {
		p.Variants = append([]StrategyVariant(nil), p.Variants...)
		for i := range p.Variants {
			if p.Variants[i].DefaultBaseURLs != nil {
				p.Variants[i].DefaultBaseURLs = cloneBaseURLs(p.Variants[i].DefaultBaseURLs)
			}
		}
	}
	if p.Protocols != nil {
		p.Protocols = append([]ProtocolPreset(nil), p.Protocols...)
	}
	if p.Capabilities != nil {
		p.Capabilities = append([]Capability(nil), p.Capabilities...)
	}
	if p.ModelList.Fallback != nil {
		p.ModelList.Fallback = append([]ModelPreset(nil), p.ModelList.Fallback...)
		for i := range p.ModelList.Fallback {
			if p.ModelList.Fallback[i].Capabilities != nil {
				p.ModelList.Fallback[i].Capabilities = append([]Capability(nil), p.ModelList.Fallback[i].Capabilities...)
			}
		}
	}
	return p
}

func cloneBaseURLs(in map[ProtocolID]string) map[ProtocolID]string {
	out := make(map[ProtocolID]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
