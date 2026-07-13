package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/mcp"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/project"
	"github.com/p-chat/pchat/internal/search"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
	"github.com/p-chat/pchat/internal/version"
)

// Handler serves the P-Chat HTTP API. It holds references to the
// agent and the persistent memory store so that messages and
// sessions survive across requests.
type Handler struct {
	agent      *agent.Agent
	cfg        atomic.Pointer[config.Config]
	store      *memory.Store
	styleMgr   *style.Manager
	summarizer *memory.Summarizer
	mcpMgr     *mcp.Manager

	metaMu sync.Mutex
	// sessionLocks serialises concurrent SendMessage calls per
	// session to prevent interleaved message writes.
	sessionLocks sync.Map // string → struct{}
	meta   map[string]sessionMeta
}

type sessionMeta struct {
	Style           string
	Provider        string
	Model           string
	ReasoningEffort string // "off" | "low" | "medium" | "high" | "max"
	ProjectPath     string // project root directory, "" = global
	PlanMode        bool   // plan mode (no tools, single turn)
	PermissionLevel string // "ask" | "auto" | "full"
	KnowledgeBase   string // "" = off, "__all__" = all bases, or a specific base name
}

// sessionMetaBlob is the on-disk shape written to
// conversations.metadata. The field names are JSON lower-case so
// the web side can pass them straight back to the PATCH endpoint.
type sessionMetaBlob struct {
	Style           string `json:"style,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	ProjectPath     string `json:"project_path,omitempty"`
	PlanMode        bool   `json:"plan_mode,omitempty"`
	PermissionLevel string `json:"permission_level,omitempty"`
	KnowledgeBase   string `json:"knowledge_base,omitempty"`
}

func NewHandler(a *agent.Agent, cfg *config.Config, store *memory.Store, styleMgr *style.Manager, mcpMgr *mcp.Manager) *Handler {
	h := &Handler{
		agent:    a,
		store:    store,
		styleMgr: styleMgr,
		mcpMgr:   mcpMgr,
		meta:     make(map[string]sessionMeta),
	}
	h.cfg.Store(cfg)
	// Wire the upload resolver so the agent can read attached
	// files by their upload id. The resolver lives in the agent
	// (not the handler) because attachment expansion happens
	// inside the LLM call path, after the handler has already
	// handed the request to the agent.
	a.SetAttachmentResolver(&agent.DiskAttachmentResolver{BaseDir: UploadDir()})
	return h
}

// getCfg returns the current config snapshot. Safe for
// concurrent use; the underlying atomic.Pointer gives every
// reader a consistent pointer to a specific Config value.
// Replaces the previous direct `h.cfg` field access, which
// was a data race with reloadAfterConfigChange.
func (h *Handler) getCfg() *config.Config {
	return h.cfg.Load()
}

// setSessionMeta updates the in-memory cache and, when any field
// actually changes, writes the new meta blob through to the
// conversations.metadata column. Empty arguments are ignored (so
// "leave provider alone, just change model" is expressible).
func (h *Handler) setSessionMeta(id, style, provider, model string) {
	h.metaMu.Lock()
	defer h.metaMu.Unlock()
	m := h.meta[id]
	changed := false
	if style != "" && style != m.Style {
		m.Style = style
		changed = true
	}
	if provider != "" && provider != m.Provider {
		m.Provider = provider
		changed = true
	}
	if model != "" && model != m.Model {
		m.Model = model
		changed = true
	}
	if !changed {
		return
	}
	h.meta[id] = m
	if h.store == nil {
		return
	}
		blob, _ := json.Marshal(sessionMetaBlob{Style: m.Style, Provider: m.Provider, Model: m.Model, ReasoningEffort: m.ReasoningEffort, ProjectPath: m.ProjectPath, PlanMode: m.PlanMode, PermissionLevel: m.PermissionLevel, KnowledgeBase: m.KnowledgeBase})
	if err := h.store.UpdateConversationMeta(id, string(blob)); err != nil {
		// Non-fatal: in-memory map already updated, request still
		// works for this session. The next setSessionMeta call
		// will retry the write.
		return
	}
}

// ensureMetaLoaded re-hydrates the in-memory meta map for `id`
// from conversations.metadata, on first read. After the first
// hit the map stays warm for the rest of the process lifetime.
func (h *Handler) ensureMetaLoaded(id string) sessionMeta {
	h.metaMu.Lock()
	defer h.metaMu.Unlock()
	if m, ok := h.meta[id]; ok {
		return m
	}
	m := sessionMeta{}
	if h.store != nil {
		if cv, err := h.store.GetConversation(id); err == nil && cv.Metadata != "" {
			var blob sessionMetaBlob
			if json.Unmarshal([]byte(cv.Metadata), &blob) == nil {
				m.Style = blob.Style
				m.Provider = blob.Provider
				m.Model = blob.Model
				m.ReasoningEffort = blob.ReasoningEffort
				m.ProjectPath = blob.ProjectPath
				m.PlanMode = blob.PlanMode
				m.PermissionLevel = blob.PermissionLevel
				m.KnowledgeBase = blob.KnowledgeBase
			}
		}
	}
	h.meta[id] = m
	return m
}

func (h *Handler) sessionStyle(id string) string {
	m := h.ensureMetaLoaded(id)
	return m.Style
}

func (h *Handler) sessionProvider(id string) string {
	m := h.ensureMetaLoaded(id)
	if p := m.Provider; p != "" {
		return p
	}
	// Fall back to the configured default
	return h.getCfg().LLM.Default
}

// sessionModel returns the per-session model name, falling back to
// the provider's default model (EffectiveModel) when no override is
// set. Returns "" if the provider itself is unknown.
func (h *Handler) sessionModel(id, provider string) string {
	m := h.ensureMetaLoaded(id)
	if m.Model != "" {
		return m.Model
	}
	for _, p := range h.getCfg().LLM.Providers {
		if p.Name == provider {
			return p.EffectiveModel()
		}
	}
	return ""
}

// sessionProjectPath returns the project path for a session.
func (h *Handler) sessionProjectPath(id string) string {
	m := h.ensureMetaLoaded(id)
	return m.ProjectPath
}

// setSessionMetaProjectPath updates just the project_path field.
func (h *Handler) setSessionMetaProjectPath(id, projectPath string) {
	h.metaMu.Lock()
	m := h.meta[id]
	m.ProjectPath = projectPath
	h.meta[id] = m
	h.metaMu.Unlock()
	if h.store != nil {
			blob, _ := json.Marshal(sessionMetaBlob{Style: m.Style, Provider: m.Provider, Model: m.Model, ReasoningEffort: m.ReasoningEffort, ProjectPath: m.ProjectPath, PlanMode: m.PlanMode, PermissionLevel: m.PermissionLevel, KnowledgeBase: m.KnowledgeBase})
		_ = h.store.UpdateConversationMeta(id, string(blob))
	}
}

// validProvider returns true if name is a configured provider.
func (h *Handler) validProvider(name string) bool {
	for _, p := range h.getCfg().LLM.Providers {
		if p.Name == name {
			return true
		}
	}
	return false
}

// validModel returns true if name exists under provider
// (configured models list) OR is the provider's single-model
// legacy form (ProviderConfig.Model).
func (h *Handler) validModel(provider, name string) bool {
	for _, p := range h.getCfg().LLM.Providers {
		if p.Name != provider {
			continue
		}
		if p.Model == name {
			return true
		}
		for _, m := range p.Models {
			if m.Name == name {
				return true
			}
		}
		return false
	}
	return false
}

// --- Request/Response types ---

// SendMessageRequest is the body of POST /sessions/:id/messages.
type SendMessageRequest struct {
	Message string `json:"message" binding:"required"`
	Style   string `json:"style,omitempty"`
	// Provider / Model, when set, override the per-session defaults
	// for this turn. They are also written back to the per-session
	// meta so subsequent turns keep using the new model. Empty
	// values mean "no change".
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// Attachments are the files the user attached to this turn.
	// The new SPA path sends the bytes inline in `Data` (a data:
	// URL for binaries, raw text for text files). The legacy
	// path sends an `id` from /api/v1/uploads which the
	// resolver reads from disk. Either way the handler hands
	// the agent a list and the agent turns them into a
	// multi-part trailing user message before the LLM call.
	// The protocol-specific serialisation (OpenAI image_url vs
	// Anthropic image+source) is handled by the LLM client.
	Attachments []agent.Attachment `json:"attachments,omitempty"`
	// SkillContext is the full SKILL.md content for a skill
	// activated via /skillname slash command.
	SkillContext string `json:"skill_context,omitempty"`
}

// CreateSessionRequest is the body of POST /sessions.
type CreateSessionRequest struct {
	Style       string `json:"style,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	Title       string `json:"title,omitempty"`
	ProjectPath string `json:"project_path,omitempty"`
}

// RenameSessionRequest is the body of PATCH /sessions/:id when the
// caller only wants to change the title.
type RenameSessionRequest struct {
	Title string `json:"title" binding:"required"`
}

// UpdateSessionMetaRequest is the body of PATCH /sessions/:id when
// the caller wants to change provider / model / style. All fields
// are pointers so the client can send partial updates — a missing
// field means "leave that field alone", a non-nil field means
// "set this to the new value (possibly empty)".
type UpdateSessionMetaRequest struct {
	Provider *string `json:"provider,omitempty"`
	Model    *string `json:"model,omitempty"`
	Style    *string `json:"style,omitempty"`
	// PermissionLevel sets the sandbox permission level for this session.
	// Values: "ask", "auto", "full". Omit to leave unchanged.
	PermissionLevel *string `json:"permission_level,omitempty"`
	// VectorStore sets the knowledge base vector store for this session.
	// Empty string resets to the global default.
	VectorStore *string `json:"vector_store,omitempty"`
	// KnowledgeBase selects a knowledge base. "" / "__off__" = off,
	// "__all__" = all bases, or a specific base name.
	KnowledgeBase *string `json:"knowledge_base,omitempty"`
}

// SessionResponse is the JSON form of a memory.Conversation.
// Provider / Model / Style reflect the per-session overrides
// (resolved from the in-memory + on-disk meta blob, with the
// process default for unset fields).
type SessionResponse struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	Style       string `json:"style,omitempty"`
	ProjectPath string `json:"project_path,omitempty"`
	PlanMode    bool   `json:"plan_mode,omitempty"`
	PermissionLevel string `json:"permission_level,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	VectorStore    string `json:"vector_store,omitempty"`
	KnowledgeBase  string `json:"knowledge_base,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// MessageResponse is the JSON form of a single message in a
