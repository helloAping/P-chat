package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// yamlRoot mirrors the JSON Config layout but with yaml field tags.
// It exists purely so the legacy config.yaml can be parsed once
// during migration; nothing else in the codebase should use it.
type yamlRoot struct {
	Server   yamlServerConfig   `yaml:"server"`
	LLM      yamlLLMConfig      `yaml:"llm"`
	Style    yamlStyleConfig    `yaml:"style"`
	Tools    yamlToolsConfig    `yaml:"tools"`
	Memory   yamlMemoryConfig   `yaml:"memory"`
	Sandbox  yamlSandboxConfig  `yaml:"sandbox"`
	SubAgent yamlSubAgentConfig `yaml:"subagent"`
}

type yamlServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type yamlLLMConfig struct {
	Default     string             `yaml:"default"`
	Providers   []yamlProviderCfg  `yaml:"providers"`
	Temperature float64            `yaml:"temperature,omitempty"`
	TopP        float64            `yaml:"top_p,omitempty"`
	MaxTokens   int                `yaml:"max_tokens,omitempty"`
	Output      yamlOutputConfig   `yaml:"output"`
}

type yamlOutputConfig struct {
	Language string `yaml:"language,omitempty"`
	Verbose  bool   `yaml:"verbose,omitempty"`
}

type yamlProviderCfg struct {
	Name     string        `yaml:"name"`
	Protocol string        `yaml:"protocol"`
	Type     string        `yaml:"type"`
	BaseURL  string        `yaml:"base_url"`
	APIKey   string        `yaml:"api_key"`
	Model    string        `yaml:"model"`
	Models   []yamlModel   `yaml:"models,omitempty"`
}

type yamlModel struct {
	Name             string         `yaml:"name"`
	DisplayName      string         `yaml:"display_name,omitempty"`
	Default          bool           `yaml:"default,omitempty"`
	Description      string         `yaml:"description,omitempty"`
	MaxTokensContext int            `yaml:"max_tokens_context,omitempty"`
	MaxTokensOutput  int            `yaml:"max_tokens_output,omitempty"`
	Capabilities     yamlCapability `yaml:"capabilities,omitempty"`
}

type yamlCapability struct {
	ThinkingEffort string `yaml:"thinking_effort,omitempty"`
	ContextWindow  int    `yaml:"context_window,omitempty"`
	SupportsVision bool   `yaml:"supports_vision,omitempty"`
	SupportsAudio  bool   `yaml:"supports_audio,omitempty"`
}

type yamlStyleConfig struct {
	Default string `yaml:"default"`
}

type yamlToolsConfig struct {
	Enabled []string         `yaml:"enabled"`
	Servers []yamlToolServer `yaml:"servers"`
}

type yamlToolServer struct {
	Name    string   `yaml:"name"`
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
}

type yamlMemoryConfig struct {
	Enabled    bool `yaml:"enabled"`
	MaxHistory int  `yaml:"max_history"`
}

type yamlSandboxConfig struct {
	Enabled               bool     `yaml:"enabled"`
	RequireConfirm        string   `yaml:"require_confirm,omitempty"`
	WriteProtectedPaths   []string `yaml:"write_protected_paths,omitempty"`
	ExecDangerousPatterns []string `yaml:"exec_dangerous_patterns,omitempty"`
	MaxCommandLength      int      `yaml:"max_command_length,omitempty"`
}

type yamlSubAgentConfig struct {
	AllowedTools []string `yaml:"allowed_tools,omitempty"`
	DeniedTools  []string `yaml:"denied_tools,omitempty"`
	Timeout      string   `yaml:"timeout,omitempty"`
	CacheTTL     string   `yaml:"cache_ttl,omitempty"`
}

// unmarshalYAML decodes legacy YAML bytes into the Config struct.
// It uses an intermediate yamlRoot so any future fields the user
// added in their yaml (and which we don't yet support) are
// silently dropped instead of failing the parse.
func unmarshalYAML(data []byte, cfg *Config) error {
	var y yamlRoot
	if err := yaml.Unmarshal(data, &y); err != nil {
		return fmt.Errorf("parse legacy yaml: %w", err)
	}

	// Re-marshal through a thin Config-shaped value to ensure the
	// JSON output is structurally identical to a fresh Default().
	converted := Config{
		Server: ServerConfig{
			Host: y.Server.Host,
			Port: y.Server.Port,
		},
		LLM: LLMConfig{
			Default:     y.LLM.Default,
			Temperature: y.LLM.Temperature,
			TopP:        y.LLM.TopP,
			MaxTokens:   y.LLM.MaxTokens,
			Output: OutputConfig{
				Language: y.LLM.Output.Language,
				Verbose:  y.LLM.Output.Verbose,
			},
		},
		Style: StyleConfig{
			Default: y.Style.Default,
		},
		Tools: ToolsConfig{
			Enabled: y.Tools.Enabled,
		},
		Memory: MemoryConfig{
			Enabled:    y.Memory.Enabled,
			MaxHistory: y.Memory.MaxHistory,
		},
		Sandbox: SandboxConfig{
			Enabled:               y.Sandbox.Enabled,
			RequireConfirm:        y.Sandbox.RequireConfirm,
			WriteProtectedPaths:   y.Sandbox.WriteProtectedPaths,
			ExecDangerousPatterns: y.Sandbox.ExecDangerousPatterns,
			MaxCommandLength:      y.Sandbox.MaxCommandLength,
		},
		SubAgent: SubAgentConfig{
			AllowedTools: y.SubAgent.AllowedTools,
			DeniedTools:  y.SubAgent.DeniedTools,
			Timeout:      y.SubAgent.Timeout,
			CacheTTL:     y.SubAgent.CacheTTL,
		},
	}

	for _, p := range y.LLM.Providers {
		pp := ProviderConfig{
			Name:     p.Name,
			Protocol: p.Protocol,
			Type:     p.Type,
			BaseURL:  p.BaseURL,
			APIKey:   p.APIKey,
			Model:    p.Model,
		}
		for _, m := range p.Models {
			pp.Models = append(pp.Models, ModelConfig{
				Name:             m.Name,
				DisplayName:      m.DisplayName,
				Default:          m.Default,
				Description:      m.Description,
				MaxTokensContext: m.MaxTokensContext,
				MaxTokensOutput:  m.MaxTokensOutput,
				Capabilities: Capabilities{
					ThinkingEffort: ThinkingEffort(m.Capabilities.ThinkingEffort),
					ContextWindow:  m.Capabilities.ContextWindow,
					SupportsVision: m.Capabilities.SupportsVision,
					SupportsAudio:  m.Capabilities.SupportsAudio,
				},
			})
		}
		converted.LLM.Providers = append(converted.LLM.Providers, pp)
	}

	for _, ts := range y.Tools.Servers {
		converted.Tools.Servers = append(converted.Tools.Servers, ToolServerConfig{
			Name:    ts.Name,
			Command: ts.Command,
			Args:    ts.Args,
		})
	}

	*cfg = converted
	return nil
}
