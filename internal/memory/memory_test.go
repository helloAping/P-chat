package memory

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/llm"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenAt(filepath.Join(dir, "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStore_AddAndGet(t *testing.T) {
	s := testStore(t)

	s.AddMessage(llm.Message{Role: "user", Content: "hi"})
	s.AddMessage(llm.Message{Role: "assistant", Content: "hello!"})
	_ = s.Flush()

	got := s.GetMessages()
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0].Role != "user" || got[0].Content != "hi" {
		t.Errorf("first msg wrong: %+v", got[0])
	}
	if got[1].Role != "assistant" || got[1].Content != "hello!" {
		t.Errorf("second msg wrong: %+v", got[1])
	}
}

func TestStore_MaxHistory(t *testing.T) {
	s := testStore(t)
	_ = s.Flush()

	// Add 60 messages; only the last 50 should remain (maxHistory=50).
	for i := 0; i < 60; i++ {
		s.AddMessage(llm.Message{Role: "user", Content: "msg"})
	}
	_ = s.Flush()

	got := s.GetMessages()
	if len(got) != 50 {
		t.Errorf("expected 50 (capped), got %d", len(got))
	}
}

func TestStore_MultipleConversations(t *testing.T) {
	s := testStore(t)

	convA := s.CurrentConversationID()
	if convA == "" {
		t.Fatal("expected an initial conversation")
	}
	s.AddMessage(llm.Message{Role: "user", Content: "A1"})

	convB, err := s.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	if convB == convA {
		t.Error("new conversation should have a different id")
	}
	s.AddMessage(llm.Message{Role: "user", Content: "B1"})

	_ = s.Flush()

	// Switch back to A and verify isolation.
	if err := s.SetCurrent(convA); err != nil {
		t.Fatal(err)
	}
	if got := s.GetMessages(); len(got) != 1 || got[0].Content != "A1" {
		t.Errorf("conv A should have only A1, got %+v", got)
	}

	// Switch to B.
	if err := s.SetCurrent(convB); err != nil {
		t.Fatal(err)
	}
	if got := s.GetMessages(); len(got) != 1 || got[0].Content != "B1" {
		t.Errorf("conv B should have only B1, got %+v", got)
	}
}

func TestStore_Rename(t *testing.T) {
	s := testStore(t)
	id := s.CurrentConversationID()

	if err := s.RenameConversation(id, "Project discussion"); err != nil {
		t.Fatal(err)
	}

	convs := s.ListConversations()
	if len(convs) != 1 {
		t.Fatalf("expected 1 conv, got %d", len(convs))
	}
	if convs[0].Title != "Project discussion" {
		t.Errorf("title not updated: %q", convs[0].Title)
	}
}

func TestStore_Delete(t *testing.T) {
	s := testStore(t)
	id := s.CurrentConversationID()

	if err := s.DeleteConversation(id); err != nil {
		t.Fatal(err)
	}
	// After deletion a new current conversation should be created.
	if s.CurrentConversationID() == "" {
		t.Error("expected new current conversation after delete")
	}
	if id == s.CurrentConversationID() {
		t.Error("current id should differ from deleted id")
	}
}

func TestStore_SetCurrent_NotFound(t *testing.T) {
	s := testStore(t)
	if err := s.SetCurrent("conv_nonexistent"); err == nil {
		t.Error("expected error for non-existent conversation")
	}
}

func TestStore_AddMessageWithMeta(t *testing.T) {
	s := testStore(t)
	_ = s.Flush()

	s.AddMessageWithMeta(llm.Message{
		Role:       "tool",
		Content:    "result",
		ToolCallID: "call_xyz",
	}, map[string]string{
		"tool_call_id": "call_xyz",
		"tool_name":    "read_file",
	})
	_ = s.Flush()

	got := s.GetMessages()
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if got[0].ToolCallID != "call_xyz" {
		t.Errorf("ToolCallID not restored: %q", got[0].ToolCallID)
	}
}

func TestStore_Summary(t *testing.T) {
	s := testStore(t)
	id := s.CurrentConversationID()
	_ = s.Flush()

	if err := s.SaveSummary(id, 1, 10, "summary text"); err != nil {
		t.Fatal(err)
	}
	summaries := s.GetSummaries(id)
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if summaries[0].Summary != "summary text" {
		t.Errorf("summary text wrong: %q", summaries[0].Summary)
	}
	if summaries[0].RangeStart != 1 || summaries[0].RangeEnd != 10 {
		t.Errorf("range wrong: %+v", summaries[0])
	}
}

