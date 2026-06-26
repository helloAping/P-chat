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
	agent *agent.Agent
	cfg   *config.Config
	store *memory.Store

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

func NewHandler(a *agent.Agent, cfg *config.Config, store *memory.Store) *Handler {
	return &Handler{
		agent: a,
		cfg:   cfg,
		store: store,
		meta:  make(map[string]sessionMeta),
	}
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
}

// StreamEvent is one chunk of a Server-Sent Events stream from
// POST /sessions/:id/messages. The Type field is one of:
// "content", "phase", "tool", "error", "done".
type StreamEvent struct {
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
	Phase   string `json:"phase,omitempty"`
	Step    string `json:"step,omitempty"`
	Message string `json:"message,omitempty"`

	// Tool fields
	ToolName   string `json:"tool_name,omitempty"`
	ToolStatus  string `json:"tool_status,omitempty"`
	ToolResult  string `json:"tool_result,omitempty"`
	ToolError   string `json:"tool_error,omitempty"`
	ToolElapsed string `json:"tool_elapsed,omitempty"`

	// Done fields
	TokensIn  int    `json:"tokens_in,omitempty"`
	TokensOut int    `json:"tokens_out,omitempty"`
	Elapsed   string `json:"elapsed,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`

	// Error fields
	Error      string `json:"error,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
}

// --- Health / metadata ---

func (h *Handler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) Styles(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"styles": []gin.H{
			{"id": "cute", "label": "小P (PiPi)", "desc": "软萌治愈风格"},
			{"id": "guofeng", "label": "墨言 (MoYan)", "desc": "古风雅致风格"},
			{"id": "tech", "label": "NEXUS (零号)", "desc": "科技极客风格"},
		},
	})
}

func (h *Handler) Providers(c *gin.Context) {
	type providerInfo struct {
		Name     string               `json:"name"`
		Model    string               `json:"model"`
		Protocol string               `json:"protocol"`
		IsDefault bool                `json:"is_default"`
		Models   []config.ModelConfig `json:"models"`
	}

	providers := []providerInfo{}
	for _, p := range h.cfg.LLM.Providers {
		providers = append(providers, providerInfo{
			Name:      p.Name,
			Model:     p.EffectiveModel(),
			Protocol:  p.GetProtocol(),
			IsDefault: p.Name == h.cfg.LLM.Default,
			Models:    p.AllModels(),
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
	msgs := h.store.GetMessages()
	out := make([]MessageResponse, 0, len(msgs))
	for _, m := range msgs {
		created := time.Now().Unix() // best-effort; exact ts requires schema change
		out = append(out, MessageResponse{
			ID:         0, // not exposed individually yet
			Role:       m.Role,
			Content:    m.Content,
			CreatedAt:  created,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		})
	}
	c.JSON(http.StatusOK, gin.H{"messages": out})
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

	// Resolve style (per-session override > body > default)
	s, _ := style.ParseStyle(req.Style)
	if s == "" {
		// Fall back to the agent's default
		s = style.Tech
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
		Style:    s,
		Provider: provider,
		Model:    model,
		Messages: msgs,
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
func chunkToEvent(chunk agent.ChatStreamChunk, provider, model string) StreamEvent {
	ev := StreamEvent{Phase: chunk.Phase, Step: chunk.Step, Message: chunk.Message, Provider: provider, Model: model}

	if chunk.Content != "" {
		ev.Type = "content"
		ev.Content = chunk.Content
		return ev
	}
	if chunk.ToolName != "" {
		ev.Type = "tool"
		ev.ToolName = chunk.ToolName
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
	if chunk.Error != "" {
		ev.Type = "error"
		ev.Error = chunk.Error
		// Try to extract a suggestion (the *llm.APIError format is
		// "[kind] message" and the UI prefix is already there).
		return ev
	}
	if chunk.Done {
		ev.Type = "done"
		ev.TokensIn = chunk.TokensIn
		ev.TokensOut = chunk.TokensOut
		ev.Elapsed = chunk.Duration
		return ev
	}
	// Other phase events (system, memory, plan) — surface as "phase"
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
