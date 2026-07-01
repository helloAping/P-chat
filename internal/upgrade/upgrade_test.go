package upgrade

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestRun_CurrentVersion(t *testing.T) {
	// Simulate a fresh install at Current version: V0→V3 all at once.
	orig := versionFilePath()

	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	os.MkdirAll(filepath.Join(dir, ".p-chat"), 0o755)

	db, err := sql.Open("sqlite", filepath.Join(dir, ".p-chat", "test.db")+
		"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Remove existing version file from temp dir.
	_ = os.Remove(versionFilePath())

	if err := Run(db); err != nil {
		t.Fatalf("Run from V0: %v", err)
	}

	// Verify version file was written.
	if v := readUserVersion(); v != Current {
		t.Errorf("expected version %d, got %d", Current, v)
	}

	// Verify styles table has built-in rows.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM styles WHERE is_builtin=1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count < 3 {
		t.Errorf("expected >=3 built-in styles, got %d", count)
	}

	// Re-run should be a no-op.
	if err := Run(db); err != nil {
		t.Fatalf("Run again (should be no-op): %v", err)
	}

	// Restore original env if test modified a real env var.
	_ = orig
}

func TestRun_Idempotent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	os.MkdirAll(filepath.Join(dir, ".p-chat"), 0o755)

	db, err := sql.Open("sqlite", filepath.Join(dir, ".p-chat", "test.db")+
		"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_ = os.Remove(versionFilePath())

	// First run.
	if err := Run(db); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	// Second run: should be idempotent.
	if err := Run(db); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	// Third run.
	if err := Run(db); err != nil {
		t.Fatalf("third Run: %v", err)
	}
}

func TestUserVersion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	os.MkdirAll(filepath.Join(dir, ".p-chat"), 0o755)
	_ = os.Remove(versionFilePath())

	// V0 when no file.
	if v := UserVersion(); v != V0 {
		t.Errorf("expected V0, got %d", v)
	}
}
