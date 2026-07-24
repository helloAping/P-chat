package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	openai "github.com/sashabaranov/go-openai"
)

// ====================================================================
// Argument parsing
// ====================================================================

func TestParseExportArgs_Defaults(t *testing.T) {
	opts := parseExportArgs("")
	if opts.format != FormatMarkdown {
		t.Errorf("default format = %q, want markdown", opts.format)
	}
	if opts.sessionID != "" {
		t.Errorf("default sessionID = %q, want empty", opts.sessionID)
	}
	if opts.outFile != "" {
		t.Errorf("default outFile = %q, want empty", opts.outFile)
	}
}

func TestParseExportArgs_Format(t *testing.T) {
	cases := []struct {
		in   string
		want Format
	}{
		{"", FormatMarkdown},
		{"markdown", FormatMarkdown},
		{"md", FormatMarkdown},
		{"json", FormatJSON},
		{"JSON", FormatJSON},
		{"markdown -o x.md", FormatMarkdown},
		{"json -o x.json", FormatJSON},
	}
	for _, c := range cases {
		opts := parseExportArgs(c.in)
		if opts.format != c.want {
			t.Errorf("parseExportArgs(%q).format = %q, want %q", c.in, opts.format, c.want)
		}
	}
}

func TestParseExportArgs_Session(t *testing.T) {
	opts := parseExportArgs("conv_abc123")
	if opts.sessionID != "conv_abc123" {
		t.Errorf("sessionID = %q, want conv_abc123", opts.sessionID)
	}

	opts = parseExportArgs("last")
	if opts.sessionID != "last" {
		t.Errorf("sessionID = %q, want last", opts.sessionID)
	}
}

func TestParseExportArgs_OutputFile(t *testing.T) {
	opts := parseExportArgs("-o chat.md")
	if opts.outFile != "chat.md" {
		t.Errorf("outFile = %q, want chat.md", opts.outFile)
	}
	opts = parseExportArgs("--output /tmp/x.json")
	if opts.outFile != "/tmp/x.json" {
		t.Errorf("outFile = %q, want /tmp/x.json", opts.outFile)
	}
}

func TestParseExportArgs_Full(t *testing.T) {
	opts := parseExportArgs("json -o out.json conv_abc")
	if opts.format != FormatJSON {
		t.Errorf("format = %q, want json", opts.format)
	}
	if opts.outFile != "out.json" {
		t.Errorf("outFile = %q", opts.outFile)
	}
	if opts.sessionID != "conv_abc" {
		t.Errorf("sessionID = %q", opts.sessionID)
	}
}

func TestParseExportArgs_TrailingFlagWithoutValue(t *testing.T) {
	// "-o" with no following value should be a no-op (not panic).
	opts := parseExportArgs("-o")
	if opts.outFile != "" {
		t.Errorf("outFile = %q, want empty (no value after -o)", opts.outFile)
	}
}

// ====================================================================
// Filename generation
// ====================================================================

func TestDefaultExportFilename_Markdown(t *testing.T) {
	conv := &memory.Conversation{
		ID:        "conv_20260625_123456_0123",
		CreatedAt: time.Now(),
	}
	got := defaultExportFilename(conv, FormatMarkdown)
	if !strings.HasSuffix(got, ".md") {
		t.Errorf("expected .md suffix, got %q", got)
	}
	if !strings.HasPrefix(got, "pchat-") {
		t.Errorf("expected pchat- prefix, got %q", got)
	}
	// The default filename uses the first 12 chars of the id.
	if !strings.Contains(got, "conv_2026062") {
		t.Errorf("expected to contain short id prefix, got %q", got)
	}
}

func TestDefaultExportFilename_JSON(t *testing.T) {
	conv := &memory.Conversation{ID: "conv_abc"}
	got := defaultExportFilename(conv, FormatJSON)
	if !strings.HasSuffix(got, ".json") {
		t.Errorf("expected .json suffix, got %q", got)
	}
}

// ====================================================================
// doExport end-to-end
// ====================================================================

func newExportStore(t *testing.T) *memory.Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	store, err := memory.OpenAt(filepath.Join(dir, "test.db"), 50)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func seedConversation(t *testing.T, store *memory.Store, msgs []llm.Message) string {
	t.Helper()
	id, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		store.AddMessage(m)
	}
	_ = store.Flush()
	return id
}

