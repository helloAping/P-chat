package export

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	openai "github.com/sashabaranov/go-openai"
)

// toFullMFs converts a slice of llm.Message to the rich
// memory.MessageFull shape the writers expect. Used by
// the legacy tests so we don't have to re-author every
// fixture in the new shape.
func toFullMFs(msgs []llm.Message) []memory.MessageFull {
	out := make([]memory.MessageFull, len(msgs))
	for i, m := range msgs {
		out[i] = memory.MessageFull{Msg: m}
	}
	return out
}

// ====================================================================
// Markdown — basic shape
// ====================================================================

func TestToMarkdown_Header(t *testing.T) {
	conv := &memory.Conversation{
		ID:        "conv_abc",
		Title:     "Project discussion",
		CreatedAt: time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC),
	}
	got := ToMarkdown(conv, nil)
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

func TestToMarkdown_EmptyTitle(t *testing.T) {
	conv := &memory.Conversation{ID: "conv_x"}
	got := ToMarkdown(conv, nil)
	if !strings.Contains(got, "# (untitled)") {
		t.Errorf("empty title should fall back to (untitled), got:\n%s", got)
	}
}

func TestToMarkdown_Messages(t *testing.T) {
	conv := &memory.Conversation{ID: "conv_1", Title: "T"}
	msgs := toFullMFs([]llm.Message{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there"},
		{Role: "tool", Content: "result data", ToolCallID: "call_xyz", Name: "read_file"},
	})
	got := ToMarkdown(conv, msgs)
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

func TestToMarkdown_CodeBlock(t *testing.T) {
	conv := &memory.Conversation{ID: "c"}
	msgs := toFullMFs([]llm.Message{
		{Role: "assistant", Content: "func foo() {\n\treturn 42\n}"},
	})
	got := ToMarkdown(conv, msgs)
	// Multi-line content with `{` should be wrapped in a code fence.
	if !strings.Contains(got, "```") {
		t.Errorf("expected code fence, got:\n%s", got)
	}
}

func TestToMarkdown_NoFenceForPlainText(t *testing.T) {
	conv := &memory.Conversation{ID: "c"}
	msgs := toFullMFs([]llm.Message{
		{Role: "assistant", Content: "Just a plain sentence without code."},
	})
	got := ToMarkdown(conv, msgs)
	// Single-line plain text should NOT be fenced.
	if strings.Contains(got, "```") {
		t.Errorf("plain text should not be fenced, got:\n%s", got)
	}
}

// ====================================================================
// Markdown — attachments (v2)
// ====================================================================

func TestToMarkdown_ImageAttachmentInline(t *testing.T) {
	conv := &memory.Conversation{ID: "c", Title: "T"}
	dataURL := "data:image/png;base64,iVBORw0KGgo="
	msgs := []memory.MessageFull{
		{
			Msg: llm.Message{Role: "user", Content: "看这张图"},
			Attachments: []memory.Attachment{
				{Type: "image_url", URL: dataURL, Name: "shot.png", Mime: "image/png"},
			},
		},
	}
	got := ToMarkdown(conv, msgs)
	if !strings.Contains(got, "![shot.png](data:image/png;base64,") {
		t.Errorf("expected inline image, got:\n%s", got)
	}
	if !strings.Contains(got, "看这张图") {
		t.Errorf("expected user body text, got:\n%s", got)
	}
}

func TestToMarkdown_AudioAttachmentAsLink(t *testing.T) {
	conv := &memory.Conversation{ID: "c"}
	msgs := []memory.MessageFull{
		{
			Msg: llm.Message{Role: "user"},
			Attachments: []memory.Attachment{
				{Type: "audio_url", URL: "data:audio/mp3;base64,XYZ", Name: "song.mp3", Mime: "audio/mp3"},
			},
		},
	}
	got := ToMarkdown(conv, msgs)
	if !strings.Contains(got, "[🔊 song.mp3](data:audio/mp3") {
		t.Errorf("expected audio link, got:\n%s", got)
	}
}

func TestToMarkdown_TextAttachmentAsCodeBlock(t *testing.T) {
	conv := &memory.Conversation{ID: "c"}
	msgs := []memory.MessageFull{
		{
			Msg: llm.Message{Role: "user"},
			Attachments: []memory.Attachment{
				{Type: "text", URL: "name,age\nalice,30", Name: "people.csv", Mime: "text/csv"},
			},
		},
	}
	got := ToMarkdown(conv, msgs)
	if !strings.Contains(got, "```csv") {
		t.Errorf("expected csv code fence, got:\n%s", got)
	}
	if !strings.Contains(got, "alice,30") {
		t.Errorf("expected csv body, got:\n%s", got)
	}
}

// ====================================================================
// Markdown — parts[] (v2)
// ====================================================================

func TestToMarkdown_PartsText(t *testing.T) {
	conv := &memory.Conversation{ID: "c"}
	partsJSON := []byte(`[{"kind":"text","text":"hello from parts"}]`)
	msgs := []memory.MessageFull{
		{
			Msg:   llm.Message{Role: "assistant", Content: "stale legacy content"},
			Parts: partsJSON,
		},
	}
	got := ToMarkdown(conv, msgs)
	if !strings.Contains(got, "hello from parts") {
		t.Errorf("expected parts text, got:\n%s", got)
	}
	// Legacy content should NOT also appear — the parts
	// array takes precedence.
	if strings.Contains(got, "stale legacy content") {
		t.Errorf("legacy content should not appear when parts[] is set, got:\n%s", got)
	}
}

func TestToMarkdown_PartsTool(t *testing.T) {
	conv := &memory.Conversation{ID: "c"}
	partsJSON := []byte(`[{"kind":"tool","name":"browser_screenshot","status":"ok","result":"data:image/png;base64,ABCD"}]`)
	msgs := []memory.MessageFull{
		{
			Msg:   llm.Message{Role: "assistant"},
			Parts: partsJSON,
		},
	}
	got := ToMarkdown(conv, msgs)
	// tool name + status header.
	if !strings.Contains(got, "**browser_screenshot**") {
		t.Errorf("expected tool name header, got:\n%s", got)
	}
	if !strings.Contains(got, "`ok`") {
		t.Errorf("expected tool status, got:\n%s", got)
	}
	// base64 screenshot result → inline image (not a
	// giant code block).
	if !strings.Contains(got, "![tool result](data:image/png;base64,ABCD)") {
		t.Errorf("expected base64 screenshot inlined as image, got:\n%s", got)
	}
}

func TestToMarkdown_PartsThinking(t *testing.T) {
	conv := &memory.Conversation{ID: "c"}
	partsJSON := []byte(`[{"kind":"text","text":"answer"},{"kind":"thinking","text":"let me think"}]`)
	msgs := []memory.MessageFull{
		{Msg: llm.Message{Role: "assistant"}, Parts: partsJSON},
	}
	got := ToMarkdown(conv, msgs)
	if !strings.Contains(got, "answer") {
		t.Errorf("expected text body, got:\n%s", got)
	}
	if !strings.Contains(got, "<details>") || !strings.Contains(got, "thinking") {
		t.Errorf("expected thinking details block, got:\n%s", got)
	}
}

func TestToMarkdown_TopLevelThinking(t *testing.T) {
	// Pre-parts-snapshot rows stored thinking as a top-level
	// field, not inside the parts array. The exporter
	// surfaces that as a details block too.
	conv := &memory.Conversation{ID: "c"}
	msgs := []memory.MessageFull{
		{
			Msg:      llm.Message{Role: "assistant", Content: "the answer"},
			Thinking: "let me think hard",
		},
	}
	got := ToMarkdown(conv, msgs)
	if !strings.Contains(got, "the answer") {
		t.Errorf("expected body, got:\n%s", got)
	}
	if !strings.Contains(got, "let me think hard") {
		t.Errorf("expected thinking body, got:\n%s", got)
	}
}

// ====================================================================
// JSON rendering
// ====================================================================

func TestToJSON_Shape(t *testing.T) {
	conv := &memory.Conversation{
		ID:        "conv_1",
		Title:     "T",
		CreatedAt: time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC),
	}
	msgs := toFullMFs([]llm.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello", ToolCallID: "c1", Name: "x"},
	})
	body, err := ToJSON(conv, msgs)
	if err != nil {
		t.Fatal(err)
	}

	var got struct {
		Version    string         `json:"version"`
		ExportedAt string         `json:"exported_at"`
		Session    map[string]any `json:"session"`
		Messages   []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, body)
	}
	if got.Version != JSONSchemaVersion {
		t.Errorf("version = %q, want %q", got.Version, JSONSchemaVersion)
	}
	if got.Session["id"] != "conv_1" {
		t.Errorf("session.id = %v", got.Session["id"])
	}
	if len(got.Messages) != 2 {
		t.Errorf("messages len = %d, want 2", len(got.Messages))
	}
	if got.Messages[1]["tool_call_id"] != "c1" {
		t.Errorf("tool_call_id = %v", got.Messages[1]["tool_call_id"])
	}
	// v2: every message carries an `attachments` array
	// (possibly empty) so consumers can iterate without
	// a nil check.
	for i, m := range got.Messages {
		if _, ok := m["attachments"]; !ok {
			t.Errorf("messages[%d] missing attachments field", i)
		}
	}
}

