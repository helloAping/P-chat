package memory

import (
	"path/filepath"
	"testing"
)

// TestMigration_AddMessageSeq_Schema verifies migration v8's
// schema effect: after OpenAt runs all migrations, the
// messages table has a `seq` column and the
// idx_messages_conv_seq index exists. The data-side
// verification (per-conversation backfill counter, insert-time
// seq assignment) lives in the C4 commits' tests.
func TestMigration_AddMessageSeq_Schema(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenAt(filepath.Join(dir, "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	// The seq column must exist on the messages table.
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name='seq'`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected exactly one seq column on messages, got %d", n)
	}

	// The idx_messages_conv_seq index must exist. SQLite
	// exposes indexes as 'index' rows in sqlite_master.
	var idxName string
	_ = s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='messages' AND name='idx_messages_conv_seq'`,
	).Scan(&idxName)
	if idxName != "idx_messages_conv_seq" {
		t.Errorf("idx_messages_conv_seq not found in sqlite_master; "+
			"the seq-based pagination cursor would table-scan")
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
