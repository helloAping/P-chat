package server

// styles.go — style CRUD endpoints.
//
//   GET    /api/v1/styles
//   POST   /api/v1/styles
//   GET    /api/v1/styles/:id
//   PATCH  /api/v1/styles/:id
//   DELETE /api/v1/styles/:id
//
// Split from handler.go in T04. Behaviour unchanged.

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/style"
)

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

