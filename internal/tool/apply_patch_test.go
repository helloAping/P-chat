package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchCodexUpdateMultiHunk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	original := "package main\n\nfunc A() {}\n\nfunc B() {}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	patch := "*** Begin Patch\n" +
		"*** Update File: " + path + "\n" +
		"@@\n" +
		" func A() {}\n" +
		" \n" +
		"-func B() {}\n" +
		"+func B() { /* changed */ }\n" +
		"*** End Patch\n"

	registry := NewRegistry()
	RegisterBuiltin(registry)
	_, handler, ok := registry.Lookup("apply_patch")
	if !ok {
		t.Fatal("apply_patch not registered")
	}
	args, _ := json.Marshal(map[string]any{"patch": patch})
	result, err := handler(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("apply_patch failed: %s", result.Content)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "func B() { /* changed */ }") {
		t.Fatalf("content not updated: %q", got)
	}
	if len(result.ChangedPaths) != 1 {
		t.Fatalf("ChangedPaths = %#v", result.ChangedPaths)
	}
}

func TestApplyPatchStructuredAddAndUpdate(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(existing, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(dir, "b.txt")

	registry := NewRegistry()
	RegisterBuiltin(registry)
	_, handler, ok := registry.Lookup("apply_patch")
	if !ok {
		t.Fatal("apply_patch not registered")
	}
	args, _ := json.Marshal(map[string]any{
		"operations": []map[string]any{
			{
				"op":   "update",
				"path": existing,
				"hunks": []map[string]any{
					{"old_text": "hello world\n", "new_text": "hello p-chat\n"},
				},
			},
			{
				"op":      "add",
				"path":    created,
				"content": "new file\n",
			},
		},
	})
	result, err := handler(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("failed: %s", result.Content)
	}
	a, _ := os.ReadFile(existing)
	b, _ := os.ReadFile(created)
	if string(a) != "hello p-chat\n" {
		t.Fatalf("a.txt = %q", a)
	}
	if string(b) != "new file\n" {
		t.Fatalf("b.txt = %q", b)
	}
}

func TestApplyPatchRejectsAmbiguousHunk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dup.txt")
	if err := os.WriteFile(path, []byte("x\nx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	RegisterBuiltin(registry)
	_, handler, _ := registry.Lookup("apply_patch")
	args, _ := json.Marshal(map[string]any{
		"operations": []map[string]any{
			{
				"op":   "update",
				"path": path,
				"hunks": []map[string]any{
					{"old_text": "x\n", "new_text": "y\n"},
				},
			},
		},
	})
	result, err := handler(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Content, "matched 2 times") {
		t.Fatalf("want ambiguous error, got %#v", result)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "x\nx\n" {
		t.Fatalf("file was modified: %q", got)
	}
}

func TestApplyPatchDryRunDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dry.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	RegisterBuiltin(registry)
	_, handler, _ := registry.Lookup("apply_patch")
	args, _ := json.Marshal(map[string]any{
		"dry_run": true,
		"operations": []map[string]any{
			{
				"op":   "update",
				"path": path,
				"hunks": []map[string]any{
					{"old_text": "before\n", "new_text": "after\n"},
				},
			},
		},
	})
	result, err := handler(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Content, "dry-run") {
		t.Fatalf("result = %#v", result)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "before\n" {
		t.Fatalf("dry-run wrote file: %q", got)
	}
}

func TestApplyPatchDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gone.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	RegisterBuiltin(registry)
	_, handler, _ := registry.Lookup("apply_patch")
	args, _ := json.Marshal(map[string]any{
		"patch": "*** Begin Patch\n*** Delete File: " + path + "\n*** End Patch\n",
	})
	result, err := handler(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("delete failed: %s", result.Content)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still exists: %v", err)
	}
}

func TestPathsFromApplyPatchArgs(t *testing.T) {
	args, _ := json.Marshal(map[string]any{
		"operations": []map[string]any{
			{"op": "update", "path": "a.go", "hunks": []map[string]any{{"old_text": "a", "new_text": "b"}}},
			{"op": "add", "path": "b.go", "content": "x"},
		},
	})
	paths := PathsFromApplyPatchArgs(string(args))
	if len(paths) != 2 {
		t.Fatalf("paths = %#v", paths)
	}
}
