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
	"github.com/p-chat/pchat/internal/browser"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/mcp"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/project"
	"github.com/p-chat/pchat/internal/search"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
	"github.com/p-chat/pchat/internal/trace"
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
	browserMgr *browser.Manager
	// toolReg is the P3-2 shared tool registry. The
	// dynamic-tool watcher in server.go writes here; the
	// GET /api/v1/tools endpoint reads from here. May be
	// nil in tests that don't need tool listing.
	toolReg *tool.Registry

	// listenAddr is the actual address the HTTP server is bound to
	// (e.g. "127.0.0.1:14712"). Set once at startup by the server.
	// Used by the browser extension UI to construct a real WS URL.
	// Safe to read without a mutex — written synchronously before
	// any HTTP handler runs, never mutated afterwards.
	listenAddr string

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
	// AutoContinue is a pointer so we can distinguish "user
	// never set" (nil → default true) from "user explicitly
	// disabled" (*bool == false). The P0-3 auto-continue
	// guard in agent.ChatWithTools reads through
	// sessionAutoContinue() which applies the default.
	AutoContinue *bool
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
	// AutoContinue mirrors sessionMeta.AutoContinue. Pointer
	// so JSON omits it when never set, instead of
	// round-tripping "false" as if the user had disabled it.
	AutoContinue *bool `json:"auto_continue,omitempty"`
}

