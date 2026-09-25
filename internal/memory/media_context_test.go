package memory

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/llm"
)

func TestMigration_MediaContexts_Schema(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	if !hasTable(s.db, "media_contexts") {
		t.Fatal("media_contexts table missing")
	}
	assertColumnExists(t, s, "media_contexts", "session_id")
	assertColumnExists(t, s, "media_contexts", "input_refs_json")
	assertColumnExists(t, s, "media_contexts", "output_refs_json")
	assertColumnExists(t, s, "media_contexts", "context_refs_json")
	assertColumnExists(t, s, "media_contexts", "regen_group_id")
	assertColumnExists(t, s, "media_contexts", "archive_reason")
}

func TestMigration_MediaContexts_Rollback(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	if err := s.Rollback(12); err != nil {
		t.Fatalf("Rollback(12): %v", err)
	}
	if hasTable(s.db, "media_contexts") {
		t.Fatal("media_contexts table still exists after rollback")
	}
	cur, _, err := s.AppliedMigrations()
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	if cur != 12 {
		t.Fatalf("current migration = %d, want 12", cur)
	}
}

func TestMediaContext_CRUDAndRecentFilter(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()
	sessionID, err := s.NewConversation()
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}

	firstID, err := s.AddMediaContext(MediaContext{
		SessionID:  sessionID,
		Kind:       MediaContextKindRecognition,
		ToolName:   "media_recognize",
		InputRefs:  []string{"upl_1"},
		Prompt:     "read first column",
		ResultText: "A\n1\n2",
		Summary:    "recognized first column",
		CreatedAt:  time.Unix(10, 0),
	})
	if err != nil {
		t.Fatalf("AddMediaContext first: %v", err)
	}
	_, err = s.AddMediaContext(MediaContext{
		SessionID:  sessionID,
		Kind:       MediaContextKindGeneration,
		ToolName:   "generate_image",
		InputRefs:  []string{"upl_1"},
		OutputRefs: []string{"asset_1"},
		Prompt:     "make a chart",
		Summary:    "generated image",
		Archived:   true,
		CreatedAt:  time.Unix(20, 0),
	})
	if err != nil {
		t.Fatalf("AddMediaContext archived: %v", err)
	}

	contexts, err := s.RecentMediaContexts(sessionID, 10)
	if err != nil {
		t.Fatalf("RecentMediaContexts: %v", err)
	}
	if len(contexts) != 1 {
		t.Fatalf("contexts len = %d, want 1: %+v", len(contexts), contexts)
	}
	got := contexts[0]
	if got.ID != firstID || got.InputRefs[0] != "upl_1" || got.ResultText != "A\n1\n2" {
		t.Fatalf("unexpected context: %+v", got)
	}
}

func TestArchiveSiblings_ArchivesMediaContextsForGroup(t *testing.T) {
	s := newTestStore(t)
	convID, _ := s.NewConversation()
	insertTestAssistantWithGroupID(t, s, convID, "user", "u1", "")
	insertTestAssistantWithGroupID(t, s, convID, "assistant", "v1", "1")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMediaContext(MediaContext{
		SessionID:    convID,
		Kind:         MediaContextKindRecognition,
		ToolName:     "media_recognize",
		InputRefs:    []string{"upl_1"},
		ResultText:   "old OCR",
		RegenGroupID: "1",
		CreatedAt:    time.Unix(10, 0),
	}); err != nil {
		t.Fatalf("AddMediaContext: %v", err)
	}

	if _, err := s.ArchiveSiblings(convID, "1", 0); err != nil {
		t.Fatalf("ArchiveSiblings: %v", err)
	}
	contexts, err := s.RecentMediaContexts(convID, 10)
	if err != nil {
		t.Fatalf("RecentMediaContexts: %v", err)
	}
	if len(contexts) != 0 {
		t.Fatalf("archived regen media contexts should be hidden: %+v", contexts)
	}
}

func TestResolveMediaContextGraph_ExpandsParents(t *testing.T) {
	s := newTestStore(t)
	convID, _ := s.NewConversation()
	if _, err := s.AddMediaContext(MediaContext{
		ID:         "mctx_parent",
		SessionID:  convID,
		Kind:       MediaContextKindRecognition,
		ToolName:   "media_recognize",
		ResultText: "first read",
		CreatedAt:  time.Unix(10, 0),
	}); err != nil {
		t.Fatalf("add parent: %v", err)
	}
	if _, err := s.AddMediaContext(MediaContext{
		ID:          "mctx_child",
		SessionID:   convID,
		Kind:        MediaContextKindRecognition,
		ToolName:    "media_recognize",
		ContextRefs: []string{"mctx_parent"},
		ResultText:  "second read",
		CreatedAt:   time.Unix(20, 0),
	}); err != nil {
		t.Fatalf("add child: %v", err)
	}

	graph, err := s.ResolveMediaContextGraph(convID, []string{"mctx_child"}, 3, 4)
	if err != nil {
		t.Fatalf("ResolveMediaContextGraph: %v", err)
	}
	if len(graph) != 2 || graph[0].ID != "mctx_parent" || graph[1].ID != "mctx_child" {
		t.Fatalf("graph = %+v, want parent before child", graph)
	}
}

