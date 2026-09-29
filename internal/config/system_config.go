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
	CacheTTL *string `json:"cache_ttl,omitempty"`
	Timeout  *string `json:"timeout,omitempty"`
}

// WorkModeConfigPatch is a partial update for WorkModeConfig.
type WorkModeConfigPatch struct {
	Default *WorkMode `json:"default,omitempty"`
}

// UIConfigPatch is a partial update for UIConfig.
type UIConfigPatch struct {
	CloseBehavior *CloseBehavior `json:"close_behavior,omitempty"`
}

// VisionRecognitionConfigPatch 是 media_recognize 外接图片模型的部分更新。
// VisionRecognitionConfigPatch is a partial update for the external image
// recognition model used by the image strategy of media_recognize.
type VisionRecognitionConfigPatch struct {
	Enabled        *bool   `json:"enabled,omitempty"`
	Provider       *string `json:"provider,omitempty"`
	Model          *string `json:"model,omitempty"`
	TimeoutSeconds *int    `json:"timeout_seconds,omitempty"`
	MaxImageBytes  *int64  `json:"max_image_bytes,omitempty"`
}

// RecognitionRoutePatch is a partial update for one media route.
type RecognitionRoutePatch struct {
	Enabled        *bool   `json:"enabled,omitempty"`
	Provider       *string `json:"provider,omitempty"`
	Model          *string `json:"model,omitempty"`
	TimeoutSeconds *int    `json:"timeout_seconds,omitempty"`
	MaxBytes       *int64  `json:"max_bytes,omitempty"`
}

// RecognitionConfigPatch updates independent media recognition routes.
type RecognitionConfigPatch struct {
	Routes map[MediaKind]RecognitionRoutePatch `json:"routes,omitempty"`
}

// GenerationConfigPatch updates application defaults independently for each
// canonical media operation. An empty target removes that default.
type GenerationConfigPatch struct {
	Defaults       map[GenerationOperation]GenerationModelTarget `json:"defaults,omitempty"`
	RequireConfirm *bool                                         `json:"require_confirm,omitempty"`
}

// SystemConfigPatch is a partial update for system-level config
// (limits + subagent + work_mode + ui + vision_recognition).
type SystemConfigPatch struct {
	Limits      *LimitsConfigPatch            `json:"limits,omitempty"`
	SubAgent    *SubAgentConfigPatch          `json:"sub_agent,omitempty"`
	WorkMode    *WorkModeConfigPatch          `json:"work_mode,omitempty"`
	UI          *UIConfigPatch                `json:"ui,omitempty"`
	Vision      *VisionRecognitionConfigPatch `json:"vision_recognition,omitempty"`
	Recognition *RecognitionConfigPatch       `json:"recognition,omitempty"`
	Generation  *GenerationConfigPatch        `json:"generation,omitempty"`
}

// UpdateSystemConfig merges a SystemConfigPatch into the persisted config.
func UpdateSystemConfig(patch SystemConfigPatch) (*Config, error) {
	return UpdateSystemConfigAt("", patch)
}

// UpdateSystemConfigAt 按活动配置路径更新系统配置。
// UpdateSystemConfigAt updates system configuration at the active path.
func UpdateSystemConfigAt(configPath string, patch SystemConfigPatch) (*Config, error) {
	cfg, err := Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load system config: %w", err)
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
		cfg.Recognition.Normalize()
		cfg.Recognition.Routes[MediaImage] = RecognitionRoute{
			Enabled: cfg.Vision.Enabled, Provider: cfg.Vision.Provider, Model: cfg.Vision.Model,
			TimeoutSeconds: cfg.Vision.TimeoutSeconds, MaxBytes: cfg.Vision.MaxImageBytes,
		}
	}
	if patch.Recognition != nil {
		mergeRecognition(&cfg.Recognition, patch.Recognition)
		if image, ok := cfg.Recognition.Routes[MediaImage]; ok {
			cfg.Vision = VisionRecognitionConfig{
				Enabled: image.Enabled, Provider: image.Provider, Model: image.Model,
				TimeoutSeconds: image.TimeoutSeconds, MaxImageBytes: image.MaxBytes,
			}
			cfg.Vision.Normalize()
		}
	}
	if patch.Generation != nil {
		if err := mergeGeneration(cfg, patch.Generation); err != nil {
			return nil, err
		}
	}

	if err := saveConfigFieldsAt(configPath, cfg, systemConfigOverlayFields(cfg, patch)...); err != nil {
		return nil, fmt.Errorf("save system config: %w", err)
	}
	return cfg, nil
}

