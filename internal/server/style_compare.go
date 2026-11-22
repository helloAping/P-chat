package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/style"
)

type styleCompareVariant struct {
	Style string `json:"style"`
	Label string `json:"label"`
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

// CompareStyleIDs picks up to two personas other than the current one.
func CompareStyleIDs(current string, all []style.Style) []style.Style {
	var out []style.Style
	for _, item := range all {
		if item.IsOff() || string(item) == current {
			continue
		}
		out = append(out, item)
		if len(out) == 2 {
			break
		}
	}
	return out
}

// ResolveCompareStyles picks the personas for one compare call.
// An empty request keeps the previous "two other styles" default.
// One or two ids must already exist and must not be off. The current
// session style is allowed so a draft can be rewritten in place.
func ResolveCompareStyles(current string, requested []string, all []style.Style) ([]style.Style, error) {
	cleaned := make([]string, 0, len(requested))
	for _, id := range requested {
		id = strings.TrimSpace(id)
		if id != "" {
			cleaned = append(cleaned, id)
		}
	}
	if len(cleaned) == 0 {
		return CompareStyleIDs(current, all), nil
	}
	if len(cleaned) > 2 {
		return nil, fmt.Errorf("最多选择 2 个风格")
	}
	known := make(map[string]style.Style, len(all))
	for _, item := range all {
		known[string(item)] = item
	}
	out := make([]style.Style, 0, len(cleaned))
	seen := map[string]struct{}{}
	for _, id := range cleaned {
		item, ok := known[id]
		if !ok || item.IsOff() {
			return nil, fmt.Errorf("未知风格: %s", id)
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("未知风格")
	}
	return out, nil
}

// StyleCompare answers the same sentence in two other personas.
// Nothing is written to the session.
func (h *Handler) StyleCompare(c *gin.Context) {
	if h.store == nil || h.styleMgr == nil || h.agent == nil || h.agent.LLM() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "style compare unavailable"})
		return
	}
	id := c.Param("id")
	if _, err := h.store.GetConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	var body struct {
		Text   string   `json:"text"`
		Styles []string `json:"styles"`
	}
	_ = c.ShouldBindJSON(&body)
	text := strings.TrimSpace(body.Text)
	if text == "" {
		var err error
		text, err = h.store.LastVisibleUserText(id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		text = strings.TrimSpace(text)
	}
	if text == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有可对照的句子"})
		return
	}
	if utf8.RuneCountInString(text) > 4000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "句子过长"})
		return
	}
	provider := h.sessionProvider(id)
	model := h.sessionModel(id, provider)
	if provider == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "当前会话没有可用模型"})
		return
	}
	current := h.sessionStyle(id)
	alts, err := ResolveCompareStyles(current, body.Styles, h.styleMgr.ListAll())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	variants := make([]styleCompareVariant, len(alts))
	var wg sync.WaitGroup
	parent := c.Request.Context()
	for i, alt := range alts {
		wg.Add(1)
		go func(i int, alt style.Style) {
			defer wg.Done()
			variants[i] = h.oneStyleTake(parent, provider, model, alt, text)
		}(i, alt)
	}
	wg.Wait()
	c.JSON(http.StatusOK, gin.H{
		"source":   text,
		"current":  current,
		"variants": variants,
	})
}

func (h *Handler) oneStyleTake(parent context.Context, provider, model string, persona style.Style, text string) styleCompareVariant {
	out := styleCompareVariant{Style: string(persona), Label: h.styleMgr.DisplayLabel(persona)}
	prompt, err := h.styleMgr.GetSystemPrompt(persona)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	mem, _ := h.styleMgr.GetMemory(persona)
	system := strings.TrimSpace(prompt)
	if strings.TrimSpace(mem) != "" {
		system += "\n\n" + strings.TrimSpace(mem)
	}
	system += "\n\n用这个人格直接回答下面这句话。不要调用工具，不要解释你在扮演谁。"
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	reply, err := h.agent.LLM().ChatCM(ctx, provider, model, []llm.ChatMessage{
		{Role: llm.RoleSystem, Content: system},
		{Role: llm.RoleUser, Content: text},
	}, llm.ChatOptions{MaxTokens: 700})
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Text = strings.TrimSpace(reply)
	if out.Text == "" {
		out.Error = "模型没有返回文字"
	}
	return out
}

// AdoptStyleCompare writes one chosen reply as a normal assistant message.
func (h *Handler) AdoptStyleCompare(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	if _, err := h.store.GetConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	var body struct {
		Text  string `json:"text"`
		Style string `json:"style"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" || utf8.RuneCountInString(text) > 8000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "采用的回复无效"})
		return
	}
	rowID, err := h.store.AddChatMessageWithMetaToNow(id, llm.ChatMessage{
		Role:        llm.RoleAssistant,
		Type:        llm.TypeText,
		Content:     text,
		MsgType:     llm.MsgTypeText,
		SubmitToLLM: 1,
		Meta: map[string]any{
			"origin": "style_compare",
			"style":  strings.TrimSpace(body.Style),
		},
	}, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "message_id": rowID})
}
