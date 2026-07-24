package server

// interactive.go — interactive mid-loop endpoints that the agent
// pings during a chat turn (question, confirm, plan) and the
// auxiliary GET/POST endpoints for todos, system messages, and
// summarizer configuration.
//
//   POST /api/v1/sessions/:id/question            (QuestionResponse)
//   POST /api/v1/sessions/:id/confirm             (ConfirmResponse)
//   POST /api/v1/sessions/:id/execute-plan        (ExecutePlan)
//   POST /api/v1/sessions/:id/system-message      (SaveSystemMessage)
//   GET  /api/v1/sessions/:id/todos               (GetTodos)
//   POST /api/v1/sessions/:id/compress            (CompressConversation)
//   POST /api/v1/sessions/:id/reasoning-effort    (SetReasoningEffort)
//
// Split from handler.go in T04. Behaviour unchanged.

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/tool"
)

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
