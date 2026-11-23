package tool

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestShellDeleteTargetsSkipsNonDeleteSegments(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"2.txt", "3.txt", "8.txt", "example.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("你好\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	command := `del /q 2.txt & del /q 3.txt & del /q 8.txt & echo ----- & dir /b *.txt`
	got := shellDeleteTargets(command, dir)
	want := map[string]bool{
		filepath.Clean(filepath.Join(dir, "2.txt")): true,
		filepath.Clean(filepath.Join(dir, "3.txt")): true,
		filepath.Clean(filepath.Join(dir, "8.txt")): true,
	}
	if len(got) != len(want) {
		t.Fatalf("targets = %v, want 2.txt 3.txt 8.txt", got)
	}
	for _, path := range got {
		if !want[path] {
			t.Fatalf("unexpected target %s", path)
		}
	}
}

func TestShellDeleteTargetsGlobAndQuotedName(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "keep.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	globs := shellDeleteTargets(`del /q *.txt`, dir)
	if len(globs) != 2 {
		t.Fatalf("glob targets = %v, want a.txt and b.txt", globs)
	}
	quoted := filepath.Join(dir, "my file.txt")
	if err := os.WriteFile(quoted, []byte("q\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	quotedTargets := shellDeleteTargets(`del /q "my file.txt"`, dir)
	if len(quotedTargets) != 1 || quotedTargets[0] != filepath.Clean(quoted) {
		t.Fatalf("quoted = %v", quotedTargets)
	}
	ps := shellDeleteTargets(`Remove-Item -Path 2.txt,3.txt -Force`, dir)
	if len(ps) != 2 {
		t.Fatalf("powershell targets = %v", ps)
	}
}

func TestSnapshotShellDeletesReplacesPreviousTurn(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "8.txt")
	other := filepath.Join(dir, "2.txt")
	kept := filepath.Join(dir, "example.txt")
	for _, path := range []string{old, other, kept} {
		if err := os.WriteFile(path, []byte("你好\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	j := NewFileJournal()
	const sid = "s"
	j.BeginTurn(sid)
	j.RecordBeforeMutation(sid, "write_file", old)
	j.BeginTurn(sid)
	ctx := WithFileJournal(WithSessionID(context.Background(), sid), j)

	command := `del /q 2.txt & del /q 8.txt & echo ----- & dir /b *.txt`
	recorded := snapshotShellDeletes(ctx, command, dir)
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(other); err != nil {
		t.Fatal(err)
	}
	forgetUnchangedShellDeletes(ctx, recorded)

	peek := j.PeekLastTurn(sid)
	if peek.Count != 2 {
		t.Fatalf("count = %d, entries = %#v", peek.Count, peek.Entries)
	}
	got := map[string]int{}
	for _, entry := range peek.Entries {
		got[filepath.Base(entry.Path)] = entry.Deleted
		if entry.Added != 0 {
			t.Fatalf("%s added = %d, want 0", entry.Path, entry.Added)
		}
	}
	if got["2.txt"] != 1 || got["8.txt"] != 1 {
		t.Fatalf("deleted lines = %#v, want both -1", got)
	}
	if _, ok := got["example.txt"]; ok {
		t.Fatal("example.txt was not deleted")
	}
}