func systemConfigOverlayFields(cfg *Config, patch SystemConfigPatch) []configOverlayField {
	fields := make([]configOverlayField, 0, 32)
	if p := patch.Limits; p != nil {
		if p.AutoCompactBuffer != nil {
			fields = append(fields, overlayField(cfg.Limits.AutoCompactBuffer, "limits", "auto_compact_buffer"))
		}
		if p.ToolResultExecCap != nil {
			fields = append(fields, overlayField(cfg.Limits.ToolResultExecCap, "limits", "tool_result_exec_cap"))
		}
		if p.ToolResultReadCap != nil {
			fields = append(fields, overlayField(cfg.Limits.ToolResultReadCap, "limits", "tool_result_read_cap"))
		}
		if p.ToolResultDefaultCap != nil {
			fields = append(fields, overlayField(cfg.Limits.ToolResultDefaultCap, "limits", "tool_result_default_cap"))
		}
		if p.PruneAfterRounds != nil {
			fields = append(fields, overlayField(cfg.Limits.PruneAfterRounds, "limits", "prune_after_rounds"))
		}
		if p.MaxRounds != nil {
			fields = append(fields, overlayField(cfg.Limits.MaxRounds, "limits", "max_rounds"))
		}
		if p.TodoLongRunMode != nil {
			fields = append(fields, overlayField(cfg.Limits.TodoLongRunMode, "limits", "todo_long_run_mode"))
		}
		if p.MaxStoredMessages != nil {
			fields = append(fields, overlayField(cfg.Limits.MaxStoredMessages, "limits", "max_stored_messages"))
		}
	}
	if p := patch.SubAgent; p != nil {
		if p.CacheTTL != nil {
			fields = append(fields, overlayField(cfg.SubAgent.CacheTTL, "subagent", "cache_ttl"))
		}
		if p.Timeout != nil {
			fields = append(fields, overlayField(cfg.SubAgent.Timeout, "subagent", "timeout"))
		}
	}
	if patch.WorkMode != nil && patch.WorkMode.Default != nil {
		fields = append(fields, overlayField(cfg.WorkMode.Default, "work_mode", "default"))
	}
	if patch.UI != nil && patch.UI.CloseBehavior != nil {
		fields = append(fields, overlayField(cfg.UI.CloseBehavior, "ui", "close_behavior"))
	}
	if p := patch.Vision; p != nil {
		image := cfg.Recognition.Routes[MediaImage]
		if p.Enabled != nil {
			fields = append(fields, overlayField(cfg.Vision.Enabled, "vision_recognition", "enabled"))
		}
		if p.Provider != nil {
			fields = append(fields, overlayField(cfg.Vision.Provider, "vision_recognition", "provider"))
		}
		if p.Model != nil {
			fields = append(fields, overlayField(cfg.Vision.Model, "vision_recognition", "model"))
		}
		if p.TimeoutSeconds != nil {
			fields = append(fields, overlayField(cfg.Vision.TimeoutSeconds, "vision_recognition", "timeout_seconds"))
		}
		if p.MaxImageBytes != nil {
			fields = append(fields, overlayField(cfg.Vision.MaxImageBytes, "vision_recognition", "max_image_bytes"))
		}
		fields = append(fields, recognitionRouteOverlayFields(MediaImage, image)...)
	}
	if patch.Recognition != nil {
		for kind, routePatch := range patch.Recognition.Routes {
			if !kind.IsValid() {
				continue
			}
			route := cfg.Recognition.Routes[kind]
			fields = append(fields, recognitionRouteOverlayFields(kind, route)...)
			if kind == MediaImage {
				fields = append(fields, visionOverlayFields(cfg.Vision, routePatch)...)
			}
		}
	}
	if p := patch.Generation; p != nil {
		if p.Defaults != nil {
			fields = append(fields, overlayField(cfg.Generation.Defaults, "generation", "defaults"))
		}
		if p.RequireConfirm != nil {
			fields = append(fields, overlayField(cfg.Generation.RequireConfirm, "generation", "require_confirm"))
		}
	}
	return fields
}

