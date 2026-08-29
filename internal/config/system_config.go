package config

import "fmt"

// LimitsConfigPatch is a partial update for LimitsConfig.
type LimitsConfigPatch struct {
	AutoCompactBuffer    *int             `json:"auto_compact_buffer,omitempty"`
	ToolResultExecCap    *int             `json:"tool_result_exec_cap,omitempty"`
	ToolResultReadCap    *int             `json:"tool_result_read_cap,omitempty"`
	ToolResultDefaultCap *int             `json:"tool_result_default_cap,omitempty"`
	PruneAfterRounds     *int             `json:"prune_after_rounds,omitempty"`
	MaxRounds            *int             `json:"max_rounds,omitempty"`
	TodoLongRunMode      *TodoLongRunMode `json:"todo_long_run_mode,omitempty"`
	MaxStoredMessages    *int             `json:"max_stored_messages,omitempty"`
}

// SubAgentConfigPatch is a partial update for SubAgentConfig.
type SubAgentConfigPatch struct {
	CacheTTL *string                   `json:"cache_ttl,omitempty"`
	Timeout  *string                   `json:"timeout,omitempty"`
	Model    *SubAgentModelConfigPatch `json:"model,omitempty"`
}

// SubAgentModelConfigPatch is a partial update for SubAgentModelConfig.
type SubAgentModelConfigPatch struct {
	Enabled  *bool   `json:"enabled,omitempty"`
	Provider *string `json:"provider,omitempty"`
	Model    *string `json:"model,omitempty"`
}

// WorkModeConfigPatch is a partial update for WorkModeConfig.
type WorkModeConfigPatch struct {
	Default *WorkMode `json:"default,omitempty"`
}

// UIConfigPatch is a partial update for UIConfig.
type UIConfigPatch struct {
	CloseBehavior *CloseBehavior `json:"close_behavior,omitempty"`
}

// VisionRecognitionConfigPatch is a partial update for the external image
// recognition model used by the image_recognize tool.
type VisionRecognitionConfigPatch struct {
	Enabled        *bool   `json:"enabled,omitempty"`
	Provider       *string `json:"provider,omitempty"`
	Model          *string `json:"model,omitempty"`
	TimeoutSeconds *int    `json:"timeout_seconds,omitempty"`
	MaxImageBytes  *int64  `json:"max_image_bytes,omitempty"`
}

// SystemConfigPatch is a partial update for system-level config
// (limits + subagent + work_mode + ui + vision_recognition).
type SystemConfigPatch struct {
	Limits   *LimitsConfigPatch            `json:"limits,omitempty"`
	SubAgent *SubAgentConfigPatch          `json:"sub_agent,omitempty"`
	WorkMode *WorkModeConfigPatch          `json:"work_mode,omitempty"`
	UI       *UIConfigPatch                `json:"ui,omitempty"`
	Vision   *VisionRecognitionConfigPatch `json:"vision_recognition,omitempty"`
}

// UpdateSystemConfig merges a SystemConfigPatch into the persisted config.
func UpdateSystemConfig(patch SystemConfigPatch) (*Config, error) {
	cfg, err := Load("")
	if err != nil {
		return nil, err
	}

	if patch.Limits != nil {
		mergeLimits(&cfg.Limits, patch.Limits)
	}
	if patch.SubAgent != nil {
		mergeSubAgent(&cfg.SubAgent, patch.SubAgent)
	}
	if patch.WorkMode != nil {
		mergeWorkMode(&cfg.WorkMode, patch.WorkMode)
	}
	if patch.UI != nil {
		mergeUI(&cfg.UI, patch.UI)
	}
	if patch.Vision != nil {
		mergeVisionRecognition(&cfg.Vision, patch.Vision)
	}

	mgr := NewManager()
	if err := mgr.SaveGlobal(cfg); err != nil {
		return nil, fmt.Errorf("save system config: %w", err)
	}
	return cfg, nil
}

func mergeLimits(l *LimitsConfig, p *LimitsConfigPatch) {
	if p.AutoCompactBuffer != nil {
		l.AutoCompactBuffer = *p.AutoCompactBuffer
	}
	if p.ToolResultExecCap != nil {
		l.ToolResultExecCap = *p.ToolResultExecCap
	}
	if p.ToolResultReadCap != nil {
		l.ToolResultReadCap = *p.ToolResultReadCap
	}
	if p.ToolResultDefaultCap != nil {
		l.ToolResultDefaultCap = *p.ToolResultDefaultCap
	}
	if p.PruneAfterRounds != nil {
		l.PruneAfterRounds = *p.PruneAfterRounds
	}
	if p.MaxRounds != nil {
		l.MaxRounds = *p.MaxRounds
	}
	if p.TodoLongRunMode != nil {
		l.TodoLongRunMode = NormalizeTodoLongRunMode(*p.TodoLongRunMode)
	}
	if p.MaxStoredMessages != nil {
		l.MaxStoredMessages = *p.MaxStoredMessages
	}
}

func mergeSubAgent(s *SubAgentConfig, p *SubAgentConfigPatch) {
	if p.CacheTTL != nil {
		s.CacheTTL = *p.CacheTTL
	}
	if p.Timeout != nil {
		s.Timeout = *p.Timeout
	}
	if p.Model != nil {
		mergeSubAgentModel(&s.Model, p.Model)
	}
}

func mergeSubAgentModel(m *SubAgentModelConfig, p *SubAgentModelConfigPatch) {
	if p.Enabled != nil {
		m.Enabled = *p.Enabled
	}
	if p.Provider != nil {
		m.Provider = *p.Provider
	}
	if p.Model != nil {
		m.Model = *p.Model
	}
	m.Normalize()
}

func mergeWorkMode(w *WorkModeConfig, p *WorkModeConfigPatch) {
	if p.Default != nil {
		w.Default = p.Default.Normalize()
	}
}

func mergeUI(u *UIConfig, p *UIConfigPatch) {
	if p.CloseBehavior != nil {
		u.CloseBehavior = p.CloseBehavior.Normalize()
	}
}

func mergeVisionRecognition(v *VisionRecognitionConfig, p *VisionRecognitionConfigPatch) {
	if p.Enabled != nil {
		v.Enabled = *p.Enabled
	}
	if p.Provider != nil {
		v.Provider = *p.Provider
	}
	if p.Model != nil {
		v.Model = *p.Model
	}
	if p.TimeoutSeconds != nil {
		v.TimeoutSeconds = *p.TimeoutSeconds
	}
	if p.MaxImageBytes != nil {
		v.MaxImageBytes = *p.MaxImageBytes
	}
	v.Normalize()
}
