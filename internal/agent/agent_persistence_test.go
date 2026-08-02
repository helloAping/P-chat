package agent

import (
	"context"
	"testing"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
	"github.com/p-chat/pchat/internal/upgrade"
)

func TestChatWithTools_DoesNotRepersistHistory(t *testing.T) {
	cfg, _ := config.Load("")
	llmClient, _ := llm.NewClient(&cfg.LLM)
	store, _ := memory.OpenAt(":memory:", 50)
	defer store.Close()
	upgrade.SeedForTesting(store.DB())
	styleMgr, _ := style.NewManager(store.DB())

	sessionID, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	previous := llm.ChatMessage{Role: llm.RoleUser, Type: llm.TypeText, Content: "old", MsgType: llm.MsgTypeText, SubmitToLLM: 1}
	store.AddChatMessageTo(sessionID, previous)
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	agt := New(cfg, llmClient, styleMgr, store, tool.NewRegistry())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range agt.ChatWithTools(ctx, ChatRequest{
		Style:               style.Tech,
		Provider:            "none",
		SessionID:           sessionID,
		HistoryMessageCount: 1,
		Messages: []llm.ChatMessage{
			previous,
			{Role: llm.RoleUser, Type: llm.TypeText, Content: "continue", MsgType: llm.MsgTypeText, SubmitToLLM: 1},
		},
	}) {
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	if got := store.CountChatMessages(sessionID); got != 2 {
		t.Fatalf("message count after one send = %d, want 2 (old history + new user only)", got)
	}
}
