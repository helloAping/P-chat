package rotatelog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewWritesToDatedFile(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir, "server-debug", 7)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	day := time.Now().Format("2006-01-02")
	path := filepath.Join(dir, "server-debug-"+day+".log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("unexpected content %q", data)
	}
}

func TestCleanupRemovesExpired(t *testing.T) {
	dir := t.TempDir()
	// Create stale files outside the writer's knowledge so cleanup
	// must discover them on the first write. Dates are relative to
	// "today" (was hard-coded 2026-07-xx before, which flaked the day
	// the fixed date crossed the 7-day retention boundary):
	//   - 8 days ago is beyond the 7-day retention → must be removed;
	//   - 6 days ago is within it → must be kept.
	// Log filenames only carry the day, so an exactly-7-days-old file
	// races the boundary; 6/8 give the test a stable margin.
	expired := time.Now().AddDate(0, 0, -8).Format("2006-01-02")
	kept := time.Now().AddDate(0, 0, -6).Format("2006-01-02")
	for _, day := range []string{expired, kept} {
		path := filepath.Join(dir, "server-debug-"+day+".log")
		if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	w, err := New(dir, "server-debug", 7)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("x\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "server-debug-"+expired+".log")); !os.IsNotExist(err) {
		t.Errorf("expected %s removed, err=%v", expired, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "server-debug-"+kept+".log")); err != nil {
		t.Errorf("expected %s kept, err=%v", kept, err)
	}
}

func TestCleanupIgnoresOtherFiles(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, "pchat-gui-2026-07-01.log")
	if err := os.WriteFile(other, []byte("old"), 0o644); err != nil {
		t.Fatalf("write %s: %v", other, err)
	}
	w, err := New(dir, "server-debug", 7)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("x\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("unrelated file should be kept, err=%v", err)
	}
}

func TestRotateOnDateChange(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir, "server-debug", 7)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	prev := time.Now().Add(-24 * time.Hour).Format("2006-01-02")
	today := time.Now().Format("2006-01-02")
	if prev == today {
		t.Skip("test window straddles midnight; skip")
	}
	// Pre-create yesterday's file, then make the writer believe it is
	// still on yesterday so the next Write triggers a rotation.
	prevPath := filepath.Join(dir, "server-debug-"+prev+".log")
	if err := os.WriteFile(prevPath, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", prevPath, err)
	}
	w.mu.Lock()
	w.day = prev
	w.mu.Unlock()

	if _, err := w.Write([]byte("next\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(prevPath); err != nil {
		t.Errorf("yesterday file should remain, err=%v", err)
	}
	todayPath := filepath.Join(dir, "server-debug-"+today+".log")
	data, err := os.ReadFile(todayPath)
	if err != nil {
		t.Fatalf("expected today file %s: %v", todayPath, err)
	}
	if string(data) != "next\n" {
		t.Errorf("unexpected content %q", data)
	}
}

func TestNewErrorsOnBadDir(t *testing.T) {
	// A file where the directory should be → MkdirAll fails.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	if _, err := New(filepath.Join(blocker, "sub"), "server-debug", 7); err == nil {
		t.Fatal("expected error when directory cannot be created")
	}
}
