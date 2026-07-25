package config

import "fmt"

// IMConfig 是 IM 桥接的顶层配置。
// IMConfig is the top-level configuration for the IM bridge.
type IMConfig struct {
	Enabled               bool                 `json:"enabled"`
	Session               IMSessionPolicy      `json:"session"`
	Identity              IMIdentityPolicy     `json:"identity"`
	Command               IMCommandPolicy      `json:"command"`
	RateLimit             []IMRateLimitRule    `json:"rate_limit,omitempty"`
	AuditLog              bool                 `json:"audit_log"`
	AuditLocalOnly        bool                 `json:"audit_local_only"`
	ToolsAllowlistDefault []string             `json:"tools_allowlist_default,omitempty"`
	Personas              map[string]IMPersona `json:"personas,omitempty"`
	Cron                  IMCronConfig         `json:"cron"`
	Fallback              []IMFallbackRule     `json:"fallback,omitempty"`
	Media                 IMMediaConfig        `json:"media"`
	Platforms             []IMPlatformConfig   `json:"platforms,omitempty"`
}

// IMSessionPolicy 控制 IM 消息映射到 P-Chat session 的粒度。
// IMSessionPolicy controls how IM messages map to P-Chat sessions.
type IMSessionPolicy struct {
	Scope         string `json:"scope"`
	RecordSender  bool   `json:"record_sender"`
	CrossPlatform bool   `json:"cross_platform"`
}

// IMIdentityPolicy 描述跨平台 principal 绑定。
// IMIdentityPolicy describes cross-platform principal links.
type IMIdentityPolicy struct {
	Links    []IMIdentityLink `json:"links,omitempty"`
	AutoLink IMAutoLinkPolicy `json:"auto_link"`
}

// IMIdentityLink 把多个平台账号绑定到同一 principal。
// IMIdentityLink binds platform accounts to one principal.
type IMIdentityLink struct {
	Principal string      `json:"principal"`
	Accounts  []IMAccount `json:"accounts,omitempty"`
}

// IMAccount 是一个平台账号引用。
// IMAccount is a platform account reference.
type IMAccount struct {
	Platform string `json:"platform"`
	ID       string `json:"id"`
}

// IMAutoLinkPolicy 控制自动身份绑定策略。
// IMAutoLinkPolicy controls automatic identity linking.
type IMAutoLinkPolicy struct {
	Enabled bool   `json:"enabled"`
	Trust   string `json:"trust"`
}

// IMCommandPolicy 控制 IM 端斜杠命令行为。
// IMCommandPolicy controls slash-command behavior in IM channels.
type IMCommandPolicy struct {
	Prefix                string   `json:"prefix"`
	ForwardUnknownToAgent bool     `json:"forward_unknown_to_agent"`
	AdminSenders          []string `json:"admin_senders,omitempty"`
	RequireMentionInGroup bool     `json:"require_mention_in_group"`
}

// IMRateLimitRule 是一条 IM 限流规则。
// IMRateLimitRule is one IM rate-limit rule.
type IMRateLimitRule struct {
	Scope string  `json:"scope"`
	Key   string  `json:"key,omitempty"`
	RPS   float64 `json:"rps"`
	Burst int     `json:"burst"`
}

// IMPersona 描述某个 IM 渠道匹配到的风格和工具策略。
// IMPersona describes style and tool policy for a matched IM channel.
type IMPersona struct {
	Style        string   `json:"style,omitempty"`
	WorkMode     WorkMode `json:"work_mode,omitempty"`
	Model        string   `json:"model,omitempty"`
	ToolsAllow   []string `json:"tools_allow,omitempty"`
	PromptInject string   `json:"prompt_inject,omitempty"`
}

// IMCronConfig 控制 IM 调度任务。
// IMCronConfig controls IM scheduled jobs.
type IMCronConfig struct {
	Enabled bool        `json:"enabled"`
	Jobs    []IMCronJob `json:"jobs,omitempty"`
}

// IMCronJob 是一个 IM 调度任务定义。
// IMCronJob is one IM scheduled job definition.
type IMCronJob struct {
	ID       string `json:"id"`
	Schedule string `json:"schedule"`
	Timezone string `json:"timezone,omitempty"`
	Platform string `json:"platform"`
	ChatID   string `json:"chat_id"`
	Prompt   string `json:"prompt"`
	Persona  string `json:"persona,omitempty"`
}

// IMFallbackRule 描述平台故障时的降级转发规则。
// IMFallbackRule describes failover routing when a platform is down.
type IMFallbackRule struct {
	From    IMFallbackEndpoint `json:"from"`
	To      IMFallbackEndpoint `json:"to"`
	Trigger string             `json:"trigger"`
}