// conversation history.
type MessageResponse struct {
	ID         int64  `json:"id"`
	// Seq is the per-conversation logical position. Unlike
	// `id` (a global AUTOINCREMENT that's never reused), seq
	// survives rollback+undo and is the new stable cursor
	// for the pagination API. Clients should prefer seq over
	// id for any identity that needs to survive a rollback
	// (Vue :key, undo payload, infinite-scroll cursor). The
	// id field is kept for back-compat with clients built
	// before the seq field was added.
	Seq        int64  `json:"seq"`
	Role       string `json:"role"`
	MsgType    int    `json:"msg_type"`
	Content    string `json:"content"`
	CreatedAt  int64  `json:"created_at"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
	// SubmitToLLM is the round-tripped value the agent
	// used to decide whether to include this row in the
	// LLM context. Restored on undo (was hard-coded to
	// 1 pre-fix, which leaked thinking/tool rows back
	// into the LLM context).
	SubmitToLLM int `json:"submit_to_llm,omitempty"`
	// Provider / Model that produced the assistant's reply, when
	// known. Populated only for messages tagged at request time;
	// the legacy single-conversation flow leaves them empty.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// Attachments are the image/file parts that were sent with this
	// user message, in their wire form (data URLs for images, text
	// blocks for files). The frontend renders them as part of the
	// message bubble so the user can see what was actually sent.
	Attachments []AttachmentPart `json:"attachments,omitempty"`
	// Parts is the assistant message's structured rendering:
	// text + thinking + tool calls + sub-agent cards in stream
	// order. Mirrors src/api/client.ts:MessagePart on the client.
	// Populated only for assistant messages; the agent encodes
	// it as JSON in messages.metadata and the handler decodes
	// it back here so a session reload replays the same view
	// the user saw during streaming. Without this, thinking
	// blocks and tool cards vanish on every reopen.
	Parts []MessagePart `json:"parts,omitempty"`
}

// MessagePart is one block of a structured assistant message.
// Wire shape is identical to the client-side MessagePart so the
// web UI can drop it directly into its `message.parts` array.
//
// Field JSON tags are split in two:
//
//   * The structural fields (Kind / Text / Status / etc.) use
//     the wire format directly — both server and frontend
//     agree on snake_case for these (the frontend's TS type
//     uses snake_case here, mirroring the server's wire).
//
//   * The sub-agent metadata fields (AgentType / AgentColor /
//     AgentModel / TaskID / AgentDescription) use snake_case
//     JSON tags to match the storage format the agent writes
//     to meta["parts"] (see internal/agent/parts.go). A
//     custom MarshalJSON below re-emits them in camelCase on
//     the wire to match the frontend's TypeScript MessagePart
//     type (client.ts:121-132), which uses camelCase for
//     these fields. Without the custom marshal, the
//     snake_case keys would arrive at the frontend with no
//     matching TS property and the SubAgentCard would render
//     without its header label / accent color / model chip /
//     task_id badge / description tooltip on session reload.
//
// QuestionStatus is the odd one out: it's part of the
// question card, where the frontend type uses snake_case
// (client.ts:143), so its JSON tag here is also snake_case.
// Go's encoding/json silently drops unknown fields on
// unmarshal, so a struct that omitted any of these fields
// would lose the data across a save → load round-trip and
// the corresponding card would render stale.
type MessagePart struct {
	Kind      string        `json:"kind"`
	Text      string        `json:"text,omitempty"`
	Streaming bool          `json:"streaming,omitempty"`
	Name      string        `json:"name,omitempty"`
	Args      string        `json:"args,omitempty"`
	Status    string        `json:"status,omitempty"`
	Result    string        `json:"result,omitempty"`
	Error     string        `json:"error,omitempty"`
	Elapsed   string        `json:"elapsed,omitempty"`
	Task      string        `json:"task,omitempty"`
	Parts     []MessagePart `json:"parts,omitempty"`
	ToolID    string        `json:"tool_id,omitempty"`
	// Question part lifecycle ("open" / "ok" / "error").
	// Snake_case to match the frontend's TS type.
	QuestionStatus string `json:"question_status,omitempty"`
	// Sub-agent metadata. Snake_case to match storage;
	// MarshalJSON below re-emits as camelCase on the wire.
	AgentType        string `json:"agent_type,omitempty"`
	AgentColor       string `json:"agent_color,omitempty"`
	AgentModel       string `json:"agent_model,omitempty"`
	TaskID           string `json:"task_id,omitempty"`
	AgentDescription string `json:"agent_description,omitempty"`
}

// messagePartWire is the on-the-wire shape of MessagePart,
// used by MarshalJSON to translate the snake_case storage
// fields into the camelCase keys the frontend's TypeScript
// MessagePart type expects. Keeping this private + inline
// avoids polluting the rest of the package with a second
// near-identical type — the conversion is mechanical, so
// making it explicit in one method is more readable than
// scattering field copies across callers.
type messagePartWire struct {
	Kind      string        `json:"kind"`
	Text      string        `json:"text,omitempty"`
	Streaming bool          `json:"streaming,omitempty"`
	Name      string        `json:"name,omitempty"`
	Args      string        `json:"args,omitempty"`
	Status    string        `json:"status,omitempty"`
	Result    string        `json:"result,omitempty"`
	Error     string        `json:"error,omitempty"`
	Elapsed   string        `json:"elapsed,omitempty"`
	Task      string        `json:"task,omitempty"`
	Parts     []MessagePart `json:"parts,omitempty"`
	ToolID    string        `json:"tool_id,omitempty"`
	// QuestionStatus stays snake_case — see MessagePart
	// doc comment.
	QuestionStatus string `json:"question_status,omitempty"`
	// Sub-agent metadata, camelCase wire format.
	AgentType        string `json:"agentType,omitempty"`
	AgentColor       string `json:"agentColor,omitempty"`
	AgentModel       string `json:"agentModel,omitempty"`
	TaskID           string `json:"taskId,omitempty"`
	AgentDescription string `json:"agentDescription,omitempty"`
}

// MarshalJSON emits the wire format for MessagePart. The
// structural fields pass through with their declared tags;
// the sub-agent metadata fields are re-emitted in camelCase
// to match the frontend's TypeScript MessagePart type (see
// MessagePart doc comment for why the storage side uses
// snake_case but the wire side uses camelCase for these
// fields).
func (p MessagePart) MarshalJSON() ([]byte, error) {
	w := messagePartWire{
		Kind:             p.Kind,
		Text:             p.Text,
		Streaming:        p.Streaming,
		Name:             p.Name,
		Args:             p.Args,
		Status:           p.Status,
		Result:           p.Result,
		Error:            p.Error,
		Elapsed:          p.Elapsed,
		Task:             p.Task,
		Parts:            p.Parts,
		ToolID:           p.ToolID,
		QuestionStatus:   p.QuestionStatus,
		AgentType:        p.AgentType,
		AgentColor:       p.AgentColor,
		AgentModel:       p.AgentModel,
		TaskID:           p.TaskID,
		AgentDescription: p.AgentDescription,
	}
	return json.Marshal(w)
}

// AttachmentPart is a single part of a multi-content message,
// suitable for the frontend to render. Mirrors the OpenAI
// ChatMessagePart shape but is scoped to what the UI needs.
type AttachmentPart struct {
	Type     string `json:"type"`               // "image_url" | "text"
	URL      string `json:"url,omitempty"`       // data URL for images
	Text     string `json:"text,omitempty"`      // text body for text parts
	Name     string `json:"name,omitempty"`     // original filename, for display
	MIME     string `json:"mime,omitempty"`     // MIME type
	Kind     string `json:"kind,omitempty"`     // image / audio / text / file
}

// StreamEvent is one chunk of a Server-Sent Events stream from
// POST /sessions/:id/messages. The Type field is one of:
// "content", "thinking", "tool", "phase", "error", "done".
//
// The wire is intentionally flat: every event carries
// provider+model so the client doesn't have to remember
// per-session state, and any combination of fields may be
// non-empty on any event. The client discriminates by
// (type, content, thinking, tool_*, sub_agent_*) — not by
// exclusive categories.
type StreamEvent struct {
	Type string `json:"type"`
	// Content is an assistant text delta. Type "content" on
	// its own; "thinking" events have non-empty Thinking
	// and empty Content.
	Content string `json:"content,omitempty"`
	// Thinking is a reasoning / chain-of-thought delta.
	// Only emitted by LLM clients that surface a separate
	// reasoning stream (Anthropic thinking blocks,
	// DeepSeek reasoning_content, OpenAI o1 reasoning).
	// Type "thinking" on its own.
	Thinking string `json:"thinking,omitempty"`
	Phase    string `json:"phase,omitempty"`
	Step     string `json:"step,omitempty"`
	Message  string `json:"message,omitempty"`

	// Tool fields — Type "tool".
	ToolID   string `json:"tool_id,omitempty"`
	ToolName string `json:"tool_name,omitempty"`
	ToolStatus  string `json:"tool_status,omitempty"`
	ToolResult  string `json:"tool_result,omitempty"`
	// ToolResultFull is the untruncated tool result for tools
	// whose output the frontend needs to parse (todo_write).
	// ToolResult is a 300-char preview for display; ToolResultFull
	// preserves the full payload (newlines and all) so the
	// frontend can JSON.parse it without corruption. The chat
	// store uses ToolResultFull in preference to ToolResult when
	// the tool name is todo_write / question.
	ToolResultFull string `json:"tool_result_full,omitempty"`
	ToolError      string `json:"tool_error,omitempty"`
	ToolElapsed    string `json:"tool_elapsed,omitempty"`
	// ToolArgs is the JSON-encoded arguments string the
	// tool was called with (best-effort; LLM clients only
	// surface this once the call is complete, not as a
	// delta).
	ToolArgs string `json:"tool_args,omitempty"`

	// Sub-agent fields. When SubAgent is true, the event
	// originated from a `task` tool's child run, not the
	// parent agent. The UI renders the stream of such
	// events inside a nested card with header
	// `SubAgentTask`. The card's outer status (running /
	// ok / error) is driven by the matching
	// "sub_agent_start" / "sub_agent_ok" / "sub_agent_err"
	// phase events.
	SubAgent       bool   `json:"sub_agent,omitempty"`
	SubAgentTask   string `json:"sub_agent_task,omitempty"`
	SubAgentStatus string `json:"sub_agent_status,omitempty"`
	// SubAgentType is the agent name (e.g. "explore",
	// "plan", "general-purpose", or a custom agent from
	// .p-chat/agent/*.md). Surfaced in the SubAgentCard
	// header so the user can see which agent ran.
	SubAgentType string `json:"sub_agent_type,omitempty"`
	// SubAgentColor is the agent's accent color ("#RRGGBB"
	// or CSS color name). Tints the card border + badge.
	SubAgentColor string `json:"sub_agent_color,omitempty"`
	// SubAgentModel is the model the sub-agent is using
	// (e.g. "gpt-4o-mini"). Shown as a small chip in the
	// card header.
	SubAgentModel string `json:"sub_agent_model,omitempty"`
	// SubAgentTaskID is the resume-by-id key. Surfaced as
	// a monospace badge in the card footer.
	SubAgentTaskID string `json:"sub_agent_task_id,omitempty"`
	// SubAgentDescription is the agent's "when to use" hint.
	// Surfaced as a hover tooltip on the agent-name badge
	// in the SubAgentCard so the user can read the full
	// hint without expanding the card body.
	SubAgentDescription string `json:"sub_agent_description,omitempty"`

	// ThinkingRewrite is the post-stream redactor's
	// replacement text for the LLM's thinking block. The
	// UI should REPLACE the trailing thinking part's text
	// with this value (same pattern as content_rewrite
	// for the text body). Empty when no rewrite is needed.
	ThinkingRewrite string `json:"thinking_rewrite,omitempty"`

	// SubAgentFailureReason explains why the sub-agent
	// failed. Only set on `sub_agent_err` close events.
	// The Wails GUI surfaces this in the SubAgentCard
	// header so the user can tell "stream tail-end
	// hiccup" (soft fail, content was already produced)
	// from "could not reach the LLM" (hard fail, no
	// content). Empty on `sub_agent_ok` close events.
	SubAgentFailureReason string `json:"sub_agent_failure_reason,omitempty"`

	// Done fields
	TokensIn      int    `json:"tokens_in,omitempty"`
	TokensOut     int    `json:"tokens_out,omitempty"`
	Elapsed       string `json:"elapsed,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Model         string `json:"model,omitempty"`
	// UserMessageID is the SQLite row id of the user message
	// that started this turn. Set only on the "done" event so
	// the frontend can stamp it on the local Message for fork.
	UserMessageID int64 `json:"user_message_id,omitempty"`
	// LastMessageID is the highest row id in this session
	// (typically the assistant reply just produced). Used to
	// stamp the assistant message for fork targeting.
	LastMessageID int64 `json:"last_message_id,omitempty"`

	// Question fields — when the question tool is called, the
	// server emits a "question" event with question_json set
	// to the serialized question array. The frontend renders
	// a modal and posts the answer back.
	QuestionJSON string `json:"question_json,omitempty"`

	// ToolConfirm fields — when the sandbox requires user
	// confirmation before executing a tool.
	ToolConfirmJSON string `json:"tool_confirm_json,omitempty"`

	// SessionStatus is the lifecycle signal for a chat turn:
	// "busy" at the start of the agent loop, "idle" when it
	// exits (any reason — success, error, cancel, max-rounds,
	// stuck, panic). The frontend uses it to drive the
	// TodoPanel state machine: `live = session_status === "busy"`.
	// Without this signal, the UI has no way to tell "the LLM
	// is mid-turn, don't clear stale todos" from "the LLM
	// stopped and forgot to clear them".
	SessionStatus string `json:"session_status,omitempty"`

	// Error fields
	Error      string `json:"error,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
	// ErrorKind is the classification of the error
	// ("auth_error", "rate_limit", "vision_unsupported", …).
	// Empty when the chunk isn't an error or wasn't
	// classified. The UI uses "vision_unsupported" to
	// render a special chip on the offending user message.
	ErrorKind string `json:"error_kind,omitempty"`
}

// --- Health / metadata ---

func (h *Handler) Health(c *gin.Context) {
	// Probe the memory store so a load balancer or
	// orchestrator sees unhealthy if the DB is wedged.
	// A simple ping is enough; we don't care about
	// business state.
	if h.store != nil {
		if err := h.store.Ping(); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "degraded",
				"error":  "store ping failed: " + err.Error(),
			})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// VersionHandler GET /api/v1/version
func (h *Handler) VersionHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"version":    version.String(),
		"full":       version.FullString(),
		"git_commit": version.GitCommit,
	})
}

// MigrationStatus GET /api/v1/migrations
func (h *Handler) MigrationStatus(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	current, available, err := h.store.AppliedMigrations()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"current":   current,
		"available": available,
	})
}

// MigrationRollback POST /api/v1/migrations/rollback
func (h *Handler) MigrationRollback(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	var body struct {
		Target int `json:"target"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if body.Target < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "target must be >= 0"})
		return
	}
	if err := h.store.Rollback(body.Target); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	current, available, _ := h.store.AppliedMigrations()
	c.JSON(http.StatusOK, gin.H{
		"ok":        true,
		"current":   current,
		"available": available,
	})
}

