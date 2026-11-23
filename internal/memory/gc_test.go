package memory

import (
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/llm"
)

func TestStorageGC_ExternalizesImageAndKeepsRecentThinking(t *testing.T) {
	s := testStore(t)
	cid := s.CurrentConversationID()
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	longTool := strings.Repeat("abcdefghijklmnopqrstuvwxyz", 80)
	parts := `[{"kind":"thinking","text":"secret chain of thought that should leave the old turn"},{"kind":"text","text":"最终回复保留"},{"kind":"tool","name":"read_file","result":"` + longTool + `"}]`
	s.AddChatMessageWithMetaTo(cid, llm.ChatMessage{
		Role: llm.RoleAssistant, Type: llm.TypeText, Content: "最终回复保留",
		MsgType: llm.MsgTypeText, SubmitToLLM: 1,
	}, map[string]string{"parts": parts})
	s.AddChatMessageTo(cid, llm.ChatMessage{
		Role: llm.RoleUser, Type: llm.TypeText, Content: png,
		MsgType: llm.MsgTypeText, SubmitToLLM: 1,
	})
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}

	var calls int
	store := func(convID, mime string, data []byte) (string, error) {
		calls++
		if convID != cid || mime != "image/png" || len(data) < 32 {
			t.Fatalf("sink conv=%s mime=%s bytes=%d", convID, mime, len(data))
		}
		return "abcdef0123456789", nil
	}
	report, err := s.StorageGC(StorageGCOptions{
		Apply: true, InlineImages: true, HistoricalPayload: true, KeepRecent: 1,
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("image writes = %d, want 1", calls)
	}
	if report.TotalBytes <= 0 {
		t.Fatalf("saved bytes = %d", report.TotalBytes)
	}

	var assistantMeta, imageContent string
	var imageType int
	if err := s.db.QueryRow(
		`SELECT metadata FROM messages WHERE conversation_id = ? AND role = 'assistant'`, cid,
	).Scan(&assistantMeta); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(
		`SELECT content, msg_type FROM messages WHERE conversation_id = ? AND role = 'user'`, cid,
	).Scan(&imageContent, &imageType); err != nil {
		t.Fatal(err)
	}
	if imageContent != "upl://abcdef0123456789" || imageType != llm.MsgTypeImage {
		t.Fatalf("image row = type %d content %q", imageType, imageContent)
	}
	if strings.Contains(assistantMeta, "secret chain of thought") {
		t.Fatalf("thinking survived: %s", assistantMeta)
	}
	if !strings.Contains(assistantMeta, "最终回复保留") {
		t.Fatalf("answer dropped: %s", assistantMeta)
	}
	if !strings.Contains(assistantMeta, "历史工具输出已回收") {
		t.Fatalf("tool output was not trimmed: %s", assistantMeta)
	}
}

func TestStorageGC_DeletesOldBlankConversationOnly(t *testing.T) {
	s := testStore(t)
	live := s.CurrentConversationID()
	oldID, err := s.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		`UPDATE conversations SET title = ?, updated_at = ? WHERE id = ?`,
		"标签给我列10个左右", time.Now().Add(-48*time.Hour).Unix(), oldID,
	); err != nil {
		t.Fatal(err)
	}
	s.AddChatMessageTo(live, llm.ChatMessage{
		Role: llm.RoleUser, Type: llm.TypeText, Content: "还在",
		MsgType: llm.MsgTypeText, SubmitToLLM: 1,
	})
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO summaries(conversation_id, range_start, range_end, summary, created_at) VALUES (?, ?, ?, ?, ?)`,
		oldID, 1, 9, "孤儿摘要", time.Now().Unix(),
	); err != nil {
		t.Fatal(err)
	}

	report, err := s.StorageGC(StorageGCOptions{
		Apply: true, BlankConversations: true, StaleSummaries: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var blanks, summaries int
	for _, item := range report.Items {
		if item.ID == "blank_conversations" {
			blanks = item.Count
		}
		if item.ID == "stale_summaries" {
			summaries = item.Count
		}
	}
	if blanks != 1 || summaries != 1 {
		t.Fatalf("blanks=%d summaries=%d", blanks, summaries)
	}
	if _, err := s.GetConversation(oldID); err == nil {
		t.Fatal("old blank conversation still exists")
	}
	if _, err := s.GetConversation(live); err != nil {
		t.Fatal(err)
	}
	if got := s.GetChatMessagesFor(live, 10); len(got) != 1 || got[0].Content != "还在" {
		t.Fatalf("live messages = %+v", got)
	}
}
