package memory

import (
	"path/filepath"
	"testing"

	"github.com/p-chat/pchat/internal/llm"
)

// TestMigration_AddMessageSeq_Backfill verifies migration v8's
// backfill behavior: existing rows in messages are stamped
// with seq in id-order, one counter per conversation.
//
// The backfill must run *after* the rows are present (the
// migration's `UPDATE messages SET seq = (SELECT COUNT(*)
// ...)` only re-stamps rows that already exist when v8 runs).
// In a real upgrade the rows are obviously already there.
// In the test we simulate that: bring the DB to v7, plant
// pre-existing rows, then run migrateTo(8) and check the
// backfill produced per-conversation counters starting at 1.
func TestMigration_AddMessageSeq_Backfill(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenAt(filepath.Join(dir, "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	// Roll forward to v7 only. We then need to physically
	// drop the seq column too (otherwise it's present but
	// not in the migration table — and our next INSERT
	// would fail trying to write to a non-existent column
	// since the INSERT statement doesn't include seq yet
	// at this point). SQLite's ALTER TABLE DROP COLUMN
	// errors out if an index references the column, so
	// drop the index first.
	//
	// The pattern: drop the migration record, drop the
	// index + column, plant the rows, then re-run
	// migrateTo(8) which will recreate both + backfill.
	// Subsequent inserts in this same store will go
	// through the seq-assigning AddMessage path (added
	// in commit C4), so this test only covers the
	// one-shot backfill behavior.
	if _, err := s.db.Exec(
		`DELETE FROM schema_migrations WHERE version >= 8`,
	); err != nil {
		t.Fatalf("downgrade schema_migrations: %v", err)
	}
	// Drop the seq index first (it references the seq
	// column we're about to drop), then the column.
	if _, err := s.db.Exec(
		`DROP INDEX IF EXISTS idx_messages_conv_seq`,
	); err != nil {
		t.Fatalf("drop seq index: %v", err)
	}
	if _, err := s.db.Exec(
		`ALTER TABLE messages DROP COLUMN seq`,
	); err != nil {
		t.Fatalf("drop seq column: %v", err)
	}

	// Plant pre-existing rows in two conversations.
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

	// Now run v8 — this should add the seq column AND
	// backfill the planted rows.
	if err := s.migrateTo(8); err != nil {
		t.Fatalf("migrateTo(8): %v", err)
	}

	// Verify the seqs. Per-conversation counter starting
	// at 1: convA → [1, 2, 3], convB → [1, 2]. Critically
	// convB does NOT continue from where A left off (4, 5)
	// — the counter is per-conversation, not global.
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
	if len(got[convA]) != len(wantA) {
		t.Fatalf("convA: want %d seqs, got %d (%v)", len(wantA), len(got[convA]), got[convA])
	}
	for i, v := range wantA {
		if got[convA][i] != v {
			t.Errorf("convA seq[%d] = %d, want %d (full: %v)",
				i, got[convA][i], v, got[convA])
		}
	}
	wantB := []int64{1, 2}
	if len(got[convB]) != len(wantB) {
		t.Fatalf("convB: want %d seqs, got %d (%v) — seqs must be per-conversation, not global",
			len(wantB), len(got[convB]), got[convB])
	}
	for i, v := range wantB {
		if got[convB][i] != v {
			t.Errorf("convB seq[%d] = %d, want %d (full: %v)",
				i, got[convB][i], v, got[convB])
		}
	}

	// Verify the index exists.
	var idxName string
	_ = s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='messages' AND name='idx_messages_conv_seq'`,
	).Scan(&idxName)
	if idxName != "idx_messages_conv_seq" {
		t.Errorf("idx_messages_conv_seq not found in sqlite_master; "+
			"the new pagination cursor (seq-based) would table-scan")
	}
}

// TestMigration_AddMessageSeq_Idempotent verifies that
// re-running migrateTo(8) after the migration is already
// applied is a no-op (the migration's ALTER TABLE would
// otherwise fail with "duplicate column").
func TestMigration_AddMessageSeq_Idempotent(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenAt(filepath.Join(dir, "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	// OpenAt already ran the migration. Re-run it.
	if err := s.migrateTo(8); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	// And again to be sure.
	if err := s.migrateTo(8); err != nil {
		t.Fatalf("re-migrate 2x: %v", err)
	}

	// The table must still have a single seq column.
	var n int
	_ = s.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name='seq'`,
	).Scan(&n)
	if n != 1 {
		t.Errorf("expected exactly one seq column after idempotent re-migrate, got %d", n)
	}
}
