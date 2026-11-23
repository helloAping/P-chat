// Package httpcli is a thin HTTP client for pchat-server's REST API.
// Both the CLI REPL and the Wails GUI use it; the only process that
// actually touches memory/agent/tools is the server.
package httpcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/p-chat/pchat/internal/config"
)

const maxToolResultResponseBytes = 64 << 20

// Client talks to pchat-server over HTTP. All methods are safe for
// concurrent use.
type Client struct {
	base string
	http *http.Client

	// Cached "current" selection. Mirrors what the server thinks
	// the active session / provider / model is, so CLI helpers like
	// `/provider` don't have to round-trip for every command.
	mu              sync.Mutex
	currentProvider string
	currentStyle    string
	currentWorkMode string
	// currentModel is the model the user selected for the
	// current provider (via SetModel). The server doesn't
	// expose a dedicated set-model endpoint, so this is a
	// client-side cache so DisplayModel / ProviderModel
	// return the right name. When empty, ProviderModel
	// returns the provider's default model.
	currentModel string

	// Local cache of providers, populated by SetCfgProviders.
	// Used by SetModel / DisplayModel / ModelsFor which the server
	// doesn't expose as dedicated endpoints.
	cfgProviders []ProviderInfo
}

// NewClient builds a client targeting the given base URL (e.g.
// "http://127.0.0.1:15150"). No timeout is set on the HTTP client —
// SSE streams for long agent runs can last minutes; the caller
// controls cancellation via context.
//
// The HTTP client uses a tuned transport with keep-alive
// connection pooling. The default http.DefaultTransport has
// MaxIdleConnsPerHost=2, which forces a new TCP connection for
// almost every request to the same backend. For a CLI that
// makes many requests to pchat-server (e.g. switching
// providers, listing models, polling status), this is slow and
// burns sockets. We bump the per-host pool to 20 and add
// IdleConnTimeout so connections eventually close.
func NewClient(base string) *Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &Client{
		base: strings.TrimRight(base, "/"),
		http: &http.Client{Timeout: 0, Transport: transport},
	}
}

// Session mirrors server.SessionResponse.
type Session struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	ProjectPath string `json:"project_path,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	Style       string `json:"style,omitempty"`
	WorkMode    string `json:"work_mode,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
	// AutoContinue mirrors the server's P0-3 flag (default
	// true). The CLI uses it to display the current
	// auto-continue state next to /auto-continue's status
	// output and to remember the user's last choice.
	AutoContinue bool `json:"auto_continue"`
}

// ExportFile 是 pchat-server 返回的二进制会话导出。
// ExportFile is a binary session export returned by pchat-server.
// Filename 是移除路径成分后的服务端建议文件名。Filename is the server's
// suggested basename after removing path components.
type ExportFile struct {
	Body        []byte
	Filename    string
	ContentType string
}

// ToolResult is the full content returned for one previously truncated tool call.
type ToolResult struct {
	ToolID  string `json:"tool_id"`
	Content string `json:"content"`
	Bytes   int    `json:"bytes"`
}

// KnowledgeBase 映射 CLI 使用的服务端知识库载荷。KnowledgeBase mirrors the
// server knowledge-base payload used by the CLI.
type KnowledgeBase struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Enabled  bool   `json:"enabled"`
	Status   string `json:"status,omitempty"`
	DocCount int    `json:"doc_count,omitempty"`
}

// KnowledgeScanStatus 映射知识库扫描状态端点返回的进度载荷。
// KnowledgeScanStatus mirrors the progress payload returned by a knowledge
// base scan-status endpoint.
type KnowledgeScanStatus struct {
	Chunks  int    `json:"chunks"`
	Done    bool   `json:"done"`
	Error   string `json:"error,omitempty"`
	Current int    `json:"current"`
	Total   int    `json:"total"`
	Changed int    `json:"changed,omitempty"`
	Skipped int    `json:"skipped,omitempty"`
	Deleted int    `json:"deleted,omitempty"`
	Failed  int    `json:"failed,omitempty"`
	Message string `json:"message,omitempty"`
}

// KnowledgeSearchRequest 是 /knowledge/search 接受的请求体。
// KnowledgeSearchRequest is the request body accepted by /knowledge/search.
type KnowledgeSearchRequest struct {
	Query string   `json:"query"`
	TopK  int      `json:"top_k,omitempty"`
	Grep  string   `json:"grep,omitempty"`
	Bases []string `json:"bases,omitempty"`
}

// KnowledgeSearchResult 是一条 canonical 知识库搜索命中。
// KnowledgeSearchResult is one canonical knowledge search hit.
type KnowledgeSearchResult struct {
	Source      string  `json:"source"`
	Content     string  `json:"content"`
	Similarity  float64 `json:"similarity"`
	Rank        int     `json:"rank"`
	Base        string  `json:"base,omitempty"`
	Title       string  `json:"title,omitempty"`
	MatchType   string  `json:"match_type,omitempty"`
	Query       string  `json:"query,omitempty"`
	Explanation string  `json:"explanation,omitempty"`
}

// KnowledgeSearchResponse 是 /knowledge/search 的 canonical 响应。
// KnowledgeSearchResponse is the canonical response from /knowledge/search.
type KnowledgeSearchResponse struct {
	Query   string                  `json:"query"`
	Results []KnowledgeSearchResult `json:"results"`
}