// StyleMeta is the wire shape returned by /api/v1/styles. id is
// the machine identifier (used as the session-meta value), label
// is the human-readable display name, and desc is a one-line
// description that comes from the style's identity/*.md file's
// first non-empty paragraph (or a generic fallback when the
// style has no description of its own).
type StyleMeta struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Desc  string `json:"desc"`
}

func (h *Handler) Styles(c *gin.Context) {
	if h.styleMgr == nil {
		c.JSON(http.StatusOK, gin.H{"styles": []StyleMeta{}})
		return
	}
	out := []StyleMeta{}
	for _, s := range h.styleMgr.ListAll() {
		out = append(out, StyleMeta{
			ID:    string(s),
			Label: h.styleMgr.DisplayLabel(s),
			Desc:  styleDescFor(h.styleMgr, s),
		})
	}
	c.JSON(http.StatusOK, gin.H{"styles": out})
}

// styleDescFor extracts a one-line description from a style's
// prompt text (first non-empty, non-heading line).
func styleDescFor(m *style.Manager, s style.Style) string {
	prompt, err := m.GetIdentity(s)
	if err != nil || prompt == "" {
		return ""
	}
	for _, line := range strings.Split(prompt, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if strings.HasPrefix(trim, "#") {
			continue
		}
		r := []rune(trim)
		if len(r) > 60 {
			return string(r[:60]) + "…"
		}
		return trim
	}
	return ""
}

// CreateStyleRequest is the POST /api/v1/styles body.
// v2 uses "prompt" (single merged field); v1 "identity"/"soul" are
// accepted for backward compat and merged with a --- separator.
type CreateStyleRequest struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Identity string `json:"identity,omitempty"`
	Soul     string `json:"soul,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
	Memory   string `json:"memory,omitempty"`
}

func (h *Handler) CreateStyle(c *gin.Context) {
	if h.styleMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "style manager not available"})
		return
	}
	var req CreateStyleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	prompt := req.Prompt
	if prompt == "" {
		// v1 compat: merge identity + soul
		id := req.Identity
		so := req.Soul
		if id == "" {
			id = "# P-Chat AI 编程程序\n\n当前是 P-Chat AI 编程程序。\n"
		}
		if so == "" {
			so = "你是一个 AI 助手。"
		}
		prompt = id + "\n\n---\n\n" + so
	}
	s, err := h.styleMgr.Create(req.ID, req.Label, prompt, req.Memory)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"id":    string(s),
		"label": h.styleMgr.DisplayLabel(s),
		"desc":  styleDescFor(h.styleMgr, s),
	})
}

// GetStyle returns the full prompt of a single style.
func (h *Handler) GetStyle(c *gin.Context) {
	if h.styleMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "style manager not available"})
		return
	}
	id := c.Param("id")
	s := style.Style(id)
	prompt, err := h.styleMgr.GetIdentity(s)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	memory, _ := h.styleMgr.GetMemory(s)
	c.JSON(http.StatusOK, gin.H{
		"id":     id,
		"label":  h.styleMgr.DisplayLabel(s),
		"prompt": prompt,
		"memory": memory,
	})
}

// UpdateStyleRequest is the PATCH /api/v1/styles/:id body.
// v2 uses "prompt"; v1 "identity"/"soul" accepted and merged.
type UpdateStyleRequest struct {
	Label    string `json:"label,omitempty"`
	Identity string `json:"identity,omitempty"`
	Soul     string `json:"soul,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
	Memory   string `json:"memory,omitempty"`
}

func (h *Handler) UpdateStyle(c *gin.Context) {
	if h.styleMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "style manager not available"})
		return
	}
	id := c.Param("id")
	var req UpdateStyleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	prompt := req.Prompt
	if prompt == "" && (req.Identity != "" || req.Soul != "") {
		// v1 compat: merge identity + soul
		prompt = req.Identity + "\n\n---\n\n" + req.Soul
	}
	if err := h.styleMgr.Update(id, req.Label, prompt, req.Memory); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": id})
}

func (h *Handler) DeleteStyle(c *gin.Context) {
	if h.styleMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "style manager not available"})
		return
	}
	id := c.Param("id")
	if err := h.styleMgr.Delete(id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": id})
}

func (h *Handler) Providers(c *gin.Context) {
	type modelInfo struct {
		config.ModelConfig
		SupportsVision bool `json:"supports_vision"`
	}
	type providerInfo struct {
		Name      string      `json:"name"`
		Model     string      `json:"model"`
		Protocol  string      `json:"protocol"`
		IsDefault bool        `json:"is_default"`
		Models    []modelInfo `json:"models"`
	}

	providers := []providerInfo{}
	for _, p := range h.getCfg().LLM.Providers {
		raw := p.AllModels()
		ms := make([]modelInfo, 0, len(raw))
		for _, m := range raw {
			ms = append(ms, modelInfo{
				ModelConfig:    m,
				SupportsVision: m.Capabilities.SupportsVision,
			})
		}
		providers = append(providers, providerInfo{
			Name:      p.Name,
			Model:     p.EffectiveModel(),
			Protocol:  p.GetProtocol(),
			IsDefault: p.Name == h.getCfg().LLM.Default,
			Models:    ms,
		})
	}
	c.JSON(http.StatusOK, gin.H{"providers": providers})
}

// --- Session CRUD ---

func (h *Handler) ListSessions(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	projectPath := c.Query("project_path")
	hasProjectParam := c.Request.URL.Query().Has("project_path")
	// Cap the list at 200 to bound the response size for users
	// with thousands of sessions. The SPA can paginate via
	// future ?before_id/limit params if it needs more.
	convs := h.store.ListConversationsLimit(200)
	out := make([]SessionResponse, 0, len(convs))
	for _, conv := range convs {
		resp := h.sessionToResponse(conv)
		if projectPath != "" {
			if resp.ProjectPath != projectPath {
				continue
			}
		} else if hasProjectParam {
			if resp.ProjectPath != "" {
				continue
			}
		}
		out = append(out, resp)
	}
	c.JSON(http.StatusOK, gin.H{"sessions": out})
}

func (h *Handler) SearchMessages(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	q := c.Query("q")
	if q == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query parameter 'q' is required"})
		return
	}
	// Cap the query length and limit. A user-supplied 10KB
	// string of `%` wildcards in a single search can be a
	// denial-of-service vector — SQLite's LIKE is O(N) and
	// pathological patterns can drive the query to take
	// seconds. Combined with the missing `limit` cap (e.g.
	// `?limit=1000000` would be honored), this is a resource
	// amplification concern.
	if len(q) > 256 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query 'q' is too long (max 256 chars)"})
		return
	}
	limit := parseIntQuery(c, "limit", 20)
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	results := h.store.SearchMessages(q, limit)
	if results == nil {
		results = []memory.SearchResult{}
	}
	c.JSON(http.StatusOK, gin.H{"results": results})
}

func (h *Handler) TokenStats(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	stats := h.store.TokenStats()
	if stats == nil {
		stats = []memory.ConversationTokenStats{}
	}
	c.JSON(http.StatusOK, gin.H{"stats": stats})
}

func (h *Handler) CreateSession(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	var req CreateSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Allow empty body for "create with defaults"
		req = CreateSessionRequest{}
	}

	id, err := h.store.NewConversation()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if req.Title != "" {
		_ = h.store.RenameConversation(id, req.Title)
	}

	// Resolve the effective provider/model for this new session.
	// Priority: request body → configured default provider →
	// that provider's default model. Validate before persisting
	// so we never end up with a session pointing at a stale
	// (deleted) provider/model.
	provider := req.Provider
	if provider == "" {
		provider = h.getCfg().LLM.Default
	}
	if !h.validProvider(provider) {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unknown provider %q", provider)})
		return
	}
	model := req.Model
	if model == "" {
		for _, p := range h.getCfg().LLM.Providers {
			if p.Name == provider {
				model = p.EffectiveModel()
				break
			}
		}
	} else if !h.validModel(provider, model) {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("model %q not found under provider %q", model, provider)})
		return
	}

	h.setSessionMeta(id, req.Style, provider, model)
	// Store project path if provided.
	if req.ProjectPath != "" {
		h.setSessionMetaProjectPath(id, req.ProjectPath)
	}

	// Re-fetch and return the full session record.
	convs := h.store.ListConversations()
	for _, cv := range convs {
		if cv.ID == id {
			c.JSON(http.StatusCreated, h.sessionToResponse(cv))
			return
		}
	}
	// Shouldn't happen, but fall back to just returning the id.
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

func (h *Handler) GetSession(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	convs := h.store.ListConversations()
	for _, cv := range convs {
		if cv.ID == id {
			c.JSON(http.StatusOK, h.sessionToResponse(cv))
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
}

func (h *Handler) DeleteSession(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	if err := h.store.ArchiveConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	h.metaMu.Lock()
	delete(h.meta, id)
	h.metaMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"archived": id})
}

// PermanentDeleteSession DELETE /api/v1/sessions/:id/permanent
func (h *Handler) PermanentDeleteSession(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	if err := h.store.DeleteConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	h.metaMu.Lock()
	delete(h.meta, id)
	h.metaMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

// ClearSessionMessages DELETE /api/v1/sessions/:id/messages
func (h *Handler) ClearSessionMessages(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	if err := h.store.ClearMessages(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"cleared": id})
}

