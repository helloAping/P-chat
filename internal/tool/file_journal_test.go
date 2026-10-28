package tool

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileJournalUndoCreateUpdateDelete(t *testing.T) {
	dir := t.TempDir()
	j := NewFileJournal()
	sid := "s1"
	j.BeginTurn(sid)

	created := filepath.Join(dir, "new.txt")
	updated := filepath.Join(dir, "old.txt")
	if err := os.WriteFile(updated, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := WithFileJournal(WithSessionID(context.Background(), sid), j)
	RecordBeforeMutationFromCtx(ctx, "write_file", created)
	if err := writeFile(created, []byte("brand new\n")); err != nil {
		t.Fatal(err)
	}
	RecordBeforeMutationFromCtx(ctx, "edit_file", updated)
	if err := writeFile(updated, []byte("v2\n")); err != nil {
		t.Fatal(err)
	}

	peek := j.PeekLastTurn(sid)
	if peek.Count != 2 {
		t.Fatalf("peek count = %d, want 2", peek.Count)
	}

	summary, err := j.UndoLastTurn(sid)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Count != 2 {
		t.Fatalf("undo count = %d", summary.Count)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("created file should be removed after undo: %v", err)
	}
	got, _ := os.ReadFile(updated)
	if string(got) != "v1\n" {
		t.Fatalf("updated file = %q, want v1", got)
	}
	if j.PeekLastTurn(sid).Count != 0 {
		t.Fatal("journal should be empty after undo")
	}
}

func TestFileJournalPeekSkipsEmptyTurns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	j := NewFileJournal()
	j.BeginTurn("s")
	j.RecordBeforeMutation("s", "write_file", path)
	_ = writeFile(path, []byte("v2"))
	j.BeginTurn("s") // empty follow-up turn
	peek := j.PeekLastTurn("s")
	if peek.Count != 1 {
		t.Fatalf("peek count = %d, want 1 (skip empty turn)", peek.Count)
	}
	if peek.Entries[0].Path != path {
		t.Fatalf("peek path = %q", peek.Entries[0].Path)
	}
}

func TestFileJournalKeepsFirstSnapshotOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	j := NewFileJournal()
	j.BeginTurn("s")
	j.RecordBeforeMutation("s", "write_file", path)
	_ = writeFile(path, []byte("two"))
	j.RecordBeforeMutation("s", "write_file", path) // second write same turn
	_ = writeFile(path, []byte("three"))
	_, err := j.UndoLastTurn("s")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "one" {
		t.Fatalf("want original one, got %q", got)
	}
}
