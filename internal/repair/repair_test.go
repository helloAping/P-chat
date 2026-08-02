package repair

import (
	"path/filepath"
	"testing"

	"github.com/p-chat/pchat/internal/memory"
)

// TestFixPurgesCorruptRanges verifies the shared Fix path: it backs up the
// DB, then opens the store (which runs migration v10) and clears every
// cross-scheme corrupt range while leaving legitimate summaries intact.
func TestFixPurgesCorruptRanges(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")
	st, err := memory.OpenAt(dbPath, 50)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetCurrent(conv); err != nil {
		t.Fatal(err)
	}
	// Corrupt cross-scheme range + a legitimate batch range.
	if err := st.SaveSummary(conv, 1, int64(1785479327781664), ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSummary(conv, 20058, 20157, "legit"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	count, maxSpan, err := Check(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("Check: corrupt count = %d, want 1", count)
	}
	if maxSpan <= CorruptRangeThreshold {
		t.Fatalf("Check: maxSpan = %d, want > threshold", maxSpan)
	}

	backupPath, err := Fix(dbPath)
	if err != nil {
		t.Fatalf("Fix: %v", err)
	}
	if backupPath == "" {
		t.Fatal("Fix: expected a backup path")
	}
	if _, err := filepath.Glob(backupPath + "*"); err != nil {
		t.Fatalf("backup missing: %v", err)
	}

	count, _, err = Check(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("after Fix: corrupt count = %d, want 0", count)
	}
}
