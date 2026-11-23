package cli

import (
	"strings"
	"testing"
	"time"
)

func TestToolResultCache_Basic(t *testing.T) {
	c := newToolResultCache(3)

	c.record("read_file", "call-1", "session", `{"path":"a.txt"}`, "hello", "", 100*time.Millisecond, false, 0)
	c.record("write_file", "call-2", "session", `{"path":"b.txt","content":"x"}`, "written 1 bytes", "", 50*time.Millisecond, false, 0)
	c.record("exec_command", "call-3", "session", `{"command":"ls"}`, "a.txt\nb.txt", "", 30*time.Millisecond, false, 0)

	if c.len() != 3 {
		t.Errorf("expected 3 results, got %d", c.len())
	}

	// Test lookup by displayed sequence number.
	r := c.get(1)
	if r == nil || r.tool != "read_file" {
		t.Errorf("get(1) returned wrong: %+v", r)
	}

	// Test last.
	last := c.last()
	if last == nil || last.tool != "exec_command" {
		t.Errorf("last() returned wrong: %+v", last)
	}

	// Test overflow: cache cap is 3, add one more.
	c.record("read_file", "call-4", "session", `{"path":"d.txt"}`, "data", "", 10*time.Millisecond, false, 0)
	if c.len() != 3 {
		t.Errorf("expected 3 (capped), got %d", c.len())
	}
	// Oldest (read_file a.txt) should be evicted without renumbering.
	if c.get(1) != nil {
		t.Error("evicted sequence #1 should not resolve")
	}
	if r := c.get(2); r == nil || r.tool != "write_file" {
		t.Errorf("sequence #2 resolved incorrectly: %+v", r)
	}
	if r := c.get(4); r == nil || r.tool != "read_file" {
		t.Errorf("new sequence #4 resolved incorrectly: %+v", r)
	}
}

func TestToolResultCache_Empty(t *testing.T) {
	c := newToolResultCache(5)
	if c.len() != 0 {
		t.Errorf("empty cache should have len=0, got %d", c.len())
	}
	if c.last() != nil {
		t.Error("last() on empty cache should be nil")
	}
	if c.get(1) != nil {
		t.Error("get on empty cache should be nil")
	}
}

func TestToolResultCache_Error(t *testing.T) {
	c := newToolResultCache(5)
	c.record("exec_command", "call-1", "session", `{"command":"rm -rf /"}`, "", "E_SANDBOX: blocked", 5*time.Millisecond, false, 0)
	last := c.last()
	if last == nil {
		t.Fatal("last should not be nil")
	}
	if last.err == "" {
		t.Error("error should be recorded")
	}
	if !strings.Contains(last.err, "E_SANDBOX") {
		t.Errorf("unexpected error: %q", last.err)
	}
}

func TestFormatOneLine(t *testing.T) {
	if got := formatOneLine("hello", 10); got != "hello" {
		t.Errorf("short string should not truncate: %q", got)
	}
	long := strings.Repeat("x", 200)
	got := formatOneLine(long, 20)
	// The truncated form is 19 chars + 1 multi-byte "…" = up to 22 bytes.
	// What matters is the rune count.
	runeCount := len([]rune(got))
	if runeCount > 20 {
		t.Errorf("truncated rune count should be <= 20, got %d (%q)", runeCount, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated should end with …, got %q", got)
	}
	if got := formatOneLine("line1\nline2\nline3", 100); strings.Contains(got, "\n") {
		t.Errorf("newlines should be replaced with spaces, got %q", got)
	}
}