func TestToJSON_AttachmentsPresent(t *testing.T) {
	conv := &memory.Conversation{ID: "c"}
	msgs := []memory.MessageFull{
		{
			Msg: llm.Message{Role: "user", Content: "看"},
			Attachments: []memory.Attachment{
				{Type: "image_url", URL: "data:image/png;base64,ABCD", Name: "a.png", Mime: "image/png"},
			},
		},
	}
	body, err := ToJSON(conv, msgs)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	atts, ok := got.Messages[0]["attachments"].([]any)
	if !ok {
		t.Fatalf("attachments not an array: %T", got.Messages[0]["attachments"])
	}
	if len(atts) != 1 {
		t.Fatalf("attachments len = %d, want 1", len(atts))
	}
	att := atts[0].(map[string]any)
	if att["type"] != "image_url" {
		t.Errorf("attachment type = %v", att["type"])
	}
	if att["url"] != "data:image/png;base64,ABCD" {
		t.Errorf("attachment url = %v", att["url"])
	}
	if att["name"] != "a.png" {
		t.Errorf("attachment name = %v", att["name"])
	}
}

func TestToJSON_PartsPresent(t *testing.T) {
	conv := &memory.Conversation{ID: "c"}
	partsJSON := []byte(`[{"kind":"text","text":"hi from parts"}]`)
	msgs := []memory.MessageFull{
		{
			Msg:   llm.Message{Role: "assistant"},
			Parts: partsJSON,
		},
	}
	body, err := ToJSON(conv, msgs)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	raw, ok := got.Messages[0]["parts"]
	if !ok {
		t.Fatalf("parts field missing")
	}
	partsArr, ok := raw.([]any)
	if !ok {
		t.Fatalf("parts is not an array: %T", raw)
	}
	if len(partsArr) != 1 {
		t.Fatalf("parts len = %d, want 1", len(partsArr))
	}
	part := partsArr[0].(map[string]any)
	if part["kind"] != "text" || part["text"] != "hi from parts" {
		t.Errorf("part = %+v", part)
	}
}