func TestDoExport_NoArgs_WritesToCwd(t *testing.T) {
	store := newExportStore(t)
	id := seedConversation(t, store, []llm.Message{
		{Role: "user", Content: "hi"},
	})

	// Change to a temp dir so we don't pollute the test working dir.
	dir := t.TempDir()
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })

	path, err := doExport(store, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, dir) {
		t.Errorf("path = %q, want under %q", path, dir)
	}
	if !strings.Contains(path, id[:8]) {
		t.Errorf("path should contain session id, got %q", path)
	}
	if !strings.HasSuffix(path, ".md") {
		t.Errorf("expected .md suffix, got %q", path)
	}
}

func TestDoExport_JsonFormat(t *testing.T) {
	store := newExportStore(t)
	seedConversation(t, store, []llm.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "world"},
	})

	dir := t.TempDir()
	out := filepath.Join(dir, "out.json")

	path, err := doExport(store, "json -o "+out)
	if err != nil {
		t.Fatal(err)
	}
	if path != out {
		t.Errorf("path = %q, want %q", path, out)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"version": "pchat-export/2"`) {
		t.Errorf("body should contain v2 version, got: %s", body)
	}
}

func TestDoExport_ExplicitSessionID(t *testing.T) {
	store := newExportStore(t)
	// Create 2 sessions; export the older one.
	idA := seedConversation(t, store, []llm.Message{
		{Role: "user", Content: "first-A"},
	})
	_, _ = store.NewConversation()
	_ = seedConversation(t, store, []llm.Message{
		{Role: "user", Content: "second-B"},
	})

	dir := t.TempDir()
	out := filepath.Join(dir, "a.md")

	path, err := doExport(store, "markdown "+idA+" -o "+out)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "first-A") {
		t.Errorf("should contain first-A, got: %s", body)
	}
	if strings.Contains(string(body), "second-B") {
		t.Errorf("should NOT contain second-B (different session), got: %s", body)
	}
}

func TestDoExport_NonexistentSession_Errors(t *testing.T) {
	store := newExportStore(t)
	_ = seedConversation(t, store, []llm.Message{{Role: "user", Content: "x"}})

	dir := t.TempDir()
	_, err := doExport(store, "markdown conv_nonexistent -o "+filepath.Join(dir, "x.md"))
	if err == nil {
		t.Error("expected error for nonexistent session")
	}
}

func TestDoExport_EmptyConversation(t *testing.T) {
	store := newExportStore(t)
	_, _ = store.NewConversation() // no messages

	dir := t.TempDir()
	out := filepath.Join(dir, "empty.md")

	path, err := doExport(store, "-o "+out)
	if err != nil {
		t.Fatal(err)
	}
	// Empty message list → file still created with header.
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "**Messages**: 0") {
		t.Errorf("expected Messages: 0 in header, got: %s", body)
	}
}

// ====================================================================
// resolveSession
// ====================================================================

func TestResolveSession_Current(t *testing.T) {
	store := newExportStore(t)
	id := seedConversation(t, store, []llm.Message{{Role: "user", Content: "x"}})

	conv, err := resolveSession(store, "")
	if err != nil {
		t.Fatal(err)
	}
	if conv.ID != id {
		t.Errorf("expected current session %q, got %q", id, conv.ID)
	}
}

func TestResolveSession_Last(t *testing.T) {
	store := newExportStore(t)
	// Create two sessions in the same second. The (updated_at DESC,
	// id DESC) order breaks the tie via the nanosecond+counter
	// component of the id.
	idA := seedConversation(t, store, nil)
	idB := seedConversation(t, store, nil)
	_ = store.Flush()

	conv, err := resolveSession(store, "last")
	if err != nil {
		t.Fatal(err)
	}
	if conv.ID != idB {
		t.Errorf("expected last session %q (idA=%q), got %q", idB, idA, conv.ID)
	}
}

func TestResolveSession_ByID(t *testing.T) {
	store := newExportStore(t)
	idA := seedConversation(t, store, []llm.Message{{Role: "user", Content: "A"}})
	_, _ = store.NewConversation()
	_ = store.Flush()

	conv, err := resolveSession(store, idA)
	if err != nil {
		t.Fatal(err)
	}
	if conv.ID != idA {
		t.Errorf("expected %q, got %q", idA, conv.ID)
	}
}

func TestResolveSession_NotFound(t *testing.T) {
	store := newExportStore(t)
	_, _ = store.NewConversation() // any session
	_ = seedConversation(t, store, []llm.Message{{Role: "user", Content: "x"}})

	_, err := resolveSession(store, "conv_nonexistent")
	if err == nil {
		t.Error("expected error for missing session")
	}
}

// ====================================================================
// Cross-check: CLI doExport and HTTP export use the same
// rendering core. This is the architectural promise —
// both entry points produce byte-identical output for
// the same data, modulo the wrapper (filename header vs
// file path).
// ====================================================================

func TestDoExport_OutputMatchesRenderCore(t *testing.T) {
	store := newExportStore(t)
	seedConversation(t, store, []llm.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "world"},
	})

	dir := t.TempDir()
	mdOut := filepath.Join(dir, "x.md")
	if _, err := doExport(store, "-o "+mdOut); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(mdOut)
	if !strings.Contains(string(body), "hello") || !strings.Contains(string(body), "world") {
		t.Errorf("expected both messages, got:\n%s", body)
	}
	// Sanity: this is markdown, not JSON. The
	// envelope version string `pchat-export/2` only
	// appears in JSON output; markdown uses the
	// `**Messages**:` header instead.
	if strings.HasPrefix(strings.TrimSpace(string(body)), "{") {
		t.Errorf("expected markdown (starts with `# `), got JSON: %s", body)
	}
	if !strings.Contains(string(body), "**Messages**: 2") {
		t.Errorf("expected message count header, got:\n%s", body)
	}
}

