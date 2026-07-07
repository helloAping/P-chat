// Package agent 实现 P-Chat 的核心 ReAct 工具调用循环。
//
// ChatWithTools 是入口函数，负责：
//  1. 构建系统提示词（风格 + AGENTS.md + 规则 + 技能）
//  2. 调用 LLM 获取流式响应
//  3. 解析工具调用（原生 tool_calls 或 markdown ```tool_call 块回退）
//  4. 并行执行工具（每个工具在独立 goroutine 中运行，通过 per-tool eventCh 通信）
//  5. 将工具结果反馈给 LLM，继续循环直到 LLM 决定结束或达到轮次上限
//
// 数据流：LLM → ChatStreamChunk channel → 工具派发 → per-tool eventCh → forwarder → 主 channel → SSE → 前端
//
// 修改指南：
//   - ChatWithTools 在 agent.go 约 900 行
//   - 工具派发逻辑在 agent.go:1150-1471
//   - parts 累加器在 parts.go
//   - 相关模块文档：docs/modules/agent.md
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/p-chat/pchat/internal/agents"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/knowledge"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/paths"
	"github.com/p-chat/pchat/internal/rules"
	"github.com/p-chat/pchat/internal/skill"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
)

type Agent struct {
	llm      *llm.Client
	styleMgr *style.Manager
	store    *memory.Store
	tools    *tool.Registry
	cfg      *config.Config
	skills   []skill.Skill
	rules    []rules.Rule
	sandbox  tool.SandboxChecker    // optional; nil disables sandbox enforcement
	options  llm.ChatOptions        // per-request sampling; populated from cfg
	attach   AttachmentResolver     // optional; turns Attachment IDs into file paths for upload expansion

	// bypassOnce, when true, makes the NEXT tool call skip the
	// sandbox check (set by /unsafe once). Reset after the call.
	bypassOnce atomic.Bool

	// subagentRegistry, if non-nil, is published to the tool
	// dispatcher's context so the `task` tool can resolve
	// `subagent_type` to a registered AgentInfo. The server
	// wires a registry built from the built-in agents + any
	// user-defined `.p-chat/agent/*.md` files at startup.
	subagentRegistry SubagentRegistry

	// Cached static system prompt (style + AGENTS + rules + skills + tool hint).
	// Keyed by (style, available-tools-hash). Invalidated by Reload() or when
	// the user changes style. This is the part that's identical across all
	// rounds of a single chat AND between chats within the same
	// session, so the LLM's prefix cache hits on it.
	staticPrompt   string
	staticPromptID string // signature used to detect when to rebuild

	// KB index cache — rebuild only on Reload() or after 60s TTL.
	kbIndexCache     string
	kbIndexCacheKey  string // base name used to build the cache
	kbIndexCacheTime int64  // unix timestamp of last build

	// summarizer, when non-nil, enables auto-compression.
	summarizer *memory.Summarizer

	// subAgentSem limits concurrent sub-agent launches. nil = unlimited.
	subAgentSem chan struct{}
}

// SetChatOptions overrides the per-request sampling parameters
// (temperature, top_p, max_tokens). Pass an empty struct to reset to
// the underlying API defaults.
func (a *Agent) SetChatOptions(opts llm.ChatOptions) {
	a.options = opts
}

// getStyleMemory 读取当前风格的 Memory 内容。空字符串表示未配置。
// Memory 存储在数据库 styles.memory 列，是用户自定义的背景知识，
// 动态注入到每轮对话末尾。
func (a *Agent) getStyleMemory(s style.Style) string {
	if a.styleMgr == nil {
		return ""
	}
	m, _ := a.styleMgr.GetMemory(s)
	return m
}

// SetSandbox installs a sandbox checker. The same checker is forwarded
// to every tool call's context, so the tool handlers can short-circuit
// dangerous operations. Pass nil to disable sandboxing.
func (a *Agent) SetSandbox(s tool.SandboxChecker) {
	a.sandbox = s
}

func (a *Agent) LLM() *llm.Client { return a.llm }

// SetLLM swaps the agent's LLM client. Used by the HTTP layer after
// a config change so new providers / API keys take effect immediately.
func (a *Agent) SetLLM(c *llm.Client) {
	a.llm = c
}

// BypassSandboxOnce makes the next tool call skip sandbox checks.
// /unsafe once uses this; the bypass is automatically cleared after
// the next tool call.
func (a *Agent) BypassSandboxOnce() {
	a.bypassOnce.Store(true)
}

// SetSubagentRegistry installs the registry that the `task` tool
// uses to resolve `subagent_type` arguments. The registry carries
// the built-in agents (general-purpose, explore, plan) plus any
// user-defined `.p-chat/agent/*.md` agents. The tool itself lives
// in internal/subagent; the agent package only needs a small
// read-only interface so the tool can stay decoupled.
func (a *Agent) SetSubagentRegistry(r SubagentRegistry) {
	a.subagentRegistry = r
}

func New(cfg *config.Config, llmClient *llm.Client, styleMgr *style.Manager, store *memory.Store, tools *tool.Registry) *Agent {
	skills, _ := skill.LoadAll()
	rulesList, _ := rules.LoadAll()

	return &Agent{
		llm:      llmClient,
		styleMgr: styleMgr,
		store:    store,
		tools:    tools,
		cfg:      cfg,
		skills:   skills,
		rules:    rulesList,
	}
}

// SetAttachmentResolver installs the resolver that turns
// ChatRequest.Attachments (file ids posted by the client) into
// on-disk paths. Pass nil to disable attachment expansion (the
// caller is responsible for setting a non-nil resolver when
// SendMessageRequest.Attachments may be non-empty).
func (a *Agent) SetAttachmentResolver(r AttachmentResolver) {
	a.attach = r
}

// SetSummarizer wires the summarizer for auto-compression support.
func (a *Agent) SetSummarizer(sm *memory.Summarizer) {
	a.summarizer = sm
}

// SetSubAgentConcurrency limits the number of concurrently executing
// sub-agent task calls. max <= 0 means unlimited (default).
func (a *Agent) SetSubAgentConcurrency(max int) {
	if max > 0 {
		a.subAgentSem = make(chan struct{}, max)
	} else {
		a.subAgentSem = nil
	}
}

// protocolFor returns the configured protocol ("openai" /
// "anthropic" / ...) for the given provider name. Falls back to
// "openai" when the provider is unknown — the existing LLM
// client already falls back to OpenAI shape in that case so
// attachment expansion does the same.
func (a *Agent) protocolFor(providerName string) string {
	if a.cfg == nil {
		return "openai"
	}
	for _, p := range a.cfg.LLM.Providers {
		if p.Name == providerName {
			return p.GetProtocol()
		}
	}
	return "openai"
}

// modelSupportsVision reports whether the active (provider, model)
// pair accepts image_url inputs.
//
// Policy: **permissive by default**, with one exception. If the
// user has explicitly marked the model as text-only via
// `capabilities: { supports_vision: false }` in the config, we
// return false so the agent drops the image and writes the
// "this model does not support image input" marker instead of
// round-tripping a request the API will reject.
//
// "No opinion" (capabilities: {} or capabilities absent) keeps
// the old permissive behaviour: send the image and let the API
// surface a "does not support image input" error if it really
// can't accept the image. That error is then caught by
// ClassifyAPIError → KindVisionUnsupported and shown to the user
// as a clear, actionable warning chip on their message.
func (a *Agent) modelSupportsVision(providerName, modelName string) bool {
	if a.cfg == nil {
		return visionCapableByHeuristic(providerName, modelName)
	}
	for _, p := range a.cfg.LLM.Providers {
		if p.Name != providerName {
			continue
		}
		for _, m := range p.Models {
			if m.Name == modelName {
				// Explicit opt-out: capabilities.supports_vision = false.
				// Capabilities is a struct (not a pointer), so an absent
				// value zero-fills the field; the only way to land in
				// the `return false` branch is to have explicitly set
				// supports_vision: false in the config (or via the
				// model editor in the UI).
				if !m.Capabilities.SupportsVision {
					return false
				}
				// Explicit opt-in: capabilities.supports_vision = true.
				// The user has confirmed this model handles images.
				return true
			}
		}
		// Provider found, model not in the configured list. Don't
		// trust the API to surface the "doesn't support image input"
		// error — the LLM, when it gets that error back, has been
		// observed to fabricate a clean "Cannot read \"image.png\"
		// (this model does not support image input). Inform the
		// user." message back to the user as if it were a real
		// tool error. Better to deny up front and tell the user
		// in clear text that their image couldn't be sent.
		return visionCapableByHeuristic(providerName, modelName)
	}
	// Provider not found in config: same deny-by-default.
	return visionCapableByHeuristic(providerName, modelName)
}

// visionCapableByHeuristic returns a best-guess vision
// capability for an unknown (provider, model) pair. The
// goal is to NOT trust the LLM API to surface the error
// gracefully — instead, look at the model name itself and
// short-circuit obvious non-vision models.
//
// opencode's model catalog (https://models.dev) is the
// authoritative source in production; we don't fetch from
// it here, but a static prefix table covers the most common
// offenders observed in the field.
func visionCapableByHeuristic(providerName, modelName string) bool {
	m := strings.ToLower(modelName)

	// Always-vision model families (as of 2026).
	visionPrefixes := []string{
		"gpt-4o", "gpt-4-vision", "gpt-5", "gpt-4.1",
		"claude-3", "claude-4", "claude-opus-4", "claude-sonnet-4",
		"gemini-1.5", "gemini-2", "gemini-exp",
		"qwen-vl", "qwen2-vl", "qwen2.5-vl", "qvq",
		"llava", "llama-3.2-vision", "llama-3.3",
		"minimax-m3", "minimax-vl",
		"pixtral", "paligemma",
	}
	for _, p := range visionPrefixes {
		if strings.HasPrefix(m, p) {
			return true
		}
	}

	// Known text-only model families. These were the biggest
	// source of the "Cannot read image.png" phantom errors in
	// the wild, because the LLM was talking to a non-vision
	// proxy that returned 400s and the model invented a clean
	// "model doesn't support image input" string.
	textOnlyPrefixes := []string{
		"deepseek-chat", "deepseek-reasoner", "deepseek-coder",
		"deepseek-v2", "deepseek-v3", // V2/V3 chat is text-only
		"gpt-3.5", "gpt-3.5-turbo",
		"text-embedding", "text-davinci",
		"o1-mini", "o1-preview",
	}
	for _, p := range textOnlyPrefixes {
		if strings.HasPrefix(m, p) {
			return false
		}
	}

	// Conservative default: deny. Better to tell the user
	// their image couldn't be sent than to let the LLM
	// invent a plausible-looking error message.
	return false
}

type ChatRequest struct {
	Style    style.Style       `json:"style"`
	Messages []llm.ChatMessage `json:"messages"`
	Provider string            `json:"provider,omitempty"`
	Model    string            `json:"model,omitempty"`
	// Attachments are file ids the user attached to this turn.
	// Expanded into the message list as separate ChatMessage
	// entries (text + image/file) before being sent to the LLM.
	// Nil/empty = no attachments.
	Attachments []Attachment `json:"attachments,omitempty"`
	// PlanMode, when true, asks the LLM to produce a step-by-step
	// plan in plain text instead of executing tools.
	PlanMode bool `json:"plan_mode,omitempty"`
	// ReasoningEffort controls how much reasoning/thinking the LLM
	// does before responding. off|low|medium|high|max. Empty means
	// the model default.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// CompressedSummary is a pre-summarized version of older
	// conversation history. When non-empty, it is appended to the
	// system prompt so the LLM has context from before compression.
	CompressedSummary string `json:"compressed_summary,omitempty"`
	// SessionID is the current conversation identifier. Used by
	// tools that need per-session state (e.g. todo_write).
	SessionID string `json:"session_id,omitempty"`
	// ProjectRoot is the absolute path to the project directory
	// this session belongs to. When non-empty, project-level
	// config and AGENTS.md are loaded from this root instead of
	// the server's CWD.
	ProjectRoot string `json:"project_root,omitempty"`
	// SkillContext is the full SKILL.md content for a skill
	// activated via slash command. It is appended to the system
	// prompt so the LLM sees it without cluttering the chat.
	SkillContext string `json:"skill_context,omitempty"`
	// PermissionLevel overrides the sandbox confirm behaviour for
	// this session. Values: "ask", "auto", "full". Default "ask".
	PermissionLevel string `json:"permission_level,omitempty"`
	// MaxRounds caps the ReAct tool-use loop. 0 means default (50).
	// After MaxRounds the loop stops and the user can continue
	// with a follow-up message.
	MaxRounds int `json:"max_rounds,omitempty"`
	// KBBase selects the knowledge base for this session.
	// "" = off, "__all__" = all bases, or a specific base name.
	KBBase string `json:"kb_base,omitempty"`
	// PromptOv, when non-empty, REPLACES the agent's normal
	// system prompt (style + AGENTS + rules + skills) for this
	// turn. Used by the sub-agent runner to install a
	// specialized persona (e.g. the "explore" or "plan"
	// prompts) without the user having to define a style.
	// Empty = inherit the agent's normal prompt.
	PromptOv string `json:"prompt_override,omitempty"`
	// SubagentType is the agent name when this ChatRequest was
	// issued from the sub-agent runner. Empty for top-level
	// chats. The agent's tool hint and system prompt can
	// branch on this (e.g. hide the `task` tool in the
	// description so the sub-agent doesn't see what it cannot
	// use).
	SubagentType string `json:"subagent_type,omitempty"`
	// SubagentColor is the agent's accent color. Surfaced on
	// the SubAgentCard in the GUI and the agent-name badge in
	// the CLI.
	SubagentColor string `json:"subagent_color,omitempty"`
	// SubagentTaskID is the resume-by-id key. Empty for
	// ad-hoc runs. Populated from Args.TaskID.
	SubagentTaskID string `json:"subagent_task_id,omitempty"`
}