// Message mirrors server.MessageResponse.
type Message struct {
	ID         int64  `json:"id"`
	Role       string `json:"role"`
	Content    string `json:"content"`
	CreatedAt  int64  `json:"created_at"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
}

// StreamEvent mirrors server.StreamEvent (the SSE chunk format).
type StreamEvent struct {
	Type     string `json:"type"`
	Content  string `json:"content,omitempty"`
	Thinking string `json:"thinking,omitempty"`

	// Phase/tool fields
	Phase               string   `json:"phase,omitempty"`
	Step                string   `json:"step,omitempty"`
	Msg                 string   `json:"message,omitempty"`
	ToolID              string   `json:"tool_id,omitempty"`
	ToolName            string   `json:"tool_name,omitempty"`
	ToolStatus          string   `json:"tool_status,omitempty"`
	ToolResult          string   `json:"tool_result,omitempty"`
	ToolResultFull      string   `json:"tool_result_full,omitempty"`
	ToolResultTruncated bool     `json:"tool_result_truncated,omitempty"`
	ToolResultFullLen   int      `json:"tool_result_full_len,omitempty"`
	ToolError           string   `json:"tool_error,omitempty"`
	ToolElapsed         string   `json:"tool_elapsed,omitempty"`
	ToolCallStatus      string   `json:"tool_call_status,omitempty"`
	ToolSummary         string   `json:"tool_summary,omitempty"`
	ToolChangedPaths    []string `json:"tool_changed_paths,omitempty"`
	ToolRetryable       bool     `json:"tool_retryable,omitempty"`
	ToolRequiresUser    bool     `json:"tool_requires_user,omitempty"`
	ToolNextAction      string   `json:"tool_next_action,omitempty"`
	ToolArgs            string   `json:"tool_args,omitempty"`
	SkillName           string   `json:"skill_name,omitempty"`
	SkillStatus         string   `json:"skill_status,omitempty"`
	SkillScope          string   `json:"skill_scope,omitempty"`
	SkillSource         string   `json:"skill_source,omitempty"`
	SkillDependencies   []string `json:"skill_dependencies,omitempty"`
	SkillError          string   `json:"skill_error,omitempty"`

	// 子代理事件与父流交错传输。Sub-agent events are interleaved with the parent stream.
	SubAgent              bool   `json:"sub_agent,omitempty"`
	SubAgentTask          string `json:"sub_agent_task,omitempty"`
	SubAgentStatus        string `json:"sub_agent_status,omitempty"`
	SubAgentType          string `json:"sub_agent_type,omitempty"`
	SubAgentColor         string `json:"sub_agent_color,omitempty"`
	SubAgentModel         string `json:"sub_agent_model,omitempty"`
	SubAgentTaskID        string `json:"sub_agent_task_id,omitempty"`
	SubAgentRunMode       string `json:"sub_agent_run_mode,omitempty"`
	SubAgentDescription   string `json:"sub_agent_description,omitempty"`
	SubAgentFailureReason string `json:"sub_agent_failure_reason,omitempty"`

	ContentRewrite  string `json:"content_rewrite,omitempty"`
	ThinkingRewrite string `json:"thinking_rewrite,omitempty"`

	// Done fields
	TokensIn      int    `json:"tokens_in,omitempty"`
	TokensOut     int    `json:"tokens_out,omitempty"`
	Elapsed       string `json:"elapsed,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Model         string `json:"model,omitempty"`
	SessionStatus string `json:"session_status,omitempty"`

	// Error fields
	Error      string `json:"error,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
	ErrorKind  string `json:"error_kind,omitempty"`
	Seq        uint64 `json:"seq,omitempty"`
	TraceID    string `json:"trace_id,omitempty"`

	// Question event (LLM asks user a question)
	QuestionJSON    string `json:"question_json,omitempty"`
	ToolConfirmJSON string `json:"tool_confirm_json,omitempty"`
}

// ProviderInfo mirrors the providers endpoint payload.
type ProviderInfo struct {
	Name            string  `json:"name"`
	ProviderID      string  `json:"provider_id,omitempty"`
	StrategyVariant string  `json:"strategy_variant,omitempty"`
	Model           string  `json:"model"`
	Protocol        string  `json:"protocol"`
	BaseURL         string  `json:"base_url,omitempty"`
	IsDefault       bool    `json:"is_default"`
	Models          []Model `json:"models"`
}

// ProviderDetails is the safe rich provider view returned by GET and PATCH
// /api/v1/providers/:name. It deliberately exposes only whether an API key is
// configured, never the plaintext key returned by the server.
type ProviderDetails struct {
	Name            string               `json:"name"`
	ProviderID      string               `json:"provider_id"`
	StrategyVariant string               `json:"strategy_variant"`
	Protocol        string               `json:"protocol"`
	BaseURL         string               `json:"base_url"`
	APIKeySet       bool                 `json:"api_key_set"`
	IsDefault       bool                 `json:"is_default"`
	Models          []config.ModelConfig `json:"models"`
	Model           string               `json:"model"`
}

type providerDetailsResponse struct {
	Name            string               `json:"name"`
	ProviderID      string               `json:"provider_id"`
	StrategyVariant string               `json:"strategy_variant"`
	Protocol        string               `json:"protocol"`
	BaseURL         string               `json:"base_url"`
	APIKey          string               `json:"api_key"`
	IsDefault       bool                 `json:"is_default"`
	Models          []config.ModelConfig `json:"models"`
	Model           string               `json:"model"`
}

func (p providerDetailsResponse) safeView() ProviderDetails {
	return ProviderDetails{
		Name: p.Name, ProviderID: p.ProviderID, StrategyVariant: p.StrategyVariant,
		Protocol: p.Protocol, BaseURL: p.BaseURL, APIKeySet: p.APIKey != "",
		IsDefault: p.IsDefault, Models: p.Models, Model: p.Model,
	}
}

// ProviderCreate is the body used to create a provider through pchat-server.
type ProviderCreate struct {
	Name            string `json:"name"`
	ProviderID      string `json:"provider_id,omitempty"`
	StrategyVariant string `json:"strategy_variant,omitempty"`
	Protocol        string `json:"protocol"`
	BaseURL         string `json:"base_url"`
	APIKey          string `json:"api_key"`
	Model           string `json:"model"`
}

// ProviderPatch is the partial body used to update a provider. APIKey remains
// a pointer so an empty key is intentionally serialized when clearing it.
type ProviderPatch struct {
	APIKey *string `json:"api_key,omitempty"`
}

// Model mirrors a single model entry returned by the server. We
// re-use it in two places: (1) the per-provider model list (future
// endpoint), (2) the current-model field on ProviderInfo. The JSON
// shape is intentionally small so it can sit in either payload.
type Model struct {
	Name             string `json:"name"`
	Type             string `json:"type,omitempty"`
	DisplayName      string `json:"display_name,omitempty"`
	Default          bool   `json:"default,omitempty"`
	Description      string `json:"description,omitempty"`
	MaxTokensContext int    `json:"max_tokens_context,omitempty"`
	MaxTokensOutput  int    `json:"max_tokens_output,omitempty"`
}

// ToolInfo 映射服务端生效工具注册表中的一个条目。ToolInfo mirrors one entry in the server's effective tool registry.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Dynamic     bool   `json:"dynamic"`
	Scope       string `json:"scope"`
	Source      string `json:"source,omitempty"`
	ProjectRoot string `json:"project_root,omitempty"`
}

// ToolLoadDiagnostic 映射动态工具加载诊断。ToolLoadDiagnostic mirrors dynamic tool load diagnostics.
type ToolLoadDiagnostic struct {
	Source      string `json:"source"`
	Name        string `json:"name,omitempty"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	Scope       string `json:"scope,omitempty"`
	ProjectRoot string `json:"project_root,omitempty"`
	ModAt       string `json:"mod_at,omitempty"`
}