// RollbackMessages POST /api/v1/sessions/:id/rollback
// Deletes the message with the given id and all messages after it.
// Returns the deleted messages so the client can undo.
//
// The deleted messages are returned in the same wire shape as
// ListMessages (`MessageResponse`, with `parts` decoded from
// `metadata`) — NOT the raw `memory.Message` shape. Without
// this, the frontend's undoRollback splices the messages back
// into its in-memory array but each message has `parts =
// undefined` (only `metadata` as a raw JSON string, which the
// Message type doesn't read). MessageBubble.vue then falls back
// to plain-text rendering of `content`, silently dropping the
// thinking block, tool call cards, sub-agent cards, and
// question cards that the user had before rolling back. The
// undo "restores the messages" but visually the structural
// formatting is gone — see the bug report from 2026-07-09.
//
// buildMessageResponse filters out tool_call / tool_result /
// exec_command rows (their data is already embedded in the
// parent assistant message's `parts` array — exactly what
// the parts-driven render path consumes). `deleted_count` is
// therefore the count of items the frontend splices back via
// `msgs.splice(fromIndex, 0, ...deleted_messages)`, which
// matches the count of items it had originally removed via
// `msgs.splice(messageIndex)` (the in-memory array was loaded
// through the same filter).
func (h *Handler) RollbackMessages(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	var req struct {
		BeforeID int64 `json:"before_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.BeforeID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "before_id is required and must be > 0"})
		return
	}

	deleted, err := h.store.DeleteMessagesFrom(id, req.BeforeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Build parallel slices for buildMessageResponse (same
	// shape as the loop in ListMessages that does the same
	// transformation for a paged read).
	metas := make([]string, len(deleted))
	createds := make([]int64, len(deleted))
	rowIDs := make([]int64, len(deleted))
	seqs := make([]int64, len(deleted))
	for i, m := range deleted {
		metas[i] = m.Metadata
		createds[i] = m.CreatedAt.Unix()
		rowIDs[i] = m.ID
		seqs[i] = m.Seq
	}

	deletedResp := make([]MessageResponse, 0, len(deleted))
	for i, m := range deleted {
		// buildMessageResponse takes llm.ChatMessage, not
		// memory.Message. Convert via a small inline helper
		// — the new-format metadata keys map 1:1 to the
		// ChatMessage fields, and the legacy multi_content
		// expansion that decodeChatMessages does is NOT
		// what we want here (one DB row should produce
		// one MessageResponse, not several).
		cm := memoryMessageToChatMessage(m)
		if resp := buildMessageResponse(cm, metas, createds, i, rowIDs[i], seqs[i]); resp != nil {
			deletedResp = append(deletedResp, *resp)
		}
	}

	// Collapse consecutive assistant rows into a single
	// message — the same merge ListMessages does on read. The
	// agent's persistAssistant writes one row per ReAct
	// round, so without this the rollback response would
	// hand back N separate "assistant" messages and the
	// frontend would render them as N separate bubbles on
	// undo. The user would then see a turn that was
	// originally one bubble split into N pieces, breaking
	// the visual continuity they had before rolling back.
	deletedResp = mergeConsecutiveAssistant(deletedResp)

	c.JSON(http.StatusOK, gin.H{
		"deleted_count":    len(deletedResp),
		"deleted_messages": deletedResp,
	})
}

// memoryMessageToChatMessage converts a memory.Message row into
// the protocol-agnostic llm.ChatMessage shape that
// buildMessageResponse consumes. Only the new-format metadata
// keys (type / name / mime_type / tool_* / tool_error) are
// mapped; legacy multi_content / tool_calls arrays are
// intentionally left un-expanded because the rollback path
// wants one row → one response (the agent's streaming path
// already expanded them into a single message's parts).
func memoryMessageToChatMessage(m memory.Message) llm.ChatMessage {
	cm := llm.ChatMessage{
		Role:        m.Role,
		Content:     m.Content,
		MsgType:     m.MsgType,
		SubmitToLLM: m.SubmitToLLM,
	}
	if m.Metadata != "" {
		var meta map[string]string
		if err := json.Unmarshal([]byte(m.Metadata), &meta); err == nil {
			cm.Name = meta["name"]
			cm.Type = meta["type"]
			cm.MimeType = meta["mime_type"]
			cm.ToolID = meta["tool_id"]
			cm.ToolName = meta["tool_name"]
			cm.ToolInput = meta["tool_input"]
			cm.ToolError = meta["tool_error"] == "true"
		}
	}
	return cm
}

// ForkSession POST /api/v1/sessions/:id/fork
// Creates a new session containing all messages up to and including
// before_id from the source session.
func (h *Handler) ForkSession(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	var req struct {
		BeforeID int64 `json:"before_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.BeforeID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "before_id is required and must be > 0"})
		return
	}

	conv, err := h.store.ForkConversation(id, req.BeforeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	srcMeta := h.ensureMetaLoaded(id)
	h.setSessionMeta(conv.ID, srcMeta.Style, srcMeta.Provider, srcMeta.Model)
	if srcMeta.ProjectPath != "" {
		h.setSessionMetaProjectPath(conv.ID, srcMeta.ProjectPath)
	}

	c.JSON(http.StatusCreated, h.sessionToResponse(*conv))
}

// UndoRollback POST /api/v1/sessions/:id/rollback/undo
// Restores previously-deleted messages.
//
// The wire format is `[]MessageResponse` (same shape as
// ListMessages / RollbackMessages return), NOT
// `[]memory.Message`. The previous version of this handler
// expected `[]memory.Message`, but after the RollbackMessages
// wire-format fix the frontend's undo payload is
// `[]MessageResponse` — and parsing that into
// `[]memory.Message` fails with a 400 on `time.Time`
// (memory.Message.CreatedAt is time.Time, expects RFC3339
// string, but MessageResponse.CreatedAt is int64 Unix).
// The user-visible symptom was: rollback worked, but
// clicking 撤销 did nothing — the request 400'd before the
// frontend's msgs.splice ever ran.
//
// The conversion here is the inverse of buildMessageResponse:
// re-serialize `parts` + `thinking` + content into the
// `metadata` JSON string the store wants, and convert
// `created_at` (Unix int64) → `time.Time`. Without this,
// even if the parse succeeded the restored rows would have
// `metadata = ""` and the next reload would show the
// assistant messages as plain text (no thinking / tool /
// sub-agent cards).
func (h *Handler) UndoRollback(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	var req struct {
		Messages []MessageResponse `json:"messages"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Convert wire → storage. See the doc comment above
	// for why each field is what it is.
	restored := make([]memory.Message, 0, len(req.Messages))
	for _, r := range req.Messages {
		m := memory.Message{
			ID:             r.ID,
			// Seq is preserved from the rollback's
			// deleted_messages so the restored row
			// keeps the same per-conversation
			// position it had before the rollback.
			// Without this, the new seq-based cursor
			// (migration 8) would skip the row on
			// the next page request — the original
			// (deleted) row was at seq=N, but the
			// restored row would get whatever
			// MAX(seq)+1 looks like at undo time,
			// and any cursor at seq=N would miss it.
			Seq:            r.Seq,
			ConversationID: id, // override from URL, defense-in-depth
			Role:           r.Role,
			Content:        r.Content,
			MsgType:        r.MsgType,
			// submit_to_llm is preserved from the
			// rollback's deleted_messages, NOT
			// hard-coded to 1. Pre-fix this was
			// always 1, which meant that an undone
			// thinking row (originally
			// submit_to_llm=0) was re-inserted as
			// a normal assistant row, sending its
			// chain-of-thought back into the LLM
			// context. The thinking text was the
			// exact content the user already saw —
			// the LLM would then echo it back as
			// the next "answer". The same applied
			// to tool_result rows for exec_command
			// (submit_to_llm=0) — the raw command
			// output would re-enter the LLM
			// context as if it were user content.
			// Carrying the original value through
			// the rollback/undo round-trip fixes
			// the leak.
			SubmitToLLM: r.SubmitToLLM,
			CreatedAt:   time.Unix(r.CreatedAt, 0),
			Metadata:    encodeMessageResponseMeta(r),
		}
		restored = append(restored, m)
	}

	if err := h.store.RestoreMessages(restored); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":             true,
		"restored_count": len(restored),
	})
}

// encodeMessageResponseMeta reconstructs the `metadata` JSON
// string the store wants, given a wire-shape MessageResponse.
// This is the inverse of decodePartsFromMeta: the parts
// array is serialized back to a JSON string and stored in
// `meta["parts"]` (the same double-encoding the agent uses
// when writing via snapshotStructural).
//
// We always write the v2 full-snapshot format regardless of
// what the original was: a MessageResponse built from
// decodePartsFromMeta always carries the decoded parts in
// `Parts`, including the text part that was previously
// derived from `content`. So `meta["parts"]` round-trips
// losslessly for both v1-original (now upgraded to v2) and
// v2-original messages.
//
// Note: MessageResponse has no `Thinking` field — thinking
// is always inside `Parts` as `{kind: "thinking", ...}`.
// The agent's snapshotStructural writes the whole parts
// array into meta["parts"] verbatim, and decodePartsFromMeta
// returns it as-is when the array contains text or thinking
// (the v2 fast path). Re-serializing that array here keeps
// the shape identical.
func encodeMessageResponseMeta(r MessageResponse) string {
	if len(r.Parts) == 0 {
		return ""
	}
	partsJSON, err := json.Marshal(r.Parts)
	if err != nil {
		return ""
	}
	meta := map[string]string{
		"parts": string(partsJSON),
	}
	b, _ := json.Marshal(meta)
	return string(b)
}

func (h *Handler) RenameSession(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	var req RenameSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.store.RenameConversation(id, req.Title); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"renamed": id, "title": req.Title})
}

// UpdateSessionMeta is the "change provider / model / style without
// sending a message" endpoint. Bound to PATCH /sessions/:id. The
// request body uses pointer fields so callers can send partial
// updates. The response is the refreshed SessionResponse so the
// web UI can sync the picker state in one round-trip.
//
// To stay backwards-compatible with the old PATCH behaviour
// (which only renamed a session), this handler also accepts a
// plain `{"title": "..."}` body and dispatches to the rename path.
func (h *Handler) UpdateSessionMeta(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")

	// Verify the session exists before we touch anything.
	cv, err := h.store.GetConversation(id)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Peek at the raw body. If the only key is "title", route to
	// RenameSession for backwards compat. Otherwise try the meta
	// update.
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Restore the body so the next ShouldBindJSON call still sees it.
	c.Request.Body = io.NopCloser(strings.NewReader(string(raw)))

	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Legacy rename path: only the title field is present.
	if _, hasTitle := probe["title"]; hasTitle {
		if len(probe) == 1 {
			var req RenameSessionRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			// Match the old "binding:required" semantics:
			// reject empty title explicitly so legacy callers
			// keep getting a 400 on a degenerate body.
			if strings.TrimSpace(req.Title) == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
				return
			}
			if err := h.store.RenameConversation(id, req.Title); err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			cv.Title = req.Title
			c.JSON(http.StatusOK, h.sessionToResponse(cv))
			return
		}
		// title + meta in one body: rename first, then update meta.
		var rn RenameSessionRequest
		if err := json.Unmarshal(raw, &rn); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if strings.TrimSpace(rn.Title) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
			return
		}
		if err := h.store.RenameConversation(id, rn.Title); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		cv.Title = rn.Title
	}

	var req UpdateSessionMetaRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate provider (if specified) before touching meta.
	provider := h.sessionProvider(id)
	if req.Provider != nil {
		if !h.validProvider(*req.Provider) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unknown provider %q", *req.Provider)})
			return
		}
		provider = *req.Provider
	}
	if req.Model != nil {
		if !h.validModel(provider, *req.Model) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("model %q not found under provider %q", *req.Model, provider)})
			return
		}
	}
	h.setSessionMeta(id, deref(req.Style), provider, deref(req.Model))

	// Handle permission level separately — validate and write directly.
	if req.PermissionLevel != nil {
		level := *req.PermissionLevel
		if level != "ask" && level != "auto" && level != "full" {
			c.JSON(http.StatusBadRequest, gin.H{"error": `permission_level must be "ask", "auto", or "full"`})
			return
		}
		h.metaMu.Lock()
		m := h.meta[id]
		m.PermissionLevel = level
		h.meta[id] = m
		h.metaMu.Unlock()
		if h.store != nil {
			blob, _ := json.Marshal(sessionMetaBlob{Style: m.Style, Provider: m.Provider, Model: m.Model, ReasoningEffort: m.ReasoningEffort, ProjectPath: m.ProjectPath, PlanMode: m.PlanMode, PermissionLevel: m.PermissionLevel, KnowledgeBase: m.KnowledgeBase})
			_ = h.store.UpdateConversationMeta(id, string(blob))
		}
	}

	// Handle vector_store as a first-class conversation column.
	if req.VectorStore != nil {
		if h.store != nil {
			_ = h.store.SetConversationVectorStore(id, *req.VectorStore)
		}
	}

	// Handle knowledge_base
	if req.KnowledgeBase != nil {
		h.metaMu.Lock()
		m := h.meta[id]
		m.KnowledgeBase = *req.KnowledgeBase
		h.meta[id] = m
		h.metaMu.Unlock()
		if h.store != nil {
			blob, _ := json.Marshal(sessionMetaBlob{Style: m.Style, Provider: m.Provider, Model: m.Model, ReasoningEffort: m.ReasoningEffort, ProjectPath: m.ProjectPath, PlanMode: m.PlanMode, PermissionLevel: m.PermissionLevel, KnowledgeBase: m.KnowledgeBase})
			_ = h.store.UpdateConversationMeta(id, string(blob))
		}
	}

	// Re-read so the response reflects the on-disk truth.
	cv, err = h.store.GetConversation(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, h.sessionToResponse(cv))
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// --- Messages ---

func (h *Handler) ListMessages(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	// Use the per-session query — do NOT mutate the global
	// currentID here. Two concurrent ListMessages on different
	// sessions would otherwise race.
	if _, err := h.store.GetConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	// Pagination:
	//   ?before_seq=N — return only messages with seq < N
	//                    (preferred; survives rollback/undo
	//                     because seq is per-conversation and
	//                     never reused)
	//   ?before_id=N  — return only messages with id < N
	//                    (legacy cursor; kept for back-compat
	//                     with older clients. BUG: id is
	//                     AUTOINCREMENT, so a rollback+undo
	//                     leaves the page with new ids and
	//                     the cursor becomes stale. Use
	//                     before_seq when possible.)
	//   ?limit=K      — cap the result at K rows
	//
	// When neither `before_*` is set the response is the
	// full (limited) history, which is the right thing for
	// the first switch into a session the frontend has
	// never seen (e.g. after app reload).
	beforeSeq := parseInt64Query(c, "before_seq", 0)
	beforeID := parseInt64Query(c, "before_id", 0)
	if beforeSeq == 0 && beforeID != 0 {
		// Legacy client sent only before_id. The cursor is
		// still the id-based one, so we keep using the id
		// SQL filter and the id-based has_more check.
		// Newer clients send before_seq and we prefer that.
	}
	queryLimit := parseIntQuery(c, "limit", 0)

	meta := h.ensureMetaLoaded(id)
	contextCap := h.contextMessageLimit(meta.Provider, meta.Model)

	// Effective limit: explicit query param wins; otherwise
	// the context-window cap (keeps the same default the old
	// un-paged endpoint used).
	limit := queryLimit
	if limit <= 0 || limit > contextCap {
		limit = contextCap
	}

	// We dispatch to the seq- or id-based query based on
	// which cursor the client sent. Newer clients send
	// `before_seq` and get the seq-aware filter
	// (stable across rollback+undo); older clients send
	// `before_id` and get the id-aware filter. When
	// neither is set, both code paths issue the
	// unfiltered "give me the latest N rows" query.
	var (
		msgs    []llm.ChatMessage
		metas   []string
		createds []int64
		rowIDs  []int64
		seqs    []int64
	)
	if beforeSeq > 0 {
		msgs, metas, createds, rowIDs, seqs = h.store.GetChatMessagesWithMetaPageBySeq(id, beforeSeq, limit)
	} else {
		msgs, metas, createds, rowIDs, seqs = h.store.GetChatMessagesWithMetaPage(id, beforeID, limit)
	}
	out := make([]MessageResponse, 0, len(msgs))
	// rowIDs[i] is the SQLite row id for msgs[i]. We pair them
	// in buildMessageResponse so the client can use the lowest
	// row id as the `before_id` cursor for the next page.
	// rowIDs is in DESC order (matches the row order), so
	// rowIDs[len-1] is the smallest id (oldest returned row).
	for i, m := range msgs {
		resp := buildMessageResponse(m, metas, createds, i, rowIDs[i], seqs[i])
		if resp != nil {
			out = append(out, *resp)
		}
	}

	var (
		hasMore   = false
		oldestID  int64
		oldestSeq int64
	)
	if len(rowIDs) > 0 {
		// `rowIDs` is parallel to `msgs` and is oldest-first
		// (we reversed the SQL DESC order), so rowIDs[0] is
		// the smallest id in this page (the "oldest"
		// message) and rowIDs[len-1] is the largest (the
		// "newest" message). The pagination cursor is
		// "fetch the next older page", so we report the
		// smallest id here; the client's next request is
		// `?before_id=<oldestID>` and the SQL predicate
		// `id < ?` then naturally targets everything
		// strictly older than that row. The previous code
		// returned rowIDs[len-1] (the newest id) which
		// caused every page to overlap with the previous
		// one by all-but-one row and `has_more` to stay
		// `true` forever — the infinite-scroll history
		// loader would re-fetch the same messages and
		// append them to the in-memory list, producing
		// visible duplicate bubbles (see handler
		// CursorTest for the regression lock).
		oldestID = rowIDs[0]
		oldestSeq = seqs[0]
		// `has_more` = "is there at least one row older
		// than the oldest one we returned?". Cheap: a single
		// indexed COUNT/EXISTS query. We use the seq-based
		// check when possible (matches the new cursor) and
		// fall back to the id-based one for legacy clients.
		if beforeSeq > 0 {
			hasMore = h.store.HasOlderMessagesBySeq(id, oldestSeq)
		} else {
			hasMore = h.store.HasOlderMessages(id, oldestID)
		}
	}

	// Question/answer persistence was simplified: instead of
	// separate msg_type=6 rows (which required a pairing pass
	// here), the question tool now lives as a `kind:"question"`
	// part inside the assistant message's persisted parts
	// snapshot. Reload reads parts straight from
	// meta["parts"] (see decodePartsFromMeta); no pairing
	// step is needed. Question parts round-trip through the
	// question tool's tool_call/tool_result row (msg_type=4),
	// which buildMessageResponse already filters out below.

	// Merge consecutive assistant messages that belong to the
	// same user turn. During live streaming the frontend
	// accumulates all ReAct-round outputs into a single message
	// bubble; this merge reconstructs that same single-bubble
	// view on reload so the user doesn't see each round as an
	// independent message.
	out = mergeConsecutiveAssistant(out)

	c.JSON(http.StatusOK, gin.H{
		"messages":   out,
		"has_more":   hasMore,
		"oldest_id":  oldestID,
		"oldest_seq": oldestSeq,
	})
}

// mergeConsecutiveAssistant collapses runs of consecutive
// assistant messages into a single message. In the database,
// each ReAct round produces its own assistant row (plus
// intermediate tool_call / tool_result rows which are filtered
// out by buildMessageResponse). After filtering, consecutive
// assistant messages belong to the same user turn and should
// render as one bubble — matching what the user saw during
// live streaming.
func mergeConsecutiveAssistant(msgs []MessageResponse) []MessageResponse {
	if len(msgs) <= 1 {
		return msgs
	}
	merged := make([]MessageResponse, 0, len(msgs))
	i := 0
	for i < len(msgs) {
		msg := msgs[i]
		if msg.Role != "assistant" || i+1 >= len(msgs) || msgs[i+1].Role != "assistant" {
			merged = append(merged, msg)
			i++
			continue
		}
		run := []MessageResponse{msg}
		i++
		for i < len(msgs) && msgs[i].Role == "assistant" {
			run = append(run, msgs[i])
			i++
		}
		merged = append(merged, mergeAssistantRun(run))
	}
	return merged
}

// mergeAssistantRun merges a slice of consecutive assistant
// messages (from the same user turn) into a single message.
// Each message's parts are appended in round order, preserving
// the interleaved sequence (text → tool → sub_agent → text → …)
// that the frontend's parts-driven render path expects. Without
// this the relaoded conversation loses the round structure and
// shows all text first, then all tools below.
func mergeAssistantRun(run []MessageResponse) MessageResponse {
	if len(run) == 1 {
		return run[0]
	}
	base := run[0]
	var parts []MessagePart

	for _, m := range run {
		parts = append(parts, m.Parts...)
	}

	base.Parts = parts
	// `base.Content` is intentionally NOT regenerated from
	// parts. The MessageBubble component (frontend/src/components/
	// MessageBubble.vue) renders assistant messages off
	// `message.parts` exclusively, so the joined-Content
	// projection was dead — and it would have been wrong
	// anyway: multi-round text joined with "\n" doesn't
	// match the live-streaming view (where each round's
	// text is its own part with its own styling), so
	// recomputing Content here would diverge from the
	// user-visible streaming bubble.
	return base
}

// buildMessageResponse shapes one ChatMessage row into the
// public MessageResponse. Returns nil for rows the frontend
// shouldn't render (tool call / tool result rows are
// reconstructed into the assistant message's Parts; image
// rows are surfaced as Attachments). rowID is the SQLite row
// id, propagated so the client can use it as the
// `before_id` cursor for the next page request. seq is the
// per-conversation logical position, propagated for the new
// seq-based cursor (stable across rollback+undo).
func buildMessageResponse(m llm.ChatMessage, metas []string, createds []int64, i int, rowID int64, seq int64) *MessageResponse {
	created := time.Now().Unix()
	if i < len(createds) && createds[i] != 0 {
		created = createds[i]
	}
	resp := MessageResponse{
		ID:          rowID,
		Seq:         seq,
		Role:        m.Role,
		MsgType:     m.MsgType,
		Content:     m.Content,
		CreatedAt:   created,
		Name:        m.Name,
		SubmitToLLM: m.SubmitToLLM,
	}

	// Media messages (image / audio / video): compute a data
	// URL for the frontend. The frontend's MessageBubble
	// distinguishes them by the `kind` field and the wire
	// `type` (image_url / audio_url / video_url). We keep
	// the same structure for all three so the storage
	// path (raw base64 in messages.content) is identical.
	if isMediaType(m.Type) && m.Content != "" {
		mime := m.MimeType
		if mime == "" {
			mime = defaultMIMEForType(m.Type)
		}
		dataURL := "data:" + mime + ";base64," + m.Content
		resp.Attachments = append(resp.Attachments, AttachmentPart{
			Type: typeURLFor(m.Type),
			URL:  dataURL,
			Name: m.Name,
			Kind: kindFor(m.Type),
			MIME: mime,
		})
	}

	// Tool call metadata.
	if m.ToolID != "" {
		resp.ToolCallID = m.ToolID
	}

	// Note: command extraction (msg_type=5 / exec_command output)
	// USED to set resp.Name here, but the row is dropped by the
	// MsgTypeTool/MsgTypeCommand filter at the bottom of this
	// function, so the assignment was dead code. The frontend's
	// ExecOutputCard now reads the command from the persisted
	// tool part's `args` field (see MessageBubble.vue routing for
	// tool kind:tool parts), so the reload path is correct
	// without any post-filter mutation of resp.Name.

	// Restore assistant parts from metadata.
	if i < len(metas) && metas[i] != "" {
		if parts := decodePartsFromMeta(metas[i], m.Content); len(parts) > 0 {
			resp.Parts = parts
		}
	}
	// Media messages carry their payload as data URLs in
	// Attachments; clear Content so the frontend doesn't
	// render the raw base64 string as text.
	if isMediaType(m.Type) {
		resp.Content = ""
	}
	// Tool call / result messages are embedded in the
	// main assistant message's Parts — the separate rows
	// are only for DB reconstruction and cause blank
	// bubbles if returned to the frontend.
	// Command messages (msg_type=5) are also filtered:
	// exec_command results already appear in the parts
	// as ToolCallCard entries — returning them as
	// independent bubbles would duplicate the display.
	if m.MsgType == llm.MsgTypeTool || m.MsgType == llm.MsgTypeCommand {
		return nil
	}

	return &resp
}

// parseInt64Query returns the int64 value of a query string
// parameter, or `def` if the parameter is missing or invalid.
func parseInt64Query(c *gin.Context, key string, def int64) int64 {
	raw := c.Query(key)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return def
	}
	return v
}

// parseIntQuery returns the int value of a query string
// parameter, or `def` if the parameter is missing or invalid.
func parseIntQuery(c *gin.Context, key string, def int) int {
	raw := c.Query(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

// isMediaType reports whether t is a binary media type the
// frontend can render inline (image / audio / video). All three
// share the same storage path: raw base64 in messages.content,
// surfaced to the UI as a data URL on the MessageAttachment.
func isMediaType(t string) bool {
	return t == llm.TypeImage || t == llm.TypeAudio || t == llm.TypeVideo
}

// defaultMIMEForType picks a sane MIME for a media row whose
// metadata didn't capture one (defensive; the upload pipeline
// always sets it). Bumping a video without a recorded MIME
// down to video/mp4 lets the <video> element at least try
// to play it.
func defaultMIMEForType(t string) string {
	switch t {
	case llm.TypeImage:
		return "image/png"
	case llm.TypeAudio:
		return "audio/mpeg"
	case llm.TypeVideo:
		return "video/mp4"
	}
	return "application/octet-stream"
}

// typeURLFor maps a media Type to the wire name the frontend
// uses on MessageAttachment. We keep the OpenAI-style *_url
// suffix for symmetry with the existing image path; the
// MessageBubble component dispatches on this string.
func typeURLFor(t string) string {
	switch t {
	case llm.TypeImage:
		return "image_url"
	case llm.TypeAudio:
		return "audio_url"
	case llm.TypeVideo:
		return "video_url"
	}
	return ""
}

// kindFor maps a media Type to the lower-level "kind" the
// frontend uses for its MessageAttachment.kind (drives the
// chip icon / fallback render). Same vocabulary as the
// uploadKind constants on the way in.
func kindFor(t string) string {
	switch t {
	case llm.TypeImage:
		return "image"
	case llm.TypeAudio:
		return "audio"
	case llm.TypeVideo:
		return "video"
	}
	return "file"
}

// mimeFromDataURL extracts the MIME type from a data URL
// ("data:<mime>;base64,..."). Returns "" if the URL is not a
// data URL.
func mimeFromDataURL(u string) string {
	if !strings.HasPrefix(u, "data:") {
		return ""
	}
	rest := strings.TrimPrefix(u, "data:")
	if i := strings.Index(rest, ";"); i >= 0 {
		return rest[:i]
	}
	return ""
}

// inferTextPartMeta pulls a filename / kind hint out of a text
// part produced by the agent's attachment expansion. The agent
// prefixes file dumps with "--- <name> ---"; we surface that as
// the part's display name.
func inferTextPartMeta(s string) (name, kind, mime string) {
	s = strings.TrimSpace(s)
	const marker = "--- "
	if strings.HasPrefix(s, marker) {
		rest := strings.TrimPrefix(s, marker)
		if i := strings.Index(rest, " ---"); i >= 0 {
			return rest[:i], "text", "text/plain"
		}
	}
	return "", "text", "text/plain"
}

// buildLLMMessages turns a session's stored history into the
// message slice fed to the LLM. Two responsibilities:
//
//  1. Filter out display-only rows (SubmitToLLM == 0): the
//     system prompt, thinking blocks, raw exec_command output,
//     etc. — anything the user sees in the chat but the LLM
//     doesn't need in its context.
//
//  2. Rewrite `task` tool results from role=tool to role=user.
//     The `task` tool is the sub-agent system entry point.
//     The PARENT agent issues the task call (so the parent
//     sees its own tool_call), but the sub-agent's response
//     arrives as a tool_result on the PARENT's message
//     stream. From the parent's LLM perspective, the
//     tool_call id never appears in the LLM context — only
//     the result does — and a bare `role: tool` orphan
//     would be rejected by some providers. The historical
//     workaround (commit 51039e2) flipped ALL tool_results
//     to `role: user`, but that's wrong for the main
//     tool loop: read_file / write_file / exec_command /
//     todo_write / question results all need to stay as
//     `role: tool` so the LLM can match them against the
//     tool_call that produced them.
//
//     The fix scopes the rewrite to `task` results only.
//     `m.ToolName` is populated on the result row when the
//     agent writes the tool_result message (see
//     internal/agent/agent.go toolMsg construction).
func buildLLMMessages(histMsgs []llm.ChatMessage) []llm.ChatMessage {
	msgs := make([]llm.ChatMessage, 0, len(histMsgs)+1)
	for _, m := range histMsgs {
		if m.SubmitToLLM == 0 {
			continue
		}
		if m.MsgType == llm.MsgTypeTool && m.Role == llm.RoleTool && m.ToolName == "task" {
			m.Role = llm.RoleUser
		}
		msgs = append(msgs, m)
	}
	return msgs
}

// decodePartsFromMeta pulls the assistant message's `parts`
// (thinking + text + tool + sub-agent) out of the raw
// metadata string and the message content.
//
// Storage format (two versions, auto-detected):
//
//   New (v2) — meta["parts"] is a full snapshot of the parts
//   accumulator, including text and thinking in stream order.
//   When the JSON contains any text or thinking part it is
//   returned as-is — the interleaved order the user saw during
//   streaming is preserved exactly.
//
//   Old (v1) — meta["parts"] contains only structural parts
//   (tool + sub_agent). Thinking is stored as a raw string in
//   meta["thinking"]; text comes from the `content` column.
//   The rebuild appends thinking first, then structural parts,
//   then a trailing text part from `content`.
//
// Returns nil when the row has no parts — that's the legacy
// path where the assistant message is just plain text, and the
// UI's `parts.length > 0` check falls through to the markdown
// fallback in MessageBubble. The decode is best-effort:
// malformed JSON produces nil so the UI degrades to plain text
// rather than 500-ing.
func decodePartsFromMeta(meta string, content string) []MessagePart {
	if meta == "" {
		return nil
	}
	var blob map[string]string
	if err := json.Unmarshal([]byte(meta), &blob); err != nil {
		return nil
	}

	// 1. Try the new (v2) full-snapshot format first.
	//    When meta["parts"] already contains text or thinking
	//    parts, the array is self-contained and needs no
	//    rebuilding.
	if raw, ok := blob["parts"]; ok && raw != "" {
		var full []MessagePart
		if err := json.Unmarshal([]byte(raw), &full); err == nil && hasTextOrThinking(full) {
			return full
		}
	}

	// 2. Old (v1) format: rebuild from separate fields.
	var parts []MessagePart

	// Thinking — stored as raw string (no double-encode).
	if t, ok := blob["thinking"]; ok && t != "" {
		parts = append(parts, MessagePart{Kind: "thinking", Text: t})
	}

	// Structural parts (tool + sub_agent) — JSON blob.
	if raw, ok := blob["parts"]; ok && raw != "" {
		var structural []MessagePart
		if err := json.Unmarshal([]byte(raw), &structural); err == nil {
			parts = append(parts, structural...)
		}
	}

	// Text — from content (not stored in meta).
	parts = append(parts, MessagePart{Kind: "text", Text: content})

	if len(parts) == 0 {
		return nil
	}
	return parts
}

// hasTextOrThinking reports whether a parts array contains at
// least one part whose kind is "text" or "thinking". Used to
// distinguish the new full-snapshot format (which includes
// these kinds inline) from the old structural-only format
// (which only stores tool / sub_agent parts).
func hasTextOrThinking(parts []MessagePart) bool {
	for _, p := range parts {
		if p.Kind == "text" || p.Kind == "thinking" {
			return true
		}
	}
	return false
}

// SendMessage is the main streaming endpoint. It accepts a user
// message, appends it to the session's history, and streams the
// assistant's response back as Server-Sent Events.
//
// The request may optionally carry `provider` and/or `model` to
// override the per-session defaults. Overrides are validated
// against the configured providers and models, then written back
// to the session meta so the next message in this session keeps
// using the new model.
func (h *Handler) SendMessage(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	if _, err := h.store.GetConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	// Cap the request body so a malicious client cannot OOM the
	// server by posting a multi-GB JSON. 10 MiB is generous: a
	// 1 MiB message + a few inline base64 image attachments
	// easily fits; anything larger should be sent as a /upload
	// reference, not inlined.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 10<<20)

	var req SendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Defensive: also cap the parsed Message field in case the
	// body limit is bypassed by a proxy.
	const maxMessageLen = 1 << 20 // 1 MiB
	if len(req.Message) > maxMessageLen {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": fmt.Sprintf("message too long: %d bytes (max %d); split into multiple turns or attach a file reference", len(req.Message), maxMessageLen),
		})
		return
	}
	if len(req.Attachments) > 16 {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("too many attachments: %d (max 16)", len(req.Attachments))})
		return
	}

	// Resolve style: body override → per-session override →
	// configured default → built-in "tech" fallback. The
	// per-session lookup is the one piece that was missing —
	// without it, switching the picker never took effect on the
	// next message because the body always omits the style.
	s, _ := style.ParseStyle(req.Style)
	if s == "" {
		s = style.Style(h.sessionStyle(id))
	}
	if s == "" {
		if def := style.Style(h.getCfg().Style.Default); def != "" {
			s = def
		} else {
			s = style.Tech
		}
	}

	// Resolve provider: body override → per-session override →
	// configured default. Validate before mutating anything.
	provider := h.sessionProvider(id)
	if req.Provider != "" {
		if !h.validProvider(req.Provider) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unknown provider %q", req.Provider)})
			return
		}
		provider = req.Provider
	}

	// Resolve model: body override → per-session override → that
	// provider's EffectiveModel.
	model := h.sessionModel(id, provider)
	if req.Model != "" {
		if !h.validModel(provider, req.Model) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("model %q not found under provider %q", req.Model, provider)})
			return
		}
		model = req.Model
	}

	// Persist whichever fields the caller actually changed. The
	// setSessionMeta helper is a no-op when nothing differs, so
	// sending an empty body is fine.
	h.setSessionMeta(id, string(s), provider, model)

	// Serialise concurrent SendMessage calls on the same session
	// so message writes don't interleave and corrupt history.
	if _, loaded := h.sessionLocks.LoadOrStore(id, struct{}{}); loaded {
		c.JSON(http.StatusConflict, gin.H{"error": "a message is already being processed for this session"})
		return
	}
	defer h.sessionLocks.Delete(id)

	// Build messages: history after last compression + new user message.
	// Messages older than the compressed range are replaced by the
	// CompressedSummary field on the ChatRequest. All reads go through
	// the per-session variants so concurrent SendMessage calls on
	// different sessions don't race.
	meta := h.ensureMetaLoaded(id)
	lastComp := h.store.LastCompressedIDFor(id)
	var histMsgs []llm.ChatMessage
	var compSummary string
	if lastComp > 0 {
		histMsgs, _, _ = h.store.GetChatMessagesAfterIDFor(id, 0, lastComp)
		compSummary = h.store.CompressedSummaryFor(id)
	} else {
		histMsgs = h.store.GetChatMessagesFor(id, 0)
	}
	msgs := buildLLMMessages(histMsgs)
	msgs = append(msgs, llm.ChatMessage{
		Role:        llm.RoleUser,
		Type:        llm.TypeText,
		Content:     req.Message,
		MsgType:     llm.MsgTypeText,
		SubmitToLLM: 1,
	})

	chatReq := agent.ChatRequest{
		Style:             s,
		Provider:          provider,
		Model:             model,
		Messages:          msgs,
		Attachments:       req.Attachments,
		ReasoningEffort:   meta.ReasoningEffort,
		CompressedSummary: compSummary,
		SessionID:         id,
		ProjectRoot:       meta.ProjectPath,
		SkillContext:      req.SkillContext,
		PlanMode:          meta.PlanMode,
		PermissionLevel:   meta.PermissionLevel,
		KBBase:            meta.KnowledgeBase,
	}

	stream := h.agent.ChatStream(c.Request.Context(), chatReq)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Header("X-Session-ID", id)
	c.Header("X-Provider", provider)
	c.Header("X-Model", model)

	// Flush headers immediately so the browser knows this is
	// a streaming response (not waiting for full body).
	c.Writer.Flush()

	c.Stream(func(w io.Writer) bool {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[sse] panic in stream writer: %v", r)
			}
		}()
		chunk, ok := <-stream
		if !ok {
			return false
		}
		ev := chunkToEvent(chunk, provider, model)
		if chunk.Done {
			if uid := h.store.GetLastUserMessageID(id); uid > 0 {
				ev.UserMessageID = uid
			}
			if lid := h.store.GetLastMessageID(id); lid > 0 {
				ev.LastMessageID = lid
			}
		}
		if ev.Type == "question" {
			log.Printf("[sse] writing question event (%d bytes json)", len(ev.QuestionJSON))
		}
		data, _ := json.Marshal(ev)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		// Flush after every event so the client sees it
		// immediately, even when the next event is far
		// away (e.g. the question tool blocks waiting
		// for user input). Gin's stream writer already
		// calls Flush(), but belt-and-suspenders for
		// reverse-proxy scenarios (Wails desktop GUI).
		if fl, ok := c.Writer.(http.Flusher); ok {
			fl.Flush()
		}
		return !chunk.Done
	})
}

// chunkToEvent maps an internal ChatStreamChunk to a public
// StreamEvent the API exposes. provider/model are stamped on
// every event so the client can show a small "produced by" badge
// on the assistant message even when the model is unknown to the
// chunk itself.
//
// ★ chunkToEvent 是服务端 ChatStreamChunk → 前端 StreamEvent 的映射器。
// 映射规则（按优先级，更具体的匹配优先）：
//   1. question JSON 非空     → type:"question"   (问题模态框)
//   2. tool_confirm JSON 非空 → type:"tool_confirm" (沙箱确认)
//   3. Error 非空             → type:"error"       (LLM 错误)
//   4. Done == true           → type:"done"        (★ 终止 SSE)
//   5. ToolName 非空          → type:"tool"        (工具调用结果)
//   6. Thinking 非空          → type:"thinking"    (思考增量)
//   7. Content 非空           → type:"content"     (文本增量)
//   8. ContentRewrite 非空    → type:"content_rewrite" (后处理文本重写)
//   9. ThinkingRewrite 非空   → type:"thinking_rewrite"
//  10. Phase 非空             → type:"phase"       (子代理开始/结束 + 系统状态)
//  11. 其他                   → type:"phase"       (心跳)
//
// Sub_agent 字段在所有分支中无条件拷贝，确保子代理的 content/thinking/tool/phase
// 事件全部带有 sub_agent=true 标记，前端能正确路由到嵌套 SubAgentCard。
//
// 修改指南 → docs/modules/server.md
// toolStatusFromChunkStep mirrors toolStatusFromStep in
// internal/agent/parts.go so the wire format and the
// accumulator agree on the status string. Parse the trailing
// segment of "call-N-status" instead of substring-matching
// "ok" / "err" so a future status name can't accidentally
// match.
func toolStatusFromChunkStep(step, errMsg string) string {
	if errMsg != "" {
		return "error"
	}
	idx := strings.LastIndex(step, "-")
	if idx < 0 || idx+1 >= len(step) {
		return "start"
	}
	switch step[idx+1:] {
	case "ok":
		return "ok"
	case "warn":
		return "warn"
	case "err", "error":
		return "error"
	case "start":
		return "start"
	}
	return "start"
}

func chunkToEvent(chunk agent.ChatStreamChunk, provider, model string) StreamEvent {
	ev := StreamEvent{
		Phase:          chunk.Phase,
		Step:           chunk.Step,
		Message:        chunk.Message,
		Provider:       provider,
		Model:          model,
		SubAgent:       chunk.SubAgent,
		SubAgentTask:   chunk.SubAgentTask,
		SubAgentStatus: chunk.SubAgentStatus,
		SubAgentType:   chunk.SubAgentType,
		SubAgentColor:  chunk.SubAgentColor,
		SubAgentModel:  chunk.SubAgentModel,
		SubAgentTaskID: chunk.SubAgentTaskID,
		SubAgentDescription: chunk.SubAgentDescription,
		SubAgentFailureReason: chunk.SubAgentFailureReason,
		ThinkingRewrite: chunk.ThinkingRewrite,
		SessionStatus:  chunk.SessionStatus,
		// Elapsed carries the duration the server stamped on the
		// chunk. The agent sets it on the final "done" chunk AND
		// on every sub_agent_* lifecycle close event (so the
		// SubAgentCard can show the elapsed time once the run
		// finishes). Surfacing it here unconditionally lets the
		// frontend read ev.elapsed on phase events without
		// waiting for a separate "done" tick.
		Elapsed: chunk.Duration,
	}

	// Question events are emitted by the question tool handler
	// before it blocks waiting for user input.
	if chunk.QuestionJSON != "" {
		ev.Type = "question"
		ev.QuestionJSON = chunk.QuestionJSON
		return ev
	}
	if chunk.ToolConfirmJSON != "" {
		ev.Type = "tool_confirm"
		ev.ToolConfirmJSON = chunk.ToolConfirmJSON
		return ev
	}
	if chunk.Error != "" {
		ev.Type = "error"
		ev.Error = chunk.Error
		ev.Suggestion = chunk.Suggestion
		ev.ErrorKind = chunk.ErrorKind
		return ev
	}
	if chunk.Done {
		ev.Type = "done"
		ev.TokensIn = chunk.TokensIn
		ev.TokensOut = chunk.TokensOut
		// ev.Elapsed already populated above (chunk.Duration).
		return ev
	}
	if chunk.ToolName != "" {
		ev.Type = "tool"
		ev.ToolID = chunk.ToolID
		ev.ToolName = chunk.ToolName
		ev.ToolArgs = chunk.ToolArgs
		ev.ToolResult = chunk.ToolResult
		ev.ToolResultFull = chunk.ToolResultFull
		ev.ToolError = chunk.ToolError
		ev.ToolElapsed = chunk.ToolElapsed
		// Status: parse the trailing segment of "call-N-status"
		// rather than substring-matching "ok" / "err" so a future
		// status name can't accidentally match. See
		// internal/agent/parts.go for the canonical parser.
		ev.ToolStatus = toolStatusFromChunkStep(chunk.Step, chunk.ToolError)
		return ev
	}
	if chunk.Thinking != "" {
		ev.Type = "thinking"
		ev.Thinking = chunk.Thinking
		return ev
	}
	if chunk.Content != "" {
		ev.Type = "content"
		ev.Content = chunk.Content
		return ev
	}
	// ContentRewrite: the agent's post-stream redactor rewrote
	// the assistant's trailing text (e.g. stripped a phantom
	// vision error). The UI should REPLACE the trailing text
	// part with this value, not append it. We treat this as a
	// distinct event type so the chat store can route it
	// differently from regular content deltas.
	if chunk.ContentRewrite != "" {
		ev.Type = "content_rewrite"
		ev.Content = chunk.ContentRewrite
		return ev
	}
	// ThinkingRewrite: same pattern but for the LLM's
	// chain-of-thought block. Some phantoms appear in
	// thinking rather than the text response; the UI
	// replaces the trailing thinking part's text.
	if chunk.ThinkingRewrite != "" {
		ev.Type = "thinking_rewrite"
		ev.Thinking = chunk.ThinkingRewrite
		return ev
	}
	// SessionStatus events carry lifecycle signals ("busy" /
	// "idle") so the frontend can drive the TodoPanel state
	// machine. Must be checked BEFORE Phase because the chunk
	// may also carry a Phase field.
	if chunk.SessionStatus != "" {
		ev.Type = "session_status"
		return ev
	}
	// Other phase events (system, memory, plan, sub-agent
	// start/ok/err) — surface as "phase" with the original
	// Phase/Step/Message fields. Sub-agent lifecycle events
	// (sub_agent_start / sub_agent_ok / sub_agent_err) come
	// through here.
	if chunk.Phase != "" {
		ev.Type = "phase"
		return ev
	}
	// Unknown / empty event — emit as a heartbeat so the client
	// doesn't appear to hang.
	ev.Type = "phase"
	ev.Message = ""
	return ev
}

// sessionToResponse converts a memory.Conversation into the API
// representation. The per-session provider/model/style are read
// from the meta cache (lazily re-hydrated from
// conversations.metadata). When the session has no override, the
// server's default provider + EffectiveModel is reported so the
// client always sees a complete picker state.
func (h *Handler) sessionToResponse(cv memory.Conversation) SessionResponse {
	m := h.ensureMetaLoaded(cv.ID)
	provider := m.Provider
	if provider == "" {
		provider = h.getCfg().LLM.Default
	}
	model := m.Model
	if model == "" {
		for _, p := range h.getCfg().LLM.Providers {
			if p.Name == provider {
				model = p.EffectiveModel()
				break
			}
		}
	}
	return SessionResponse{
		ID:          cv.ID,
		Title:       cv.Title,
		Provider:    provider,
		Model:       model,
		Style:       m.Style,
		ProjectPath: m.ProjectPath,
		PlanMode:    m.PlanMode,
		PermissionLevel: m.PermissionLevel,
		ReasoningEffort: m.ReasoningEffort,
		VectorStore:    cv.VectorStore,
		KnowledgeBase:  m.KnowledgeBase,
		CreatedAt:   cv.CreatedAt.Unix(),
		UpdatedAt:   cv.UpdatedAt.Unix(),
	}
}

// ParseLimit returns the value of the `?limit=N` query parameter, or
// the default if absent / invalid. Exposed for the new
// GET /sessions/:id/messages?limit=20 endpoint.
func ParseLimit(c *gin.Context, def int) int {
	s := c.Query("limit")
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// SetSummarizer wires the summarizer for compress support.
func (h *Handler) SetSummarizer(sm *memory.Summarizer) {
	h.summarizer = sm
}

// CompressConversation compresses the current conversation's history.
// POST /api/v1/sessions/:id/compress
func (h *Handler) CompressConversation(c *gin.Context) {
	id := c.Param("id")
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "store not available"})
		return
	}
	if h.summarizer == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "summarizer not available"})
		return
	}
	if _, err := h.store.GetConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	ok, summary, err := h.summarizer.Compress(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"compressed": ok, "summary": summary})
}

// SetReasoningEffort updates the per-session reasoning effort.
// PATCH /api/v1/sessions/:id/reasoning-effort
func (h *Handler) SetReasoningEffort(c *gin.Context) {
	id := c.Param("id")
	var req struct{ Level string `json:"level"` }
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	valid := map[string]bool{"off": true, "low": true, "medium": true, "high": true, "max": true}
	if !valid[req.Level] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "level must be off, low, medium, high, or max"})
		return
	}
	h.metaMu.Lock()
	m := h.meta[id]
	m.ReasoningEffort = req.Level
	h.meta[id] = m
	h.metaMu.Unlock()
	if h.store != nil {
			blob, _ := json.Marshal(sessionMetaBlob{Style: m.Style, Provider: m.Provider, Model: m.Model, ReasoningEffort: m.ReasoningEffort, ProjectPath: m.ProjectPath, PlanMode: m.PlanMode, PermissionLevel: m.PermissionLevel, KnowledgeBase: m.KnowledgeBase})
		_ = h.store.UpdateConversationMeta(id, string(blob))
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "reasoning_effort": req.Level})
}

// SaveSystemMessage persists a system message to the conversation
// history for display-only purposes. System messages are shown in
// the chat window but excluded from LLM context.
// POST /api/v1/sessions/:id/system-message
func (h *Handler) SaveSystemMessage(c *gin.Context) {
	id := c.Param("id")
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	var body struct{ Content string `json:"content"` }
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.Content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content is required"})
		return
	}
	if _, err := h.store.GetConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	h.store.AddChatMessageTo(id, llm.ChatMessage{
		Role:        llm.RoleSystem,
		Type:        llm.TypeText,
		Content:     body.Content,
		MsgType:     llm.MsgTypeText,
		SubmitToLLM: 1,
	})
	h.store.Flush()
	c.JSON(http.StatusCreated, gin.H{"ok": true})
}

// GetTodos returns the current todo list for a session.
// GET /api/v1/sessions/:id/todos
func (h *Handler) GetTodos(c *gin.Context) {
	id := c.Param("id")
	todos := tool.GetSessionTodos(id)
	// On cold cache (server restart), hydrate from SQLite.
	if len(todos) == 0 && h.store != nil {
		dbTodos := h.store.LoadTodos(id)
		if len(dbTodos) > 0 {
			toolTodos := make([]tool.TodoItem, len(dbTodos))
			for i, t := range dbTodos {
				toolTodos[i] = tool.TodoItem{
					ID:      t.ID,
					Content: t.Content,
					Status:  t.Status,
				}
			}
			tool.SetSessionTodos(id, toolTodos)
			todos = toolTodos
		}
	}
	c.JSON(http.StatusOK, gin.H{"todos": todos})
}

// QuestionResponse receives the user's answer to a pending
// question from the frontend and delivers it to the waiting
// question tool handler.
// POST /api/v1/sessions/:id/question-response
func (h *Handler) QuestionResponse(c *gin.Context) {
	id := c.Param("id")
	var resp tool.QuestionResponse
	if err := c.ShouldBindJSON(&resp); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !tool.SubmitAnswer(id, resp) {
		c.JSON(http.StatusNotFound, gin.H{"error": "no pending question for this session"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ConfirmResponse receives the user's approve/reject answer to a
// pending tool confirm prompt.
// POST /api/v1/sessions/:id/confirm-response
func (h *Handler) ConfirmResponse(c *gin.Context) {
	id := c.Param("id")
	var body struct {
		Approved bool `json:"approved"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !tool.SubmitConfirm(id, body.Approved) {
		c.JSON(http.StatusNotFound, gin.H{"error": "no pending confirm for this session"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ExecutePlan saves a user-reviewed plan as an assistant message
// and clears plan mode so the next user message triggers actual
// execution. The frontend should follow up by sending a normal
// message (e.g. "请按计划执行") via the streaming endpoint.
// POST /api/v1/sessions/:id/execute-plan
func (h *Handler) ExecutePlan(c *gin.Context) {
	id := c.Param("id")
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	var body struct {
		PlanText string `json:"plan_text"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.PlanText == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "plan_text is required"})
		return
	}
	if _, err := h.store.GetConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	h.store.AddChatMessageTo(id, llm.ChatMessage{
		Role:        llm.RoleAssistant,
		Type:        llm.TypeText,
		Content:     body.PlanText,
		MsgType:     llm.MsgTypeText,
		SubmitToLLM: 1,
	})
	h.store.Flush()

	// Turn off plan mode for this session.
	h.metaMu.Lock()
	m := h.meta[id]
	m.PlanMode = false
	h.meta[id] = m
	h.metaMu.Unlock()
	if h.store != nil {
		blob, _ := json.Marshal(sessionMetaBlob{Style: m.Style, Provider: m.Provider, Model: m.Model, ReasoningEffort: m.ReasoningEffort, ProjectPath: m.ProjectPath, PlanMode: false, PermissionLevel: m.PermissionLevel, KnowledgeBase: m.KnowledgeBase})
		_ = h.store.UpdateConversationMeta(id, string(blob))
	}

	c.JSON(http.StatusOK, gin.H{"ok": true, "id": id})
}

// --- Project CRUD ---

type projectResponse struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// ListProjects GET /api/v1/projects
func (h *Handler) ListProjects(c *gin.Context) {
	projects, err := project.Load()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if projects == nil {
		projects = []project.Project{}
	}
	out := make([]projectResponse, 0, len(projects))
	for _, p := range projects {
		out = append(out, projectResponse{Name: p.Name, Path: p.Path})
	}
	c.JSON(http.StatusOK, gin.H{"projects": out})
}

// AddProject POST /api/v1/projects
func (h *Handler) AddProject(c *gin.Context) {
	var req projectResponse
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if req.Name == "" || req.Path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and path are required"})
		return
	}
	projects, err := project.Add(req.Name, req.Path)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]projectResponse, 0, len(projects))
	for _, p := range projects {
		out = append(out, projectResponse{Name: p.Name, Path: p.Path})
	}
	c.JSON(http.StatusCreated, gin.H{"projects": out})
}

// RemoveProject DELETE /api/v1/projects
func (h *Handler) RemoveProject(c *gin.Context) {
	var req struct{ Path string `json:"path"` }
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if req.Path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is required"})
		return
	}
	// Archive all sessions associated with this project.
	if h.store != nil {
		h.store.ArchiveByProjectPath(req.Path)
	}
	projects, err := project.Remove(req.Path)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]projectResponse, 0, len(projects))
	for _, p := range projects {
		out = append(out, projectResponse{Name: p.Name, Path: p.Path})
	}
	c.JSON(http.StatusOK, gin.H{"projects": out})
}

// contextMessageLimit returns the message fetch limit for the given
// provider/model pair, scaled to the model's configured context window.
// When LimitsConfig.MaxStoredMessages is set (> 0) it takes precedence.
// Otherwise the limit is max(50, contextWindow / 2000), capped at 1000.
func (h *Handler) contextMessageLimit(provider, model string) int {
	if h.getCfg() != nil && h.getCfg().Limits.MaxStoredMessages > 0 {
		return h.getCfg().Limits.MaxStoredMessages
	}
	ctxWin := 0
	if h.agent != nil && provider != "" && model != "" {
		ctxWin = h.agent.LLM().ContextWindow(provider, model)
	}
	if ctxWin <= 0 {
		ctxWin = llm.DefaultContextWindow
	}
	limit := ctxWin / 2000
	if limit < 50 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	return limit
}

// reloadAfterConfigChange re-reads the on-disk config, rebuilds the
// LLM client, and pushes both into the agent. Called after any
// config-mutating handler (AddProvider, SetCapabilities, ...) so the
// changes take effect on the next request.
func (h *Handler) reloadAfterConfigChange() {
	if h.cfg.Load() == nil {
		return
	}
	cfg, err := config.Load("")
	if err != nil {
		return
	}
	h.cfg.Store(cfg)
	// Hot-swap the search provider so changes to web_search
	// (enable, key, provider) take effect on the very next
	// tool call without a server restart.
	search.SetGlobal(search.BuildProvider(cfg.Search))
	if h.agent == nil {
		return
	}
	newClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		return
	}
	h.agent.SetLLM(newClient)
}

// PickFolder opens the native OS folder picker dialog and returns
// the selected absolute path. POST /api/v1/dialog/folder
func (h *Handler) PickFolder(c *gin.Context) {
	path, err := nativePickFolder()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if path == "" {
		c.JSON(http.StatusOK, gin.H{"path": ""})
		return
	}
	c.JSON(http.StatusOK, gin.H{"path": path})
}

// --- Archive ---

// ArchiveSession POST /api/v1/sessions/:id/archive
func (h *Handler) ArchiveSession(c *gin.Context) {
	id := c.Param("id")
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	if err := h.store.ArchiveConversation(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// UnarchiveSession POST /api/v1/sessions/:id/unarchive
func (h *Handler) UnarchiveSession(c *gin.Context) {
	id := c.Param("id")
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	if err := h.store.UnarchiveConversation(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// If the session's project directory no longer exists (project
	// was removed), clear the project association so it shows under
	// the global view.
	meta := h.ensureMetaLoaded(id)
	if meta.ProjectPath != "" {
		if _, err := os.Stat(meta.ProjectPath); os.IsNotExist(err) {
			h.setSessionMetaProjectPath(id, "")
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ListArchived GET /api/v1/sessions/archived
func (h *Handler) ListArchived(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	convs := h.store.ListArchivedConversationsLimit(200)
	out := make([]SessionResponse, 0, len(convs))
	for _, conv := range convs {
		out = append(out, h.sessionToResponse(conv))
	}
	c.JSON(http.StatusOK, gin.H{"sessions": out})
}

// ListMCPServers GET /api/v1/mcp/servers
func (h *Handler) ListMCPServers(c *gin.Context) {
	if h.mcpMgr == nil {
		c.JSON(http.StatusOK, gin.H{"servers": []mcp.ServerInfo{}, "global_enabled": false})
		return
	}
	servers := h.mcpMgr.List()
	c.JSON(http.StatusOK, gin.H{"servers": servers, "global_enabled": h.mcpMgr.GlobalEnabled()})
}

// AddMCPServer POST /api/v1/mcp/servers
func (h *Handler) AddMCPServer(c *gin.Context) {
	if h.mcpMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "MCP manager not available"})
		return
	}

	var body struct {
		Name    string            `json:"name"`
		Type    string            `json:"type,omitempty"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env,omitempty"`
		URL     string            `json:"url,omitempty"`
		Enabled bool              `json:"enabled"`
		Timeout string            `json:"timeout,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if body.Type != "sse" && body.Command == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "command is required for stdio transport"})
		return
	}
	if body.Type == "sse" && body.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url is required for SSE transport"})
		return
	}
	// For stdio transports, require the command to be an
	// absolute path that points to an existing executable.
	// Without this, a typo (e.g. "pyhton" instead of "python")
	// would only fail when the user tries to use the MCP server,
	// with a confusing exec error. Catching it at config time
	// produces a clearer error.
	if body.Command != "" {
		if !filepath.IsAbs(body.Command) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "command must be an absolute path; the MCP server runs without a shell"})
			return
		}
		if info, err := os.Stat(body.Command); err != nil || info.IsDir() {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("command %q is not an executable file: %v", body.Command, err)})
			return
		}
	}
	// Reject shell-interpreter invocations that could run arbitrary
	// commands. The MCP manager execs the command verbatim; if the
	// caller wants to run a shell pipeline they should bundle it
	// into a script with its own shebang/permissions.
	if body.Command != "" {
		base := filepath.Base(strings.ToLower(body.Command))
		switch base {
		case "cmd", "cmd.exe", "sh", "bash", "zsh", "fish", "powershell", "powershell.exe", "pwsh", "csh", "tcsh", "ksh":
			c.JSON(http.StatusBadRequest, gin.H{"error": "command must be a direct executable, not a shell interpreter; bundle your script and invoke it directly"})
			return
		}
	}
	// Cap env and args to prevent resource abuse.
	if len(body.Args) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "args must be 64 or fewer entries"})
		return
	}
	if len(body.Env) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "env must be 64 or fewer entries"})
		return
	}

	var timeout time.Duration
	if body.Timeout != "" {
		if d, err := time.ParseDuration(body.Timeout); err == nil && d > 0 {
			timeout = d
		}
	}
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	tp := body.Type
	if tp == "" {
		tp = "stdio"
	}

	if err := h.mcpMgr.AddServer(mcp.ServerConfig{
		Name:    body.Name,
		Type:    tp,
		Command: body.Command,
		Args:    body.Args,
		Env:     body.Env,
		URL:     body.URL,
		Enabled: body.Enabled,
		Timeout: timeout,
	}); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.persistMCPServers()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// RemoveMCPServer DELETE /api/v1/mcp/servers/:name
func (h *Handler) RemoveMCPServer(c *gin.Context) {
	if h.mcpMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "MCP manager not available"})
		return
	}
	name := c.Param("name")
	if err := h.mcpMgr.RemoveServer(name); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	h.persistMCPServers()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// RestartMCPServer POST /api/v1/mcp/servers/:name/restart
func (h *Handler) RestartMCPServer(c *gin.Context) {
	if h.mcpMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "MCP manager not available"})
		return
	}
	name := c.Param("name")
	if err := h.mcpMgr.Restart(name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// SetMCPGlobal PATCH /api/v1/mcp/global
func (h *Handler) SetMCPGlobal(c *gin.Context) {
	if h.mcpMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "MCP manager not available"})
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.mcpMgr.SetGlobalEnabled(body.Enabled)
	h.persistMCPServers()
	c.JSON(http.StatusOK, gin.H{"global_enabled": h.mcpMgr.GlobalEnabled()})
}

func (h *Handler) persistMCPServers() {
	srvInfos := h.mcpMgr.List()
	servers := make([]config.MCPServerConfig, 0, len(srvInfos))
	for _, info := range srvInfos {
		srv, ok := h.mcpMgr.GetServer(info.Name)
		if !ok {
			continue
		}
		timeoutStr := ""
		if srv.Timeout > 0 {
			timeoutStr = srv.Timeout.String()
		}
		servers = append(servers, config.MCPServerConfig{
			Name:    srv.Name,
			Type:    srv.Type,
			Command: srv.Command,
			Args:    srv.Args,
			Env:     srv.Env,
			URL:     srv.URL,
			Enabled: srv.Enabled,
			Timeout: timeoutStr,
		})
	}
	h.getCfg().MCP.Servers = servers
	h.getCfg().MCP.Enabled = h.mcpMgr.GlobalEnabled()
	if mgr := config.NewManager(); mgr != nil {
		if err := mgr.SaveGlobal(h.getCfg()); err != nil {
			log.Printf("[mcp] persist config: %v", err)
		}
	}
}