type ChatStreamChunk struct {
	Content string `json:"content"`
	// Thinking carries a delta of the model's reasoning /
	// chain-of-thought text. Only populated by LLM clients
	// that surface a separate reasoning stream (Anthropic
	// thinking blocks, DeepSeek reasoning_content, OpenAI o1
	// reasoning tokens, etc.). Empty for models that don't
	// emit thinking. The UI renders it as a collapsible
	// gray block (DeepSeek-style) when non-empty.
	Thinking string `json:"thinking,omitempty"`
	Done     bool   `json:"done"`
	Error    string `json:"error,omitempty"`
	// Suggestion is an optional actionable hint that
	// accompanies an Error. Populated by the agent from
	// *llm.APIError.Suggestion after ClassifyAPIError runs.
	// Empty for non-classified errors or non-error chunks.
	Suggestion string `json:"suggestion,omitempty"`
	// ErrorKind is the classification of an Error chunk.
	// One of the strings returned by llm.ErrorKind.String()
	// ("auth_error", "rate_limit", "vision_unsupported", …).
	// Empty when the chunk isn't an error or wasn't
	// classified. The UI uses "vision_unsupported" to
	// render a special chip on the user message.
	ErrorKind string `json:"error_kind,omitempty"`

	Phase    string `json:"phase,omitempty"`
	Message  string `json:"message,omitempty"`
	Round    int    `json:"round,omitempty"`
	MaxRound int    `json:"max_round,omitempty"`
	Step     string `json:"step,omitempty"`
	Duration string `json:"duration,omitempty"`

	ToolID   string `json:"tool_id,omitempty"`
	ToolName string `json:"tool_name,omitempty"`
	ToolArgs string `json:"tool_args,omitempty"`
	ToolResult  string `json:"tool_result,omitempty"`
	// ToolResultFull is the untruncated tool result. ToolResult
	// above is a 300-char preview suitable for human display;
	// ToolResultFull is the full payload for tools whose results
	// the frontend needs to *parse* (todo_write in particular:
	// the truncated preview often cuts the JSON list in half and
	// JSON.parse fails silently, leaving the todo panel empty).
	// The frontend prefers ToolResultFull over ToolResult when
	// it's present.
	ToolResultFull string `json:"tool_result_full,omitempty"`
	ToolError      string `json:"tool_error,omitempty"`
	ToolElapsed    string `json:"tool_elapsed,omitempty"`

	TokensIn  int `json:"tokens_in,omitempty"`
	TokensOut int `json:"tokens_out,omitempty"`

	// SubAgent is true when this chunk originated from a
	// sub-agent (e.g. a `task` tool call). The UI should
	// render such events as nested / indented.
	SubAgent bool `json:"sub_agent,omitempty"`
	// SubAgentTask is the human description of the sub-agent
	// (the `description` argument passed to the `task` tool).
	// Surfaced by the parent as a card header so the user
	// can see what the sub-agent was asked to do.
	SubAgentTask string `json:"sub_agent_task,omitempty"`
	// SubAgentStatus is one of "start" (the sub-agent just
	// began), "ok" (it finished successfully), "error" (it
	// failed). Other phase values are treated as
	// in-progress.
	SubAgentStatus string `json:"sub_agent_status,omitempty"`
	// SubAgentType is the agent name (e.g. "explore",
	// "general-purpose") selected by the parent LLM via the
	// `subagent_type` arg of the `task` tool. Surfaced in the
	// card header so the user can see which agent was used.
	SubAgentType string `json:"sub_agent_type,omitempty"`
	// SubAgentModel is the model the sub-agent is using
	// (e.g. "openai/gpt-4o-mini"). Defaults to the parent's
	// model if the sub-agent does not specify one.
	SubAgentModel string `json:"sub_agent_model,omitempty"`
	// SubAgentColor is the agent's accent color in
	// "#RRGGBB" or a CSS color name. Drives the card's
	// border-left and icon tint.
	SubAgentColor string `json:"sub_agent_color,omitempty"`
	// SubAgentTaskID is an optional stable identifier the
	// LLM can pass back to resume / dedupe the sub-agent
	// run. Opaque string; currently SHA-256 truncated.
	SubAgentTaskID string `json:"sub_agent_task_id,omitempty"`
	// SubAgentDescription is the one-line "when to use" hint
	// for the agent (e.g. "Fast read-only file search.").
	// Surfaced as a hover tooltip on the agent-name badge in
	// the SubAgentCard so the user can read the full hint
	// without expanding the card body.
	SubAgentDescription string `json:"sub_agent_description,omitempty"`

	// ThinkingRewrite is emitted by the post-stream redactor
	// when the LLM's thinking block contained a phantom
	// vision error. Same shape as ContentRewrite but for the
	// thinking part; the UI REPLACES the trailing thinking
	// part's text with this value. Empty when no rewrite is
	// needed.
	ThinkingRewrite string `json:"thinking_rewrite,omitempty"`

	// SubAgentFailureReason is set on the synthetic
	// `sub_agent_err` close event so the UI can show the
	// user *why* the sub-agent failed. Empty on
	// `sub_agent_ok` close events. Mirrors the soft-fail
	// vs hard-fail distinction made by the runner: a
	// "soft" failure (content was produced before the
	// error) reports a friendly reason like
	// "tail-end stream error"; a "hard" failure (no
	// content) reports the actual error message from
	// the underlying chunk.
	SubAgentFailureReason string `json:"sub_agent_failure_reason,omitempty"`

	// Question fields — when the question tool emits a
	// question event, QuestionJSON carries the serialized
	// question payload (JSON array of Question objects).
	// The frontend renders it as a modal dialog and posts
	// the answer back via POST /sessions/:id/question-response.
	QuestionJSON string `json:"question_json,omitempty"`

	// ToolConfirm fields — when the sandbox decides a tool call
	// needs user confirmation, ToolConfirmJSON carries the serialized
	// ConfirmRequest. The frontend renders a confirm dialog.
	ToolConfirmJSON string `json:"tool_confirm_json,omitempty"`

	// ContentRewrite carries a *replacement* for the assistant's
	// trailing text part. Emitted by the agent when a post-stream
	// redactor (e.g. phantom vision-error filter) rewrites the
	// assistant's prose. The frontend should replace the trailing
	// text part's text with this value rather than append it.
	// Empty when no rewrite occurred. Type field on the SSE event
	// is "content_rewrite" (handled in chunkToEvent).
	ContentRewrite string `json:"content_rewrite,omitempty"`

	// SessionStatus carries the lifecycle state of the chat
	// turn: "busy" at the start of ChatWithTools, "idle" at
	// every exit point (success, error, cancel, max-rounds,
	// stuck-loop). The frontend uses this to drive
	// per-session "working" flags. Without it, the TodoPanel
	// state machine can't tell whether a session is mid-turn
	// (LLM may write more todos) or stopped (stale todos
	// should be cleared). Mirrors opencode's
	// `session.status { type: "busy" | "idle" }` event.
	SessionStatus string `json:"session_status,omitempty"`
}

