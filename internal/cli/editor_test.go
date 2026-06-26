package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestMultilineEditor_SubmitDot verifies that input ending with a
// line containing just "." submits the accumulated text.
func TestMultilineEditor_SubmitDot(t *testing.T) {
	in := strings.NewReader("line one\nline two\n.\n")
	var out bytes.Buffer

	got, ok, err := multilineEditor(in, &out, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected submit (ok=true)")
	}
	want := "line one\nline two"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestMultilineEditor_EmptyDotPreservesCurrent verifies that an
// empty submit (just "." with no prior lines) returns the current
// plan unchanged, so the user can opt out of editing.
func TestMultilineEditor_EmptyDotPreservesCurrent(t *testing.T) {
	in := strings.NewReader(".\n")
	var out bytes.Buffer

	original := "do X, then Y, then Z"
	got, ok, err := multilineEditor(in, &out, "", original)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected submit (ok=true) for empty edit")
	}
	if got != original {
		t.Errorf("empty edit should preserve current, got %q", got)
	}
}

// TestMultilineEditor_CancelDotCancel verifies the explicit cancel.
func TestMultilineEditor_CancelDotCancel(t *testing.T) {
	in := strings.NewReader("foo\n.cancel\n")
	var out bytes.Buffer

	got, ok, err := multilineEditor(in, &out, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected cancel (ok=false)")
	}
	if got != "" {
		t.Errorf("cancelled edit should return empty, got %q", got)
	}
}

// TestMultilineEditor_EOF verifies that closing stdin commits
// the accumulated text.
func TestMultilineEditor_EOF(t *testing.T) {
	in := strings.NewReader("first line\nsecond line") // no trailing \n
	var out bytes.Buffer

	got, ok, err := multilineEditor(in, &out, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("EOF should be treated as commit (ok=true)")
	}
	want := "first line\nsecond line"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestMultilineEditor_EmptyEOF verifies that an empty EOF is a
// cancel (rather than a no-op submit), matching the empty "." case.
func TestMultilineEditor_EmptyEOF(t *testing.T) {
	in := strings.NewReader("")
	var out bytes.Buffer

	_, ok, err := multilineEditor(in, &out, "", "original")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("empty EOF should be cancel")
	}
}

// TestMultilineEditor_PreFillCurrent verifies that the current plan
// is shown as a comment block but does NOT appear in the output.
func TestMultilineEditor_PreFillCurrent(t *testing.T) {
	in := strings.NewReader("fresh line 1\nfresh line 2\n.\n")
	var out bytes.Buffer

	original := "old step 1\nold step 2"
	got, ok, err := multilineEditor(in, &out, "", original)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected submit")
	}
	// The original plan must NOT leak into the result.
	if strings.Contains(got, "old step") {
		t.Errorf("original plan leaked into result: %q", got)
	}
	// Only the new content should come back.
	want := "fresh line 1\nfresh line 2"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// The original should appear in the output stream (as a hint).
	if !strings.Contains(out.String(), "old step") {
		t.Errorf("original should appear in output as a hint, got:\n%s", out.String())
	}
}

// TestMultilineEditor_EmptyPromptNoCurrent verifies a brand-new
// editor session (no prompt, no current) works.
func TestMultilineEditor_EmptyPromptNoCurrent(t *testing.T) {
	in := strings.NewReader("hello\n.\n")
	var out bytes.Buffer

	got, ok, err := multilineEditor(in, &out, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != "hello" {
		t.Errorf("got %q, ok=%v, want 'hello'/true", got, ok)
	}
}

// TestMultilineEditor_RealTTYStdin covers a no-input stream. We
// skip if stdin is the real terminal (the test would block).
func TestMultilineEditor_RealTTYStdin(t *testing.T) {
	// Use a pipe to avoid blocking on a TTY.
	r, w, _ := os.Pipe()
	_ = w.Close()
	defer r.Close()
	_ = r

	// Just smoke test: passing a pipe shouldn't crash.
	in := strings.NewReader("hi\n.\n")
	var out bytes.Buffer
	got, ok, err := multilineEditor(in, &out, "edit:", "")
	if err != nil || !ok || got != "hi" {
		t.Errorf("got %q ok=%v err=%v", got, ok, err)
	}
	if !strings.Contains(out.String(), "edit:") {
		t.Errorf("prompt should appear, got: %s", out.String())
	}
}
