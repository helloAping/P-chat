package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
	"github.com/sashabaranov/go-openai"
)

// Handler serves the P-Chat HTTP API. It holds references to the
// agent and the persistent memory store so that messages and
// sessions survive across requests.
type Handler struct {
	agent    *agent.Agent
	cfg      *config.Config
	store    *memory.Store
	styleMgr *style.Manager

	// sessionMeta remembers the per-session style + provider +
	// model overrides. The in-memory map is the hot path for
	// reads; every write is also persisted to conversations.metadata
	// (a JSON blob on the Conversation row) so the override
	// survives a pchat-server restart. On startup we lazily
	// re-hydrate the map from the store on first access.
	metaMu sync.Mutex
	meta   map[string]sessionMeta
}

type sessionMeta struct {
	Style    string
	Provider string
	Model    string
}

// sessionMetaBlob is the on-disk shape written to
// conversations.metadata. The field names are JSON lower-case so
// the web side can pass them straight back to the PATCH endpoint.
type sessionMetaBlob struct {
	Style    string `json:"style,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

func NewHandler(a *agent.Agent, cfg *config.Config, store *memory.Store, styleMgr *style.Manager) *Handler {
	h := &Handler{
		agent:    a,
		cfg:      cfg,
		store:    store,
		styleMgr: styleMgr,
		meta:     make(map[string]sessionMeta),
	}
	// Wire the upload resolver so the agent can read attached
	// files by their upload id. The resolver lives in the agent
	// (not the handler) because attachment expansion happens
	// inside the LLM call path, after the handler has already
	// handed the request to the agent.
	a.SetAttachmentResolver(&agent.DiskAttachmentResolver{BaseDir: UploadDir()})
	return h
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
	blob, _ := json.Marshal(sessionMetaBlob{Style: m.Style, Provider: m.Provider, Model: m.Model})
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
	return h.cfg.LLM.Default
}

// sessionModel returns the per-session model name, falling back to
// the provider's default model (EffectiveModel) when no override is
// set. Returns "" if the provider itself is unknown.
func (h *Handler) sessionModel(id, provider string) string {
	m := h.ensureMetaLoaded(id)
	if m.Model != "" {
		return m.Model
	}
	for _, p := range h.cfg.LLM.Providers {
		if p.Name == provider {
			return p.EffectiveModel()
		}
	}
	return ""
}

// validProvider returns true if name is a configured provider.
func (h *Handler) validProvider(name string) bool {
	for _, p := range h.cfg.LLM.Providers {
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
	for _, p := range h.cfg.LLM.Providers {
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
}

// CreateSessionRequest is the body of POST /sessions.
type CreateSessionRequest struct {
	Style    string `json:"style,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Title    string `json:"title,omitempty"`
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
}

// SessionResponse is the JSON form of a memory.Conversation.
// Provider / Model / Style reflect the per-session overrides
// (resolved from the in-memory + on-disk meta blob, with the
// process default for unset fields).
type SessionResponse struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	Style     string `json:"style,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// MessageResponse is the JSON form of a single message in a
// conversation history.
type MessageResponse struct {
	ID         int64  `json:"id"`
	Role       string `json:"role"`
	Content    string `json:"content"`
	CreatedAt  int64  `json:"created_at"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
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
	ToolName    string `json:"tool_name,omitempty"`
	ToolStatus  string `json:"tool_status,omitempty"`
	ToolResult  string `json:"tool_result,omitempty"`
	ToolError   string `json:"tool_error,omitempty"`
	ToolElapsed string `json:"tool_elapsed,omitempty"`
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

	// Done fields
	TokensIn  int    `json:"tokens_in,omitempty"`
	TokensOut int    `json:"tokens_out,omitempty"`
	Elapsed   string `json:"elapsed,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`

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
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
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
			Label: h.styleMgr.Label(s),
			Desc:  styleDescFor(h.styleMgr, s),
		})
	}
	c.JSON(http.StatusOK, gin.H{"styles": out})
}

// styleDescFor extracts a one-line description from a style's
// identity text. We use the first non-empty, non-heading paragraph
// — that's where the prompt typically introduces the persona
// ("你是墨言..."). Falls back to the label for built-ins and a
// generic message for user-added styles whose first paragraph is
// missing.
func styleDescFor(m *style.Manager, s style.Style) string {
	identity, err := m.GetIdentity(s)
	if err != nil || identity == "" {
		return ""
	}
	for _, line := range strings.Split(identity, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		// Skip the markdown header line.
		if strings.HasPrefix(trim, "#") {
			continue
		}
		// Take the first non-heading line and cap at ~60 runes so
		// the table row stays one line.
		r := []rune(trim)
		if len(r) > 60 {
			return string(r[:60]) + "…"
		}
		return trim
	}
	return ""
}