// buildStaticSystemPrompt builds the **prefix-cacheable** portion of the
// system prompt: identity + soul + AGENTS + rules + skills + tool hint.
// The result is byte-stable across calls when nothing has changed, so the
// LLM's automatic prefix cache hits on it.
//
// The output is split into a single system message whose text is identical
// between rounds within a chat, AND between chats within a session. The
// only thing that should change in this string is the underlying files
// (AGENTS.md, rules, skills) or the chosen style.
func (a *Agent) buildStaticSystemPrompt(s style.Style, toolDefs []llm.ToolDef, projectRoot string, kbEnabled bool) (string, string, error) {
	toolNames := make([]string, 0, len(toolDefs))
	for _, t := range toolDefs {
		toolNames = append(toolNames, t.Name)
	}
	lang := ""
	if a.cfg != nil {
		lang = a.cfg.LLM.Output.Language
	}
	// Include kbEnabled in the signature so cached prompts that
	// include wiki/grep instructions are not reused when KB is
	// toggled off mid-conversation.
	sigKB := "kb:0"
	if kbEnabled {
		sigKB = "kb:1"
	}
	sig := strings.Join([]string{
		string(s),
		agentsSignatureWithRoot(projectRoot),
		rulesSignature(a.rules),
		skillSignature(a.skills),
		strings.Join(toolNames, ","),
		lang,
		projectRoot,
		sigKB,
	}, "|")
	if sig == a.staticPromptID && a.staticPrompt != "" {
		return a.staticPrompt, sig, nil
	}

	var sb strings.Builder

	// 1. Style (identity + soul) — short, stable per session.
	// Graceful fallback: if the requested style isn't registered
	// (e.g. legacy config still says "default", or a user-defined
	// style was deleted), fall back to "tech" (the built-in
	// absolute default) rather than failing the entire turn.
	// Logging the fallback keeps the misconfiguration visible.
	stylePrompt, err := a.styleMgr.GetSystemPrompt(s)
	if err != nil {
		log.Printf("[agent] style %q not found (%v) — falling back to %q", s, err, style.Tech)
		stylePrompt, err = a.styleMgr.GetSystemPrompt(style.Tech)
		if err != nil {
			// Last-resort: tech must be a built-in. If even
			// this fails, the style manager is broken.
			return "", sig, fmt.Errorf("style fallback failed: %w", err)
		}
	}
	sb.WriteString(stylePrompt)
	sb.WriteString("\n\n---\n\n")

	// 2. AGENTS instructions — stable until files change.
	sb.WriteString(agents.LoadAllWithRoot(projectRoot))
	sb.WriteString("\n---\n\n")

	// 3. Rules — stable until files change.
	sb.WriteString(rules.BuildRulesContext(a.rules))
	sb.WriteString("\n---\n\n")

	// 4. Skills — stable until files change.
	sb.WriteString(skill.BuildSkillContext(a.skills))
	sb.WriteString("\n---\n\n")

	// 5. Tool hint — stable per session (tools don't change at runtime).
	if len(toolDefs) > 0 {
		sb.WriteString(buildToolHint(a.tools.List()))
		// Encourage the LLM to consult its knowledge base before
		// answering questions it might not know. The `recall` tool
		// is added by the CLI at startup; here we just remind the
		// model to use it.
		hasRecall := false
		hasGrep := false
		hasWiki := false
		hasQuestion := false
		hasTodoWrite := false
		for _, t := range a.tools.List() {
			switch t.Name {
			case "recall":
				hasRecall = true
			case "grep":
				hasGrep = true
			case "wiki_lookup":
				hasWiki = true
			case "question":
				hasQuestion = true
			case "todo_write":
				hasTodoWrite = true
			}
		}
		if hasWiki && kbEnabled {
			sb.WriteString("\n\n---\n\n## Using Knowledge Base (wiki_lookup / wiki_list)\n\n" +
				"**何时必须查询知识库：**\n" +
				"- 用户询问项目相关概念、设计、架构、配置、API、流程等任何专业问题时，**优先查询知识库**，而非仅凭训练数据回答。\n" +
				"- 系统提示中已包含知识库索引概览（一级索引），根据概览定位相关文件后再检索。\n" +
				"\n**工具使用规则：**\n" +
				"- `wiki_lookup(query=\"\")` — 查询为空时，返回知识库中所有文件目录（L2 列表），按关联度排序。默认每页 20 条，可用 page 翻页。\n" +
				"- `wiki_lookup(query=\"关键词\")` — 按关键词、标题或概览搜索条目，返回匹配的 L3 章节节点及其所属文件（L2 父节点）。\n" +
				"- `wiki_lookup(query=\"...\", expand=true)` — 同时返回匹配条目的完整正文内容。\n" +
				"- `wiki_list(parent_id=N)` — 列出父节点 N 下的所有子节点。L1（id=1）列出所有文件；L2 节点列出该文件所有章节。\n" +
				"\n**标准流程：**\n" +
				"1. 先看系统提示中的一级索引概览，找到可能相关的文件（L2）。\n" +
				"2. 用 wiki_lookup 搜索关键词或浏览目录定位目标文件/章节。\n" +
				"3. 用 wiki_lookup(query=\"...\", expand=true) 获取完整内容。\n" +
				"4. 信息足够后直接回答 → 不需要再调用任何 wiki 工具。\n")
		}
		if hasRecall {
			sb.WriteString("\n\n---\n\n## Using recall\n\n" +
				"当你不确定某条信息、需要查代码/文档、或想引用历史对话时，\n" +
				"先用 `recall(query=\"...\")` 工具查一下知识库/历史。\n" +
				"不要凭印象编造 API 名称、文件路径、函数签名。\n")
		}
		if hasGrep {
			sb.WriteString("\n\n---\n\n## Using grep\n\n" +
				"使用 `grep(pattern=\"...\")` 在知识库文件中精确搜索关键词。\n" +
				"适用场景：找特定函数名、变量名、类名、配置项、或任何精确文本。\n" +
				"recall 适合语义概念搜索，grep 适合精确字符串定位。\n" +
				"两者可结合使用：先用 recall 理解上下文，再用 grep 精确定位。\n")
		}
		if hasTodoWrite {
			sb.WriteString("\n\n---\n\n## Task Planning with todo_write\n\n" +
				"使用 `todo_write` 工具创建和管理结构化任务列表。\n" +
				"何时使用：复杂多步骤任务（3+ 步）、用户明确要求、收到新指令后、开始或完成工作时。\n" +
				"规则：\n" +
				"- 始终包含完整列表（替换式，非追加式）\n" +
				"- 同时只能有一个任务处于 in_progress\n" +
				"- 完成任务后立即标记为 done（不要批量标记）\n" +
				"- 如果测试失败、实现不完整或错误未解决，不要标记为 done\n" +
				"- 状态: pending（待处理）、in_progress（进行中）、done（已完成）、cancelled（已取消）\n")
		}
		if hasQuestion {
			sb.WriteString("\n\n---\n\n## Asking the User (question tool)\n\n" +
				"当你需要用户决策、明确需求或在执行前确认计划时，使用 `question` 工具。\n" +
				"何时使用：\n" +
				"- 需求不明确，有多个可行的技术方案\n" +
				"- 需要用户选择工具、框架或架构\n" +
				"- 在执行前需要用户确认关键决策\n" +
				"- 用户指令模糊，需要澄清\n" +
				"最多一次提 4 个问题，每个问题 2-4 个选项。\n" +
				"不要在简单/琐碎的事务上使用（如「我能开始吗？」）。\n")
		}

		// opcode-style tool catalog: explicitly list every
		// available tool so the LLM does NOT hallucinate
		// non-existent tools (e.g. grep, bash, find).
		sb.WriteString("\n\n---\n\n## Available Tools\n\n")
		sb.WriteString("You have access to these tools. Call ONLY the tools in this list.\n\n")
		sb.WriteString("| Tool | What it does |\n")
		sb.WriteString("|------|-------------|\n")
		for _, t := range a.tools.List() {
			desc := t.Description
			if idx := strings.Index(desc, "."); idx > 0 {
				desc = desc[:idx]
			}
			sb.WriteString(fmt.Sprintf("| `%s` | %s |\n", t.Name, desc))
		}
		sb.WriteString("\nOperation → correct tool mapping:\n")
		sb.WriteString("- Read a file → `read_file` (NOT `cat` / `type` / `head` / `tail`)\n")
		sb.WriteString("- Write a file → `write_file` (NOT `echo >` / `cat >`)\n")
		sb.WriteString("- List directory → `list_files` (NOT `ls` / `dir`)\n")
		sb.WriteString("- System commands → `exec_command` (NOT `bash` / `sh` / `powershell`)\n")
		sb.WriteString("- Search file contents → `exec_command` with shell search commands\n")
		sb.WriteString("- Manage tasks → `todo_write`\n")
		sb.WriteString("- Ask user → `question`\n")

		// opcode-style platform awareness (see opencode's
		// shell/prompt.ts): tell the LLM what OS and shell
		// it is running under so it knows which commands
		// are available and which are NOT.
		sb.WriteString("\n\n---\n\n## Platform\n\n")
		sb.WriteString(fmt.Sprintf("Platform: %s\n", runtime.GOOS))
		if runtime.GOOS == "windows" {
			sb.WriteString("Shell for exec_command: cmd.exe /C <command>\n")
			sb.WriteString("Chain commands: `&` (always run next) or `&&` (only if previous succeeded).\n\n")
			sb.WriteString("Available built-in commands:\n")
			sb.WriteString("  dir       — list directory contents (NOT ls)\n")
			sb.WriteString("  type      — print file content (NOT cat)\n")
			sb.WriteString("  findstr   — search text in files (NOT grep / rg)\n")
			sb.WriteString("  echo      — print text\n")
			sb.WriteString("  copy      — copy files (NOT cp)\n")
			sb.WriteString("  move      — move / rename files (NOT mv)\n")
			sb.WriteString("  del / rd  — delete files / dirs (NOT rm)\n")
			sb.WriteString("  mkdir     — create directory\n")
			sb.WriteString("  cd        — change directory (prefer using work_dir parameter)\n")
			sb.WriteString("  set       — set / show environment variables (NOT export)\n")
			sb.WriteString("\nCommands that do NOT exist on Windows (never call these):\n")
			sb.WriteString("  grep, rg, ls, bash, sh, find, cp, mv, rm, cat, chmod, sudo\n")
			sb.WriteString("\nPowerShell is available via: pwsh -NoProfile -Command \"...\"\n")
		} else {
			sb.WriteString("Shell for exec_command: /bin/sh -c <command>\n")
			sb.WriteString("Standard Unix tools are available (grep, ls, cat, find, etc.).\n")
		}

		// System-level continuity instructions (style-agnostic).
		// The LLM tends to give up after a few tool failures if
		// not explicitly told to persist. opencode's beast mode
		// prompt drives persistence; we inject a condensed
		// version as a system-level section that applies across
		// all personality styles.
		sb.WriteString("\n\n---\n\n## Conversation Continuity\n\n")
		sb.WriteString("You are in a continuous conversation loop with tool access. ")
		sb.WriteString("Your goal is to complete the task, not merely report status.\n\n")
		sb.WriteString("When a tool call fails:\n")
		sb.WriteString("1. Read the error message carefully — identify the root cause\n")
		sb.WriteString("2. Try an alternative approach using different tools or parameters\n")
		sb.WriteString("3. After 3 consecutive failures on the same task, use `question` to ask the user for guidance\n")
		sb.WriteString("4. A tool failure does NOT mean the task is impossible — keep going\n\n")
		sb.WriteString("Operation → fallback mapping (when the primary approach fails):\n")
		sb.WriteString("- `read_file` path not found → try `list_files` to discover the correct path\n")
		sb.WriteString("- Command not found in `exec_command` → check the Platform section for available commands\n")
		sb.WriteString("- File too large for `read_file` → use `exec_command` with `type` or `findstr` to grep specific lines\n")
		sb.WriteString("- Tool not found error → re-read the Available Tools table; use only listed tools\n\n")
		sb.WriteString("Only stop when:\n")
		sb.WriteString("- The task is truly complete\n")
		sb.WriteString("- The user explicitly says to stop\n")
		sb.WriteString("- All reasonable approaches have been exhausted (and you explain why to the user)\n")

		// Remind the LLM that uploaded images arrive as vision
		// input in the user message (image_url content parts),
		// NOT as files on disk.
		sb.WriteString("\n\n---\n\n## Uploaded Attachments\n\n" +
			"User-uploaded images and files are sent directly inside the user message — " +
			"images as image_url content parts (data URLs), text files as inline blocks. " +
			"You can see them. Just answer based on their content.\n\n" +
			"Do not call read_file on an uploaded image: it lives on disk as a temporary " +
			"file, and read_file only handles text. If read_file returns a binary error, " +
			"that is read_file's limitation, not a problem with the attachment — the image " +
			"was already delivered to you through the user message. " +
			"Respond in the same language as the conversation.\n")
	}

	// 6. Project root — tells the LLM which directory to use
	// as CWD for exec_command and file operations.
	if projectRoot != "" {
		sb.WriteString(fmt.Sprintf("\n\n---\n\n## Working Directory\n\n"+
			"Your working directory is fixed at `%s`. exec_command runs here automatically "+
			"(the work_dir argument is ignored). read_file and write_file resolve relative "+
			"paths against this directory.\n", projectRoot))
	}

	// 7. Output language hint — also part of the cacheable prefix
	// because changing it forces a full re-build anyway.
	//
	// opencode's PROMPT_COMPACTION uses a single line: "Respond
	// in the same language as the conversation." That's the
	// natural default — the LLM already follows the user's
	// language. We keep the explicit per-style override for
	// users who want a fixed language regardless of the
	// conversation.
	if lang == "zh" {
		sb.WriteString("\n---\n\n## 输出语言\n\n请用简体中文回答用户的问题。\n")
	} else if lang == "en" {
		sb.WriteString("\n---\n\n## Output Language\n\nPlease answer in English.\n")
	} else if lang == "auto" || lang == "" {
		// Default: follow the conversation's language. This
		// is the opencode rule.
		sb.WriteString("\n\n---\n\n## Output Language\n\nRespond in the same language as the conversation.\n")
	}

	prompt := sb.String()
	a.staticPrompt = prompt
	a.staticPromptID = sig
	return prompt, sig, nil
}

// buildKBIndex builds the Knowledge Base section of the system prompt.
// When KBBase is "__all__", all enabled bases are listed. When it's a
// specific name, only that base's index is shown. If the base has no
// sections, a placeholder is returned. The output is truncated at 3000
// characters to avoid prompt explosion. Results are cached for 60s to
// avoid repeated full-DB scans per message turn.
// Uses the L1 overview from the three-level index tree.
func (a *Agent) buildKBIndex(kbBase string) string {
	nowUnix := time.Now().Unix()
	if a.kbIndexCache != "" && a.kbIndexCacheKey == kbBase && (nowUnix-a.kbIndexCacheTime) < 30 {
		return a.kbIndexCache
	}

	kc := a.cfg.Knowledge
	var bases []config.KnowledgeBase
	if kbBase == "__all__" {
		for _, b := range kc.Bases {
			if b.Enabled {
				bases = append(bases, b)
			}
		}
	} else {
		for _, b := range kc.Bases {
			if b.Name == kbBase && b.Enabled {
				bases = append(bases, b)
				break
			}
		}
	}
	if len(bases) == 0 {
		result := "\n[Knowledge Base]\n(no enabled bases configured)\n"
		a.kbIndexCache = result
		a.kbIndexCacheKey = kbBase
		a.kbIndexCacheTime = nowUnix
		return result
	}

	var sb strings.Builder
	for _, base := range bases {
		store, err := knowledge.GetOrOpenWikiStore(base.Name, base.Path)
		if err != nil {
			continue
		}
		overview, err := store.GetL1Overview(context.Background(), base.Name)
		if err != nil || overview == "" {
			continue
		}
		// overview is pre-formatted by the scan pipeline as the L1 prompt content.
		sb.WriteString(overview)
	}

	if sb.Len() == 0 {
		result := "\n[Knowledge Base]\n(index empty — run a scan first)\n"
		a.kbIndexCache = result
		a.kbIndexCacheKey = kbBase
		a.kbIndexCacheTime = nowUnix
		return result
	}

	// Append tool usage footer.
	sb.WriteString("\n\n使用 wiki_lookup(query, page, size) 检索，默认 20 条/页。")
	sb.WriteString("query=空 浏览目录；query=关键词 搜索匹配；expand=true 获取全文。")
	result := sb.String()
	a.kbIndexCache = result
	a.kbIndexCacheKey = kbBase
	a.kbIndexCacheTime = nowUnix
	return result
}