func TestToJSON_RoundTrip(t *testing.T) {
	// The exported JSON should be re-readable; downstream tools can
	// re-feed it to an LLM.
	conv := &memory.Conversation{ID: "x", Title: "t"}
	msgs := toFullMFs([]llm.Message{
		{Role: "user", Content: "hi"},
		{Role: openai.ChatMessageRoleAssistant, Content: "world"},
	})
	body, _ := ToJSON(conv, msgs)
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
// "undefined" content regression
// ====================================================================

// TestToMarkdown_UndefinedStringInContent is the
// regression lock for the user-reported "MD content is
// undefined" bug. The current behaviour is intentional:
// the literal string "undefined" coming from the LLM
// (or a tool result) is rendered as-is — we never
// invent the string "undefined" ourselves. If the
// rendering layer ever does start emitting the literal
// (e.g. by interpolating a Go nil value into the
// template), this test catches it.
func TestToMarkdown_UndefinedStringInContent(t *testing.T) {
	conv := &memory.Conversation{ID: "c", Title: "T"}
	// Tool result that happens to contain the literal
	// string "undefined" (LLM pasted it from a JSON
	// payload).
	partsJSON := []byte(`[{"kind":"tool","name":"x","status":"ok","result":"value is undefined in this scope"}]`)
	msgs := []memory.MessageFull{
		{Msg: llm.Message{Role: "assistant"}, Parts: partsJSON},
	}
	got := ToMarkdown(conv, msgs)
	// The string is in the output (as part of a code
	// block or blockquote). What we DON'T want is the
	// template literal "undefined" appearing in a place
	// that has no source — e.g. as a tool name or
	// status with no underlying data.
	if !strings.Contains(got, "value is undefined in this scope") {
		t.Errorf("expected the literal string to be preserved, got:\n%s", got)
	}
	// Guard: no part-name / status field should ever
	// render as the literal "undefined" without a
	// corresponding source string in the input.
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "**undefined**") {
			t.Errorf("tool name rendered as undefined (no source): %q", line)
		}
		if strings.HasPrefix(strings.TrimSpace(line), "🔧 **undefined**") {
			t.Errorf("tool header with no name rendered: %q", line)
		}
	}
}

