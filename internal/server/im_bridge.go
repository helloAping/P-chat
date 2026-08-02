package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/p-chat/pchat/internal/agent"
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
	if h.getCfg() == nil {
		return fmt.Errorf("config not available")
	}
	sessionID := imConversationID(ev)
	if sessionID == "" {
		return fmt.Errorf("im event missing session key")
	}
	if err := h.store.EnsureConversation(sessionID, imConversationTitle(ev)); err != nil {
		return fmt.Errorf("ensure im conversation: %w", err)
	}
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
	provider := h.sessionProvider(sessionID)
	model := h.sessionModel(sessionID, provider)
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
		Style:               style.Style(h.sessionStyle(sessionID)),
		WorkMode:            h.sessionWorkMode(sessionID),
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

func imConversationID(ev im.IMEvent) string {
	chatID := strings.TrimSpace(ev.Chat.ChatID)
	if chatID == "" {
		chatID = strings.TrimSpace(ev.Sender.ID)
	}
	if chatID == "" || ev.Platform == "" {
		return ""
	}
	return fmt.Sprintf("im:%s:%s", ev.Platform, chatID)
}

func imConversationTitle(ev im.IMEvent) string {
	if name := strings.TrimSpace(ev.Sender.DisplayName); name != "" {
		return name
	}
	if ev.Chat.ChatID != "" {
		return ev.Chat.ChatID
	}
	return ev.Platform
}