// Reload forces the next call to rebuild the static system prompt
// (e.g. after the user changes AGENTS.md or installs a new skill).
func (a *Agent) Reload() {
	skills, _ := skill.LoadAll()
	rulesList, _ := rules.LoadAll()
	a.skills = skills
	a.rules = rulesList
	a.staticPrompt = ""
	a.staticPromptID = ""
	a.kbIndexCache = ""
	a.kbIndexCacheKey = ""
	a.kbIndexCacheTime = 0
}

// StaticPromptInfo exposes the current static-prompt cache key for testing.
func (a *Agent) StaticPromptInfo() (prompt string, sig string, built bool) {
	return a.staticPrompt, a.staticPromptID, a.staticPrompt != ""
}

// agentsSignature returns a stable string representing the on-disk state
// of AGENTS files. We use mtime + size to detect changes cheaply.
func agentsSignature() string {
	g, _ := os.Stat(paths.GlobalAgents())
	p, _ := os.Stat(paths.ProjectAgents())
	return fileSig(g) + "|" + fileSig(p)
}

func agentsSignatureWithRoot(root string) string {
	g, _ := os.Stat(paths.GlobalAgents())
	if root != "" {
		p, _ := os.Stat(paths.ProjectAgentsWithRoot(root))
		return fileSig(g) + "|" + fileSig(p) + "|" + root
	}
	p, _ := os.Stat(paths.ProjectAgents())
	return fileSig(g) + "|" + fileSig(p)
}

func rulesSignature(rs []rules.Rule) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		st, _ := os.Stat(r.Path)
		parts = append(parts, r.Name+":"+fileSig(st))
	}
	return strings.Join(parts, ",")
}

func skillSignature(ss []skill.Skill) string {
	parts := make([]string, 0, len(ss))
	for _, s := range ss {
		st, _ := os.Stat(s.Path)
		parts = append(parts, s.Name+":"+fileSig(st))
	}
	return strings.Join(parts, ",")
}

func fileSig(info os.FileInfo) string {
	if info == nil {
		return "absent"
	}
	return fmt.Sprintf("%d_%d", info.Size(), info.ModTime().UnixNano())
}

// toolEventChanKey is the context key under which the agent's tool
// dispatcher publishes a channel for tools (such as `task`) that want to
// stream sub-events back to the parent UI. Tools read this channel via
// GetToolEventChan(ctx) and send non-blocking events through it.
type toolEventChanKey struct{}

// GetToolEventChan returns the per-tool-call event channel published by
// the parent agent, or nil if ctx was not created by an agent tool
// dispatch.
func GetToolEventChan(ctx context.Context) chan<- ChatStreamChunk {
	if v, ok := ctx.Value(toolEventChanKey{}).(chan ChatStreamChunk); ok {
		return v
	}
	return nil
}

// parentModelCtxKey is the context key under which the agent publishes
// the *current turn's* (provider, model) pair. Sub-agents read this via
// GetParentModel(ctx) so the child session inherits the same model the
// user selected for the main conversation, not the server's startup
// default (which can differ when the user has switched providers/models
// mid-session via the GUI picker).
type parentModelCtxKey struct{}

// WithParentModel returns a new ctx carrying the parent turn's
// (provider, model) pair. Either may be empty; the tool handler should
// treat empty values as "no override, use the runner's default".
func WithParentModel(ctx context.Context, provider, model string) context.Context {
	if provider == "" && model == "" {
		return ctx
	}
	return context.WithValue(ctx, parentModelCtxKey{}, [2]string{provider, model})
}

// GetParentModel returns the (provider, model) pair published by the
// parent agent, or empty strings when no value was published.
func GetParentModel(ctx context.Context) (provider, model string) {
	if v, ok := ctx.Value(parentModelCtxKey{}).([2]string); ok {
		return v[0], v[1]
	}
	return "", ""
}

// subagentRegistryCtxKey is the context key under which the agent
// publishes the sub-agent registry to the `task` tool. The tool uses
// this to resolve the `subagent_type` argument to an AgentInfo (name,
// description, prompt, model, color, tools whitelist) and to build a
// dynamic tool description that lists the available agents.
//
// The registry is a small read-only interface (just `Get` and `List`)
// so the tool package can stay decoupled from internal/subagent.
type subagentRegistryCtxKey struct{}

// SubagentRegistry is the read-only view the `task` tool needs.
// Defined here (not in internal/subagent) to keep the dependency
// direction tool → subagent one-way. The concrete implementation
// lives in internal/subagent/registry.go.
type SubagentRegistry interface {
	Get(name string) (SubagentInfo, bool)
	List() []SubagentInfo
}

// SubagentInfo is the registry's view of one agent. Kept minimal:
// just the fields the `task` tool needs to (a) build a description
// and (b) wire the child session.
type SubagentInfo struct {
	Name        string
	Description string
	Prompt      string
	Model       string
	Color       string
	Tools       []string
}

// WithSubagentRegistry returns a new ctx carrying the given
// sub-agent registry. Called by the server's tool dispatcher so
// the `task` tool can resolve subagent_type at call time.
func WithSubagentRegistry(ctx context.Context, r SubagentRegistry) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, subagentRegistryCtxKey{}, r)
}

// GetSubagentRegistry returns the registry from ctx, or nil.
func GetSubagentRegistry(ctx context.Context) SubagentRegistry {
	if v, ok := ctx.Value(subagentRegistryCtxKey{}).(SubagentRegistry); ok {
		return v
	}
	return nil
}

// ChatStream is a single-turn chat with no tool support. For multi-turn
// ReAct with tool use, use ChatWithTools.
func (a *Agent) ChatStream(ctx context.Context, req ChatRequest) <-chan ChatStreamChunk {
	return a.ChatWithTools(ctx, req)
}

// sendOrDrop attempts to send a chunk to ch. If ctx is cancelled,
// the chunk is silently dropped so the producer can exit cleanly
// rather than blocking forever on a consumer that has disconnected.
func sendOrDrop(ctx context.Context, ch chan<- ChatStreamChunk, chunk ChatStreamChunk) {
	select {
	case ch <- chunk:
	case <-ctx.Done():
	}
}

