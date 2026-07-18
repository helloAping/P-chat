package server

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/browser"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/mcp"
	"github.com/p-chat/pchat/internal/memory"
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