// CreateStyleRequest is the POST /api/v1/styles body.
type CreateStyleRequest struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Identity string `json:"identity"`
	Soul     string `json:"soul"`
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
	if req.Identity == "" {
		req.Identity = "# P-Chat AI 编程程序\n\n当前是 P-Chat AI 编程程序。\n"
	}
	if req.Soul == "" {
		req.Soul = "你是一个 AI 助手。"
	}
	s, err := h.styleMgr.Create(req.ID, req.Label, req.Identity, req.Soul)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"id":    string(s),
		"label": h.styleMgr.Label(s),
		"desc":  styleDescFor(h.styleMgr, s),
	})
}

// GetStyle returns the full identity + soul of a single style.
func (h *Handler) GetStyle(c *gin.Context) {
	if h.styleMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "style manager not available"})
		return
	}
	id := c.Param("id")
	s := style.Style(id)
	identity, err := h.styleMgr.GetIdentity(s)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	soul, _ := h.styleMgr.GetSoul(s)
	c.JSON(http.StatusOK, gin.H{
		"id":       id,
		"label":    h.styleMgr.Label(s),
		"identity": identity,
		"soul":     soul,
	})
}

// UpdateStyleRequest is the PATCH /api/v1/styles/:id body. Any
// non-empty field is overwritten; empty fields are skipped.
type UpdateStyleRequest struct {
	Label    string `json:"label,omitempty"`
	Identity string `json:"identity,omitempty"`
	Soul     string `json:"soul,omitempty"`
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
	if err := h.styleMgr.Update(id, req.Label, req.Identity, req.Soul); err != nil {
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
	for _, p := range h.cfg.LLM.Providers {
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
			IsDefault: p.Name == h.cfg.LLM.Default,
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
	convs := h.store.ListConversations()
	out := make([]SessionResponse, 0, len(convs))
	for _, conv := range convs {
		out = append(out, h.sessionToResponse(conv))
	}
	c.JSON(http.StatusOK, gin.H{"sessions": out})
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
		provider = h.cfg.LLM.Default
	}
	if !h.validProvider(provider) {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unknown provider %q", provider)})
		return
	}
	model := req.Model
	if model == "" {
		for _, p := range h.cfg.LLM.Providers {
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
	if err := h.store.DeleteConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	// Drop the cached meta so a recycled id (extremely unlikely
	// with our id scheme, but possible across long uptimes) does
	// not inherit the dead session's overrides.
	h.metaMu.Lock()
	delete(h.meta, id)
	h.metaMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"deleted": id})
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
	// Switch to this session so GetMessages returns its history.
	if err := h.store.SetCurrent(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	// Use the variant that returns raw metadata so we can pull
	// the assistant message's `parts` blob (thinking + tool +
	// sub-agent) back out of the SQLite row. GetMessages
	// discards everything it doesn't recognise, which used to
	// drop the parts silently — the user would reopen a
	// session and see only the plain text.
	msgs, metas, createds := h.store.GetMessagesWithMeta()
	out := make([]MessageResponse, 0, len(msgs))
	for i, m := range msgs {
		created := time.Now().Unix() // best-effort; exact ts requires schema change
		if i < len(createds) && createds[i] != 0 {
			created = createds[i]
		}
		resp := MessageResponse{
			ID:         0, // not exposed individually yet
			Role:       m.Role,
			Content:    m.Content,
			CreatedAt:  created,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
		// Restore the assistant message's structured parts
		// (text + thinking + tool + sub-agent). The agent
		// encodes them as JSON in messages.metadata under the
		// "parts" key. Decoding is best-effort: a malformed
		// row falls back to plain text only.
		if i < len(metas) && metas[i] != "" {
			if parts := decodePartsFromMeta(metas[i], m.Content); len(parts) > 0 {
				resp.Parts = parts
			}
		}
		// Surface image / file attachments that were part of the
		// user message so the UI can render them in the bubble.
		// For user messages we lift the *first* plain text part
		// back into `content` so the spoken text becomes the
		// main bubble body and the attachments render as chips
		// alongside it. This mirrors what the frontend already
		// does at send time and means reloading a session after
		// a server restart shows the same message layout.
		if len(m.MultiContent) > 0 {
			liftedText := false
			for _, p := range m.MultiContent {
				switch p.Type {
				case "image_url":
					url := ""
					if p.ImageURL != nil {
						url = p.ImageURL.URL
					}
					resp.Attachments = append(resp.Attachments, AttachmentPart{
						Type: "image_url",
						URL:  url,
						MIME: mimeFromDataURL(url),
						Kind: "image",
					})
				case "text":
					// Skip the "image not supported" marker for
					// user-message text-lifting: it's a system
					// note, not what the user typed. It still
					// gets emitted as an attachment so the UI
					// can show the warning chip.
					isWarn := strings.HasPrefix(p.Text, "(attached image:") && strings.Contains(p.Text, "does not support image input")
					if m.Role == "user" && !liftedText && !isWarn {
						resp.Content = p.Text
						liftedText = true
						continue
					}
					if isWarn {
						resp.Attachments = append(resp.Attachments, AttachmentPart{
							Type: "text",
							Text: p.Text,
							Name: "image-not-supported",
							Kind: "image_not_supported",
							MIME: "text/plain",
						})
						continue
					}
					name, kind, mime := inferTextPartMeta(p.Text)
					resp.Attachments = append(resp.Attachments, AttachmentPart{
						Type: "text",
						Text: p.Text,
						Name: name,
						Kind: kind,
						MIME: mime,
					})
				}
			}
		}
		out = append(out, resp)
	}
	c.JSON(http.StatusOK, gin.H{"messages": out})
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

// decodePartsFromMeta pulls the assistant message's `parts`
// (thinking + tool + sub-agent) out of the raw metadata string
// and the message content.
//
// Storage format:
//   - meta["thinking"]  → raw thinking text (not JSON-encoded)
//   - meta["parts"]     → only structural parts (tool + sub_agent)
//   - content           → text part on reload (not duplicated in meta)
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

	var parts []MessagePart

	// 1. Thinking — stored as raw string (no double-encode).
	if t, ok := blob["thinking"]; ok && t != "" {
		parts = append(parts, MessagePart{Kind: "thinking", Text: t})
	}

	// 2. Structural parts (tool + sub_agent) — JSON blob.
	if raw, ok := blob["parts"]; ok && raw != "" {
		var structural []MessagePart
		if err := json.Unmarshal([]byte(raw), &structural); err == nil {
			parts = append(parts, structural...)
		}
	}

	// 3. Text — from content (not stored in meta).
	if content != "" {
		parts = append(parts, MessagePart{Kind: "text", Text: content})
	}

	if len(parts) == 0 {
		return nil
	}
	return parts
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
	if err := h.store.SetCurrent(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	var req SendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
		if def := style.Style(h.cfg.Style.Default); def != "" {
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

	// Build messages: history + new user message
	histMsgs := h.store.GetMessages()
	msgs := make([]openai.ChatCompletionMessage, 0, len(histMsgs)+1)
	msgs = append(msgs, histMsgs...)
	msgs = append(msgs, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: req.Message,
	})

	chatReq := agent.ChatRequest{
		Style:       s,
		Provider:    provider,
		Model:       model,
		Messages:    msgs,
		Attachments: req.Attachments,
	}

	stream := h.agent.ChatStream(c.Request.Context(), chatReq)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Session-ID", id)
	c.Header("X-Provider", provider)
	c.Header("X-Model", model)

	c.Stream(func(w io.Writer) bool {
		chunk, ok := <-stream
		if !ok {
			return false
		}
		ev := chunkToEvent(chunk, provider, model)
		data, _ := json.Marshal(ev)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
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
// Mapping rules (order matters — more specific first):
//   1. Error chunk     → "error" event
//   2. Done chunk      → "done" event
//   3. Tool call chunk → "tool" event
//   4. Thinking delta  → "thinking" event
//   5. Content delta   → "content" event
//   6. Phase chunk     → "phase" event
//   7. Anything else   → "phase" heartbeat (so the client
//      doesn't appear to hang on a quiet channel).
//
// Sub-agent fields are copied verbatim when set, regardless
// of which type the chunk maps to. This lets the parent
// surface a single nested "sub-agent" card whose inner
// stream is a mix of content / thinking / phase events all
// tagged with sub_agent=true.
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
		ev.Elapsed = chunk.Duration
		return ev
	}
	if chunk.ToolName != "" {
		ev.Type = "tool"
		ev.ToolName = chunk.ToolName
		ev.ToolArgs = chunk.ToolArgs
		ev.ToolResult = chunk.ToolResult
		ev.ToolError = chunk.ToolError
		ev.ToolElapsed = chunk.ToolElapsed
		switch {
		case strings.Contains(chunk.Step, "ok"):
			ev.ToolStatus = "ok"
		case strings.Contains(chunk.Step, "warn"):
			ev.ToolStatus = "warn"
		case strings.Contains(chunk.Step, "err"):
			ev.ToolStatus = "error"
		default:
			ev.ToolStatus = "start"
		}
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
		provider = h.cfg.LLM.Default
	}
	model := m.Model
	if model == "" {
		for _, p := range h.cfg.LLM.Providers {
			if p.Name == provider {
				model = p.EffectiveModel()
				break
			}
		}
	}
	return SessionResponse{
		ID:        cv.ID,
		Title:     cv.Title,
		Provider:  provider,
		Model:     model,
		Style:     m.Style,
		CreatedAt: cv.CreatedAt.Unix(),
		UpdatedAt: cv.UpdatedAt.Unix(),
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

// reloadAfterConfigChange re-reads the on-disk config, rebuilds the
// LLM client, and pushes both into the agent. Called after any
// config-mutating handler (AddProvider, SetCapabilities, ...) so the
// changes take effect on the next request.
func (h *Handler) reloadAfterConfigChange() {
	if h.cfg == nil {
		return
	}
	cfg, err := config.Load("")
	if err != nil {
		return
	}
	h.cfg = cfg
	if h.agent == nil {
		return
	}
	newClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		return
	}
	h.agent.SetLLM(newClient)
}