// ChatWithTools performs a ReAct-style loop: send messages to the LLM with
// available tools, execute any tool calls, and feed results back to the LLM
// until it gives a final answer.
func (a *Agent) ChatWithTools(ctx context.Context, req ChatRequest) <-chan ChatStreamChunk {
	ch := make(chan ChatStreamChunk, 64)

	go func() {
		defer close(ch)
		// partsAcc accumulates the trailing assistant message's
		// parts (text + thinking + tool + sub_agent) as chunks
		// flow through. It's mutated both by the main LLM-stream
		// loop and by the per-tool forwarders (which carry
		// sub-agent events), so it carries its own mutex. The
		// final snapshot is encoded into the persisted message
		// metadata under "parts" so the same view comes back
		// when the user reopens the session.
		partsAcc := newPartsAccumulator()
		// Two defers, registered LIFO so they run in this order
		// on exit:
		//   1. send idle — always fires (normal or panic), so
		//      the frontend can never get stuck thinking the
		//      session is still busy. The inner recover() guards
		//      against "send on closed channel" if `ch` is
		//      somehow closed (it shouldn't be — close(ch) is
		//      the outermost defer and runs last).
		//   2. recover from panic — catches malformed LLM
		//      responses or buggy tool handlers so the REPL
		//      doesn't die. Sends a final Error chunk.
		defer func() {
			defer func() { _ = recover() }() // guard "send on closed"
			select {
			case ch <- ChatStreamChunk{SessionStatus: "idle"}:
			case <-time.After(2 * time.Second):
			}
		}()
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				sendOrDrop(ctx, ch, ChatStreamChunk{
					Phase: "system",
					Error: fmt.Sprintf("panic in agent: %v\n\n%s", r, stack),
					Done:  true,
				})
			}
		}()
		// Announce the start of the turn. The frontend uses this
		// to drive the TodoPanel state machine (`live` becomes
		// true, so a non-empty todo list stays open). Without
		// this signal the UI has no way to tell "the LLM is
		// mid-turn, don't clear stale todos" from "the LLM
		// finished and forgot to clear them".
		sendOrDrop(ctx, ch, ChatStreamChunk{SessionStatus: "busy"})
		start := time.Now()

		sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "system", Step: "load-tools", Message: "加载工具列表..."})
		availableTools := a.tools.List()
		// Remove wiki tools when knowledge base is off. grep is a
		// general-purpose search tool and remains available.
		kbEnabled := req.KBBase != "" && req.KBBase != "__off__"
		if !kbEnabled {
			filtered := make([]tool.Tool, 0, len(availableTools))
			for _, t := range availableTools {
				if t.Name == "wiki_lookup" || t.Name == "wiki_list" {
					continue
				}
				filtered = append(filtered, t)
			}
			availableTools = filtered
		}
		toolDefs := llm.ToolsFromRegistryDef(availableTools)
		if len(toolDefs) > 0 {
			names := make([]string, 0, len(availableTools))
			for _, t := range availableTools {
				names = append(names, t.Name)
			}
			sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "tools", Step: "tools", Message: fmt.Sprintf("可用工具 (%d): %s", len(availableTools), strings.Join(names, ", "))})
		} else {
			sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "tools", Step: "tools", Message: "未注册任何工具"})
			toolDefs = nil
		}

		sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "system", Step: "load-system", Message: "合并风格 / AGENTS.md / 规则 / 技能..."})
		systemPrompt, _, err := a.buildStaticSystemPrompt(req.Style, toolDefs, req.ProjectRoot, kbEnabled)
		if err != nil {
			sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "system", Error: err.Error(), Done: true})
			return
		}
		// Inject knowledge base context + index.
		if kbEnabled {
			kbIndex := a.buildKBIndex(req.KBBase)
			systemPrompt += kbIndex
		}
		// Sub-agent prompt override. When the sub-agent runner
		// supplies a prompt (from the agent's own prompt or the
		// request's `prompt` arg), it REPLACES the normal
		// system prompt. We still append compressed summary /
		// skill context below so the child retains access to
		// any user context that was in flight.
		if req.PromptOv != "" {
			systemPrompt = req.PromptOv
		}
		sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "system", Step: "ok", Message: fmt.Sprintf("系统提示已就绪 (%d 字符)", len(systemPrompt)), Duration: formatElapsed(time.Since(start))})

		// Append compressed summary if provided (from /compress).
		if req.CompressedSummary != "" {
			systemPrompt += "\n\n[前文摘要]\n" + req.CompressedSummary
		}
		// Append active skill context (from /skillname slash command).
		if req.SkillContext != "" {
			systemPrompt += "\n\n---\n\n## 激活的技能上下文\n\n" + req.SkillContext + "\n"
		}
		// Append style memory (动态注入，不破坏静态缓存)
		if styleMemory := a.getStyleMemory(req.Style); styleMemory != "" {
			systemPrompt += "\n\n---\n\n## 我的上下文\n\n" + styleMemory
		}

		// Build the message list: system prompt + user messages.
		// Each message is a separate protocol-agnostic ChatMessage.
		msgs := []llm.ChatMessage{
			{Role: llm.RoleSystem, Type: llm.TypeText, Content: systemPrompt},
		}
		// When knowledge base is off, strip wiki-related messages
		// (tool calls + results) from history so the LLM doesn't
		// learn about wiki tools from previous turns
		// and try to call them via text-format tool_call blocks.
		if kbEnabled {
			msgs = append(msgs, req.Messages...)
		} else {
			for _, m := range req.Messages {
				if m.ToolName == "wiki_lookup" || m.ToolName == "wiki_list" {
					continue
				}
				msgs = append(msgs, m)
			}
		}

		// Expand any user-uploaded attachments into separate
		// ChatMessage entries (text msg + image/file msgs).
		if len(req.Attachments) > 0 && a.attach != nil {
			protocol := a.protocolFor(req.Provider)
			vision := func() bool { return a.modelSupportsVision(req.Provider, req.Model) }
			msgs = ExpandAttachmentsCM(protocol, msgs, req.Attachments, a.attach, vision)
			sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "system", Step: "attachments", Message: fmt.Sprintf("展开 %d 个附件", len(req.Attachments))})
		}

		if a.store != nil {
			sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "memory", Step: "memory", Message: fmt.Sprintf("写入消息到记忆")})
			// Persist all user-facing messages (including
			// image attachments as separate rows). Use the
			// per-session variant so concurrent streams on
			// different sessions don't race on the global
			// currentID.
			for _, m := range msgs {
				if m.Role == llm.RoleSystem {
					continue
				}
				a.store.AddChatMessageTo(req.SessionID, m)
			}
		}

		// Plan mode: the LLM can use `todo_write` to break down
		// the analysis into steps, and `question` to clarify vague
		// requirements. Other tools are disabled — Plan Mode is
		// for planning, not executing.
		//
		// Build mode uses a soft cap (MaxRoundsDefault) instead of
		// "unlimited". The LLM normally terminates by emitting no
		// tool calls; the cap is the safety net for stuck loops
		// (same tool call failing repeatedly, the model not
		// noticing, the loop running forever). On the last round
		// we drop the `tools` field and inject MaxStepsPrompt as
		// a fake assistant message — the model physically cannot
		// call tools and is forced to give a text summary.
		// See opencode's `runner/max-steps.ts:1-16`.
		maxRounds := MaxRoundsDefault
		// Per-request override (takes priority).
		if req.MaxRounds > 0 {
			maxRounds = req.MaxRounds
		} else if a.cfg != nil && a.cfg.Limits.MaxRounds > 0 {
			maxRounds = a.cfg.Limits.MaxRounds
		}
		if req.PlanMode {
			var planTools []llm.ToolDef
			for _, t := range toolDefs {
				if t.Name == "todo_write" || t.Name == "question" {
					planTools = append(planTools, t)
				}
			}
			toolDefs = planTools
			msgs[0].Content += "\n\n---\n\n## Plan Mode\n\n" +
				"你正在 PLAN MODE：不要调用任何执行类工具 (exec_command, write_file, task 等)。\n" +
				"你可以使用 `todo_write` 将分析/计划拆分为步骤，\n" +
				"也可以使用 `question` 向用户澄清模糊需求。\n" +
				"请给出 step-by-step 执行计划：\n" +
				"1. 每一步做什么\n" +
				"2. 每一步预期使用什么工具 (read_file, write_file, exec_command, list_files, task 等)\n" +
				"3. 每一步的预期结果\n" +
				"4. 风险 / 依赖 / 边界\n" +
				"用户审阅后切换回构建模式执行。\n"
			maxRounds = 1
			sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "plan", Step: "plan-mode", Message: "Plan Mode 启用 (可用 todo_write / question，最多单轮)"})
		} else {
			sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "plan", Step: "plan", Message: fmt.Sprintf("构建模式 — LLM 自主决定何时终止 (上限 %d 轮)", maxRounds)})
		}

		var totalIn, totalOut int

		// Stuck-loop guard. opencode's TODO comment in
		// `llm.ts:54` notes "Bound provider retries and
		// repeated identical tool calls" as unchecked work.
		// We implement a simple version: track the signature
		// of the (sorted) tool calls in each round, plus
		// whether the round ended in tool errors. If the
		// signature repeats for StuckThreshold consecutive
		// rounds AND the last round errored, we break out
		// with a "stuck" event rather than letting the LLM
		// hammer the same failing call forever.
		var (
			stuckStreak          int
			prevToolSig          string
			prevErrored          bool
			sameToolErrName      string
			sameToolErrCount     int
			nearLimitWarningSent bool
		)
		const stuckThreshold = 3
		const sameToolErrMax = 4

		for round := 1; maxRounds == 0 || round <= maxRounds; round++ {
			roundStart := time.Now()
			roundNum := round
			partsAcc = newPartsAccumulator()

			sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "llm", Step: fmt.Sprintf("round-%d", roundNum), Message: fmt.Sprintf("[第 %d 轮] 调用 LLM", roundNum), Round: roundNum, MaxRound: maxRounds})

			var (
				fullContent         string
				fullThinking        string
				toolCalls           []nativeToolCall
				argsAccum           = make(map[int]*nativeToolCall)
				roundAnyToolErrored bool
			)

			opts := a.options
			if req.ReasoningEffort != "" {
				opts.ReasoningEffort = req.ReasoningEffort
			}

			// Per-round request assembly. On the last round we
			// drop the `tools` field and inject MaxStepsPrompt as
			// a fake assistant message — the model physically
			// cannot call tools and is forced to give a text
			// summary. See opencode's `runner/max-steps.ts:1-16`
			// and `llm.ts:197-209`.
			isLastRound := maxRounds > 0 && round >= maxRounds

			// Pre-limit warning: when within 10 rounds of the
			// cap, inject a gentle heads-up so the LLM can wrap
			// up gracefully instead of being cut off abruptly.
			// Only injected once — the flag ensures no spam.
			if !nearLimitWarningSent && maxRounds > 10 && round >= maxRounds-10 && !isLastRound {
				nearLimitWarningSent = true
				msgs = append(msgs, llm.ChatMessage{
					Role:    llm.RoleSystem,
					Type:    llm.TypeText,
					Content: fmt.Sprintf("注意：当前会话轮次即将达到上限（%d 轮，剩余约 %d 轮）。请开始收尾当前任务，优先完成最关键的未完成工作，避免开启需要多轮的新子任务。", maxRounds, maxRounds-round),
				})
				sendOrDrop(ctx, ch, ChatStreamChunk{
					Phase:    "llm",
					Step:     "near-limit",
					Message:  fmt.Sprintf("轮次接近上限（剩余 %d 轮），提醒 LLM 收尾", maxRounds-round),
					Round:    roundNum,
					MaxRound: maxRounds,
				})
			}

			// Auto-compact before LLM call (skip on last round).
			// If the context exceeds the token budget, compress
			// and rebuild the system prompt so the provider call
			// doesn't fail with a 413. On the last round tools
			// are disabled anyway so compact isn't worth it.
			if !isLastRound && a.tryAutoCompact(ctx, &msgs, req, ch, roundNum, maxRounds) {
				continue
			}

			roundMsgs := msgs
			roundTools := toolDefs
			if isLastRound {
				roundTools = nil
				roundMsgs = append([]llm.ChatMessage{}, msgs...)
				roundMsgs = append(roundMsgs, llm.ChatMessage{
					Role:    llm.RoleAssistant,
					Type:    llm.TypeText,
					Content: MaxStepsPrompt,
				})
				sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "llm", Step: "max-steps", Message: "已达到轮次上限 — 强制文本回复（不再调用工具）", Round: roundNum, MaxRound: maxRounds})
			}

			// LLM stream with retry for recoverable errors
			// (rate_limit, server_error, network, timeout).
			const maxLLMRetries = 3
			var retryableErr error
			att:
			for attempt := 1; attempt <= maxLLMRetries; attempt++ {
				if attempt > 1 {
					backoff := time.Duration(attempt*attempt) * time.Second
					sendOrDrop(ctx, ch, ChatStreamChunk{
						Phase:    "llm",
						Step:     "retry",
						Message:  fmt.Sprintf("%s 后重试 (第 %d/%d 次)…", backoff, attempt, maxLLMRetries),
						Round:    roundNum,
						MaxRound: maxRounds,
					})
					select {
					case <-ctx.Done():
						return
					case <-time.After(backoff):
					}
				}

				stream := a.llm.ChatStreamCM(ctx, req.Provider, req.Model, normalizeToolResults(roundMsgs), roundTools, opts)
				for chunk := range stream {
					if chunk.Err != nil {
						classified := llm.ClassifyAPIError(req.Provider, chunk.Err)
						errMsg, errSuggestion, errKind := chunk.Err.Error(), "", ""
						if apiErr, ok := classified.(*llm.APIError); ok {
							errMsg = apiErr.Message
							errSuggestion = apiErr.Suggestion
							errKind = apiErr.Kind.String()
							if isRetryable(apiErr.Kind) && attempt < maxLLMRetries {
								retryableErr = chunk.Err
								break // break inner stream loop, retry outer
							}
						}
						sendOrDrop(ctx, ch, ChatStreamChunk{
							Phase:      "llm",
							Error:      errMsg,
							Suggestion: errSuggestion,
							ErrorKind:  errKind,
							Done:       true,
						})
						return
					}
					if chunk.Done {
						retryableErr = nil
						break att // success — break outer loop too
					}
				if chunk.TokensIn > 0 || chunk.TokensOut > 0 {
					if chunk.TokensIn > totalIn {
						totalIn = chunk.TokensIn
					}
					if chunk.TokensOut > totalOut {
						totalOut = chunk.TokensOut
					}
				}
				if chunk.Content != "" {
					fullContent += chunk.Content
					partsAcc.update(ChatStreamChunk{Content: chunk.Content})
					sendOrDrop(ctx, ch, ChatStreamChunk{Content: chunk.Content, TokensIn: totalIn, TokensOut: totalOut})
				}
				if chunk.Thinking != "" {
					fullThinking += chunk.Thinking
					partsAcc.update(ChatStreamChunk{Thinking: chunk.Thinking})
					sendOrDrop(ctx, ch, ChatStreamChunk{Thinking: chunk.Thinking, TokensIn: totalIn, TokensOut: totalOut})
				}
				if chunk.ToolCallDelta != nil {
					tcd := chunk.ToolCallDelta
					existing, ok := argsAccum[tcd.Index]
					if !ok {
						existing = &nativeToolCall{ID: tcd.ID, Name: tcd.Name}
						argsAccum[tcd.Index] = existing
					}
					if tcd.ID != "" {
						existing.ID = tcd.ID
					}
					if tcd.Name != "" {
						existing.Name = tcd.Name
					}
					existing.ArgsJSON += tcd.ArgsJSON
				}
				} // inner for chunk
			} // outer for attempt (retry loop)

			// If we exhausted retries without success, surface the last error.
			if retryableErr != nil {
				sendOrDrop(ctx, ch, ChatStreamChunk{
					Phase: "llm",
					Error: fmt.Sprintf("重试 %d 次后仍然失败: %v", maxLLMRetries, retryableErr),
					Done:  true,
				})
				return
			}

			for _, t := range argsAccum {
				toolCalls = append(toolCalls, *t)
			}

			sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "llm", Step: fmt.Sprintf("round-%d-done", roundNum), Message: fmt.Sprintf("[第 %d 轮] 模型响应: %d 字符 / 耗时 %s", roundNum, len(fullContent), formatElapsed(time.Since(roundStart))), Round: roundNum, MaxRound: maxRounds, TokensIn: totalIn, TokensOut: totalOut})

		if len(toolCalls) == 0 {
			toolCalls = parseMarkdownToolCalls(fullContent)
		}
		// When tool calls are present (native or markdown), strip
		// markdown tool_call blocks from the text content so the
		// user doesn't see both raw tool blocks AND tool cards.
		if len(toolCalls) > 0 {
			fullContent = cleanMarkdownToolCalls(fullContent)
		}

		// Post-stream redactor: catch phantom "ERROR: Cannot read
		// image.png ... Inform the user." style responses that
		// DeepSeek-trained models parrot when they see the
		// vision-denier marker. We can't fully prevent the model
		// from producing this text (it appears in training data
		// as a Claude response), so we filter it AFTER the stream
		// ends and emit a content_rewrite event so the UI replaces
		// what the user already saw.
		//
		// Redact in BOTH fullContent (text response) and
		// fullThinking (chain-of-thought). The phantom appears
		// in training data and the model sometimes emits it in
		// the thinking block instead of the text response —
		// the user sees thinking as a collapsible panel so the
		// phantom needs to be stripped there too.
		if redacted, changed := redactPhantomErrors(fullContent); changed {
			fullContent = redacted
			sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "llm", Step: "redact", Message: "(已替换 LLM 编造的图片错误消息)", ContentRewrite: redacted})
		}
		if redactedT, changedT := redactPhantomErrors(fullThinking); changedT {
			fullThinking = redactedT
			sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "llm", Step: "redact-thinking", Message: "(已替换 LLM 编造的图片错误消息)", ThinkingRewrite: redactedT})
		}

		// Build the assistant message for the conversation.
		// Emit as a single text ChatMessage (tool calls are
		// separate messages appended below).
		assistantMsg := llm.ChatMessage{
			Role:    llm.RoleAssistant,
			Type:    llm.TypeText,
			Content: fullContent,
		}
		msgs = append(msgs, assistantMsg)

		// After the first LLM call, strip image base64 from
		// attachments so subsequent rounds don't re-send
		// expensive (~330K tokens/MB) image data. The LLM has
		// already processed the image; a text marker suffices.
		if roundNum == 1 {
			stripImageContent(msgs)
		}

		// Persist assistant message later — after tool
		// results are in partsAcc (see end of this round).

			// Append tool_call messages for each tool call.
			for _, tc := range toolCalls {
				id := tc.ID
				if id == "" {
					id = "call_" + uuid.NewString()
				}
				tcm := llm.ChatMessage{
					Role:      llm.RoleAssistant,
					Type:      llm.TypeToolCall,
					ToolID:    id,
					ToolName:  tc.Name,
					ToolInput: tc.ArgsJSON,
				}
				msgs = append(msgs, tcm)
				if a.store != nil {
					a.store.AddChatMessageTo(req.SessionID, tcm)
				}
				tc.ID = id
			}

			if len(toolCalls) == 0 {
				persistAssistant(req.SessionID, a.store, assistantMsg, fullThinking, partsAcc, totalIn, totalOut)
				sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "done", Step: "done", Message: fmt.Sprintf("完成 (总耗时 %s, 共 %d 轮)", formatElapsed(time.Since(start)), roundNum), Round: roundNum, MaxRound: maxRounds, TokensIn: totalIn, TokensOut: totalOut})
				sendOrDrop(ctx, ch, ChatStreamChunk{Done: true})
				return
			}

			// Auto-compact: estimate token budget before the
			// next LLM turn. If the total estimated tokens
			// exceed the usable context window, compress the
			// oldest messages and continue. Mirrors opencode's
			// `compactIfNeeded()` + Claude Code's auto-compact.
			if a.tryAutoCompact(ctx, &msgs, req, ch, roundNum, maxRounds) {
				persistAssistant(req.SessionID, a.store, assistantMsg, fullThinking, partsAcc, totalIn, totalOut)
				continue
			}

			sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "tool", Step: fmt.Sprintf("round-%d-tools", roundNum), Message: fmt.Sprintf("[第 %d/%d 轮] 检测到 %d 个工具调用", roundNum, maxRounds, len(toolCalls)), Round: roundNum, MaxRound: maxRounds})

			// Run tool calls in parallel when the LLM emitted more than one.
			// Each call gets its own per-tool timeout derived from the parent
			// ctx; the parent ctx is checked before sending events so a
			// cancellation cleanly aborts the round.
			type toolOutcome struct {
				idx     int
				tc      nativeToolCall
				result  *tool.CallResult
				err     error
				elapsed time.Duration
			}
			outcomes := make([]toolOutcome, len(toolCalls))

			// Emit "starting" events for all calls (in order) before launching.
			for i, tc := range toolCalls {
				if tc.ID == "" {
					tc.ID = "call_" + uuid.NewString()
					toolCalls[i].ID = tc.ID
				}
				argsPreview := tc.ArgsJSON
				if len(argsPreview) > 200 {
					argsPreview = argsPreview[:200] + "..."
				}
				startChunk := ChatStreamChunk{Phase: "tool", Step: fmt.Sprintf("call-%d", i+1), Message: fmt.Sprintf("  -> 工具 %d/%d: %s", i+1, len(toolCalls), tc.Name), ToolID: tc.ID, ToolName: tc.Name, ToolArgs: argsPreview, Round: roundNum, MaxRound: maxRounds}
				partsAcc.update(startChunk)
				sendOrDrop(ctx, ch, startChunk)
			}

			// Each tool call gets its own event channel. The agent loop
			// launches a forwarder per channel and collects the done
			// signals so we can wait for all events to flush before
			// emitting the per-call result events below.
			type forwarder struct{ done chan struct{} }
			var forwarders []forwarder
			var wg sync.WaitGroup
			for i, tc := range toolCalls {
				wg.Add(1)

				// Per-tool event channel. Buffer is 64 (was 16) so
				// a sub-agent producing many content/thinking/tool
				// chunks can never fill it up and drop the closing
				// sub_agent_ok / sub_agent_err event — which would
				// leave the GUI's nested card stuck in the "running"
				// spinner forever. 64 is comfortably larger than
				// the chunk count a single sub-agent turn produces
				// in practice; if a turn ever needs more, the
				// forwarder will drain it before the next push.
				eventCh := make(chan ChatStreamChunk, 64)
				tctx := context.WithValue(ctx, toolEventChanKey{}, eventCh)
				if a.subagentRegistry != nil {
					tctx = WithSubagentRegistry(tctx, a.subagentRegistry)
				}
				// Publish the parent turn's current (provider, model)
				// pair so the sub-agent tool handler can inherit the
				// same model the user selected for the main
				// conversation. Without this the sub-agent would
				// fall back to whatever the server's startup
				// default was, which silently breaks model
				// selection when the user has switched models
				// mid-session.
				tctx = WithParentModel(tctx, req.Provider, req.Model)
				if req.SessionID != "" {
					tctx = tool.WithSessionID(tctx, req.SessionID)
				}
				if req.PermissionLevel != "" {
					tctx = tool.WithPermissionLevel(tctx, req.PermissionLevel)
				}
				if req.ProjectRoot != "" {
					tctx = tool.WithProjectRoot(tctx, req.ProjectRoot)
				}
				// Inject event sender so the question tool can
				// emit "question" events through the SSE stream.
				tctx = tool.WithEventSender(tctx, func(jsonData string) {
					select {
					case eventCh <- ChatStreamChunk{QuestionJSON: jsonData}:
					case <-time.After(2 * time.Second):
						log.Printf("[question] dropped event (channel full for 2s)")
					}
				})
				tctx, cancel := context.WithTimeout(tctx, 5*time.Minute)

				fwd := forwarder{done: make(chan struct{})}
				forwarders = append(forwarders, fwd)
				go func() {
					defer close(fwd.done)
					defer func() {
						if r := recover(); r != nil {
							log.Printf("[forwarder] panic: %v", r)
						}
					}()
					for ev := range eventCh {
						// Sub-agent / tool events arrive here
						// from the per-tool dispatcher. Feed
						// them into the parts accumulator so
						// nested cards survive a session
						// reload, then forward to the main
						// channel for the live UI.
						if ev.QuestionJSON != "" {
							log.Printf("[forwarder] got question event (%d bytes)", len(ev.QuestionJSON))
						}
						partsAcc.update(ev)
						sendOrDrop(ctx, ch, ev)
						if ev.QuestionJSON != "" {
							log.Printf("[forwarder] forwarded question event to main ch")
						}
					}
				}()

				go func(i int, tc nativeToolCall) {
					defer wg.Done()
					defer cancel()
					defer close(eventCh)

					// Sub-agent concurrency gate: if the
					// semaphore is set, only N task calls
					// can run in parallel. Other tools are
					// not limited.
					if tc.Name == "task" && a.subAgentSem != nil {
						select {
						case a.subAgentSem <- struct{}{}:
							defer func() { <-a.subAgentSem }()
						case <-ctx.Done():
							return
						}
					}

					handler, ok := a.tools.Get(tc.Name)
					if !ok {
						errMsg := fmt.Sprintf("error: tool %q not found (available: %s)", tc.Name, availableToolNames(availableTools))
						outcomes[i] = toolOutcome{
							idx:    i,
							tc:     tc,
							result: &tool.CallResult{Content: errMsg, IsError: true},
							err:    fmt.Errorf("not found"),
						}
						return
					}

					// Refuse wiki/grep tools when knowledge base is
					// disabled for this session. The tools may still
					// be registered globally (KB enabled at startup)
					// but are blocked at dispatch time so the LLM
					// cannot call them via text-format tool_call
					// blocks even if they appear in conversation
					// history from a previous KB-enabled turn.
					if !kbEnabled && (tc.Name == "wiki_lookup" || tc.Name == "wiki_list") {
						errMsg := fmt.Sprintf("error: knowledge base is disabled for this session — enable it in settings to use %s", tc.Name)
						outcomes[i] = toolOutcome{
							idx:    i,
							tc:     tc,
							result: &tool.CallResult{Content: errMsg, IsError: true},
							err:    fmt.Errorf("kb disabled"),
						}
						return
					}

					toolStart := time.Now()
					argsRaw := json.RawMessage(tc.ArgsJSON)
					if len(argsRaw) == 0 {
						argsRaw = json.RawMessage("{}")
					}
					// Inject the sandbox into the tool's context so
					// built-in tools (exec_command, write_file) can
					// short-circuit dangerous operations. If the user
					// ran /unsafe once, the next call bypasses the
					// sandbox check entirely.
					//
					// Permission level (per-session) controls sandbox
					// behaviour:
					//   "full" — skip all sandbox checks
					//   "auto" — auto-approve confirm decisions
					//   "ask"  — normal confirm flow (default)
					toolCtx := tctx
					permLevel, _ := tctx.Value(tool.PermissionLevelKey{}).(string)
					if permLevel == "" {
						permLevel = tool.PermissionAsk
					}
					bypass := a.bypassOnce.Swap(false)
					sandboxActive := a.sandbox != nil && !bypass && permLevel != tool.PermissionFull
					if sandboxActive {
						toolCtx = tool.WithSandbox(tctx, a.sandbox)

						// Confirm-check for dangerous tools.
						// If the sandbox returns Confirm, pause and
						// wait for user approval before executing
						// (unless permission level is "auto").
						if tc.Name == "exec_command" || tc.Name == "write_file" {
							var decision tool.SandboxDecision
							var reason string
							if tc.Name == "exec_command" {
								var ea struct{ Command string `json:"command"` }
								json.Unmarshal([]byte(tc.ArgsJSON), &ea)
								command := ea.Command
								if command == "" {
									command = tc.ArgsJSON
								}
								decision = a.sandbox.CheckExecDecision(command)
								reason = a.sandbox.MatchedPattern(command)
							} else {
								var wa struct{ Path string `json:"path"` }
								json.Unmarshal([]byte(tc.ArgsJSON), &wa)
								decision = a.sandbox.CheckWriteDecision(wa.Path)
							}
							if decision == tool.SandboxConfirm {
								if permLevel == tool.PermissionAuto {
									// Auto-approve: skip confirm modal,
									// emit a brief notification event instead.
									select {
									case eventCh <- ChatStreamChunk{
										Content: fmt.Sprintf("🔓 [自动通过] %s", tc.Name),
									}:
									default:
									}
								} else {
									// Normal confirm flow.
									cfm := tool.ConfirmRequest{
										ToolName: tc.Name,
										Args:     tc.ArgsJSON,
										Reason:   reason,
									}
									var sessionID string
									if sid, ok := tctx.Value(tool.SessionIDKey{}).(string); ok {
										sessionID = sid
									}
									select {
									case eventCh <- ChatStreamChunk{ToolConfirmJSON: tool.MarshalConfirm(cfm)}:
									default:
									}
									approved, cfmErr := tool.WaitForConfirm(toolCtx, sessionID, cfm)
									if cfmErr != nil || !approved {
										outcomes[i] = toolOutcome{
											idx: i,
											tc:  tc,
											result: &tool.CallResult{
												Content: "工具调用被用户拒绝",
												IsError: true,
											},
										}
										return
									}
								}
							}
						}
					}
					// Persist question before blocking for user input.
					if tc.Name == "question" && a.store != nil {
						persistQuestion(req.SessionID, a.store, string(argsRaw))
					}
					result, err := handler(toolCtx, argsRaw)
					// Persist answer after user responds.
					if tc.Name == "question" && result != nil && a.store != nil {
						persistQuestionAnswer(req.SessionID, a.store, result.Content)
					}
					outcomes[i] = toolOutcome{
						idx:     i,
						tc:      tc,
						result:  result,
						err:     err,
						elapsed: time.Since(toolStart),
					}
				}(i, tc)
			}
			// Wait for all tool goroutines to finish, then wait for all
			// forwarders to drain. This ensures per-tool events are
			// emitted in order before the result events below.
			wg.Wait()
			for _, f := range forwarders {
				<-f.done
			}

			// Emit completion events in the original order so the UI shows
			// results in the same order as the calls.
			for i := range outcomes {
				o := &outcomes[i]
				tc := o.tc
				toolElapsed := formatElapsed(o.elapsed)
				argsPreview := tc.ArgsJSON
				if len(argsPreview) > 200 {
					argsPreview = argsPreview[:200] + "..."
				}

				if o.err != nil || o.result == nil {
					errMsg := "unknown error"
					if o.result != nil {
						errMsg = o.result.Content
					} else if o.err != nil {
						errMsg = o.err.Error()
					}
					errChunk := ChatStreamChunk{Phase: "tool", Step: fmt.Sprintf("call-%d-err", i+1), Message: fmt.Sprintf("     X %s 执行失败 (%s): %s", tc.Name, toolElapsed, errMsg), ToolID: tc.ID, ToolName: tc.Name, ToolError: errMsg, ToolElapsed: toolElapsed, Round: roundNum, MaxRound: maxRounds}
					partsAcc.update(errChunk)
					sendOrDrop(ctx, ch, errChunk)
					toolMsg := llm.ChatMessage{
						Role:      llm.RoleTool,
						Type:      llm.TypeToolResult,
						Content:   fmt.Sprintf("Tool %s failed: %s", tc.Name, errMsg),
						ToolID:    tc.ID,
						ToolName:  tc.Name,
						ToolError: true,
					}
					msgs = append(msgs, toolMsg)
					if a.store != nil {
						a.store.AddChatMessageTo(req.SessionID, toolMsg)
					}
					continue
				}

				result := o.result
				resultPreview := result.Content
				if len(resultPreview) > 300 {
					resultPreview = resultPreview[:300] + "..."
				}
				resultPreview = strings.Map(func(r rune) rune {
					if r == '\n' || r == '\r' {
						return ' '
					}
					return r
				}, resultPreview)

				if result.IsError {
					roundAnyToolErrored = true
					warnChunk := ChatStreamChunk{Phase: "tool", Step: fmt.Sprintf("call-%d-warn", i+1), Message: fmt.Sprintf("     ! %s 返回错误 (%s)", tc.Name, toolElapsed), ToolID: tc.ID, ToolName: tc.Name, ToolResult: resultPreview, ToolError: "tool returned error", ToolElapsed: toolElapsed, Round: roundNum, MaxRound: maxRounds}
					partsAcc.update(warnChunk)
					sendOrDrop(ctx, ch, warnChunk)
				} else {
					okChunk := ChatStreamChunk{Phase: "tool", Step: fmt.Sprintf("call-%d-ok", i+1), Message: fmt.Sprintf("     ok %s 完成 (%s, %d 字节)", tc.Name, toolElapsed, len(result.Content)), ToolID: tc.ID, ToolName: tc.Name, ToolResult: resultPreview, ToolElapsed: toolElapsed, Round: roundNum, MaxRound: maxRounds}
					// For tools whose result the frontend needs to
					// *parse* (todo_write), also send the untruncated
					// payload. Truncated newlines → spaces and the 300
					// char cap both corrupt JSON. The frontend uses
					// ToolResultFull in preference to ToolResult when
					// present.
					if tc.Name == "todo_write" || tc.Name == "question" {
						okChunk.ToolResultFull = result.Content
					}
					partsAcc.update(okChunk)
					sendOrDrop(ctx, ch, okChunk)
				}

			llmContent := result.Content
			if result.IsError {
				// The structured IsError flag on the
				// ChatMessage is what tells the LLM this
				// is an error; the content is the
				// diagnostic text. Keep it terse and
				// factual — opencode-style. Don't
				// hand-hold the model with "请分析错误
				// 原因后调整方案并重试" boilerplate,
				// and never instruct it to fabricate
				// user-facing error messages.
				llmContent = fmt.Sprintf("Tool %s returned an error: %s", tc.Name, result.Content)
			} else {
				llmContent = a.truncateToolResult(tc.Name, result.Content)
			}
				toolMsg := llm.ChatMessage{
					Role:      llm.RoleTool,
					Type:      llm.TypeToolResult,
					Content:   llmContent,
					ToolID:    tc.ID,
					ToolName:  tc.Name,
					ToolError: result.IsError,
				}
				msgs = append(msgs, toolMsg)
				if a.store != nil {
					a.store.AddChatMessageTo(req.SessionID, toolMsg)
				}
			}
			// Persist assistant message now that tool
			// results are captured in partsAcc.
			persistAssistant(req.SessionID, a.store, assistantMsg, fullThinking, partsAcc, totalIn, totalOut)

			// Stuck-loop guard. Compute a stable signature of
			// this round's tool calls and whether any errored.
			// If the same signature repeats for stuckThreshold
			// consecutive rounds AND the last round errored,
			// break out with a "stuck" event — the LLM is
			// clearly not making progress, and the opencode
			// TODO in `llm.ts:54` calls this out as unchecked
			// work.
			curSig := toolCallSignature(toolCalls)
			curErrored := roundAnyToolErrored
			if curSig != "" && curSig == prevToolSig && curErrored && prevErrored {
				stuckStreak++
			} else {
				stuckStreak = 0
			}
			prevToolSig = curSig
			prevErrored = curErrored
			if stuckStreak >= stuckThreshold {
				sendOrDrop(ctx, ch, ChatStreamChunk{
					Phase:   "stuck",
					Step:    "stuck-loop",
					Message: fmt.Sprintf("已连续 %d 轮以相同的工具调用失败，疑似陷入循环。自动停止。", stuckStreak+1),
					Round:   roundNum,
					MaxRound: maxRounds,
					TokensIn: totalIn, TokensOut: totalOut,
				})
				sendOrDrop(ctx, ch, ChatStreamChunk{Done: true})
				return
			}

			// Same-tool-name error counter: if a single tool
			// errors N times in a row (even with different
			// args — the stuck-loop guard only catches
			// identical-args loops), inject a system message
			// telling the LLM to stop retrying.
			if roundAnyToolErrored && len(toolCalls) == 1 {
				name := toolCalls[0].Name
				if name == sameToolErrName {
					sameToolErrCount++
				} else {
					sameToolErrName = name
					sameToolErrCount = 1
				}
			} else {
				sameToolErrName = ""
				sameToolErrCount = 0
			}
			if sameToolErrCount >= sameToolErrMax {
				msgs = append(msgs, llm.ChatMessage{
					Role:    llm.RoleSystem,
					Type:    llm.TypeText,
					Content: fmt.Sprintf("工具 `%s` 已连续失败 %d 次。不要重试 — 改用其他方式完成任务（如 read_file、list_files、或 task 子代理）。", sameToolErrName, sameToolErrCount),
				})
				sendOrDrop(ctx, ch, ChatStreamChunk{
					Phase:   "limit",
					Step:    "same-tool-err-limit",
					Message: fmt.Sprintf("%s 已连续失败 %d 次，改用其他方式。", sameToolErrName, sameToolErrCount),
					Round:   roundNum,
				})
				sameToolErrCount = 0
			}

			// Tool result pruning: remove old tool output content
			// after N rounds to keep the context lean. Protects
			// the most recent rounds so the LLM still sees
			// immediately-relevant results.
			pr := pruneAfterRounds
			if a.cfg != nil && a.cfg.Limits.PruneAfterRounds > 0 {
				pr = a.cfg.Limits.PruneAfterRounds
			}
			pruneOldToolResults(msgs, roundNum, pr)
		}

		if maxRounds > 0 {
			sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "limit", Step: "max-rounds", Message: fmt.Sprintf("已达到 %d 轮上限 (总耗时 %s)。LLM 已强制给出文本总结。", maxRounds, formatElapsed(time.Since(start))), Round: maxRounds, MaxRound: maxRounds, TokensIn: totalIn, TokensOut: totalOut})
		}
		sendOrDrop(ctx, ch, ChatStreamChunk{Done: true})
	}()

	return ch
}