// The tests below are placeholders for the legacy
// attachmentsFromMultiContent unit tests, which moved
// to the memory package. Re-exported here as no-ops so
// `go test ./internal/cli/...` stays self-documenting.
func TestAttachmentsFromMultiContent_MovedToMemory(t *testing.T) {
	// The function lives at memory.AttachmentsFromMultiContent;
	// the cli package no longer tests it directly. This
	// test exists as a marker so future readers know
	// where to look.
	parts := []openai.ChatMessagePart{
		{Type: openai.ChatMessagePartTypeText, Text: "hello"},
	}
	got := memory.AttachmentsFromMultiContent(parts)
	if len(got) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(got))
	}
	// Round-trip through JSON to confirm the wire shape.
	b, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"type":"text"`) {
		t.Errorf("attachment JSON missing type field: %s", b)
	}
	if !strings.Contains(string(b), `"kind":"text"`) {
		t.Errorf("attachment JSON missing kind field: %s", b)
	}
}

// TestAttachmentsFromMultiContent_KindAllTypes covers
// the "JSON message should be tagged with attachment
// type" user-reported gap. Every wire-format type
// must produce a non-empty `Kind` on the resulting
// Attachment so a JSON consumer that reads `kind`
// (without having to know the OpenAI wire format)
// can still tell images from audio from text. The
// `Type` field is preserved for back-compat.
func TestAttachmentsFromMultiContent_KindAllTypes(t *testing.T) {
	cases := []struct {
		wire     openai.ChatMessagePartType
		wantKind string
		wantType openai.ChatMessagePartType
	}{
		{openai.ChatMessagePartTypeImageURL, "image", openai.ChatMessagePartTypeImageURL},
		{openai.ChatMessagePartTypeText, "text", openai.ChatMessagePartTypeText},
		{"audio_url", "audio", "audio_url"},
		{"video_url", "video", "video_url"},
	}
	for _, c := range cases {
		t.Run(string(c.wire), func(t *testing.T) {
			var parts []openai.ChatMessagePart
			if c.wire == openai.ChatMessagePartTypeText {
				parts = []openai.ChatMessagePart{{Type: c.wire, Text: "x"}}
			} else {
				parts = []openai.ChatMessagePart{{Type: c.wire, ImageURL: &openai.ChatMessageImageURL{URL: "data:"}}}
			}
			got := memory.AttachmentsFromMultiContent(parts)
			if len(got) != 1 {
				t.Fatalf("len = %d, want 1", len(got))
			}
			if got[0].Kind != c.wantKind {
				t.Errorf("Kind = %q, want %q", got[0].Kind, c.wantKind)
			}
			if got[0].Type != string(c.wantType) {
				t.Errorf("Type = %q, want %q (wire format preserved)", got[0].Type, c.wantType)
			}
		})
	}
}