// ToolListResponse is the full response from GET /api/v1/tools.
type ToolListResponse struct {
	Tools       []ToolInfo           `json:"tools"`
	Diagnostics []ToolLoadDiagnostic `json:"diagnostics"`
}

// SkillInfo 映射服务端 Skill 目录中的一个元数据条目。
// SkillInfo mirrors one metadata-only entry in the server Skill catalog.
type SkillInfo struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Path           string   `json:"path,omitempty"`
	Directory      string   `json:"directory,omitempty"`
	Scope          string   `json:"scope,omitempty"`
	RequiredSkills []string `json:"required_skills,omitempty"`
	RequiredBins   []string `json:"required_bins,omitempty"`
}

// CompressResult 映射会话压缩结果。CompressResult mirrors the session compression result.
type CompressResult struct {
	Compressed bool   `json:"compressed"`
	Summary    string `json:"summary"`
}

// ContextMessage 映射上下文检查中的一条消息。ContextMessage mirrors one context inspector message.
type ContextMessage struct {
	Role         string `json:"role"`
	Tokens       int    `json:"tokens"`
	Preview      string `json:"preview"`
	IsToolResult bool   `json:"is_tool_result"`
	IsCompressed bool   `json:"is_compressed,omitempty"`
}

// ContextInspector 映射服务端的上下文用量检查结果。ContextInspector mirrors the server context inspector result.
type ContextInspector struct {
	SessionID         string           `json:"session_id"`
	Provider          string           `json:"provider"`
	Model             string           `json:"model"`
	ContextWindow     int              `json:"context_window"`
	EstimatedTokens   int              `json:"estimated_tokens"`
	UsableTokens      int              `json:"usable_tokens"`
	UtilizationPct    float64          `json:"utilization_pct"`
	CompressedSummary string           `json:"compressed_summary,omitempty"`
	Messages          []ContextMessage `json:"messages"`
}

// RuleInfo is one session-scoped rule returned by pchat-server.
type RuleInfo struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Scope string `json:"scope"`
}

// AgentsCandidate is one AGENTS.md candidate considered for a session.
type AgentsCandidate struct {
	Slot     string `json:"slot"`
	Label    string `json:"label"`
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Selected bool   `json:"selected"`
}

// AgentsContext is the session-scoped AGENTS.md diagnostic response. Prompt
// is exactly the OR-selected instruction block supplied to the agent.
type AgentsContext struct {
	Candidates []AgentsCandidate `json:"candidates"`
	Prompt     string            `json:"prompt"`
}

// StyleInfo mirrors the styles endpoint payload.
type StyleInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Desc  string `json:"desc"`
}

// ====================================================================
// HTTP helpers
// ====================================================================

func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bodyReader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode: %w (body: %s)", err, string(respBody))
		}
	}
	return nil
}

// Ping checks server reachability by calling /health. Returns nil
// on 200 OK.
func (c *Client) Ping(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", c.base+"/api/v1/health", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("unhealthy: HTTP %d", resp.StatusCode)
	}
	return nil
}

// ====================================================================
// Session API
// ====================================================================

func (c *Client) ListSessions(ctx context.Context) ([]Session, error) {
	return c.ListSessionsForProject(ctx, nil)
}