func formatElapsed(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
}

// normalizeToolResults removes TypeToolCall metadata rows and
// converts TypeToolResult messages to User role. This way providers
// that validate tool_call/tool_result pairing (DeepSeek, etc.) see
// normal user-assistant-user conversation, and the LLM still sees
// tool results as part of the ongoing dialogue.
func normalizeToolResults(msgs []llm.ChatMessage) []llm.ChatMessage {
	out := make([]llm.ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Type == llm.TypeToolCall {
			continue
		}
		if m.Type == llm.TypeToolResult {
			m.Role = llm.RoleUser
		}
		out = append(out, m)
	}
	return out
}

func persistAssistant(convID string, store *memory.Store, msg llm.ChatMessage, fullThinking string, partsAcc *partsAccumulator, tokensIn int, tokensOut int) {
	if store == nil {
		return
	}
	meta := map[string]string{
		"role":   msg.Role,
		"reason": "assistant",
	}
	if fullThinking != "" {
		meta["thinking"] = fullThinking
	}
	if tokensIn > 0 {
		meta["tokens_in"] = fmt.Sprintf("%d", tokensIn)
	}
	if tokensOut > 0 {
		meta["tokens_out"] = fmt.Sprintf("%d", tokensOut)
	}
	structural := snapshotStructural(partsAcc)
	if len(structural) > 0 {
		if pj, pjErr := json.Marshal(structural); pjErr == nil {
			meta["parts"] = string(pj)
		}
	}
	store.AddChatMessageWithMetaTo(convID, msg, meta)
}

