package memory

import (
	"path/filepath"
	"testing"

	"github.com/p-chat/pchat/internal/llm"
)

// TestStore_AssignsSeqOnInsert verifies that Flush() stamps
// every newly-inserted message with the next per-conversation
// seq (1, 2, 3, ...) starting from MAX(seq)+1.
func TestStore_AssignsSeqOnInsert(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenAt(filepath.Join(dir, "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	convA, err := s.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetCurrent(convA)
	s.AddMessage(llm.Message{Role: "user", Content: "a1"})
	s.AddMessage(llm.Message{Role: "assistant", Content: "a2"})
	s.AddMessage(llm.Message{Role: "user", Content: "a3"})

	convB, err := s.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetCurrent(convB)
	s.AddMessage(llm.Message{Role: "user", Content: "b1"})
	s.AddMessage(llm.Message{Role: "assistant", Content: "b2"})

	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}

	// Per-conversation counter: convA → [1, 2, 3],
	// convB → [1, 2] (NOT [4, 5] — seq is per-conversation).
	rows, err := s.db.Query(
		`SELECT conversation_id, seq FROM messages ORDER BY id`,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string][]int64{}
	for rows.Next() {
		var cid string
		var seq int64
		if err := rows.Scan(&cid, &seq); err != nil {
			t.Fatal(err)
		}
		got[cid] = append(got[cid], seq)
	}
	wantA := []int64{1, 2, 3}
	if !sliceEq(got[convA], wantA) {
		t.Errorf("convA seqs = %v, want %v", got[convA], wantA)
	}
	wantB := []int64{1, 2}
	if !sliceEq(got[convB], wantB) {
		t.Errorf("convB seqs = %v, want %v (seq must be per-conversation, not global)", got[convB], wantB)
	}
}

// TestStore_SeqSurvivesInterleavedInserts verifies that
// concurrent-style interleaved writes (pendingWrites from
// multiple conversations) all get correct per-conversation
// seqs. The Flush() implementation groups by convID and
// does one MAX(seq) lookup per group.
func TestStore_SeqSurvivesInterleavedInserts(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenAt(filepath.Join(dir, "test.db"), 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	convA, _ := s.NewConversation()
	_ = s.SetCurrent(convA)
	s.AddMessage(llm.Message{Role: "user", Content: "a1"})

	convB, _ := s.NewConversation()
	_ = s.SetCurrent(convB)
	s.AddMessage(llm.Message{Role: "user", Content: "b1"})

	_ = s.SetCurrent(convA)
	s.AddMessage(llm.Message{Role: "user", Content: "a2"})

	_ = s.SetCurrent(convB)
	s.AddMessage(llm.Message{Role: "user", Content: "b2"})

	_ = s.SetCurrent(convA)
	s.AddMessage(llm.Message{Role: "user", Content: "a3"})

	// Single Flush handles all three conversations in one
	// batch. The per-conversation grouping must produce
	// [1, 2, 3] for A and [1, 2] for B regardless of the
	// interleaving order in pendingWrites.
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}

	rows, _ := s.db.Query(`SELECT conversation_id, seq FROM messages ORDER BY id`)
	defer rows.Close()
	got := map[string][]int64{}
	for rows.Next() {
		var cid string
		var seq int64
		_ = rows.Scan(&cid, &seq)
		got[cid] = append(got[cid], seq)
	}
	if !sliceEq(got[convA], []int64{1, 2, 3}) {
		t.Errorf("interleaved convA seqs = %v, want [1, 2, 3]", got[convA])
	}
	if !sliceEq(got[convB], []int64{1, 2}) {
		t.Errorf("interleaved convB seqs = %v, want [1, 2]", got[convB])
	}
}

// TestStore_SeqContinuesAfterFlush covers the case where
// a session has existing rows and a new insert continues
// from MAX(seq)+1 (not from 1).
func TestStore_SeqContinuesAfterFlush(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenAt(filepath.Join(dir, "test.db"), 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	conv, _ := s.NewConversation()
	_ = s.SetCurrent(conv)
	s.AddMessage(llm.Message{Role: "user", Content: "first"})
	s.AddMessage(llm.Message{Role: "user", Content: "second"})
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}

	// New inserts after a flush should continue from seq=3.
	s.AddMessage(llm.Message{Role: "user", Content: "third"})
	s.AddMessage(llm.Message{Role: "user", Content: "fourth"})
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}

	rows, _ := s.db.Query(`SELECT seq FROM messages WHERE conversation_id = ? ORDER BY id`, conv)
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var s int64
		_ = rows.Scan(&s)
		got = append(got, s)
	}
	if !sliceEq(got, []int64{1, 2, 3, 4}) {
		t.Errorf("seqs after two flushes = %v, want [1, 2, 3, 4]", got)
	}
}

func sliceEq(a, b []int64) bool {
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