// IMFallbackEndpoint 是 fallback 的平台目标。
// IMFallbackEndpoint is a platform endpoint for fallback routing.
type IMFallbackEndpoint struct {
	Platform string `json:"platform"`
	ChatID   string `json:"chat_id,omitempty"`
}

// IMMediaConfig 控制 IM 媒体输入输出。
// IMMediaConfig controls IM media input and output.
type IMMediaConfig struct {
	STT         IMProviderModelConfig `json:"stt"`
	TTS         IMTTSConfig           `json:"tts"`
	Vision      IMVisionConfig        `json:"vision"`
	FileExtract IMFileExtractConfig   `json:"file_extract"`
}

// IMProviderModelConfig 是 provider/model 对。
// IMProviderModelConfig is a provider/model pair.
type IMProviderModelConfig struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// IMTTSConfig 控制语音输出。
// IMTTSConfig controls text-to-speech output.
type IMTTSConfig struct {
	Provider  string   `json:"provider,omitempty"`
	Voice     string   `json:"voice,omitempty"`
	EnabledIn []string `json:"enabled_in,omitempty"`
}

// IMVisionConfig 控制图片理解。
// IMVisionConfig controls image understanding.
type IMVisionConfig struct {
	Enabled       bool  `json:"enabled"`
	MaxImageBytes int64 `json:"max_image_bytes"`
}

// IMFileExtractConfig 控制文件文本提取。
// IMFileExtractConfig controls text extraction from files.
type IMFileExtractConfig struct {
	Enabled      bool     `json:"enabled"`
	MaxFileBytes int64    `json:"max_file_bytes"`
	Types        []string `json:"types,omitempty"`
}

// IMPlatformConfig 是单个平台 adapter 的配置。
// IMPlatformConfig is the configuration for one platform adapter.
type IMPlatformConfig struct {
	Type              string           `json:"type"`
	Variant           string           `json:"variant,omitempty"`
	Enabled           bool             `json:"enabled"`
	Mode              string           `json:"mode,omitempty"`
	Token             string           `json:"token,omitempty"`
	AppID             string           `json:"app_id,omitempty"`
	AppSecret         string           `json:"app_secret,omitempty"`
	VerificationToken string           `json:"verification_token,omitempty"`
	EncryptKey        string           `json:"encrypt_key,omitempty"`
	CorpID            string           `json:"corp_id,omitempty"`
	CorpSecret        string           `json:"corp_secret,omitempty"`
	AgentID           int              `json:"agent_id,omitempty"`
	CallbackAESKey    string           `json:"callback_aes_key,omitempty"`
	CallbackToken     string           `json:"callback_token,omitempty"`
	Endpoint          string           `json:"endpoint,omitempty"`
	APIKey            string           `json:"api_key,omitempty"`
	Webhook           IMWebhookConfig  `json:"webhook,omitempty"`
	Out               IMOutboundConfig `json:"out,omitempty"`
	AllowedSenders    []string         `json:"allowed_senders,omitempty"`
	Extra             map[string]any   `json:"extra,omitempty"`
}

// IMWebhookConfig 描述平台 webhook 设置。
// IMWebhookConfig describes platform webhook settings.
type IMWebhookConfig struct {
	Listen string `json:"listen,omitempty"`
	Path   string `json:"path,omitempty"`
}

// IMOutboundConfig 描述平台出站发送设置。
// IMOutboundConfig describes platform outbound settings.
type IMOutboundConfig struct {
	UseOpenAPI bool   `json:"use_openapi,omitempty"`
	APIBase    string `json:"api_base,omitempty"`
}

// DefaultIMConfig 返回安全的 IM 默认配置。
// DefaultIMConfig returns safe IM defaults.
func DefaultIMConfig() IMConfig {
	cfg := IMConfig{
		Enabled: false,
		Session: IMSessionPolicy{
			Scope:        "per_thread",
			RecordSender: true,
		},
		Command: IMCommandPolicy{
			Prefix:                "/",
			ForwardUnknownToAgent: true,
			RequireMentionInGroup: true,
		},
		AuditLog:       true,
		AuditLocalOnly: true,
		ToolsAllowlistDefault: []string{
			"read_file",
			"list_files",
			"web_search",
			"knowledge_search",
		},
		Personas: map[string]IMPersona{
			"default": {
				Style:    "tech",
				WorkMode: WorkModeDaily,
			},
		},
	}
	cfg.Normalize()
	return cfg
}