func TestStore_ConcurrentAdd(t *testing.T) {
	s := testStore(t)
	const N = 50
	done := make(chan struct{}, N)
	for i := 0; i < N; i++ {
		go func(i int) {
			s.AddMessage(llm.Message{Role: "user", Content: "concurrent"})
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < N; i++ {
		<-done
	}
	_ = s.Flush()
	// All writes should be persisted without panic; exact count is
	// best-effort (cap at maxHistory=50).
	got := s.GetMessages()
	if len(got) == 0 {
		t.Error("expected at least some messages")
	}
}

func TestStore_ListConversations_Order(t *testing.T) {
	s := testStore(t)

	// testStore creates an initial conv; create 3 more.
	for i := 0; i < 3; i++ {
		_, _ = s.NewConversation()
	}

	convs := s.ListConversations()
	if len(convs) != 4 {
		t.Fatalf("expected 4 (1 initial + 3 new), got %d", len(convs))
	}
	// Most recently updated first.
	for i := 0; i < len(convs)-1; i++ {
		if convs[i].UpdatedAt.Before(convs[i+1].UpdatedAt) {
			t.Errorf("not sorted desc: %v > %v", convs[i].UpdatedAt, convs[i+1].UpdatedAt)
		}
	}
}

// TestStore_ListConversations_SameSecondStable reproduces the bug
// where two conversations created in the same second had an
// unstable order in ListConversations (ORDER BY updated_at DESC
// ties on timestamp). After the fix (id DESC as tie-breaker),
// the order must be deterministic and reflect the actual
// creation sequence.
func TestStore_ListConversations_SameSecondStable(t *testing.T) {
	s := testStore(t)
	// testStore created an initial conv; create 3 more in the
	// same second. The id-encoding (UnixNano + atomic counter)
	// gives each a strictly larger id within the same nanosecond.
	ids := make([]string, 3)
	for i := 0; i < 3; i++ {
		id, err := s.NewConversation()
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	_ = s.Flush()

	convs := s.ListConversations()
	if len(convs) != 4 {
		t.Fatalf("expected 4 (1 initial + 3 new), got %d", len(convs))
	}

	// Run the query 10 times; result must be identical every time.
	first := idsInOrder(convs)
	for trial := 0; trial < 10; trial++ {
		got := idsInOrder(s.ListConversations())
		if !equalIDs(got, first) {
			t.Errorf("order changed between calls:\n  first: %v\n  got:   %v", first, got)
		}
	}

	// Expected order: most recent first, i.e. ids[2], ids[1], ids[0],
	// then the initial conv.
	want := []string{ids[2], ids[1], ids[0], first[3]}
	if !equalIDs(first, want) {
		t.Errorf("unexpected order:\n  want: %v\n  got:  %v", want, first)
	}
}

func TestStore_MostRecent_SameSecond(t *testing.T) {
	s := testStore(t)

	ids := make([]string, 3)
	for i := 0; i < 3; i++ {
		id, _ := s.NewConversation()
		ids[i] = id
	}
	_ = s.Flush()

	// The "most recent" should be the last created, not a random
	// one of the same-second entries.
	for trial := 0; trial < 20; trial++ {
		got, err := s.mostRecentConversation()
		if err != nil {
			t.Fatal(err)
		}
		if got != ids[2] {
			t.Fatalf("trial %d: mostRecent = %q, want %q (last created)", trial, got, ids[2])
		}
	}
}

func idsInOrder(convs []Conversation) []string {
	out := make([]string, len(convs))
	for i, c := range convs {
		out[i] = c.ID
	}
	return out
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestStore_ConversationMessageCount(t *testing.T) {
	s := testStore(t)
	_ = s.Flush()
	if got := s.ConversationMessageCount(); got != 0 {
		t.Errorf("expected 0, got %d", got)
	}
	s.AddMessage(llm.Message{Role: "user", Content: "x"})
	s.AddMessage(llm.Message{Role: "user", Content: "y"})
	_ = s.Flush()
	if got := s.ConversationMessageCount(); got != 2 {
		t.Errorf("expected 2, got %d", got)
	}
	// In another conversation, count is 0.
	id, _ := s.NewConversation()
	_ = s.SetCurrent(id)
	if got := s.ConversationMessageCount(); got != 0 {
		t.Errorf("new conv should be empty, got %d", got)
	}
}

func TestOpenAt_InvalidPath(t *testing.T) {
	// OpenAt to a directory that doesn't exist should auto-create the
	// parent. Use a nested path.
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "test.db")
	s, err := OpenAt(path, 50)
	if err != nil {
		t.Fatalf("OpenAt should create parent dirs: %v", err)
	}
	s.Close()
}

// Tiny smoke test for the migration routine. The JSON file shape is
// already validated by the legacy store; we just ensure the function
// doesn't blow up.
func TestMigrateLegacy_NotExist(t *testing.T) {
	s := testStore(t)
	// Should be a no-op when no legacy file exists.
	if err := s.migrateLegacy(); err != nil {
		t.Errorf("migrateLegacy on empty store: %v", err)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncateStr("hello", 10); got != "hello" {
		t.Errorf("short string: %q", got)
	}
	if got := truncateStr("hello world this is long", 10); !strings.HasSuffix(got, "...") {
		t.Errorf("truncated should end with ..., got %q", got)
	}
}