func recognitionRouteOverlayFields(kind MediaKind, route RecognitionRoute) []configOverlayField {
	prefix := []string{"recognition", "routes", string(kind)}
	return []configOverlayField{
		overlayField(route.Enabled, append(prefix, "enabled")...),
		overlayField(route.Provider, append(prefix, "provider")...),
		overlayField(route.Model, append(prefix, "model")...),
		overlayField(route.TimeoutSeconds, append(prefix, "timeout_seconds")...),
		overlayField(route.MaxBytes, append(prefix, "max_bytes")...),
	}
}

func visionOverlayFields(vision VisionRecognitionConfig, patch RecognitionRoutePatch) []configOverlayField {
	fields := make([]configOverlayField, 0, 5)
	if patch.Enabled != nil {
		fields = append(fields, overlayField(vision.Enabled, "vision_recognition", "enabled"))
	}
	if patch.Provider != nil {
		fields = append(fields, overlayField(vision.Provider, "vision_recognition", "provider"))
	}
	if patch.Model != nil {
		fields = append(fields, overlayField(vision.Model, "vision_recognition", "model"))
	}
	if patch.TimeoutSeconds != nil {
		fields = append(fields, overlayField(vision.TimeoutSeconds, "vision_recognition", "timeout_seconds"))
	}
	if patch.MaxBytes != nil {
		fields = append(fields, overlayField(vision.MaxImageBytes, "vision_recognition", "max_image_bytes"))
	}
	return fields
}

func mergeGeneration(cfg *Config, patch *GenerationConfigPatch) error {
	if cfg.Generation.Defaults == nil {
		cfg.Generation.Defaults = make(map[GenerationOperation]GenerationModelTarget)
	}
	for operation, target := range patch.Defaults {
		if !operation.IsValid() {
			return fmt.Errorf("unsupported generation operation %q", operation)
		}
		if !target.Valid() {
			delete(cfg.Generation.Defaults, operation)
			continue
		}
		if _, _, _, err := cfg.ResolveGenerationTarget(operation, target); err != nil {
			return err
		}
		cfg.Generation.Defaults[operation] = target
	}
	if patch.RequireConfirm != nil {
		cfg.Generation.RequireConfirm = *patch.RequireConfirm
	}
	return nil
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

func mergeRecognition(target *RecognitionConfig, patch *RecognitionConfigPatch) {
	target.Normalize()
	for kind, routePatch := range patch.Routes {
		if !kind.IsValid() {
			continue
		}
		route := target.Routes[kind]
		if routePatch.Enabled != nil {
			route.Enabled = *routePatch.Enabled
		}
		if routePatch.Provider != nil {
			route.Provider = *routePatch.Provider
		}
		if routePatch.Model != nil {
			route.Model = *routePatch.Model
		}
		if routePatch.TimeoutSeconds != nil {
			route.TimeoutSeconds = *routePatch.TimeoutSeconds
		}
		if routePatch.MaxBytes != nil {
			route.MaxBytes = *routePatch.MaxBytes
		}
		route.Normalize()
		target.Routes[kind] = route
	}
}