// ListSessionsForProject 按项目列出会话；nil 不过滤，非 nil（含空串）精确限定范围。
// ListSessionsForProject lists sessions by project; nil leaves the list unfiltered,
// while a non-nil value, including an empty string, selects that exact scope.
func (c *Client) ListSessionsForProject(ctx context.Context, projectPath *string) ([]Session, error) {
	var resp struct {
		Sessions []Session `json:"sessions"`
	}
	endpoint := "/api/v1/sessions"
	if projectPath != nil {
		endpoint += "?project_path=" + url.QueryEscape(*projectPath)
	}
	if err := c.doJSON(ctx, "GET", endpoint, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Sessions, nil
}

func (c *Client) GetSession(ctx context.Context, id string) (*Session, error) {
	var s Session
	if err := c.doJSON(ctx, "GET", "/api/v1/sessions/"+id, nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ExportSession 从服务端下载已渲染的会话导出。
// ExportSession downloads one rendered session export from the server. The
// format is forwarded as md, markdown, html, pdf, or json. The returned
// filename is normalized to a basename before callers write it locally.
func (c *Client) ExportSession(ctx context.Context, sessionID, format string) (ExportFile, error) {
	endpoint := "/api/v1/sessions/" + url.PathEscape(sessionID) + "/export?format=" + url.QueryEscape(format)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+endpoint, nil)
	if err != nil {
		return ExportFile{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return ExportFile{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ExportFile{}, fmt.Errorf("read export response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return ExportFile{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return ExportFile{
		Body:        body,
		Filename:    exportFilename(resp.Header.Get("Content-Disposition")),
		ContentType: resp.Header.Get("Content-Type"),
	}, nil
}

func exportFilename(contentDisposition string) string {
	_, params, err := mime.ParseMediaType(contentDisposition)
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(params["filename"])
	if name == "" {
		return ""
	}
	// HTTP headers are untrusted input. Treat both Windows and URL path
	// separators as directory components before returning a local filename.
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	if name == "." || name == "/" {
		return ""
	}
	return name
}

type CreateSessionOpts struct {
	Style       string `json:"style,omitempty"`
	WorkMode    string `json:"work_mode,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Title       string `json:"title,omitempty"`
	ProjectPath string `json:"project_path,omitempty"`
}

func (c *Client) CreateSession(ctx context.Context, opts CreateSessionOpts) (*Session, error) {
	var s Session
	if err := c.doJSON(ctx, "POST", "/api/v1/sessions", opts, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (c *Client) RenameSession(ctx context.Context, id, title string) error {
	body := struct {
		Title string `json:"title"`
	}{Title: title}
	return c.doJSON(ctx, "PATCH", "/api/v1/sessions/"+id, body, nil)
}

// SessionPatchOpts is the JSON body for the PATCH /sessions/:id
// endpoint. All fields are pointers so callers can send partial
// updates — see internal/server/handler.go UpdateSessionMeta
// for the server-side semantics. The CLI uses this to toggle
// per-session flags like AutoContinue without touching the
// other session metadata.
type SessionPatchOpts struct {
	Style         *string `json:"style,omitempty"`
	WorkMode      *string `json:"work_mode,omitempty"`
	Provider      *string `json:"provider,omitempty"`
	Model         *string `json:"model,omitempty"`
	Permission    *string `json:"permission_level,omitempty"`
	KnowledgeBase *string `json:"knowledge_base,omitempty"`
	AutoContinue  *bool   `json:"auto_continue,omitempty"`
}

// PatchSession sends a partial update to the session metadata
// and returns the refreshed SessionResponse. The CLI uses it
// for toggles like /auto-continue that change a single flag
// without spawning a full reload.
func (c *Client) PatchSession(ctx context.Context, id string, opts SessionPatchOpts) (*Session, error) {
	var s Session
	if err := c.doJSON(ctx, "PATCH", "/api/v1/sessions/"+id, opts, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (c *Client) DeleteSession(ctx context.Context, id string) error {
	return c.doJSON(ctx, "DELETE", "/api/v1/sessions/"+id, nil, nil)
}

// ClearMessages empties the messages of a session without
// changing the session ID. Use this instead of DeleteSession
// when the user wants to start a fresh thread but keep the
// session row (so external refs stay stable).
func (c *Client) ClearMessages(ctx context.Context, id string) error {
	return c.doJSON(ctx, "DELETE", "/api/v1/sessions/"+id+"/messages", nil, nil)
}

func (c *Client) ListMessages(ctx context.Context, id string) ([]Message, error) {
	var resp struct {
		Messages []Message `json:"messages"`
	}
	if err := c.doJSON(ctx, "GET", "/api/v1/sessions/"+id+"/messages", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Messages, nil
}

// GetToolResult retrieves one truncated tool result for its owning session.
// The response is capped to protect the CLI from an unexpectedly large body.
func (c *Client) GetToolResult(ctx context.Context, sessionID, toolID string) (ToolResult, error) {
	endpoint := "/api/v1/sessions/" + url.PathEscape(sessionID) + "/messages/0/tool-result/" + url.PathEscape(toolID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+endpoint, nil)
	if err != nil {
		return ToolResult{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return ToolResult{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxToolResultResponseBytes+1))
	if err != nil {
		return ToolResult{}, fmt.Errorf("read tool result response: %w", err)
	}
	if len(body) > maxToolResultResponseBytes {
		return ToolResult{}, fmt.Errorf("tool result exceeds %d MiB response limit", maxToolResultResponseBytes>>20)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return ToolResult{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result ToolResult
	if err := json.Unmarshal(body, &result); err != nil {
		return ToolResult{}, fmt.Errorf("decode tool result: %w", err)
	}
	return result, nil
}

// SendMessageOptions configures a chat call.
type SendMessageOptions struct {
	Message              string                `json:"message" binding:"required"`
	Style                string                `json:"style,omitempty"`
	WorkMode             string                `json:"work_mode,omitempty"`
	TodoMode             string                `json:"todo_mode,omitempty"`
	Provider             string                `json:"provider,omitempty"`
	Model                string                `json:"model,omitempty"`
	ClientMsgID          int64                 `json:"client_msg_id,omitempty"`
	Attachments          []TurnQueueAttachment `json:"attachments,omitempty"`
	UseImageRecognition  *bool                 `json:"use_image_recognition,omitempty"`
	SubAgentModelEnabled *bool                 `json:"sub_agent_model_enabled,omitempty"`
	SubAgentProvider     string                `json:"sub_agent_provider,omitempty"`
	SubAgentModel        string                `json:"sub_agent_model,omitempty"`
	SkillContext         string                `json:"skill_context,omitempty"`
	ActiveSkills         []string              `json:"active_skills,omitempty"`
	// ToolsEnabled 仅覆盖当前消息的工具可用性。
	// ToolsEnabled overrides tool availability for this message only.
	ToolsEnabled *bool `json:"tools_enabled,omitempty"`
}

// TurnQueuePayload 镜像 server.SendMessageRequest 中需要持久化排队的字段。
// TurnQueuePayload mirrors the queued subset of server.SendMessageRequest.
type TurnQueuePayload struct {
	Message              string                `json:"message"`
	TodoMode             string                `json:"todo_mode,omitempty"`
	ClientMsgID          int64                 `json:"client_msg_id"`
	Style                string                `json:"style,omitempty"`
	WorkMode             string                `json:"work_mode,omitempty"`
	Provider             string                `json:"provider,omitempty"`
	Model                string                `json:"model,omitempty"`
	Attachments          []TurnQueueAttachment `json:"attachments,omitempty"`
	UseImageRecognition  *bool                 `json:"use_image_recognition,omitempty"`
	SubAgentModelEnabled *bool                 `json:"sub_agent_model_enabled,omitempty"`
	SubAgentProvider     string                `json:"sub_agent_provider,omitempty"`
	SubAgentModel        string                `json:"sub_agent_model,omitempty"`
	SkillContext         string                `json:"skill_context,omitempty"`
	ActiveSkills         []string              `json:"active_skills,omitempty"`
}

// TurnQueueAttachment 镜像 agent.Attachment，避免 HTTP 适配层依赖 agent 包。
// TurnQueueAttachment mirrors agent.Attachment without coupling the HTTP adapter to agent.
type TurnQueueAttachment struct {
	Type     string `json:"type,omitempty"`
	ID       string `json:"id,omitempty"`
	UploadID string `json:"upload_id,omitempty"`
	Name     string `json:"name,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Kind     string `json:"kind,omitempty"`
	MIME     string `json:"mime,omitempty"`
	Data     string `json:"data,omitempty"`
	Text     string `json:"text,omitempty"`
	URL      string `json:"url,omitempty"`
}

// TurnQueueItem 表示会话 FIFO 队列中的一个未完成用户回合。
// TurnQueueItem is one unfinished user turn in a session FIFO queue.
type TurnQueueItem struct {
	ID              int64             `json:"id"`
	SessionID       string            `json:"session_id"`
	Status          string            `json:"status"`
	Message         string            `json:"message"`
	ClientMsgID     int64             `json:"client_msg_id"`
	AttachmentCount int               `json:"attachment_count"`
	Error           string            `json:"error,omitempty"`
	CreatedAt       int64             `json:"created_at"`
	UpdatedAt       int64             `json:"updated_at"`
	StartedAt       int64             `json:"started_at,omitempty"`
	FinishedAt      int64             `json:"finished_at,omitempty"`
	Payload         *TurnQueuePayload `json:"payload,omitempty"`
}

// ListTurnQueueItems 返回当前会话 queued、running 和 failed 状态的回合。
// ListTurnQueueItems returns the current session's queued, running, and failed turns.
func (c *Client) ListTurnQueueItems(ctx context.Context, sessionID string) ([]TurnQueueItem, error) {
	var resp struct {
		Items []TurnQueueItem `json:"items"`
	}
	path := "/api/v1/sessions/" + url.PathEscape(sessionID) + "/turn-queue"
	if err := c.doJSON(ctx, "GET", path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// EnqueueTurnQueueItem 将用户回合追加到持久化会话队列。
// EnqueueTurnQueueItem appends a user turn to the durable session queue.
func (c *Client) EnqueueTurnQueueItem(ctx context.Context, sessionID string, payload TurnQueuePayload) (TurnQueueItem, error) {
	var resp struct {
		Item TurnQueueItem `json:"item"`
	}
	path := "/api/v1/sessions/" + url.PathEscape(sessionID) + "/turn-queue"
	if err := c.doJSON(ctx, "POST", path, payload, &resp); err != nil {
		return TurnQueueItem{}, err
	}
	return resp.Item, nil
}

// EditTurnQueueItem 修改仍处于 queued 状态的回合文本。
// EditTurnQueueItem changes the text of a turn that is still queued.
func (c *Client) EditTurnQueueItem(ctx context.Context, sessionID string, queueID int64, message string) (TurnQueueItem, error) {
	var resp struct {
		Item TurnQueueItem `json:"item"`
	}
	path := fmt.Sprintf("/api/v1/sessions/%s/turn-queue/%d", url.PathEscape(sessionID), queueID)
	if err := c.doJSON(ctx, "PATCH", path, map[string]string{"message": message}, &resp); err != nil {
		return TurnQueueItem{}, err
	}
	return resp.Item, nil
}

// ClaimNextTurnQueueItem 原子地将 FIFO 队首转为 running 并返回完整载荷。
// ClaimNextTurnQueueItem atomically moves the FIFO head to running and returns its payload.
func (c *Client) ClaimNextTurnQueueItem(ctx context.Context, sessionID string) (TurnQueueItem, error) {
	var resp struct {
		Item TurnQueueItem `json:"item"`
	}
	path := "/api/v1/sessions/" + url.PathEscape(sessionID) + "/turn-queue/claim"
	if err := c.doJSON(ctx, "POST", path, nil, &resp); err != nil {
		return TurnQueueItem{}, err
	}
	return resp.Item, nil
}

// CompleteTurnQueueItem 将 running 回合标记为 done。
// CompleteTurnQueueItem marks a running queued turn done.
func (c *Client) CompleteTurnQueueItem(ctx context.Context, sessionID string, queueID int64) (TurnQueueItem, error) {
	var resp struct {
		Item TurnQueueItem `json:"item"`
	}
	path := fmt.Sprintf("/api/v1/sessions/%s/turn-queue/%d/complete", url.PathEscape(sessionID), queueID)
	if err := c.doJSON(ctx, "POST", path, nil, &resp); err != nil {
		return TurnQueueItem{}, err
	}
	return resp.Item, nil
}

// FailTurnQueueItem 将 running 回合标记为 failed，并暂停 FIFO 出队。
// FailTurnQueueItem marks a running queued turn failed and pauses FIFO draining.
func (c *Client) FailTurnQueueItem(ctx context.Context, sessionID string, queueID int64, message string) (TurnQueueItem, error) {
	var resp struct {
		Item TurnQueueItem `json:"item"`
	}
	path := fmt.Sprintf("/api/v1/sessions/%s/turn-queue/%d/fail", url.PathEscape(sessionID), queueID)
	if err := c.doJSON(ctx, "POST", path, map[string]string{"error": message}, &resp); err != nil {
		return TurnQueueItem{}, err
	}
	return resp.Item, nil
}

// RetryTurnQueueItem 将 failed 回合重新放回 queued 状态。
// RetryTurnQueueItem moves a failed turn back to queued.
func (c *Client) RetryTurnQueueItem(ctx context.Context, sessionID string, queueID int64) (TurnQueueItem, error) {
	var resp struct {
		Item TurnQueueItem `json:"item"`
	}
	path := fmt.Sprintf("/api/v1/sessions/%s/turn-queue/%d/retry", url.PathEscape(sessionID), queueID)
	if err := c.doJSON(ctx, "POST", path, nil, &resp); err != nil {
		return TurnQueueItem{}, err
	}
	return resp.Item, nil
}

// DeleteTurnQueueItem 取消 queued 或 failed 回合。
// DeleteTurnQueueItem cancels a queued or failed turn.
func (c *Client) DeleteTurnQueueItem(ctx context.Context, sessionID string, queueID int64) (TurnQueueItem, error) {
	var resp struct {
		Item TurnQueueItem `json:"item"`
	}
	path := fmt.Sprintf("/api/v1/sessions/%s/turn-queue/%d", url.PathEscape(sessionID), queueID)
	if err := c.doJSON(ctx, "DELETE", path, nil, &resp); err != nil {
		return TurnQueueItem{}, err
	}
	return resp.Item, nil
}

// ClearTurnQueue 取消会话中所有 queued 或 failed 回合。
// ClearTurnQueue cancels all queued or failed turns in a session.
func (c *Client) ClearTurnQueue(ctx context.Context, sessionID string) (int64, error) {
	var resp struct {
		Cleared int64 `json:"cleared"`
	}
	path := "/api/v1/sessions/" + url.PathEscape(sessionID) + "/turn-queue"
	if err := c.doJSON(ctx, "DELETE", path, nil, &resp); err != nil {
		return 0, err
	}
	return resp.Cleared, nil
}

// SendMessage streams the LLM response back via SSE. Each event
// from the server is delivered to `onEvent`. The call returns when
// the server closes the stream (Done event or terminal error).
func (c *Client) SendMessage(ctx context.Context, sessionID string, opts SendMessageOptions, onEvent func(StreamEvent)) error {
	body, err := json.Marshal(opts)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST",
		c.base+"/api/v1/sessions/"+sessionID+"/messages", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	return c.streamSSE(req, onEvent)
}

// Regenerate 重新生成指定用户消息的回复。Regenerate regenerates the reply for a user message.
func (c *Client) Regenerate(ctx context.Context, sessionID string, userMessageID int64, onEvent func(StreamEvent)) error {
	body, err := json.Marshal(struct {
		UserMessageID int64 `json:"user_message_id"`
	}{UserMessageID: userMessageID})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.base+"/api/v1/sessions/"+sessionID+"/regenerate", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	return c.streamSSE(req, onEvent)
}

func (c *Client) streamSSE(req *http.Request, onEvent func(StreamEvent)) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	// Parse SSE: each event is "data: <json>\n\n"
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "" {
			continue
		}
		var ev StreamEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			// Skip malformed event but keep streaming.
			continue
		}
		onEvent(ev)
	}
	return scanner.Err()
}

// ====================================================================
// Metadata API
// ====================================================================

func (c *Client) ListProviders(ctx context.Context) ([]ProviderInfo, error) {
	var resp struct {
		Providers []ProviderInfo `json:"providers"`
	}
	if err := c.doJSON(ctx, "GET", "/api/v1/providers", nil, &resp); err != nil {
		return nil, err
	}
	// Cache so SetModel / DisplayModel can answer without a
	// round-trip.
	c.mu.Lock()
	c.cfgProviders = resp.Providers
	c.mu.Unlock()
	return resp.Providers, nil
}

// GetProvider returns one provider's complete configuration. The API key in
// the response must be treated as sensitive by callers.
func (c *Client) GetProvider(ctx context.Context, name string) (ProviderDetails, error) {
	var response providerDetailsResponse
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/providers/"+url.PathEscape(name), nil, &response)
	return response.safeView(), err
}

// CreateProvider adds a provider through the server's active configuration
// layer, then refreshes the CLI's provider/model cache.
func (c *Client) CreateProvider(ctx context.Context, provider ProviderCreate) error {
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/providers", provider, nil); err != nil {
		return err
	}
	return c.refreshProviders(ctx)
}

// DeleteProvider removes a provider through the server's active configuration
// layer, then refreshes the CLI's provider/model cache.
func (c *Client) DeleteProvider(ctx context.Context, name string) error {
	if err := c.doJSON(ctx, http.MethodDelete, "/api/v1/providers/"+url.PathEscape(name), nil, nil); err != nil {
		return err
	}
	return c.refreshProviders(ctx)
}

// PatchProvider applies a partial provider update. Pointer fields preserve the
// distinction between omitted values and an intentionally empty API key.
func (c *Client) PatchProvider(ctx context.Context, name string, patch ProviderPatch) (ProviderDetails, error) {
	var response providerDetailsResponse
	if err := c.doJSON(ctx, http.MethodPatch, "/api/v1/providers/"+url.PathEscape(name), patch, &response); err != nil {
		return ProviderDetails{}, err
	}
	if err := c.refreshProviders(ctx); err != nil {
		return ProviderDetails{}, err
	}
	return response.safeView(), nil
}

// SetDefaultProvider makes name the server's global default provider and
// refreshes the CLI's provider/model cache.
func (c *Client) SetDefaultProvider(ctx context.Context, name string) error {
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/providers/"+url.PathEscape(name)+"/default", struct{}{}, nil); err != nil {
		return err
	}
	return c.refreshProviders(ctx)
}

// CreateModel adds a model under a provider. A requested default is applied
// as a separate server operation because the create endpoint has no default
// field.
func (c *Client) CreateModel(ctx context.Context, provider string, model config.ModelConfig) error {
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/providers/"+url.PathEscape(provider)+"/models", model, nil); err != nil {
		return err
	}
	if model.Default {
		if err := c.doJSON(ctx, http.MethodPost, "/api/v1/providers/"+url.PathEscape(provider)+"/models/"+url.PathEscape(model.Name)+"/default", struct{}{}, nil); err != nil {
			return err
		}
	}
	return c.refreshProviders(ctx)
}

// UpdateModel updates a model under a provider and refreshes the CLI cache.
func (c *Client) UpdateModel(ctx context.Context, provider, name string, patch config.ModelConfig) error {
	endpoint := "/api/v1/providers/" + url.PathEscape(provider) + "/models/" + url.PathEscape(name)
	if err := c.doJSON(ctx, http.MethodPut, endpoint, patch, nil); err != nil {
		return err
	}
	return c.refreshProviders(ctx)
}

// DeleteModel removes a model under a provider and refreshes the CLI cache.
func (c *Client) DeleteModel(ctx context.Context, provider, name string) error {
	endpoint := "/api/v1/providers/" + url.PathEscape(provider) + "/models/" + url.PathEscape(name)
	if err := c.doJSON(ctx, http.MethodDelete, endpoint, nil, nil); err != nil {
		return err
	}
	return c.refreshProviders(ctx)
}

// SetDefaultModel makes a model the provider's default and refreshes the CLI
// cache.
func (c *Client) SetDefaultModel(ctx context.Context, provider, name string) error {
	endpoint := "/api/v1/providers/" + url.PathEscape(provider) + "/models/" + url.PathEscape(name) + "/default"
	if err := c.doJSON(ctx, http.MethodPost, endpoint, struct{}{}, nil); err != nil {
		return err
	}
	return c.refreshProviders(ctx)
}

func (c *Client) refreshProviders(ctx context.Context) error {
	providers, err := c.ListProviders(ctx)
	if err != nil {
		return fmt.Errorf("refresh provider cache: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	providerExists := false
	modelExists := false
	for _, provider := range providers {
		if provider.Name != c.currentProvider {
			continue
		}
		providerExists = true
		for _, model := range provider.Models {
			if model.Name == c.currentModel {
				modelExists = true
				break
			}
		}
	}
	if !providerExists {
		c.currentProvider = ""
		c.currentModel = ""
		return nil
	}
	if c.currentModel != "" && !modelExists {
		c.currentModel = ""
	}
	return nil
}

func (c *Client) ListStyles(ctx context.Context) ([]StyleInfo, error) {
	var resp struct {
		Styles []StyleInfo `json:"styles"`
	}
	if err := c.doJSON(ctx, "GET", "/api/v1/styles", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Styles, nil
}

// ListToolsDetailed 返回会话可用工具和动态工具诊断。
// ListToolsDetailed returns tools and dynamic-tool diagnostics for a session.
//
// 可选会话决定服务端暴露的项目级动态工具。The optional session selects
// the project-level dynamic tools that the server exposes.
func (c *Client) ListToolsDetailed(ctx context.Context, sessionID string) (ToolListResponse, error) {
	path := "/api/v1/tools"
	if sessionID != "" {
		path += "?session_id=" + url.QueryEscape(sessionID)
	}
	var resp ToolListResponse
	if err := c.doJSON(ctx, "GET", path, nil, &resp); err != nil {
		return ToolListResponse{}, err
	}
	return resp, nil
}

// ListTools 返回会话可用工具。ListTools returns the tools available to a session.
func (c *Client) ListTools(ctx context.Context, sessionID string) ([]ToolInfo, error) {
	resp, err := c.ListToolsDetailed(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return resp.Tools, nil
}

// ListSkills 返回会话项目范围内可发现的 Skill 元数据。
// ListSkills returns discoverable Skill metadata for a session's project scope.
func (c *Client) ListSkills(ctx context.Context, sessionID string) ([]SkillInfo, error) {
	path := "/api/v1/skills"
	if sessionID != "" {
		path += "?session_id=" + url.QueryEscape(sessionID)
	}
	var resp struct {
		Skills []SkillInfo `json:"skills"`
	}
	if err := c.doJSON(ctx, "GET", path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Skills, nil
}

// ListSessionRules returns rules visible to a session's project scope.
func (c *Client) ListSessionRules(ctx context.Context, sessionID string) ([]RuleInfo, error) {
	var response struct {
		Rules []RuleInfo `json:"rules"`
	}
	endpoint := "/api/v1/sessions/" + url.PathEscape(sessionID) + "/rules"
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return nil, err
	}
	return response.Rules, nil
}

// GetSessionAgents returns the AGENTS.md candidates and selected instruction
// prompt for a session's project scope.
func (c *Client) GetSessionAgents(ctx context.Context, sessionID string) (AgentsContext, error) {
	var response AgentsContext
	endpoint := "/api/v1/sessions/" + url.PathEscape(sessionID) + "/agents"
	err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &response)
	return response, err
}

// ListKnowledgeBases 返回服务端配置的知识库。ListKnowledgeBases returns the
// server's configured knowledge bases.
func (c *Client) ListKnowledgeBases(ctx context.Context) ([]KnowledgeBase, error) {
	var bases []KnowledgeBase
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/knowledge/bases", nil, &bases); err != nil {
		return nil, err
	}
	return bases, nil
}

// AddKnowledgeBase 在服务端活动配置层创建知识库。AddKnowledgeBase creates a
// knowledge base in the server's active config layer.
func (c *Client) AddKnowledgeBase(ctx context.Context, base KnowledgeBase) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/knowledge/bases", base, nil)
}

// SetKnowledgeEnabled 更新服务端活动配置层的知识库总开关。
// SetKnowledgeEnabled updates the knowledge feature flag in the server's active config layer.
func (c *Client) SetKnowledgeEnabled(ctx context.Context, enabled bool) error {
	return c.doJSON(ctx, http.MethodPatch, "/api/v1/knowledge/config", struct {
		Enabled bool `json:"enabled"`
	}{Enabled: enabled}, nil)
}

// RemoveKnowledgeBase 按名称从服务端活动配置层移除知识库。RemoveKnowledgeBase
// removes one knowledge base by name from the server's active config layer.
func (c *Client) RemoveKnowledgeBase(ctx context.Context, name string) error {
	return c.doJSON(ctx, http.MethodDelete, "/api/v1/knowledge/bases/"+url.PathEscape(name), nil, nil)
}

// StartKnowledgeScan 启动服务端管理的单知识库异步扫描。StartKnowledgeScan starts
// the server-managed asynchronous scan for one knowledge base.
func (c *Client) StartKnowledgeScan(ctx context.Context, name string) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/knowledge/bases/"+url.PathEscape(name)+"/scan", struct{}{}, nil)
}

// GetKnowledgeScanStatus 返回一个知识库当前的服务端扫描状态。Done=false 且 Message
// 为空表示服务端没有活动扫描任务，此时 Chunks 是已有索引节点数量。
// GetKnowledgeScanStatus returns the server's current scan state for one
// knowledge base. A response with Done=false and an empty Message means the
// server has no active scan job for that base; Chunks is its current index size.
func (c *Client) GetKnowledgeScanStatus(ctx context.Context, name string) (KnowledgeScanStatus, error) {
	var status KnowledgeScanStatus
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/knowledge/bases/"+url.PathEscape(name)+"/scan/status", nil, &status)
	return status, err
}

// SearchKnowledge 向服务端发送 canonical 知识库搜索请求。SearchKnowledge sends a
// canonical knowledge search request to the server.
func (c *Client) SearchKnowledge(ctx context.Context, request KnowledgeSearchRequest) (KnowledgeSearchResponse, error) {
	var response KnowledgeSearchResponse
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/knowledge/search", request, &response)
	return response, err
}

// CompressSession 请求服务端压缩会话历史。CompressSession requests the server to compact session history.
func (c *Client) CompressSession(ctx context.Context, sessionID string) (CompressResult, error) {
	var result CompressResult
	err := c.doJSON(ctx, "POST", "/api/v1/sessions/"+sessionID+"/compress", nil, &result)
	return result, err
}

// ─── StyleGen ────────────────────────────────────────────────────────
// Stylegen runs as a background job on the server: StartStyleGen submits
// it and returns a job id, GetStyleGenJob polls its status.

// StyleGenRequest is the POST /api/v1/stylegen body.
type StyleGenRequest struct {
	Mode           string `json:"mode"`
	StyleID        string `json:"style_id"`
	Label          string `json:"label"`
	Requirement    string `json:"requirement"`
	ConversationID string `json:"conversation_id"`
}

// StyleGenResult is a finished stylegen outcome.
type StyleGenResult struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Prompt  string `json:"prompt"`
	Memory  string `json:"memory"`
	Mode    string `json:"mode"`
	Updated bool   `json:"updated"`
}

