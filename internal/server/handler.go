package server

import (
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

	// sessionMeta remembers the per-session style + provider
	// overrides. The Conversation table doesn't store these (yet),
	// so we use an in-memory map keyed by session id. This is good
	// enough for the GUI/CLI use case; for true multi-process
	// support this would need to move to SQLite.
	metaMu sync.Mutex
	meta   map[string]sessionMeta
}

type sessionMeta struct {
	Style    string
	Provider string
}

func NewHandler(a *agent.Agent, cfg *config.Config, store *memory.Store) *Handler {
	return &Handler{
		agent: a,
		cfg:   cfg,
		store: store,
		meta:  make(map[string]sessionMeta),
	}
}

func (h *Handler) setSessionMeta(id, style, provider string) {
	h.metaMu.Lock()
	defer h.metaMu.Unlock()
	m := h.meta[id]
	if style != "" {
		m.Style = style
	}
	if provider != "" {
		m.Provider = provider
	}
	h.meta[id] = m
}

func (h *Handler) sessionStyle(id string) string {
	h.metaMu.Lock()
	defer h.metaMu.Unlock()
	return h.meta[id].Style
}

func (h *Handler) sessionProvider(id string) string {
	h.metaMu.Lock()
	defer h.metaMu.Unlock()
	if p := h.meta[id].Provider; p != "" {
		return p
	}
	// Fall back to the configured default
	return h.cfg.LLM.Default
}

// --- Request/Response types ---

// SendMessageRequest is the body of POST /sessions/:id/messages.
type SendMessageRequest struct {
	Message string `json:"message" binding:"required"`
	Style   string `json:"style,omitempty"`
}

// CreateSessionRequest is the body of POST /sessions.
type CreateSessionRequest struct {
	Style    string `json:"style,omitempty"`
	Provider string `json:"provider,omitempty"`
	Title    string `json:"title,omitempty"`
}

// RenameSessionRequest is the body of PATCH /sessions/:id.
type RenameSessionRequest struct {
	Title string `json:"title" binding:"required"`
}

// SessionResponse is the JSON form of a memory.Conversation.
type SessionResponse struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
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
	TokensIn  int `json:"tokens_in,omitempty"`
	TokensOut int `json:"tokens_out,omitempty"`
	Elapsed   string `json:"elapsed,omitempty"`

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
		Name     string `json:"name"`
		Model    string `json:"model"`
		Protocol string `json:"protocol"`
	}

	providers := []providerInfo{}
	for _, p := range h.cfg.LLM.Providers {
		providers = append(providers, providerInfo{
			Name:     p.Name,
			Model:    p.Model,
			Protocol: p.GetProtocol(),
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
		out = append(out, sessionToResponse(conv))
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
	h.setSessionMeta(id, req.Style, req.Provider)

	// Re-fetch and return the full session record.
	convs := h.store.ListConversations()
	for _, cv := range convs {
		if cv.ID == id {
			c.JSON(http.StatusCreated, sessionToResponse(cv))
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
			c.JSON(http.StatusOK, sessionToResponse(cv))
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

	provider := h.sessionProvider(id)

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
		Messages: msgs,
	}

	stream := h.agent.ChatStream(c.Request.Context(), chatReq)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Session-ID", id)

	c.Stream(func(w io.Writer) bool {
		chunk, ok := <-stream
		if !ok {
			return false
		}
		ev := chunkToEvent(chunk)
		data, _ := json.Marshal(ev)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		return !chunk.Done
	})
}

// chunkToEvent maps an internal ChatStreamChunk to a public
// StreamEvent the API exposes.
func chunkToEvent(chunk agent.ChatStreamChunk) StreamEvent {
	ev := StreamEvent{Phase: chunk.Phase, Step: chunk.Step, Message: chunk.Message}

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
// representation. The import is local because the signature uses
// only primitives.
func sessionToResponse(cv memory.Conversation) SessionResponse {
	return SessionResponse{
		ID:        cv.ID,
		Title:     cv.Title,
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
