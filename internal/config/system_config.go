package config

import "fmt"

// LimitsConfigPatch is a partial update for LimitsConfig.
type LimitsConfigPatch struct {
	AutoCompactBuffer    *int `json:"auto_compact_buffer,omitempty"`
	ToolResultExecCap    *int `json:"tool_result_exec_cap,omitempty"`
	ToolResultReadCap    *int `json:"tool_result_read_cap,omitempty"`
	ToolResultDefaultCap *int `json:"tool_result_default_cap,omitempty"`
	PruneAfterRounds     *int `json:"prune_after_rounds,omitempty"`
	MaxRounds            *int `json:"max_rounds,omitempty"`
	MaxStoredMessages    *int `json:"max_stored_messages,omitempty"`
}

// SubAgentConfigPatch is a partial update for SubAgentConfig.
type SubAgentConfigPatch struct {
	CacheTTL *string `json:"cache_ttl,omitempty"`
	Timeout  *string `json:"timeout,omitempty"`
}

// SystemConfigPatch is a partial update for system-level config
// (limits + subagent).
type SystemConfigPatch struct {
	Limits   *LimitsConfigPatch   `json:"limits,omitempty"`
	SubAgent *SubAgentConfigPatch `json:"sub_agent,omitempty"`
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
}