// StyleGenStage is one checklist step of a stylegen job.
type StyleGenStage struct {
	Stage string `json:"stage"`
	Label string `json:"label"`
	Done  bool   `json:"done"`
}

// StyleGenJobStatus is the pollable status of a stylegen job.
type StyleGenJobStatus struct {
	ID        string          `json:"id"`
	Status    string          `json:"status"` // running | done | error
	Stages    []StyleGenStage `json:"stages"`
	Result    *StyleGenResult `json:"result"`
	Error     string          `json:"error"`
	ErrorKind string          `json:"error_kind"`
	CreatedAt int64           `json:"created_at"`
	DoneAt    int64           `json:"done_at"`
}

// StartStyleGen submits a stylegen job and returns its job id.
func (c *Client) StartStyleGen(ctx context.Context, req StyleGenRequest) (string, error) {
	var resp struct {
		JobID string `json:"job_id"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/stylegen", req, &resp); err != nil {
		return "", err
	}
	return resp.JobID, nil
}

// GetStyleGenJob polls a stylegen job's status.
func (c *Client) GetStyleGenJob(ctx context.Context, jobID string) (*StyleGenJobStatus, error) {
	var st StyleGenJobStatus
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/stylegen/"+url.PathEscape(jobID), nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// GetSessionContext 返回会话上下文估算。GetSessionContext returns the session context estimate.
func (c *Client) GetSessionContext(ctx context.Context, sessionID string) (ContextInspector, error) {
	var result ContextInspector
	err := c.doJSON(ctx, "GET", "/api/v1/sessions/"+sessionID+"/context", nil, &result)
	return result, err
}

// SetReasoningEffort 设置会话推理强度。SetReasoningEffort sets the session reasoning effort.
func (c *Client) SetReasoningEffort(ctx context.Context, sessionID, level string) (string, error) {
	var result struct {
		ReasoningEffort string `json:"reasoning_effort"`
	}
	err := c.doJSON(ctx, "PATCH", "/api/v1/sessions/"+sessionID+"/reasoning-effort", map[string]string{"level": level}, &result)
	return result.ReasoningEffort, err
}

// ====================================================================
// Stubs the HTTP client is missing today; the HTTP REPL uses them.
// The server doesn't yet expose SetModel/DisplayModel/ProviderModel
// endpoints, so the client returns the locally-cached values it
// knows about (e.g. from the most recent /sessions response).
// ====================================================================

// currentProvider is the provider name the most recent operation
// was on. The client uses it to answer ProviderName() / DisplayModel()
// without round-tripping the server.
func (c *Client) CurrentProvider() string     { return c.currentProvider }
func (c *Client) SetCurrentProvider(p string) { c.currentProvider = p }
func (c *Client) CurrentWorkMode() string     { return c.currentWorkMode }
func (c *Client) SetCurrentWorkMode(m string) { c.currentWorkMode = m }

func (c *Client) ProviderModel() string {
	c.mu.Lock()
	provider := c.currentProvider
	model := c.currentModel
	c.mu.Unlock()
	if provider == "" {
		return ""
	}
	// If SetModel was called, honour that selection.
	if model != "" {
		return model
	}
	models, _ := c.ModelsFor(provider)
	for _, m := range models {
		if m.Default {
			return m.Name
		}
	}
	if len(models) > 0 {
		return models[0].Name
	}
	return ""
}

// SetModel 选择提供商模型；空模型名采用默认模型，否则回退首个可用模型。
// 未知提供商或模型返回错误且不改变缓存；服务端目前没有此操作的独立端点。
// SetModel selects a provider model; an empty name uses its default or first model.
// Unknown selections leave cached state unchanged; the server has no dedicated endpoint.
func (c *Client) SetModel(provider, model string) error {
	models, ok := c.ModelsFor(provider)
	if !ok {
		return fmt.Errorf("provider %q not found", provider)
	}
	if model == "" {
		for _, candidate := range models {
			if candidate.Default {
				model = candidate.Name
				break
			}
		}
		if model == "" && len(models) > 0 {
			model = models[0].Name
		}
	}
	for _, m := range models {
		if m.Name == model {
			c.mu.Lock()
			c.currentProvider = provider
			c.currentModel = model
			c.mu.Unlock()
			return nil
		}
	}
	return fmt.Errorf("model %q not found in provider %q", model, provider)
}

func (c *Client) DisplayModel(provider string) string {
	models, ok := c.ModelsFor(provider)
	if !ok || len(models) == 0 {
		return ""
	}
	for _, m := range models {
		if m.Default && m.DisplayName != "" {
			return m.DisplayName
		}
	}
	return models[0].DisplayName
}

// SubmitQuestionResponse sends the user's answer to a pending
// question back to the server so the agent can continue.
func (c *Client) SubmitQuestionResponse(ctx context.Context, sessionID string, answers map[string]string) error {
	body := map[string]any{
		"answers": answers,
	}
	return c.doJSON(ctx, "POST", "/api/v1/sessions/"+sessionID+"/question-response", body, nil)
}

// SubmitConfirmResponse 提交工具执行确认。SubmitConfirmResponse submits a tool execution confirmation.
func (c *Client) SubmitConfirmResponse(ctx context.Context, sessionID string, approved bool, action string) error {
	return c.SubmitConfirmResponseWithID(ctx, sessionID, approved, action, "")
}

// SubmitConfirmResponseWithID submits a confirmation bound to one exact
// pending request. An empty confirmID keeps compatibility with servers and
// clients that only ever expose one pending confirmation per session.
func (c *Client) SubmitConfirmResponseWithID(ctx context.Context, sessionID string, approved bool, action, confirmID string) error {
	body := struct {
		ConfirmID string `json:"confirm_id,omitempty"`
		Approved  bool   `json:"approved"`
		Action    string `json:"action,omitempty"`
	}{ConfirmID: confirmID, Approved: approved, Action: action}
	return c.doJSON(ctx, "POST", "/api/v1/sessions/"+sessionID+"/confirm-response", body, nil)
}

// Flush is a no-op for the HTTP client; the server persists data
// in its own SQLite store.
func (c *Client) Flush() error { return nil }

// ModelsFor is exposed for the legacy single-model form. Kept here
// to keep the public surface of the client package small.
func (c *Client) ModelsFor(provider string) ([]Model, bool) {
	c.mu.Lock()
	providers := c.cfgProviders
	c.mu.Unlock()
	for _, p := range providers {
		if p.Name == provider {
			if p.Models != nil {
				models := make([]Model, 0, len(p.Models))
				for _, model := range p.Models {
					if model.Type == "" || model.Type == "llm" {
						models = append(models, model)
					}
				}
				return models, true
			}
			if p.Model != "" {
				return []Model{{Name: p.Model, Default: true}}, true
			}
			return nil, true
		}
	}
	return nil, false
}

// SetCfgProviders lets callers (typically the REPL on startup)
// inject the configured providers so the client can answer model
// lookups locally. The HTTP REPL reads this from the
// /api/v1/providers endpoint.
func (c *Client) SetCfgProviders(ps []ProviderInfo) {
	c.mu.Lock()
	c.cfgProviders = ps
	c.mu.Unlock()
}

// Internal state.
func (c *Client) cfgProv() []ProviderInfo { return c.cfgProviders }

// ====================================================================
// Rollback API
// ====================================================================

// RollbackResult is the server response for a rollback request.
type RollbackResult struct {
	DeletedCount    int       `json:"deleted_count"`
	DeletedMessages []Message `json:"deleted_messages"`
}

// RollbackMessages deletes the message with beforeID and all later
// messages in the session. Returns the deleted messages for undo.
func (c *Client) RollbackMessages(ctx context.Context, sessionID string, beforeID int64) (*RollbackResult, error) {
	body := struct {
		BeforeID int64 `json:"before_id"`
	}{BeforeID: beforeID}
	var result RollbackResult
	if err := c.doJSON(ctx, "POST", "/api/v1/sessions/"+sessionID+"/rollback", body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// UndoRollbackMessages restores previously-deleted messages.
func (c *Client) UndoRollbackMessages(ctx context.Context, sessionID string, messages []Message) error {
	body := struct {
		Messages []Message `json:"messages"`
	}{Messages: messages}
	return c.doJSON(ctx, "POST", "/api/v1/sessions/"+sessionID+"/rollback/undo", body, nil)
}

// ForkSession creates a new session containing all messages up to and
// including beforeID from the source session.
func (c *Client) ForkSession(ctx context.Context, sessionID string, beforeID int64) (*Session, error) {
	body := map[string]int64{"before_id": beforeID}
	var s Session
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/sessions/"+sessionID+"/fork", body, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
