package memory

import (
	"path/filepath"
	"testing"
)

// TestMigration_PurgeCorruptSummaryRanges verifies migration v10 — it
// deletes summaries rows whose numeric span exceeds the largest span a real
// compression batch can produce.
//
// Background: a batch compresses at most 100 consecutive message ids. Within
// one id scheme (small AUTOINCREMENT counters, or frontend-minted millisecond
// timestamps) 100 consecutive ids span at most ~10^8. A range whose
// `range_end - range_start + 1 > 10^12` can only come from a batch that
// straddled two schemes (small ids + timestamps), producing a start/end pair
// that does not describe any contiguous message block. Those rows are corrupt:
// the old Compress re-expanded them into an id→bool map, O(span), which blew
// the heap past 1GB on the affected device (2026-08 memory-spike incident).
//
// The migration must (a) remove the corrupt rows so LastCompressedIDFor never
// returns a quadrillion, and (b) leave every legitimate summary untouched.
func TestMigration_PurgeCorruptSummaryRanges(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenAt(filepath.Join(dir, "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	conv, err := s.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetCurrent(conv); err != nil {
		t.Fatal(err)
	}

	// Simulate a pre-v10 database: un-record v10 (and anything above) so the
	// runner re-applies only this migration.
	if _, err := s.db.Exec(`DELETE FROM schema_migrations WHERE version >= 10`); err != nil {
		t.Fatalf("downgrade schema_migrations: %v", err)
	}

	// Plant a corrupt cross-scheme range (span ~1.78e15) and a normal batch
	// range (span 100). Both belong to the same conversation so the corrupt
	// one would have been the killer.
	if _, err := s.db.Exec(
		`INSERT INTO summaries(conversation_id, range_start, range_end, summary, created_at) VALUES
		 (?, 1, 1785479327781664, '', 1),
		 (?, 20058, 20157, 'legit summary', 1)`,
		conv, conv,
	); err != nil {
		t.Fatalf("plant summaries: %v", err)
	}

	// Pre-check both rows exist.
	if n := countSummaries(t, s); n != 2 {
		t.Fatalf("pre-migration: want 2 summary rows, got %d", n)
	}

	// Re-run only v10.
	if err := s.migrateTo(10); err != nil {
		t.Fatalf("migrateTo(10): %v", err)
	}

	rows := querySummaries(t, s)
	if len(rows) != 1 {
		t.Fatalf("post-migration: want 1 surviving summary row, got %d", len(rows))
	}
	if rows[0].start != 20058 || rows[0].end != 20157 {
		t.Errorf("post-migration: legit summary damaged: got %d-%d, want 20058-20157", rows[0].start, rows[0].end)
	}
	if rows[0].summary != "legit summary" {
		t.Errorf("post-migration: legit summary text lost: %q", rows[0].summary)
	}
}

func countSummaries(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM summaries`).Scan(&n); err != nil {
		t.Fatalf("count summaries: %v", err)
	}
	return n
}

type summaryRow struct {
	start, end int64
	summary    string
}

func querySummaries(t *testing.T, s *Store) []summaryRow {
	t.Helper()
	rows, err := s.db.Query(`SELECT range_start, range_end, summary FROM summaries`)
	if err != nil {
		t.Fatalf("query summaries: %v", err)
	}
	defer rows.Close()
	var out []summaryRow
	for rows.Next() {
		var r summaryRow
		if err := rows.Scan(&r.start, &r.end, &r.summary); err != nil {
			t.Fatalf("scan summary: %v", err)
		}
		out = append(out, r)
	}
	return out
}