func TestToMarkdown_EmptyPartsFallsBackToContent(t *testing.T) {
	// A message with empty parts[] (or no parts) should
	// fall back to rendering Msg.Content, never emit
	// the literal "undefined".
	conv := &memory.Conversation{ID: "c"}
	msgs := toFullMFs([]llm.Message{
		{Role: "user", Content: "看这里"},
		{Role: "assistant", Content: ""}, // empty content
	})
	got := ToMarkdown(conv, msgs)
	if strings.Contains(got, "undefined") {
		t.Errorf("empty content should not produce 'undefined', got:\n%s", got)
	}
	if !strings.Contains(got, "看这里") {
		t.Errorf("expected user text, got:\n%s", got)
	}
}

// ====================================================================
// PartToMarkdown / ResultBlockToMarkdown helpers
// ====================================================================

func TestPartToMarkdown_Text(t *testing.T) {
	got := PartToMarkdown(MessagePart{Kind: "text", Text: "hello"}, 0)
	if got != "hello" {
		t.Errorf("text part = %q, want %q", got, "hello")
	}
}

func TestPartToMarkdown_ToolWithError(t *testing.T) {
	got := PartToMarkdown(MessagePart{
		Kind: "tool", Name: "read_file", Status: "error", Error: "permission denied",
	}, 0)
	for _, want := range []string{"**read_file**", "`error`", "permission denied", "❌"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestPartToMarkdown_SubAgentNested(t *testing.T) {
	got := PartToMarkdown(MessagePart{
		Kind: "sub_agent", Task: "explore", Status: "ok",
		Parts: []MessagePart{
			{Kind: "text", Text: "found 3 files"},
		},
	}, 0)
	for _, want := range []string{
		"### 🤖 sub-agent: explore (ok)",
		"found 3 files",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestResultBlockToMarkdown_DataImage(t *testing.T) {
	got := ResultBlockToMarkdown("data:image/png;base64,XXX", "")
	if !strings.HasPrefix(got, "![tool result](data:image/png;base64,XXX)") {
		t.Errorf("data:image should inline as markdown image, got %q", got)
	}
}

func TestResultBlockToMarkdown_JSON(t *testing.T) {
	got := ResultBlockToMarkdown(`{"a":1, "b":[2,3]}`, "")
	if !strings.Contains(got, "```json") {
		t.Errorf("JSON result should be json-fenced, got %q", got)
	}
	// We re-emit the original string verbatim inside
	// the fence (no pretty-printing) so the output
	// matches the input byte-for-byte. The only
	// guarantee is that the JSON itself is parseable.
	if !strings.Contains(got, `"a":1, "b":[2,3]`) {
		t.Errorf("JSON result body should be preserved verbatim, got %q", got)
	}
}

func TestResultBlockToMarkdown_MultiLineProse(t *testing.T) {
	// Caller passes empty indent to test the bare
	// blockquote shape; indented callers prepend their
	// own indent via the second arg.
	got := ResultBlockToMarkdown("line1\nline2", "")
	if !strings.HasPrefix(got, "> line1") {
		t.Errorf("multi-line prose should be blockquoted, got %q", got)
	}
	if !strings.Contains(got, "> line2") {
		t.Errorf("all lines should be blockquoted, got %q", got)
	}
}

func TestIsJSON(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{`{"a":1}`, true},
		{`[1,2,3]`, true},
		{`   {"x": 1}   `, true},
		{`{"a":`, false},
		{`"hello"`, false},
		{``, false},
		{`null`, false},
		{`42`, false},
	}
	for _, c := range cases {
		if got := IsJSON(c.in); got != c.want {
			t.Errorf("IsJSON(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestLooksLikeCode(t *testing.T) {
	if !LooksLikeCode("func f() {\n  return 1\n}") {
		t.Error("multi-line with `(` + `{` should look like code")
	}
	if LooksLikeCode("just a sentence") {
		t.Error("plain prose should not look like code")
	}
}

func TestLangForMime(t *testing.T) {
	cases := []struct {
		mime string
		want string
	}{
		{"application/json", "json"},
		{"text/csv", "csv"},
		{"text/markdown", "markdown"},
		{"text/x-go", "go"},
		{"application/vnd.api+json", "json"},
		{"text/plain", "text"},
		{"application/octet-stream", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := langForMime(c.mime); got != c.want {
			t.Errorf("langForMime(%q) = %q, want %q", c.mime, got, c.want)
		}
	}
}

func TestAttachmentToMarkdown_EmptyURL(t *testing.T) {
	if got := AttachmentToMarkdown(memory.Attachment{Type: "image_url", Name: "x.png"}); got != "" {
		t.Errorf("empty URL should produce no output, got %q", got)
	}
}

func TestAttachmentToMarkdown_RemoteImageLink(t *testing.T) {
	// A non-data, non-https image URL (e.g. a stale
	// blob: that the export writer doesn't recognise)
	// should still be surfaced as a link so the reader
	// can locate the asset, not silently dropped.
	got := AttachmentToMarkdown(memory.Attachment{
		Type: "image_url", URL: "blob:http://x", Name: "x.png",
	})
	if !strings.Contains(got, "[🖼 x.png](blob:http://x)") {
		t.Errorf("expected link fallback for non-data URL, got %q", got)
	}
}

// ====================================================================
// Filename sanitisation
// ====================================================================

func TestSanitizeFilename(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hello world", "hello-world"},
		{"a/b\\c:d*e?f\"g<h>i|j", "a-b-c-d-e-f-g-h-i-j"},
		{"", ""},
		{"正常 title", "正常-title"},
	}
	for _, c := range cases {
		if got := SanitizeFilename(c.in); got != c.want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