func TestRollbackArchivesAndRestoreUnarchivesMediaContexts(t *testing.T) {
	s := newTestStore(t)
	convID, _ := s.NewConversation()
	s.AddChatMessageToWithID(convID, llm.ChatMessage{Role: llm.RoleUser, Type: llm.TypeText, Content: "read image", MsgType: llm.MsgTypeText, SubmitToLLM: 1}, 10)
	s.AddChatMessageToWithID(convID, llm.ChatMessage{Role: llm.RoleAssistant, Type: llm.TypeText, Content: "done", MsgType: llm.MsgTypeText, SubmitToLLM: 1}, 20)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMediaContext(MediaContext{
		ID:           "mctx_rollback",
		SessionID:    convID,
		Kind:         MediaContextKindRecognition,
		ToolName:     "media_recognize",
		ResultText:   "ocr",
		MessageID:    20,
		RegenGroupID: "10",
	}); err != nil {
		t.Fatalf("AddMediaContext: %v", err)
	}

	deleted, err := s.DeleteMessagesFrom(convID, 20)
	if err != nil {
		t.Fatalf("DeleteMessagesFrom: %v", err)
	}
	contexts, err := s.RecentMediaContexts(convID, 10)
	if err != nil {
		t.Fatalf("RecentMediaContexts after delete: %v", err)
	}
	if len(contexts) != 0 {
		t.Fatalf("rollback context should be hidden: %+v", contexts)
	}

	if err := s.RestoreMessages(deleted); err != nil {
		t.Fatalf("RestoreMessages: %v", err)
	}
	contexts, err = s.RecentMediaContexts(convID, 10)
	if err != nil {
		t.Fatalf("RecentMediaContexts after restore: %v", err)
	}
	if len(contexts) != 1 || contexts[0].ID != "mctx_rollback" || contexts[0].ArchiveReason != "" {
		t.Fatalf("restored contexts = %+v", contexts)
	}
}

func TestActivateSiblingSwitchesMediaContextsByMessageID(t *testing.T) {
	s := newTestStore(t)
	convID, _ := s.NewConversation()
	s.AddChatMessageToWithID(convID, llm.ChatMessage{Role: llm.RoleUser, Type: llm.TypeText, Content: "make media", MsgType: llm.MsgTypeText, SubmitToLLM: 1}, 10)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	oldID, err := s.AddChatMessageWithMetaToRegenNow(convID, llm.ChatMessage{Role: llm.RoleAssistant, Type: llm.TypeText, Content: "old", MsgType: llm.MsgTypeText, SubmitToLLM: 1}, nil, "10", false)
	if err != nil {
		t.Fatalf("add old assistant: %v", err)
	}
	newID, err := s.AddChatMessageWithMetaToRegenNow(convID, llm.ChatMessage{Role: llm.RoleAssistant, Type: llm.TypeText, Content: "new", MsgType: llm.MsgTypeText, SubmitToLLM: 1}, nil, "10", true)
	if err != nil {
		t.Fatalf("add new assistant: %v", err)
	}
	if _, err := s.AddMediaContext(MediaContext{ID: "mctx_old", SessionID: convID, Kind: MediaContextKindGeneration, ToolName: "generate_image", MessageID: oldID, RegenGroupID: "10"}); err != nil {
		t.Fatalf("add old context: %v", err)
	}
	if _, err := s.AddMediaContext(MediaContext{ID: "mctx_new", SessionID: convID, Kind: MediaContextKindGeneration, ToolName: "generate_image", MessageID: newID, RegenGroupID: "10", Archived: true, ArchiveReason: "regen"}); err != nil {
		t.Fatalf("add new context: %v", err)
	}

	if err := s.ActivateSibling(convID, "10", newID); err != nil {
		t.Fatalf("ActivateSibling(new): %v", err)
	}
	contexts, err := s.RecentMediaContexts(convID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(contexts) != 1 || contexts[0].ID != "mctx_new" {
		t.Fatalf("active new contexts = %+v", contexts)
	}
	if err := s.ActivateSibling(convID, "10", oldID); err != nil {
		t.Fatalf("ActivateSibling(old): %v", err)
	}
	contexts, err = s.RecentMediaContexts(convID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(contexts) != 1 || contexts[0].ID != "mctx_old" {
		t.Fatalf("active old contexts = %+v", contexts)
	}
}