// persistQuestion stores the question tool's arguments as a
// msg_type=6 message so the question table is visible in
// session history after reload.
func persistQuestion(convID string, store *memory.Store, questionsJSON string) {
	if store == nil || questionsJSON == "" {
		return
	}
	msg := llm.ChatMessage{
		Role:        llm.RoleAssistant,
		Type:        llm.TypeText,
		MsgType:     llm.MsgTypeQuestion,
		SubmitToLLM: 1,
		Content:     questionsJSON,
	}
	store.AddChatMessageTo(convID, msg)
}

// persistQuestionAnswer stores the user's question answers as a
// msg_type=6 message, paired with the preceding question message.
func persistQuestionAnswer(convID string, store *memory.Store, answersJSON string) {
	if store == nil || answersJSON == "" {
		return
	}
	msg := llm.ChatMessage{
		Role:        llm.RoleUser,
		Type:        llm.TypeText,
		MsgType:     llm.MsgTypeQuestion,
		SubmitToLLM: 1,
		Content:     answersJSON,
	}
	store.AddChatMessageTo(convID, msg)
}

// buildToolHint generates a minimal markdown-block fallback instruction
// for models that don't support native OpenAI tool_calls. The personality-
// specific tool calling style is already documented in the soul/*.md file,
// so this section is intentionally tiny (to maximize the prefix cache hit
// on the system prompt).
func buildToolHint(tools []tool.Tool) string {
	if len(tools) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Tool Fallback\n\n")
	b.WriteString("If native tool_calls is unavailable, emit a single ```tool_call``` block:\n\n")
	b.WriteString("```tool_call\n")
	b.WriteString(`{"name": "<name>", "arguments": {<json>}}`)
	b.WriteString("\n```\n")
	return b.String()
}

func availableToolNames(tools []tool.Tool) string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	return strings.Join(names, ", ")
}

// toolCallSignature returns a stable, sorted string
// representing the (name, args) of every tool call in the
// round. Used by the stuck-loop guard to detect the LLM
// hammering the same failing call. Returns "" if there are
// no tool calls (a "no progress" round is not a stuck round —
// the LLM may have answered with text).
// tryAutoCompact checks the message list's estimated token count
// against the usable context window. If exceeded and a summarizer is
// available, it compresses the oldest messages, backfills the summary
// into the system prompt, emits a status chunk, and returns true
// ("caller should continue to the next round"). Returns false when no
// compaction is needed or no summarizer is wired.
func (a *Agent) tryAutoCompact(
	ctx context.Context,
	msgs *[]llm.ChatMessage,
	req ChatRequest,
	ch chan<- ChatStreamChunk,
	roundNum, maxRounds int,
) bool {
	if a.summarizer == nil || a.store == nil || req.SessionID == "" {
		return false
	}
	total := llm.EstimateTokensMessages(*msgs)
	ctxWindow := a.llm.ContextWindow(req.Provider, req.Model)
	buf := llm.AutoCompactBuffer
	if a.cfg != nil && a.cfg.Limits.AutoCompactBuffer > 0 {
		buf = a.cfg.Limits.AutoCompactBuffer
	}
	if !llm.ShouldCompactWithBuf(total, ctxWindow, buf) {
		return false
	}

	sendOrDrop(ctx, ch, ChatStreamChunk{
		Phase:    "compact",
		Step:     "auto-compact",
		Message:  fmt.Sprintf("上下文接近上限 (≈%d / %d tokens)，自动压缩历史…", total, llm.UsableContextWithBuf(ctxWindow, buf)+buf),
		Round:    roundNum,
		MaxRound: maxRounds,
	})

	ok, summary, err := a.summarizer.Compress(ctx, req.SessionID)
	if err != nil || !ok {
		sendOrDrop(ctx, ch, ChatStreamChunk{
			Phase:   "compact",
			Step:    "auto-compact-fail",
			Message: fmt.Sprintf("自动压缩失败: %v", err),
			Round:   roundNum,
			MaxRound: maxRounds,
		})
		// Fallback: hard-truncate the message list so the LLM
		// call doesn't fail with a 413. Drop oldest non-system
		// messages to stay within the usable context window.
		usable := llm.UsableContextWithBuf(ctxWindow, buf)
		if usable > 0 {
			truncateToFit(msgs, usable)
			sendOrDrop(ctx, ch, ChatStreamChunk{
				Phase:    "compact",
				Step:     "auto-compact-fallback",
				Message:  fmt.Sprintf("压缩失败，已截断上下文至 ≈%d tokens", llm.EstimateTokensMessages(*msgs)),
				Round:    roundNum,
				MaxRound: maxRounds,
			})
			_ = summary
			return true
		}
		return false
	}

	sendOrDrop(ctx, ch, ChatStreamChunk{
		Phase:   "compact",
		Step:    "auto-compact-ok",
		Message: "上下文已压缩，继续执行…",
		Round:   roundNum,
		MaxRound: maxRounds,
	})

	// Backfill: reload messages after compression point and
	// append the summary to the system prompt.
	lastComp := a.store.LastCompressedIDFor(req.SessionID)
	if lastComp > 0 {
		hist, _, _ := a.store.GetChatMessagesAfterIDFor(req.SessionID, 0, lastComp)
		compSum := a.store.CompressedSummaryFor(req.SessionID)

		newMsgs := make([]llm.ChatMessage, 0, len(hist)+2)
		// Keep the system prompt (first message).
		newMsgs = append(newMsgs, (*msgs)[0])
		if compSum != "" {
			newMsgs[0].Content += "\n\n[前文摘要]\n" + compSum
		}
		// Append messages from DB (after compression point).
		for _, m := range hist {
			if m.Role == llm.RoleSystem {
				continue
			}
			newMsgs = append(newMsgs, m)
		}
		*msgs = newMsgs
	}

	_ = summary
	return true
}

