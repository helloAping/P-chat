package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/im"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/style"
)

// ProcessIMEvent consumes a normalized IM event and routes it
// through the normal agent loop, then sends the final reply
// back through the IM gateway.
func (h *Handler) ProcessIMEvent(ctx context.Context, ev im.IMEvent) error {
	if h == nil || h.agent == nil || h.store == nil {
		return nil
	}
	cfg := h.getCfg()
	if cfg == nil {
		return fmt.Errorf("config not available")
	}
	imCfg := cfg.IM
	imCfg.Normalize()
	plan := im.PlanInbound(imCfg, ev)
	if !plan.Process {
		return nil
	}
	sessionID := plan.SessionID
	if sessionID == "" {
		return fmt.Errorf("im event missing session key")
	}
	title := imConversationTitle(ev)
	if err := h.store.EnsureConversation(sessionID, title); err != nil {
		return fmt.Errorf("ensure im conversation: %w", err)
	}
	h.ensureIMConversationTitle(sessionID, ev, title)
	if _, loaded := h.sessionLocks.LoadOrStore(sessionID, struct{}{}); loaded {
		return fmt.Errorf("a message is already being processed for this session")
	}
	defer h.sessionLocks.Delete(sessionID)

	text := strings.TrimSpace(ev.Text)
	if text == "" {
		return nil
	}
	// IM sessions do not pass through the web preflight, so hydrate any
	// interrupted plan before the agent constructs its guard prompt.
	h.hydrateSessionTodos(sessionID)
	meta := h.ensureMetaLoaded(sessionID)
	persona := plan.Persona
	provider, model := h.imProviderModel(persona)
	reqStyle := style.Style(h.sessionStyle(sessionID))
	if strings.TrimSpace(persona.Style) != "" {
		reqStyle = style.Style(strings.TrimSpace(persona.Style))
	}
	reqWorkMode := h.sessionWorkMode(sessionID)
	if persona.WorkMode != "" {
		reqWorkMode = persona.WorkMode.Normalize()
	}
	histMsgs, compSummary := h.loadHistoryForSend(ctx, sessionID, provider, model)
	msgs := buildLLMMessages(histMsgs)
	historyMessageCount := len(msgs)
	msgs = append(msgs, llm.ChatMessage{
		Role:        llm.RoleUser,
		Type:        llm.TypeText,
		Content:     text,
		MsgType:     llm.MsgTypeText,
		SubmitToLLM: 1,
	})

	req := agent.ChatRequest{
		Style:               reqStyle,
		WorkMode:            reqWorkMode,
		Provider:            provider,
		Model:               model,
		Messages:            msgs,
		HistoryMessageCount: historyMessageCount,
		CompressedSummary:   compSummary,
		SessionID:           sessionID,
		ProjectRoot:         h.sessionProjectPath(sessionID),
		ReasoningEffort:     meta.ReasoningEffort,
		PermissionLevel:     meta.PermissionLevel,
		KBBase:              meta.KnowledgeBase,
		AutoContinue:        h.sessionAutoContinue(sessionID),
		TodoLongRunMode:     h.sessionTodoLongRunMode(sessionID),
		TraceID:             ev.TraceID,
		UpstreamMessageID:   ev.ID,
		AllowedTools:        plan.AllowedTools,
	}
	if inject := strings.TrimSpace(persona.PromptInject); inject != "" {
		req.SkillContext = "## IM Persona\n\n" + inject
	}

	stream := h.agent.ChatStream(ctx, req)
	var finalText strings.Builder
	var lastErr string
	for chunk := range stream {
		if chunk.ContentRewrite != "" {
			finalText.Reset()
			finalText.WriteString(chunk.ContentRewrite)
		}
		if chunk.Content != "" {
			finalText.WriteString(chunk.Content)
		}
		if chunk.Error != "" {
			lastErr = chunk.Error
		}
		if !chunk.Done {
			continue
		}
		reply := strings.TrimSpace(finalText.String())
		if reply == "" {
			reply = strings.TrimSpace(lastErr)
		}
		if reply == "" {
			reply = "received"
		}
		if h.imGateway == nil {
			return nil
		}
		metadata := map[string]string{}
		if ev.ContextToken != "" {
			metadata["context_token"] = ev.ContextToken
		}
		return h.imGateway.DispatchOutbound(ctx, im.IMOutChunk{
			TraceID:  ev.TraceID,
			Platform: ev.Platform,
			Chat:     ev.Chat,
			Kind:     "text",
			Text:     reply,
			Done:     true,
			Metadata: metadata,
		})
	}
	return nil
}

func (h *Handler) imProviderModel(persona config.IMPersona) (string, string) {
	cfg := h.getCfg()
	if cfg == nil {
		return "", ""
	}
	provider := strings.TrimSpace(cfg.LLM.Default)
	model := h.defaultModelForProvider(provider)
	if override := strings.TrimSpace(persona.Model); override != "" {
		model = override
	}
	return provider, model
}