// Normalize 填充 IM 配置缺省值并规整枚举。
// Normalize fills IM defaults and normalizes enum values.
func (c *IMConfig) Normalize() {
	if c.Session.Scope == "" {
		c.Session.Scope = "per_thread"
	}
	switch c.Session.Scope {
	case "per_sender", "per_chat", "per_thread":
	default:
		c.Session.Scope = "per_thread"
	}
	if c.Command.Prefix == "" {
		c.Command.Prefix = "/"
	}
	if c.Identity.AutoLink.Trust == "" {
		c.Identity.AutoLink.Trust = "manual"
	}
	if c.Personas == nil {
		c.Personas = map[string]IMPersona{}
	}
	if _, ok := c.Personas["default"]; !ok {
		c.Personas["default"] = IMPersona{Style: "tech", WorkMode: WorkModeDaily}
	}
	for key, persona := range c.Personas {
		if persona.WorkMode != "" {
			persona.WorkMode = persona.WorkMode.Normalize()
		}
		c.Personas[key] = persona
	}
	for i := range c.Platforms {
		if c.Platforms[i].Type == "feishu" && c.Platforms[i].Variant == "" {
			c.Platforms[i].Variant = "bot"
		}
	}
}

// UpdateIMConfig 替换持久化的 IM 配置块。
// UpdateIMConfig replaces the persisted IM config block.
func UpdateIMConfig(next IMConfig) (*Config, error) {
	cfg, err := Load("")
	if err != nil {
		return nil, err
	}
	next.Normalize()
	cfg.IM = next
	mgr := NewManager()
	if err := mgr.SaveGlobal(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// IMConfigPatch 是 IM 配置的部分更新。
// IMConfigPatch is a partial update for IM config.
type IMConfigPatch struct {
	Enabled               *bool                  `json:"enabled,omitempty"`
	Session               *IMSessionPolicyPatch  `json:"session,omitempty"`
	Identity              *IMIdentityPolicyPatch `json:"identity,omitempty"`
	Command               *IMCommandPolicyPatch  `json:"command,omitempty"`
	RateLimit             *[]IMRateLimitRule     `json:"rate_limit,omitempty"`
	AuditLog              *bool                  `json:"audit_log,omitempty"`
	AuditLocalOnly        *bool                  `json:"audit_local_only,omitempty"`
	ToolsAllowlistDefault *[]string              `json:"tools_allowlist_default,omitempty"`
	Personas              *map[string]IMPersona  `json:"personas,omitempty"`
	Cron                  *IMCronConfigPatch     `json:"cron,omitempty"`
	Fallback              *[]IMFallbackRule      `json:"fallback,omitempty"`
	Media                 *IMMediaConfigPatch    `json:"media,omitempty"`
	Platforms             *[]IMPlatformConfig    `json:"platforms,omitempty"`
}

// IMSessionPolicyPatch 是 session 策略的部分更新。
// IMSessionPolicyPatch is a partial update for session policy.
type IMSessionPolicyPatch struct {
	Scope         *string `json:"scope,omitempty"`
	RecordSender  *bool   `json:"record_sender,omitempty"`
	CrossPlatform *bool   `json:"cross_platform,omitempty"`
}

// IMIdentityPolicyPatch 是 identity 策略的部分更新。
// IMIdentityPolicyPatch is a partial update for identity policy.
type IMIdentityPolicyPatch struct {
	Links    *[]IMIdentityLink      `json:"links,omitempty"`
	AutoLink *IMAutoLinkPolicyPatch `json:"auto_link,omitempty"`
}

// IMAutoLinkPolicyPatch 是自动绑定策略的部分更新。
// IMAutoLinkPolicyPatch is a partial update for auto-link policy.
type IMAutoLinkPolicyPatch struct {
	Enabled *bool   `json:"enabled,omitempty"`
	Trust   *string `json:"trust,omitempty"`
}

// IMCommandPolicyPatch 是命令策略的部分更新。
// IMCommandPolicyPatch is a partial update for command policy.
type IMCommandPolicyPatch struct {
	Prefix                *string   `json:"prefix,omitempty"`
	ForwardUnknownToAgent *bool     `json:"forward_unknown_to_agent,omitempty"`
	AdminSenders          *[]string `json:"admin_senders,omitempty"`
	RequireMentionInGroup *bool     `json:"require_mention_in_group,omitempty"`
}

// IMCronConfigPatch 是调度配置的部分更新。
// IMCronConfigPatch is a partial update for cron config.
type IMCronConfigPatch struct {
	Enabled *bool        `json:"enabled,omitempty"`
	Jobs    *[]IMCronJob `json:"jobs,omitempty"`
}

// IMMediaConfigPatch 是媒体配置的部分更新。
// IMMediaConfigPatch is a partial update for media config.
type IMMediaConfigPatch struct {
	STT         *IMProviderModelConfig `json:"stt,omitempty"`
	TTS         *IMTTSConfig           `json:"tts,omitempty"`
	Vision      *IMVisionConfig        `json:"vision,omitempty"`
	FileExtract *IMFileExtractConfig   `json:"file_extract,omitempty"`
}

// UpdateIMConfigPatch merges a partial IM config update into the persisted config.
func UpdateIMConfigPatch(patch IMConfigPatch) (*Config, error) {
	cfg, err := Load("")
	if err != nil {
		return nil, err
	}
	cfg.IM.Normalize()
	mergeIMConfig(&cfg.IM, patch)
	cfg.IM.Normalize()
	mgr := NewManager()
	if err := mgr.SaveGlobal(cfg); err != nil {
		return nil, fmt.Errorf("save im config: %w", err)
	}
	return cfg, nil
}

func mergeIMConfig(target *IMConfig, patch IMConfigPatch) {
	if patch.Enabled != nil {
		target.Enabled = *patch.Enabled
	}
	if patch.Session != nil {
		mergeIMSession(&target.Session, patch.Session)
	}
	if patch.Identity != nil {
		mergeIMIdentity(&target.Identity, patch.Identity)
	}
	if patch.Command != nil {
		mergeIMCommand(&target.Command, patch.Command)
	}
	if patch.RateLimit != nil {
		target.RateLimit = *patch.RateLimit
	}
	if patch.AuditLog != nil {
		target.AuditLog = *patch.AuditLog
	}
	if patch.AuditLocalOnly != nil {
		target.AuditLocalOnly = *patch.AuditLocalOnly
	}
	if patch.ToolsAllowlistDefault != nil {
		target.ToolsAllowlistDefault = *patch.ToolsAllowlistDefault
	}
	if patch.Personas != nil {
		target.Personas = *patch.Personas
	}
	if patch.Cron != nil {
		mergeIMCron(&target.Cron, patch.Cron)
	}
	if patch.Fallback != nil {
		target.Fallback = *patch.Fallback
	}
	if patch.Media != nil {
		mergeIMMedia(&target.Media, patch.Media)
	}
	if patch.Platforms != nil {
		target.Platforms = *patch.Platforms
	}
}

func mergeIMSession(target *IMSessionPolicy, patch *IMSessionPolicyPatch) {
	if patch.Scope != nil {
		target.Scope = *patch.Scope
	}
	if patch.RecordSender != nil {
		target.RecordSender = *patch.RecordSender
	}
	if patch.CrossPlatform != nil {
		target.CrossPlatform = *patch.CrossPlatform
	}
}

func mergeIMIdentity(target *IMIdentityPolicy, patch *IMIdentityPolicyPatch) {
	if patch.Links != nil {
		target.Links = *patch.Links
	}
	if patch.AutoLink != nil {
		if patch.AutoLink.Enabled != nil {
			target.AutoLink.Enabled = *patch.AutoLink.Enabled
		}
		if patch.AutoLink.Trust != nil {
			target.AutoLink.Trust = *patch.AutoLink.Trust
		}
	}
}

func mergeIMCommand(target *IMCommandPolicy, patch *IMCommandPolicyPatch) {
	if patch.Prefix != nil {
		target.Prefix = *patch.Prefix
	}
	if patch.ForwardUnknownToAgent != nil {
		target.ForwardUnknownToAgent = *patch.ForwardUnknownToAgent
	}
	if patch.AdminSenders != nil {
		target.AdminSenders = *patch.AdminSenders
	}
	if patch.RequireMentionInGroup != nil {
		target.RequireMentionInGroup = *patch.RequireMentionInGroup
	}
}

func mergeIMCron(target *IMCronConfig, patch *IMCronConfigPatch) {
	if patch.Enabled != nil {
		target.Enabled = *patch.Enabled
	}
	if patch.Jobs != nil {
		target.Jobs = *patch.Jobs
	}
}

func mergeIMMedia(target *IMMediaConfig, patch *IMMediaConfigPatch) {
	if patch.STT != nil {
		target.STT = *patch.STT
	}
	if patch.TTS != nil {
		target.TTS = *patch.TTS
	}
	if patch.Vision != nil {
		target.Vision = *patch.Vision
	}
	if patch.FileExtract != nil {
		target.FileExtract = *patch.FileExtract
	}
}
