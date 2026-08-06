package agent

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
	"github.com/p-chat/pchat/internal/upgrade"
)

// TestChatWithTools_UploadImagePersistsReference verifies the
// upload-id image path end to end: the attachment is expanded
// with base64 (the LLM request payload), but the persisted row
// stores "upl://<id>" instead — the database holds a reference,
// not the bytes.
func TestChatWithTools_UploadImagePersistsReference(t *testing.T) {
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

	// Write the upload file the way POST /uploads does.
	uploadsDir := t.TempDir()
	id := "abcd1234567890ab"
	raw := []byte("fake-png-bytes")
	if err := os.WriteFile(filepath.Join(uploadsDir, id+"-test.png"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	agt := New(cfg, llmClient, styleMgr, store, tool.NewRegistry())
	agt.SetAttachmentResolver(&DiskAttachmentResolver{BaseDir: uploadsDir})
	// A default-loaded config has no "none" provider; point the
	// request at a model the vision heuristic accepts so the
	// image survives expansion instead of degrading to the
	// "model does not support vision" marker.
	agt.cfg.LLM.Providers = append(agt.cfg.LLM.Providers, config.ProviderConfig{
		Name:     "none",
		Protocol: "openai",
		Models:   []config.ModelConfig{{Name: "gpt-4o"}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range agt.ChatWithTools(ctx, ChatRequest{
		Style:     style.Tech,
		Provider:  "none",
		Model:     "gpt-4o",
		SessionID: sessionID,
		Messages: []llm.ChatMessage{
			{Role: llm.RoleUser, Type: llm.TypeText, Content: "what is in this image", MsgType: llm.MsgTypeText, SubmitToLLM: 1},
		},
		Attachments: []Attachment{
			{UploadID: id, Name: "test.png", Kind: "image", MIME: "image/png"},
		},
	}) {
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	rows := store.GetChatMessagesFor(sessionID, 0)
	var img *llm.ChatMessage
	for i := range rows {
		if rows[i].MsgType == llm.MsgTypeImage {
			img = &rows[i]
			break
		}
	}
	if img == nil {
		t.Fatal("no image row persisted")
	}
	if img.Content != "upl://"+id {
		t.Errorf("persisted content = %q, want %q", img.Content, "upl://"+id)
	}
	if got := base64.StdEncoding.EncodeToString(raw); img.Content == got {
		t.Errorf("persisted content must not be the base64 payload")
	}
}

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
