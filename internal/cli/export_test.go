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
		want ExportFormat
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
// Markdown rendering
// ====================================================================

func TestExportToMarkdown_Header(t *testing.T) {
	conv := &memory.Conversation{
		ID:        "conv_abc",
		Title:     "Project discussion",
		CreatedAt: time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC),
	}
	got := exportToMarkdown(conv, nil)
	for _, want := range []string{
		"# Project discussion",
		"conv_abc",
		"**Messages**: 0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestExportToMarkdown_EmptyTitle(t *testing.T) {
	conv := &memory.Conversation{ID: "conv_x"}
	got := exportToMarkdown(conv, nil)
	if !strings.Contains(got, "# (untitled)") {
		t.Errorf("empty title should fall back to (untitled), got:\n%s", got)
	}
}

func TestExportToMarkdown_Messages(t *testing.T) {
	conv := &memory.Conversation{ID: "conv_1", Title: "T"}
	msgs := []llm.Message{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there"},
		{Role: "tool", Content: "result data", ToolCallID: "call_xyz", Name: "read_file"},
	}
	got := exportToMarkdown(conv, msgs)
	for _, want := range []string{
		"🧑 User",      // user with role icon
		"🤖 Assistant", // assistant
		"🔧 Tool",      // tool
		"Hello",
		"Hi there",
		"result data",
		"call_xyz",
		"read_file",
		"msg #1",
		"msg #3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in markdown:\n%s", want, got)
		}
	}
}

func TestExportToMarkdown_CodeBlock(t *testing.T) {
	conv := &memory.Conversation{ID: "c"}
	msgs := []llm.Message{
		{Role: "assistant", Content: "func foo() {\n\treturn 42\n}"},
	}
	got := exportToMarkdown(conv, msgs)
	// Multi-line content with `{` should be wrapped in a code fence.
	if !strings.Contains(got, "```") {
		t.Errorf("expected code fence, got:\n%s", got)
	}
}

func TestExportToMarkdown_NoFenceForPlainText(t *testing.T) {
	conv := &memory.Conversation{ID: "c"}
	msgs := []llm.Message{
		{Role: "assistant", Content: "Just a plain sentence without code."},
	}
	got := exportToMarkdown(conv, msgs)
	// Single-line plain text should NOT be fenced.
	if strings.Contains(got, "```") {
		t.Errorf("plain text should not be fenced, got:\n%s", got)
	}
}

// ====================================================================
// JSON rendering
// ====================================================================

func TestExportToJSON_Shape(t *testing.T) {
	conv := &memory.Conversation{
		ID:        "conv_1",
		Title:     "T",
		CreatedAt: time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC),
	}
	msgs := []llm.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello", ToolCallID: "c1", Name: "x"},
	}
	body, err := exportToJSON(conv, msgs)
	if err != nil {
		t.Fatal(err)
	}

	var got struct {
		Version    string `json:"version"`
		ExportedAt string `json:"exported_at"`
		Session    map[string]any
		Messages   []llm.Message
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, body)
	}
	if got.Version != "pchat-export/1" {
		t.Errorf("version = %q", got.Version)
	}
	if got.Session["id"] != "conv_1" {
		t.Errorf("session.id = %v", got.Session["id"])
	}
	if len(got.Messages) != 2 {
		t.Errorf("messages len = %d, want 2", len(got.Messages))
	}
	if got.Messages[1].ToolCallID != "c1" {
		t.Errorf("tool_call_id = %q", got.Messages[1].ToolCallID)
	}
}

func TestExportToJSON_RoundTrip(t *testing.T) {
	// The exported JSON should be re-readable; downstream tools can
	// re-feed it to an LLM.
	conv := &memory.Conversation{ID: "x", Title: "t"}
	msgs := []llm.Message{
		{Role: "user", Content: "hi"},
		{Role: openai.ChatMessageRoleAssistant, Content: "world"},
	}
	body, _ := exportToJSON(conv, msgs)
	var got struct {
		Messages []llm.Message `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Messages[0].Content != "hi" {
		t.Errorf("roundtrip lost content")
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
	if !strings.Contains(string(body), `"version": "pchat-export/1"`) {
		t.Errorf("body should contain version, got: %s", body)
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