func NewHandler(a *agent.Agent, cfg *config.Config, store *memory.Store, styleMgr *style.Manager, toolReg *tool.Registry, mcpMgr *mcp.Manager) *Handler {
	h := &Handler{
		agent:    a,
		store:    store,
		styleMgr: styleMgr,
		toolReg:  toolReg,
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
				m.AutoContinue = blob.AutoContinue
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

// sessionAutoContinue returns the P0-3 auto-continue flag for
// the session, defaulting to true (the feature is opt-out).
// A nil pointer means the user has never set the flag, which
// we treat as "use the default"; a non-nil pointer sticks at
// whatever the user last chose. See sessionMeta.AutoContinue
// for the rationale.
func (h *Handler) sessionAutoContinue(id string) bool {
	m := h.ensureMetaLoaded(id)
	if m.AutoContinue == nil {
		return true
	}
	return *m.AutoContinue
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
	// ClientMsgID is the integer the frontend minted at send time
	// (Date.now() × 1000 + random) and stamped on the local user
	// message as `msg.id`. When non-zero, the agent inserts this
	// turn's user row with this explicit id, so rollback and
	// regenerate always have a valid id to target — even when
	// the LLM call fails before the SSE `done` event would
	// otherwise broadcast `user_message_id` back. Format: a
	// 13-16 digit integer, well outside the SQLite AUTOINCREMENT
	// range (1, 2, 3, …), so it can't collide with anything
	// autoincrement produces for later assistant rows.
	ClientMsgID int64 `json:"client_msg_id,omitempty"`
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
	// AutoContinue toggles the P0-3 "todo-incomplete → re-prompt
	// LLM" guard. Pointer so the absence of the field is
	// distinct from `false`: when omitted, the per-session
	// setting is left unchanged; when present, it overrides
	// whatever was there before (including the default-true).
	AutoContinue *bool `json:"auto_continue,omitempty"`
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
	// AutoContinue is the P0-3 "todo-incomplete → re-prompt
	// LLM" guard toggle, default true. Surface so the UI can
	// show a status pill ("auto-continue on/off") next to the
	// todo panel.
	AutoContinue bool `json:"auto_continue"`
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
	// RegenGroupID is the SQLite row id (stringified) of the
	// user message that triggered this assistant reply.
	// Same value across every sibling in a regen group. Empty
	// for non-assistant rows and for pre-migration-9 assistant
	// rows that have never been regen'd. The frontend uses
	// this to associate an archived reply with its parent
	// user message (e.g. for the "上一版回答" chip + pager
	// "回复 '请帮我...'" preview).
	RegenGroupID string `json:"regen_group_id,omitempty"`
	// IsArchived is the visibility flag the UI uses to
	// pick which sibling to show. 0 = the active reply
	// (default, what ListMessages returns); 1 = an older
	// regenerated sibling that lives in the same group
	// but is hidden from the main timeline. The
	// ◀ N/M ▶ pager reads this to compute the active
	// index and to render archived siblings on demand.
	// Always false on rows returned by ListMessages
	// (the SQL filters archived rows out) — the field
	// is exposed so a future endpoint that needs to
	// surface archived rows (e.g. the /replies endpoint)
	// can do so without a separate schema.
	IsArchived bool `json:"is_archived"`
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
	// Seq is the per-stream monotonic counter the agent
	// stamps on every chunk (0, 1, 2, …). Surfaced on the
	// wire both as a JSON field AND as the standard SSE
	// `id:` line so the browser's native EventSource
	// reconnection logic and our fetch-path parser can use
	// it as a resume cursor. See P3-1 in
	// docs/plans/round2-stream-and-render-plan.md.
	Seq uint64 `json:"seq,omitempty"`
	// TraceID is the P3-3 end-to-end correlation id
	// (e.g. "T-9f3c4a2b") the agent stamps on every chunk.
	// Mirrored on the response as the `X-Trace-Id` header
	// (see respondSSE) so curl users can grep the
	// server-debug log with the same id. The frontend
	// surfaces it on error bubbles via the "复制 trace id"
	// button so support can ask the user to paste it.
	TraceID string `json:"trace_id,omitempty"`
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
	regenGroupIDs := make([]string, len(deleted))
	isArchiveds := make([]bool, len(deleted))
	for i, m := range deleted {
		metas[i] = m.Metadata
		createds[i] = m.CreatedAt.Unix()
		rowIDs[i] = m.ID
		seqs[i] = m.Seq
		if m.RegenGroupID != nil {
			regenGroupIDs[i] = *m.RegenGroupID
		}
		isArchiveds[i] = m.IsArchived
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
		if resp := buildMessageResponse(cm, metas, createds, i, rowIDs[i], seqs[i], regenGroupIDs[i], isArchiveds[i]); resp != nil {
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

	// Handle auto_continue (P0-3). Pointer semantics: nil
	// means "leave the per-session setting alone" (default
	// true), non-nil means "set it to this value explicitly".
	// The next chatReq construction reads through
	// sessionAutoContinue() which applies the default.
	if req.AutoContinue != nil {
		h.metaMu.Lock()
		m := h.meta[id]
		m.AutoContinue = req.AutoContinue
		h.meta[id] = m
		h.metaMu.Unlock()
		if h.store != nil {
			blob, _ := json.Marshal(sessionMetaBlob{Style: m.Style, Provider: m.Provider, Model: m.Model, ReasoningEffort: m.ReasoningEffort, ProjectPath: m.ProjectPath, PlanMode: m.PlanMode, PermissionLevel: m.PermissionLevel, KnowledgeBase: m.KnowledgeBase, AutoContinue: m.AutoContinue})
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
		msgs           []llm.ChatMessage
		metas          []string
		createds       []int64
		rowIDs         []int64
		seqs           []int64
		regenGroupIDs  []string
		isArchiveds    []bool
	)
	if beforeSeq > 0 {
		msgs, metas, createds, rowIDs, seqs, regenGroupIDs, isArchiveds = h.store.GetChatMessagesWithMetaPageBySeq(id, beforeSeq, limit)
	} else {
		msgs, metas, createds, rowIDs, seqs, regenGroupIDs, isArchiveds = h.store.GetChatMessagesWithMetaPage(id, beforeID, limit)
	}
	out := make([]MessageResponse, 0, len(msgs))
	// rowIDs[i] is the SQLite row id for msgs[i]. We pair them
	// in buildMessageResponse so the client can use the lowest
	// row id as the `before_id` cursor for the next page.
	// rowIDs is in DESC order (matches the row order), so
	// rowIDs[len-1] is the smallest id (oldest returned row).
	for i, m := range msgs {
		resp := buildMessageResponse(m, metas, createds, i, rowIDs[i], seqs[i], regenGroupIDs[i], isArchiveds[i])
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

// SnapshotRecovery (P0-1) returns the delta of assistant
// messages with seq > after_seq, oldest first, with the
// full metadata blob (carrying the persisted parts[]).
// The frontend uses this after a dropped SSE stream to
// rebuild the trailing assistant message that was being
// streamed when the network failed.
//
// Why a separate endpoint instead of ListMessages? Two
// reasons:
//   1. ListMessages returns the full history with
//      pagination cursors; the recovery flow wants a
//      "delta since cursor" pattern and the response
//      shape is much simpler (no has_more / oldest_*).
//   2. ListMessages applies mergeConsecutiveAssistant +
//      buildMessageResponse decoding, which is the wrong
//      shape for a partial stream. The recovery flow
//      just wants the raw row data so it can decide how
//      to merge it with whatever parts the local Pinia
//      store already accumulated.
//
// Query:
//   ?after_seq=N   — return rows with seq > N (default 0)
//
// Response:
//   { "messages": [MessageResponse...],
//     "next_seq":  <highest seq returned, 0 if none> }
func (h *Handler) SnapshotRecovery(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	if _, err := h.store.GetConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	afterSeq := parseInt64Query(c, "after_seq", 0)
	if afterSeq < 0 {
		afterSeq = 0
	}

	msgs, metas, createds, seqs := h.store.GetAssistantMessagesAfterSeq(id, afterSeq)

	out := make([]MessageResponse, 0, len(msgs))
	for i, m := range msgs {
		// We pass rowID=0 (we don't have it from this
		// query) — the recovery flow doesn't need the
		// SQLite row id, only the seq cursor. The created
		// time comes from createds[i] (parallel to msgs).
		resp := buildMessageResponse(m, metas, createds, i, 0, seqs[i], "", false)
		if resp != nil {
			out = append(out, *resp)
		}
	}

	nextSeq := int64(0)
	if len(seqs) > 0 {
		nextSeq = seqs[len(seqs)-1]
	}

	c.JSON(http.StatusOK, gin.H{
		"messages": out,
		"next_seq": nextSeq,
	})
}

// ContextMessage is one entry in the P2-3 inspector's
// per-message breakdown. Distinct from MessageResponse
// because the inspector only needs a small subset of
// fields (role + tokens + preview + is_tool_result);
// shipping the full MessageResponse (with its
// possibly-large parts[] JSON) would balloon the
// payload and slow down the drawer's render.
type ContextMessage struct {
	// Role is "user" / "assistant" / "tool" /
	// "system". The frontend uses this to colour-
	// code the row.
	Role string `json:"role"`
	// Tokens is the estimate for this single message
	// (content + small per-message overhead). Rounded
	// for display, but the underlying computation is
	// the same `llm.EstimateTokens` the agent uses to
	// decide when to compress.
	Tokens int `json:"tokens"`
	// Preview is a 100-char head of the message text
	// for the UI list. Tool results truncate more
	// aggressively (40 chars) because the front of a
	// tool result is often a JSON header.
	Preview string `json:"preview"`
	// IsToolResult is true for role="tool" rows AND
	// for assistant message parts that came from a
	// tool (sub-agent text often describes a tool's
	// output). The drawer uses this to render a
	// distinct "tool" background.
	IsToolResult bool `json:"is_tool_result"`
	// IsCompressed is true for the synthetic summary
	// message that tryAutoCompact wrote to the system
	// prompt (replaces a chunk of older history). The
	// drawer shows a small "已压缩" badge on these so
	// the user understands why the message list is
	// shorter than they remember.
	IsCompressed bool `json:"is_compressed,omitempty"`
}

// ContextInspectorResponse is the wire shape of
// GET /api/v1/sessions/:id/context. Returns a quick
// snapshot of the current conversation's footprint so
// the chat UI can render the "上下文" tab/drawer.
type ContextInspectorResponse struct {
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

// ContextInspector (P2-3) returns a quick breakdown
// of the conversation footprint: per-message token
// estimate, total vs context window, compressed
// summary (if any). The response is small enough to
// load on every session switch — the messages list
// is bounded by the same contextMessageLimit that
// ListMessages uses, so a 200-message session
// produces a ~10 KB response (no full parts[] payload).
//
// Token counts are estimates (see internal/llm/
// token_count.go). The display labels every number
// as "估算" so users don't get confused when an exact
// tokenizer (had we shipped one) disagrees by ±20%.
func (h *Handler) ContextInspector(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	if _, err := h.store.GetConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	meta := h.ensureMetaLoaded(id)
	provider := meta.Provider
	if provider == "" {
		provider = h.getCfg().LLM.Default
	}
	model := meta.Model
	if model == "" {
		for _, p := range h.getCfg().LLM.Providers {
			if p.Name == provider {
				model = p.EffectiveModel()
				break
			}
		}
	}

	// Build the LLM-bound message list the same way
	// SendMessage does: history after the compressed
	// range + the synthetic CompressedSummary. The
	// token count we report is what the LLM actually
	// sees on the next turn.
	lastComp := h.store.LastCompressedIDFor(id)
	var histMsgs []llm.ChatMessage
	var compSummary string
	if lastComp > 0 {
		histMsgs, _, _ = h.store.GetChatMessagesAfterIDFor(id, 0, lastComp)
		compSummary = h.store.CompressedSummaryFor(id)
	} else {
		histMsgs = h.store.GetChatMessagesFor(id, 0)
	}
	boundMsgs := buildLLMMessages(histMsgs)
	if compSummary != "" {
		// Mirror how the system prompt pre-pends the
		// summary — the bound message list does NOT
		// include it directly (it's prepended via the
		// system prompt), but it does consume tokens
		// from the context window. Add it to the
		// estimate so the inspector reports a number
		// consistent with the actual LLM-bound payload.
		boundMsgs = append(boundMsgs, llm.ChatMessage{
			Role:    "system",
			Content: "[compressed summary]\n" + compSummary,
		})
	}

	// Per-message breakdown for the UI list. We use
	// the RAW (un-bound) messages here so each row
	// corresponds to a single database row the user
	// can mentally map. The token count is the
	// `EstimateTokens` of the content (no per-
	// message overhead at the row level — that
	// overhead is rolled into the total below).
	const previewMax = 100
	out := make([]ContextMessage, 0, len(histMsgs))
	for _, m := range histMsgs {
		preview := m.Content
		if len(preview) > previewMax {
			preview = preview[:previewMax] + "…"
		}
		// Tool rows that are pure JSON get a tighter
		// preview — the first 40 chars of a tool
		// result is usually {"ok":true,"data":...}
		// which is more noise than signal at the
		// top of the list. (We still keep the full
		// token estimate below.)
		if m.Role == "tool" {
			if len(m.Content) > 40 {
				preview = m.Content[:40] + "…"
			}
		}
		out = append(out, ContextMessage{
			Role:         m.Role,
			Tokens:       llm.EstimateTokens(m.Content),
			Preview:      preview,
			IsToolResult: m.Role == "tool",
		})
	}

	// Total estimate matches what the agent uses
	// internally to decide when to compact, so the
	// drawer's utilisation bar lines up with the
	// agent's "context_warn" phase events.
	totalEstimate := llm.EstimateTokensMessages(boundMsgs)
	cw := h.agent.LLM().ContextWindow(provider, model)
	if cw <= 0 {
		cw = llm.DefaultContextWindow
	}
	usable := llm.UsableContextWithBuf(cw, llm.AutoCompactBuffer)
	utilPct := 0.0
	if usable > 0 {
		utilPct = float64(totalEstimate) / float64(usable) * 100.0
		if utilPct > 999.9 {
			utilPct = 999.9
		}
	}

	c.JSON(http.StatusOK, ContextInspectorResponse{
		SessionID:         id,
		Provider:          provider,
		Model:             model,
		ContextWindow:     cw,
		EstimatedTokens:   totalEstimate,
		UsableTokens:      usable,
		UtilizationPct:    utilPct,
		CompressedSummary: compSummary,
		Messages:          out,
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
//
// regenGroupID / isArchived are the P1-4 regen-history
// fields, propagated as-is so the frontend can drive the
// ◀ N/M ▶ pager and the "上一版回答" chip. The main
// ListMessages SQL filters archived rows out, so the values
// here are always (empty, false) on that path; the
// /replies endpoint (a future addition in P1-4 commit 2)
// surfaces the full sibling set including the
// (groupID, true) rows.
func buildMessageResponse(m llm.ChatMessage, metas []string, createds []int64, i int, rowID int64, seq int64, regenGroupID string, isArchived bool) *MessageResponse {
	created := time.Now().Unix()
	if i < len(createds) && createds[i] != 0 {
		created = createds[i]
	}
	resp := MessageResponse{
		ID:           rowID,
		Seq:          seq,
		Role:         m.Role,
		MsgType:      m.MsgType,
		Content:      m.Content,
		CreatedAt:    created,
		Name:         m.Name,
		SubmitToLLM:  m.SubmitToLLM,
		RegenGroupID: regenGroupID,
		IsArchived:   isArchived,
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
		// Forward the frontend's client-minted row id. The
		// agent uses it as the explicit SQLite row id for
		// this turn's user message, so rollback/regen
		// have a valid target even when the LLM call
		// fails before the SSE `done` event lands. See
		// the comment on SendMessageRequest.ClientMsgID.
		ClientMsgID:       req.ClientMsgID,
		ReasoningEffort:   meta.ReasoningEffort,
		CompressedSummary: compSummary,
		SessionID:         id,
		ProjectRoot:       meta.ProjectPath,
		SkillContext:      req.SkillContext,
		PlanMode:          meta.PlanMode,
		PermissionLevel:   meta.PermissionLevel,
		KBBase:            meta.KnowledgeBase,
		AutoContinue:      h.sessionAutoContinue(id),
		// P3-3: copy the trace id off the request context
		// so the agent loop can stamp every emitted chunk
		// without re-reading ctx. The traceIDMiddleware on
		// the server has already minted one (or adopted
		// the client-supplied one) and put it under
		// trace.ctxKey.
		TraceID: trace.FromContext(c.Request.Context()),
	}

	stream := h.agent.ChatStream(c.Request.Context(), chatReq)
	h.respondSSE(c, stream, id, provider, model)
}

// respondSSE writes a chat stream to the response. Used
// by both SendMessage and the P1-3 Regenerate endpoint —
// both paths produce an `agent.ChatStreamChunk` channel
// and need the same SSE envelope (data + id, flush
// per-frame, done-handling). Keep this the only place
// that knows how an internal chunk becomes wire bytes.
func (h *Handler) respondSSE(c *gin.Context, stream <-chan agent.ChatStreamChunk, sessionID, provider, model string) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Header("X-Session-ID", sessionID)
	c.Header("X-Provider", provider)
	c.Header("X-Model", model)
	// P3-3: echo the trace id from the request context so
	// curl users can grep server-debug.log with the same
	// id they see in the response. The middleware already
	// set this header on the *outgoing* response, but a
	// second set here is harmless and self-documenting —
	// any future caller of respondSSE that bypassed the
	// middleware (e.g. a unit test) still gets the right
	// header.
	if tid := trace.FromContext(c.Request.Context()); tid != "" {
		c.Header("X-Trace-Id", tid)
	}
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
		var ids streamDoneIDs
		if chunk.Done && h.store != nil {
			ids.userMessageID = h.store.GetLastUserMessageID(sessionID)
			ids.lastMessageID = h.store.GetLastMessageID(sessionID)
		}
		ev := streamEventFromChunk(chunk, provider, model, ids)
		if ev.Type == "question" {
			log.Printf("[sse] writing question event (%d bytes json)", len(ev.QuestionJSON))
		}
		if err := writeSSEFrame(w, ev); err != nil {
			return false
		}
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
// RegenerateRequest is the body of POST
// /api/v1/sessions/:id/regenerate. The user_message_id
// is the SQLite row id of the user message whose
// assistant reply should be re-produced. The handler
// physically deletes every message with id > user_message_id
// in the same conversation, then re-runs the agent loop
// from scratch (the user message itself stays).
//
// Why physical delete (not soft-mark): the chat store
// reads back the conversation as a single source of
// truth. Soft marks would surface as "ghost" rows in
// the assistant message list until something explicitly
// reaped them, and the per-session meta's
// last_message_id pointer would also need to roll back
// to the right value. The existing rollback code path
// already does the same physical delete + meta rewrite
// (DeleteMessagesFrom), and we don't put the result on
// the undo stack because regen is a normal flow, not
// a destructive op.
type RegenerateRequest struct {
	UserMessageID int64 `json:"user_message_id" binding:"required"`
}

// Regenerate re-runs the agent loop for the assistant
// reply of a given user message. The user message itself
// is preserved; every existing assistant sibling in the
// regen group is soft-archived (P1-4) instead of hard-
// deleted (P1-3), and the new reply becomes the active
// row. See RegenerateRequest for the rationale.
func (h *Handler) Regenerate(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	if _, err := h.store.GetConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	var req RegenerateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.UserMessageID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_message_id must be > 0"})
		return
	}

	// Validate: the row must exist, must belong to this
	// session, and must be a user message. Without the
	// role check, a malicious / buggy client could pass
	// the id of an assistant message and the agent loop
	// would re-run with no user prompt at all.
	if err := h.store.ValidateUserMessageID(id, req.UserMessageID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// P1-4: soft-archive every existing sibling in this
	// regen group. The new assistant message (created by
	// the agent loop below) will be the new active row.
	// keepActiveID = 0 means "no row stays active" —
	// every group row is archived, and the upcoming
	// agent-loop insert becomes the lone active row.
	//
	// groupID is the string form of the user message id.
	// The agent loop reads ChatRequest.RegenGroupID and
	// stamps it on the new assistant row's
	// messages.regen_group_id column.
	groupID := strconv.FormatInt(req.UserMessageID, 10)
	if _, err := h.store.ArchiveSiblings(id, groupID, 0); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "archive siblings failed: " + err.Error()})
		return
	}

	// Resolve style / provider / model the same way
	// SendMessage does (body override > per-session >
	// config default). The RegenerateRequest body is
	// intentionally minimal — we don't let the client
	// change style / model on regen to keep the diff
	// small and avoid surprising model swaps. Future
	// iterations could accept overrides here.
	styleStr := h.sessionStyle(id)
	if styleStr == "" {
		styleStr = string(style.Tech)
	}
	if def := style.Style(h.getCfg().Style.Default); def != "" && styleStr == "" {
		styleStr = string(def)
	}
	provider := h.sessionProvider(id)
	if provider == "" {
		provider = h.getCfg().LLM.Default
	}
	model := h.sessionModel(id, provider)

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
	// Note: the user message at UserMessageID is still in
	// histMsgs (we only archived sibling assistant rows).
	// The agent loop sees it as the latest message and
	// continues from there — the user prompt drives the
	// new reply. We do NOT append a fresh user message;
	// the prompt already lives in the conversation.
	//
	// The archived siblings are excluded by the
	// is_archived = 0 filter in
	// GetChatMessagesFor -> GetChatMessagesWithMetaPage
	// so they don't leak into the LLM context. The
	// regen'd LLM gets a clean history (the user prompt
	// + every earlier turn that wasn't regenerated),
	// exactly as the user expects from a "give me a
	// different answer" action.
	msgs := buildLLMMessages(histMsgs)

	chatReq := agent.ChatRequest{
		Style:             style.Style(styleStr),
		Provider:          provider,
		Model:             model,
		Messages:          msgs,
		ReasoningEffort:   meta.ReasoningEffort,
		CompressedSummary: compSummary,
		SessionID:         id,
		ProjectRoot:       meta.ProjectPath,
		SkillContext:      "",
		PlanMode:          meta.PlanMode,
		PermissionLevel:   meta.PermissionLevel,
		KBBase:            meta.KnowledgeBase,
		AutoContinue:      h.sessionAutoContinue(id),
		// P1-4: the agent loop reads this and stamps the
		// new assistant row's regen_group_id, joining the
		// user message's regen group. Empty for normal
		// (non-regen) SendMessage requests.
		RegenGroupID:      groupID,
		TraceID:           trace.FromContext(c.Request.Context()),
	}

	// Same session-locks pattern as SendMessage so a
	// concurrent regen + send on the same session can't
	// interleave writes.
	if _, loaded := h.sessionLocks.LoadOrStore(id, struct{}{}); loaded {
		c.JSON(http.StatusConflict, gin.H{"error": "a message is already being processed for this session"})
		return
	}
	defer h.sessionLocks.Delete(id)

	stream := h.agent.ChatStream(c.Request.Context(), chatReq)
	h.respondSSE(c, stream, id, provider, model)
}

// UserMessageSummary is the wire shape of the parent user
// message carried in the ListRegenReplies response. We
// include it so the frontend can render the bubble's
// "上一版回答" chip and the pager's user-message preview
// (e.g. `◀ 2/3 · "请帮我..." ▶`) without a second
// round-trip. content_preview is the first 80 chars of
// content — long enough to be useful, short enough to keep
// the response payload under a few KB even for sessions
// with hundreds of regen groups.
type UserMessageSummary struct {
	ID             int64  `json:"id"`
	Role           string `json:"role"`
	Content        string `json:"content"`
	ContentPreview string `json:"content_preview"`
	CreatedAt      int64  `json:"created_at"`
}

// RepliesResponse is the JSON body of
// GET /api/v1/sessions/:id/messages/:user_msg_id/replies.
// Returns the full regen group (active + archived) plus
// the user-message summary the UI needs to render the
// pager / chip without a second round-trip.
type RepliesResponse struct {
	UserMessage    UserMessageSummary `json:"user_message"`
	Replies        []MessageResponse  `json:"replies"`
	ActiveReplyID  int64              `json:"active_reply_id"`
}

// userMessagePreviewLength is the truncation length for
// UserMessageSummary.ContentPreview. Long enough to be
// meaningful ("请帮我写一个 todo 工具..."), short enough
// to keep the response small. 80 chars is the same
// limit the project's docs use for the assistant's
// tool-call args preview.
const userMessagePreviewLength = 80

// ListRegenReplies returns the full sibling set for the
// regen group rooted at :user_msg_id, oldest-first by
// SQLite id. The response includes a UserMessageSummary
// so the frontend has enough context to render the
// bubble's ◀ N/M ▶ pager + the "上一版回答" chip's
// user-message preview without a second round-trip.
//
// Errors:
//   - 404: session or user message not found
//   - 400: user_msg_id is not a valid integer
//   - 200 + replies: []  when the user message exists
//     but has no regen history (legacy single-shot row,
//     regen_group_id is NULL on the only assistant).
//     The frontend treats this as "no pager" — the
//     bubble stays in the single-reply shape.
func (h *Handler) ListRegenReplies(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	convID := c.Param("id")
	if _, err := h.store.GetConversation(convID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	userMsgID, err := strconv.ParseInt(c.Param("user_msg_id"), 10, 64)
	if err != nil || userMsgID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_msg_id must be a positive integer"})
		return
	}

	// Load the user message summary. If it's missing or
	// not a user message, return 404 — the endpoint is
	// only meaningful for actual user messages.
	summary, err := h.loadUserMessageSummary(convID, userMsgID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	// Load the full sibling set via the store helper.
	// regen_group_id is the string form of the user
	// message id (the same convention ArchiveSiblings
	// and ActivateSibling use).
	groupID := strconv.FormatInt(userMsgID, 10)
	_, metas, createds, ids, seqs, isArchiveds, regenGroupIDs := h.store.ListSiblings(convID, groupID)

	// Build the parallel responses. ListSiblings is
	// oldest-first by id; the frontend can render
	// ◀ older ... N/M ... newer ▶ with active_idx
	// = replies.findIndex(!is_archived).
	out := make([]MessageResponse, 0, len(ids))
	var activeID int64
	for i := range ids {
		cm := llm.ChatMessage{
			Role:    "assistant",
			Content: "",
		}
		// decode via buildMessageResponse so the wire
		// shape matches ListMessages exactly (parts
		// decoded, attachments reconstructed, etc.).
		resp := buildMessageResponse(cm, metas, createds, i, ids[i], seqs[i], regenGroupIDs[i], isArchiveds[i])
		if resp == nil {
			continue
		}
		out = append(out, *resp)
		if !isArchiveds[i] {
			activeID = ids[i]
		}
	}

	c.JSON(http.StatusOK, RepliesResponse{
		UserMessage:   *summary,
		Replies:       out,
		ActiveReplyID: activeID,
	})
}

// loadUserMessageSummary reads one user message and shapes
// it as UserMessageSummary. Returns an error when the row
// doesn't exist, isn't in the conversation, or isn't a
// user role — the caller maps the error to 404.
func (h *Handler) loadUserMessageSummary(convID string, msgID int64) (*UserMessageSummary, error) {
	_ = h.store.Flush()
	// Single-row read. We don't need a paging method —
	// loadUserMessageSummary is called only on demand
	// (the first time the user hovers a paginated bubble
	// or paginates), not on every list.
	var (
		role     string
		content  string
		created  int64
	)
	err := h.store.DB().QueryRow(
		`SELECT role, content, created_at FROM messages
		 WHERE conversation_id = ? AND id = ?`,
		convID, msgID,
	).Scan(&role, &content, &created)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("message id %d not found in session %s", msgID, convID)
	}
	if err != nil {
		return nil, err
	}
	if role != "user" {
		return nil, fmt.Errorf("message id %d has role %q, want \"user\"", msgID, role)
	}
	preview := content
	if len(preview) > userMessagePreviewLength {
		preview = preview[:userMessagePreviewLength] + "…"
	}
	return &UserMessageSummary{
		ID:             msgID,
		Role:           role,
		Content:        content,
		ContentPreview: preview,
		CreatedAt:      created,
	}, nil
}

// ActivateRegenReplyRequest is the body of
// POST /api/v1/sessions/:id/messages/:reply_id/activate.
// user_message_id is required so the handler can
// compute the regen group_id and validate the reply
// actually belongs to that group (a malicious /
// buggy client could otherwise activate any reply
// from any conversation).
type ActivateRegenReplyRequest struct {
	UserMessageID int64 `json:"user_message_id" binding:"required"`
}

// ActivateRegenReply makes :reply_id the active reply in
// its regen group, archiving every other sibling. The
// caller is the frontend's ◀ N/M ▶ pager when the user
// picks a different historical reply to view.
//
// On success, returns the new full sibling set (in the
// same shape as ListRegenReplies) so the frontend can
// re-render the bubble + pager in one round-trip.
//
// Errors:
//   - 404: session / reply / user message not found
//   - 400: reply_id / user_message_id invalid, or the
//     reply isn't in the user message's regen group
//   - 503: store unavailable
func (h *Handler) ActivateRegenReply(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	convID := c.Param("id")
	if _, err := h.store.GetConversation(convID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	replyID, err := strconv.ParseInt(c.Param("reply_id"), 10, 64)
	if err != nil || replyID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "reply_id must be a positive integer"})
		return
	}
	var req ActivateRegenReplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.UserMessageID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_message_id must be > 0"})
		return
	}

	// Compute the group_id and delegate the activation
	// to the store. The store verifies the reply belongs
	// to this group / conversation and returns a
	// descriptive error on mismatch (mapped to 400 by
	// the handler below).
	groupID := strconv.FormatInt(req.UserMessageID, 10)
	if err := h.store.ActivateSibling(convID, groupID, replyID); err != nil {
		// Validation errors (group / conversation
		// mismatch) get 400. The store's error
		// messages already name the offending id, so we
		// pass them through verbatim — the client
		// can surface them in the toast.
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Re-list the sibling set so the client gets the
	// fresh active_reply_id + every reply's current
	// is_archived flag in one round-trip. The user
	// doesn't need a second fetch to update the pager.
	summary, err := h.loadUserMessageSummary(convID, req.UserMessageID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	_, metas, createds, ids, seqs, isArchiveds, regenGroupIDs := h.store.ListSiblings(convID, groupID)
	out := make([]MessageResponse, 0, len(ids))
	var activeID int64
	for i := range ids {
		cm := llm.ChatMessage{Role: "assistant"}
		resp := buildMessageResponse(cm, metas, createds, i, ids[i], seqs[i], regenGroupIDs[i], isArchiveds[i])
		if resp == nil {
			continue
		}
		out = append(out, *resp)
		if !isArchiveds[i] {
			activeID = ids[i]
		}
	}

	c.JSON(http.StatusOK, RepliesResponse{
		UserMessage:   *summary,
		Replies:       out,
		ActiveReplyID: activeID,
	})
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
		AutoContinue:   h.sessionAutoContinue(cv.ID),
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

// SetBrowserManager wires the browser control manager for WebSocket
// and REST endpoints. Pass nil to disable browser control endpoints.
func (h *Handler) SetBrowserManager(bm *browser.Manager) {
	h.browserMgr = bm
}

// SetListenAddr records the real listen address so the browser
// extension UI can display the correct WebSocket URL. Must be
// called before Run/RunAt starts accepting connections.
func (h *Handler) SetListenAddr(addr string) {
	h.listenAddr = addr
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
