package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditFileReplacesExpectedTextAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(path, []byte("before\nkeep\nafter\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	registry := NewRegistry()
	RegisterBuiltin(registry)
	_, handler, ok := registry.Lookup("edit_file")
	if !ok {
		t.Fatal("edit_file is not registered")
	}
	args, _ := json.Marshal(map[string]any{
		"path":     path,
		"old_text": "keep\n",
		"new_text": "changed\n",
	})
	result, err := handler(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("edit_file failed: %s", result.Content)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "before\nchanged\nafter\n" {
		t.Fatalf("edited content = %q", got)
	}
	if !strings.Contains(result.Content, "edited") {
		t.Fatalf("result should describe the changed file: %q", result.Content)
	}
	if result.Summary == "" || result.NextAction != "verify" {
		t.Fatalf("structured edit result = %#v, want summary and verify next action", result)
	}
	if len(result.ChangedPaths) != 1 || result.ChangedPaths[0] != path {
		t.Fatalf("changed paths = %#v, want %q", result.ChangedPaths, path)
	}
}

func TestEditFileRejectsAmbiguousMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "duplicate.txt")
	if err := os.WriteFile(path, []byte("same\nsame\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	registry := NewRegistry()
	RegisterBuiltin(registry)
	_, handler, ok := registry.Lookup("edit_file")
	if !ok {
		t.Fatal("edit_file is not registered")
	}
	args, _ := json.Marshal(map[string]any{
		"path":     path,
		"old_text": "same",
		"new_text": "changed",
	})
	result, err := handler(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Content, "matched 2 times") {
		t.Fatalf("ambiguous edit result = %#v, want a match-count error", result)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "same\nsame\n" {
		t.Fatalf("ambiguous edit changed the file: %q", got)
	}
}

func TestEditFileDryRunDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dry-run.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}

	registry := NewRegistry()
	RegisterBuiltin(registry)
	_, handler, ok := registry.Lookup("edit_file")
	if !ok {
		t.Fatal("edit_file is not registered")
	}
	args, _ := json.Marshal(map[string]any{
		"path":     path,
		"old_text": "before",
		"new_text": "after",
		"dry_run":  true,
	})
	result, err := handler(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Content, "dry-run") {
		t.Fatalf("dry-run result = %#v", result)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "before" {
		t.Fatalf("dry-run changed the file: %q", got)
	}
}