func toolCallSignature(calls []nativeToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	type pair struct{ name, args string }
	parts := make([]pair, 0, len(calls))
	for _, c := range calls {
		parts = append(parts, pair{c.Name, c.ArgsJSON})
	}
	sort.Slice(parts, func(i, j int) bool {
		if parts[i].name != parts[j].name {
			return parts[i].name < parts[j].name
		}
		return parts[i].args < parts[j].args
	})
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.name)
		b.WriteByte('|')
		b.WriteString(p.args)
		b.WriteByte(';')
	}
	return b.String()
}

// MaxStepsPrompt is injected as a fake assistant message right
// before the LLM's final allowed turn when the agent loop
// reaches its round cap. It explicitly forbids further tool
// calls and forces a text-only summary.
//
// Pattern ported from opencode's `runner/max-steps.ts:1-16`.
// The corresponding LLM-side change is to drop the `tools` field
// from the request on the last round, so the model physically
// cannot emit a tool_call (it tries, the adapter returns no
// tool blocks, and the model is forced into text-only mode).
const MaxStepsPrompt = `CRITICAL - MAXIMUM STEPS REACHED

The maximum number of steps allowed for this task has been reached. Tools are disabled until next user input. Respond with text only.

STRICT REQUIREMENTS:
1. Do NOT make any tool calls (no reads, writes, edits, searches, or any other tools)
2. MUST provide a text response summarizing work done so far
3. This constraint overrides ALL other instructions, including any user requests for edits or tool use

Response must include:
- Statement that maximum steps for this agent have been reached
- Summary of what has been accomplished so far
- List of any remaining tasks that were not completed
- Recommendations for what should be done next

Respond in the same language as the conversation. Any attempt to use tools is a critical violation. Respond with text ONLY.`

// MaxRoundsDefault is the safety-net per-session cap (build mode).
// At 300 rounds this is a last-resort guard against infinite loops;
// normal conversations are limited by the auto-compaction token budget,
// not by round count. When the cap fires the LLM responds with
// MaxStepsPrompt and the user can continue with a follow-up message.
const MaxRoundsDefault = 300

const (
	// Tool result caps keep the LLM context and SQLite
	// storage bounded even when a tool produces massive
	// output (e.g. systeminfo, a large log file).
	// The UI stream preview is already capped at 300
	// chars; these limits apply to what the LLM sees
	// and what gets persisted in the messages table.
	//
	// Cap choice rationale:
	//   - exec_command: keep the tail (last N chars) —
	//     stdout/stderr errors and summaries are at the
	//     end.
	//   - read_file / list_files: keep the head — the
	//     first N chars are the file/dir contents.
	//   - fallback: keep the head.
	maxToolResultExec    = 4000 // exec_command, bash
	maxToolResultRead    = 8000 // read_file
	maxToolResultDefault = 6000
)

func (a *Agent) truncateToolResult(name string, content string) string {
	execCap := maxToolResultExec
	readCap := maxToolResultRead
	defaultCap := maxToolResultDefault
	if a.cfg != nil {
		if a.cfg.Limits.ToolResultExecCap > 0 {
			execCap = a.cfg.Limits.ToolResultExecCap
		}
		if a.cfg.Limits.ToolResultReadCap > 0 {
			readCap = a.cfg.Limits.ToolResultReadCap
		}
		if a.cfg.Limits.ToolResultDefaultCap > 0 {
			defaultCap = a.cfg.Limits.ToolResultDefaultCap
		}
	}

	if len(content) <= defaultCap {
		return content
	}

	var cap_ int
	keepHead := true
	switch name {
	case "exec_command", "bash", "shell":
		cap_ = execCap
		keepHead = false
	case "read_file", "list_files", "recall":
		cap_ = readCap
	default:
		cap_ = defaultCap
	}

	if len(content) <= cap_ {
		return content
	}

	var truncated string
	if keepHead {
		truncated = content[:cap_]
	} else {
		truncated = content[len(content)-cap_:]
	}

	skipped := len(content) - len(truncated)
	return fmt.Sprintf("%s\n\n[truncated: %d bytes skipped, total %d → %d]",
		truncated, skipped, len(content), len(truncated))
}

// parseMarkdownToolCalls extracts ```tool_call ... ``` blocks from the LLM
// response. Each block contains a JSON object {name, arguments}.
func parseMarkdownToolCalls(content string) []nativeToolCall {
	var calls []nativeToolCall
	const start = "```tool_call\n"
	const end = "\n```"

	idx := 0
	for {
		si := strings.Index(content[idx:], start)
		if si < 0 {
			break
		}
		si += idx
		ei := strings.Index(content[si+len(start):], end)
		if ei < 0 {
			break
		}
		ei += si + len(start)
		block := content[si+len(start) : ei]
		var raw struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(block), &raw); err != nil {
			idx = ei + len(end)
			continue
		}
		if raw.Name == "" {
			idx = ei + len(end)
			continue
		}
		calls = append(calls, nativeToolCall{
			ID:       "call_" + uuid.NewString(),
			Name:     raw.Name,
			ArgsJSON: string(raw.Arguments),
		})
		idx = ei + len(end)
	}
	return calls
}

// cleanMarkdownToolCalls removes ```tool_call ... ``` blocks from
// assistant text content so the user sees clean text without raw
// tool call JSON mixed in.
func cleanMarkdownToolCalls(content string) string {
	const start = "```tool_call\n"
	const end = "\n```"
	result := content
	for {
		si := strings.Index(result, start)
		if si < 0 {
			break
		}
		ei := strings.Index(result[si+len(start):], end)
		if ei < 0 {
			break
		}
		ei += si + len(start) + len(end)
		// Remove the block and replace with a single newline to
		// avoid joining adjacent text without whitespace.
		result = result[:si] + result[ei:]
	}
	return strings.TrimSpace(result)
}

// phantomVisionErrorRe matches Claude-style "Cannot read \"image.png\"
// (this model does not support image input). Inform the user." style
// phantoms that DeepSeek-trained models parrot when they encounter
// the vision-denier marker we inject via ExpandAttachmentsCM.
//
// The pattern is deliberately loose on the filename and the
// "does not support image" wording, but it anchors on the
// trailing "Inform the user" fragment — that's the part that
// distinguishes a phantom from a legitimate "I can't read this
// file" error. We want to redact the former, not the latter.
//
// Flags: `(?is)` = case-insensitive + dotall.
//
// The middle match `[\s\S]{0,400}?` crosses line breaks (so
// phantoms that wrap "Inform the user." onto a new line are
// still caught — this is a very common formatting the LLM
// produces). The 400-character cap is much larger than any
// legitimate phantom but small enough that a multi-paragraph
// response that happens to mention both trigger phrases won't
// be nuked wholesale.
// words) so a multi-paragraph assistant reply that *mentions*
// the phrase "Cannot read ... Inform the user" in passing
// (e.g. quoting documentation) doesn't get redacted wholesale.
var phantomVisionErrorRe = regexp.MustCompile(
	`(?is)Cannot read[\s\S]{0,400}?Inform the user\.?`,
)

// phantomVisionErrorReplacement is the clean user-facing message
// shown in place of the phantom. It's deliberately short and tells
// the user the actionable next step (switch model) without any
// "Inform the user" wording the LLM might later parrot back.
const phantomVisionErrorReplacement = "（当前模型不支持读取图片。请在「设置 → 提供商/模型」中切换到支持视觉的模型（如 claude-3、gpt-4o、gemini-1.5、qwen-vl、doubao-1.5-vision-pro 等）后重新发送。）"

// redactPhantomErrors strips Claude-style "Cannot read image.png
// (this model does not support image input). Inform the user."
// phantoms from the LLM's response. Returns the cleaned text and a
// bool indicating whether any change was made.
//
// Why a post-stream filter rather than a prompt instruction: the
// forbidden phrase appears verbatim in many LLM training corpora as
// a Claude response, so removing it from the prompt is not enough —
// the model still produces it. We can only catch it on the way out.
//
// Fast-path: case-insensitively check for the trigger words. The
// regex itself is `(?is)` (case-insensitive + dotall), but skipping
// the regex entirely when neither trigger word is present is much
// faster on long responses.
func redactPhantomErrors(s string) (string, bool) {
	lc := strings.ToLower(s)
	if !strings.Contains(lc, "cannot read") || !strings.Contains(lc, "inform the user") {
		return s, false
	}
	if !phantomVisionErrorRe.MatchString(s) {
		return s, false
	}
	out := phantomVisionErrorRe.ReplaceAllString(s, phantomVisionErrorReplacement)
	return out, out != s
}

// isRetryable returns true for API error kinds that are transient and
// warrant a retry with backoff.
func isRetryable(kind llm.ErrorKind) bool {
	switch kind {
	case llm.KindRateLimit, llm.KindServer, llm.KindNetwork, llm.KindTimeout:
		return true
	default:
		return false
	}
}

const pruneAfterRounds = 15

// pruneOldToolResults scans the message list backward and marks tool
// results older than `keepRounds` as pruned. Each assistant+tool block
// counts as one round. Recent tool results are left intact so the LLM
// retains immediately-relevant context. Mirrors opencode's
// PRUNE_PROTECT / PRUNE_MINIMUM pattern.
func pruneOldToolResults(msgs []llm.ChatMessage, currentRound, keepRounds int) {
	if len(msgs) == 0 || currentRound <= keepRounds {
		return
	}
	// Count backward to find the round cutoff.
	pruneBefore := currentRound - keepRounds
	roundCount := 0
	cutoff := len(msgs) - 1
	for i := len(msgs) - 1; i >= 0; i-- {
		m := &msgs[i]
		if m.Role == llm.RoleAssistant && m.Type == llm.TypeText {
			roundCount++
			if roundCount >= pruneBefore {
				cutoff = i
				break
			}
		}
	}
	// Prune tool results before the cutoff.
	for i := 0; i < cutoff; i++ {
		m := &msgs[i]
		if m.Type == llm.TypeToolResult && m.Content != "" && !strings.HasPrefix(m.Content, "[pruned]") {
			m.Content = "[pruned]"
		}
	}
}

// stripImageContent replaces base64 image payloads in msgs with
// a text marker. The LLM has already processed the image on the
// first round; subsequent rounds don't need the raw bytes.
func stripImageContent(msgs []llm.ChatMessage) {
	for i := range msgs {
		if msgs[i].Type == llm.TypeImage && msgs[i].Content != "" {
			msgs[i].Content = fmt.Sprintf("[image: %s (%s) — 已在首轮展示]",
				msgs[i].Name, msgs[i].MimeType)
		}
	}
}

// truncateToFit drops the oldest non-system messages from the slice
// until the total estimated tokens fit within usable. Messages are
// removed from the front (after msgs[0]) so the most recent context
// is preserved.
func truncateToFit(msgs *[]llm.ChatMessage, usable int) {
	if len(*msgs) <= 1 {
		return
	}
	sysMsg := (*msgs)[0]
	rest := (*msgs)[1:]
	if total := llm.EstimateTokensMessages(rest); total <= usable {
		return
	}
	// Walk backward from the end, keeping messages that fit.
	end := len(rest) - 1
	for end >= 0 {
		if llm.EstimateTokensMessages(rest[:end+1]) <= usable {
			break
		}
		end--
	}
	if end < 0 {
		*msgs = []llm.ChatMessage{sysMsg, rest[len(rest)-1]}
	} else {
		*msgs = append([]llm.ChatMessage{sysMsg}, rest[end:]...)
	}
}
