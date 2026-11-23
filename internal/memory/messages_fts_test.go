package memory

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/llm"
)

func TestBuildMessagesFTSQuery(t *testing.T) {
	if got := buildMessagesFTSQuery("你好"); got != "你 AND 好" {
		t.Fatalf("cjk = %q, want %q", got, "你 AND 好")
	}
	if got := buildMessagesFTSQuery("hello"); got != "hello*" {
		t.Fatalf("ascii = %q", got)
	}
	if got := buildMessagesFTSQuery("hello world"); got != "hello* AND world*" {
		t.Fatalf("multi = %q", got)
	}
	if got := buildMessagesFTSQuery("100%"); !strings.Contains(got, "100%") {
		t.Fatalf("percent literal = %q", got)
	}
}

func TestMessagesFTSMigrationAndTriggers(t *testing.T) {
	s := testStore(t)
	if !hasMessagesFTS(s.db) {
		t.Fatal("expected messages_fts after OpenAt migrate")
	}
	var ver int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver < 14 {
		t.Fatalf("schema version = %d, want >= 14", ver)
	}

	id := s.CurrentConversationID()
	s.AddChatMessageTo(id, llm.ChatMessage{Role: llm.RoleUser, Content: "unique_fts_token_alpha"})
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	got := s.SearchMessages("unique_fts_token_alpha", 5)
	if len(got) != 1 {
		t.Fatalf("trigger-indexed search = %+v", got)
	}

	// EnsureMessagesFTS is idempotent and rebuild keeps existing rows searchable.
	if err := EnsureMessagesFTS(s.db); err != nil {
		t.Fatalf("EnsureMessagesFTS: %v", err)
	}
	got = s.SearchMessages("unique_fts_token_alpha", 5)
	if len(got) != 1 {
		t.Fatalf("after rebuild search = %+v", got)
	}
}

func TestSearchMessagesFTSChinese(t *testing.T) {
	s := testStore(t)
	id := s.CurrentConversationID()
	s.AddChatMessageTo(id, llm.ChatMessage{Role: llm.RoleUser, Content: "今天天气不错适合写代码"})
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	got := s.SearchMessages("天气", 5)
	if len(got) != 1 {
		t.Fatalf("cjk search = %+v", got)
	}
}

func TestMigrateTo14Idempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "m14.db")
	s, err := OpenAt(dbPath, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.migrateTo(14); err != nil {
		t.Fatalf("migrateTo(14): %v", err)
	}
	if err := s.migrateTo(14); err != nil {
		t.Fatalf("migrateTo(14) second: %v", err)
	}
}
