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

// ====================================================================
// ExtractDataURLsFromContent (image-in-text fix)
// ====================================================================

func TestExtractDataURLsFromContent_None(t *testing.T) {
	cleaned, atts := ExtractDataURLsFromContent("no images here, just prose")
	if cleaned != "no images here, just prose" {
		t.Errorf("no-image content should pass through, got %q", cleaned)
	}
	if len(atts) != 0 {
		t.Errorf("no images → empty atts, got %v", atts)
	}
}

func TestExtractDataURLsFromContent_One(t *testing.T) {
	dataURL := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="
	in := "看这张图 " + dataURL + " 还有别的"
	cleaned, atts := ExtractDataURLsFromContent(in)
	if len(atts) != 1 {
		t.Fatalf("expected 1 attachment, got %d (%+v)", len(atts), atts)
	}
	if atts[0].Type != "image_url" {
		t.Errorf("attachment type = %q, want image_url", atts[0].Type)
	}
	if atts[0].URL != dataURL {
		t.Errorf("attachment URL mismatch")
	}
	if atts[0].Mime != "image/png" {
		t.Errorf("attachment MIME = %q, want image/png", atts[0].Mime)
	}
	if strings.Contains(cleaned, "data:image") {
		t.Errorf("URL should be lifted out of content, got %q", cleaned)
	}
	if !strings.Contains(cleaned, "看这张图") {
		t.Errorf("surrounding prose should be preserved, got %q", cleaned)
	}
}

func TestExtractDataURLsFromContent_Multiple(t *testing.T) {
	url1 := "data:image/png;base64,AAAA"
	url2 := "data:image/jpeg;base64,BBBB"
	in := url1 + " middle " + url2
	cleaned, atts := ExtractDataURLsFromContent(in)
	if len(atts) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(atts))
	}
	if atts[0].Mime != "image/png" || atts[1].Mime != "image/jpeg" {
		t.Errorf("MIME detection broken: %+v", atts)
	}
	if strings.Contains(cleaned, "data:image") {
		t.Errorf("URLs should be lifted, got %q", cleaned)
	}
}

func TestExtractDataURLsFromContent_PreservesSVG(t *testing.T) {
	// image/svg+xml is a valid image MIME; the
	// extractor must not choke on the `+`.
	url := "data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciLz4="
	_, atts := ExtractDataURLsFromContent("icon: " + url)
	if len(atts) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(atts))
	}
	if atts[0].Mime != "image/svg+xml" {
		t.Errorf("MIME = %q, want image/svg+xml", atts[0].Mime)
	}
}

// TestToMarkdown_InlineImageLiftedFromContent is the
// end-to-end regression for the user-reported "image
// shows as base64 string" bug. The LLM echoed a
// screenshot URL into its reply text; the exporter
// must lift the URL out and render it as a markdown
// image.
func TestToMarkdown_InlineImageLiftedFromContent(t *testing.T) {
	conv := &memory.Conversation{ID: "c", Title: "T"}
	dataURL := "data:image/png;base64,iVBORw0KGgo"
	msgs := toFullMFs([]llm.Message{
		{Role: "assistant", Content: "这是截图: " + dataURL},
	})
	got := ToMarkdown(conv, msgs)
	// The URL must appear in ![]() syntax (image
	// embedded inline) and not as a raw string
	// in the prose.
	if !strings.Contains(got, "!["+"assistant](data:image/png;base64,iVBORw0KGgo)") {
		// The exact name "inline-1" is generated by
		// the extractor; check the URL is wrapped
		// in markdown image syntax instead.
		if !strings.Contains(got, "![inline-1]("+dataURL+")") {
			t.Errorf("expected inline image markdown, got:\n%s", got)
		}
	}
	// The raw "data:image/png;base64," string
	// should only appear inside the image syntax.
	outsideImg := strings.Replace(got, "!["+"inline-1]("+dataURL+")", "", 1)
	if strings.Contains(outsideImg, "data:image/png;base64,") {
		t.Errorf("raw data URL should not leak outside image syntax, got:\n%s", got)
	}
}

func TestIsRawPNGPayload(t *testing.T) {
	if !isRawPNGPayload("iVBORw0KGgo" + strings.Repeat("A", 200)) {
		t.Error("valid PNG payload should be detected")
	}
	if isRawPNGPayload("iVBORw0KGgo") {
		t.Error("too-short string should not be detected")
	}
	if isRawPNGPayload("SGVsbG8gd29ybGQ=") {
		t.Error("non-PNG base64 should not be detected")
	}
	if isRawPNGPayload("not base64 at all") {
		t.Error("non-base64 string should not be detected")
	}
}

// ====================================================================
// URLEncodeFilename — RFC 5987 percent-encoding
// ====================================================================

// TestURLEncodeFilename_ASCIIOnly is the regression
// lock for the "filename garbled in WebView2" bug.
// The output must contain ONLY ASCII bytes (the
// percent-encoding result); raw UTF-8 bytes for
// Chinese / emoji / accented chars are illegal in the
// `filename*=UTF-8''…` parameter per RFC 5987, and
// WebView2 specifically mangles them. The previous
// implementation wrote the rune's UTF-8 bytes through
// when r >= 0x80, so the test that previously
// "passed" by url.QueryUnescape-decoding the result
// didn't actually catch the bug — the encoded form
// was never a valid RFC 5987 value to begin with.
func TestURLEncodeFilename_ASCIIOnly(t *testing.T) {
	cases := []string{
		"plain-ascii",
		"调试记录",                  // Chinese
		"déjà vu",                  // French accents
		"emoji 🛠",                 // Emoji
		"mixed 调试 .md",           // Mixed
		"a/b\\c",                   // Unsafe chars
		"",                         // Empty
		"x",                        // Single byte
	}
	for _, in := range cases {
		got := URLEncodeFilename(in)
		for i := 0; i < len(got); i++ {
			if got[i] > 0x7f {
				t.Errorf("URLEncodeFilename(%q) has non-ASCII byte 0x%02X at %d in %q",
					in, got[i], i, got)
			}
		}
	}
}

func TestURLEncodeFilename_KnownValues(t *testing.T) {
	// Spot-check the actual encoding output. These
	// values are the test fixtures used in the
	// WebView2 bug report; if any of them change,
	// the wire format changes and downstream
	// consumers (browsers, Wails) need to be
	// re-verified.
	cases := []struct{ in, want string }{
		{"hello", "hello"},
		{"a-b_c.d", "a-b_c.d"},
		{" ", "%20"},
		{"调试", "%E8%B0%83%E8%AF%95"},
		{"é", "%C3%A9"},
		{"🛠", "%F0%9F%9B%A0"},
	}
	for _, c := range cases {
		if got := URLEncodeFilename(c.in); got != c.want {
			t.Errorf("URLEncodeFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestURLEncodeFilename_PassthroughSafeChars verifies
// the safe set is exactly the RFC 3986 unreserved
// characters (alphanumeric + `- . _`); everything
// else is percent-encoded. We don't allow the
// `attr-char` set (RFC 5987's superset including
// `! # $ & + ^ ` | } ~`) because those characters
// can break out of HTTP header values when
// concatenated with surrounding syntax.
func TestURLEncodeFilename_PassthroughSafeChars(t *testing.T) {
	safe := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._"
	if got := URLEncodeFilename(safe); got != safe {
		t.Errorf("safe chars should pass through, got %q", got)
	}
}

// ====================================================================
// ExtractRawBase64Images — bare-base64 image extraction
// ====================================================================

// validPNGPayload is a fixed test fixture: a run
// of valid base64 starting with the PNG magic bytes
// (iVBORw0KGgo = \x89PNG\r\n\x1a\n). The total
// length is well above the 100-char threshold
// `isRawImagePayload` requires. We pad with extra
// `AAAA` blocks rather than random data so the test
// output stays byte-stable across runs.
var validPNGPayload = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg==AAAA"
var validJPEGMagic = "/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wAARCAABAAEDASIAAhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAr/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/8QAFAEBAAAAAAAAAAAAAAAAAAAAAP/EABQRAQAAAAAAAAAAAAAAAAAAAAD/2gAMAwEAAhEDEQA/AL+AB//Z"
var validGIFMagic = "R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
var validWEBPMagic = "UklGRiQAAABXRUJQVlA4IBgAAAAwAQCdASoBAAEAAwA0JaQAA3AA/v3AgAA" + strings.Repeat("A", 80)

func TestExtractRawBase64Images_None(t *testing.T) {
	in := "no images here, just prose"
	cleaned, atts := ExtractRawBase64Images(in)
	if cleaned != in {
		t.Errorf("no-image content should pass through, got %q", cleaned)
	}
	if len(atts) != 0 {
		t.Errorf("no images → empty atts, got %v", atts)
	}
}

func TestExtractRawBase64Images_PNG(t *testing.T) {
	in := "这是截图\n" + validPNGPayload + "\n完毕"
	cleaned, atts := ExtractRawBase64Images(in)
	if len(atts) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(atts))
	}
	if atts[0].Type != "image_url" {
		t.Errorf("type = %q, want image_url", atts[0].Type)
	}
	if atts[0].Mime != "image/png" {
		t.Errorf("mime = %q, want image/png", atts[0].Mime)
	}
	if !strings.HasPrefix(atts[0].URL, "data:image/png;base64,") {
		t.Errorf("URL should be data: form, got %q", atts[0].URL)
	}
	if strings.Contains(cleaned, validPNGPayload) {
		t.Errorf("raw payload should be lifted out, got %q", cleaned)
	}
	if !strings.Contains(cleaned, "这是截图") || !strings.Contains(cleaned, "完毕") {
		t.Errorf("surrounding prose should be preserved, got %q", cleaned)
	}
}

func TestExtractRawBase64Images_AllFormats(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		mime    string
	}{
		{"png", validPNGPayload, "image/png"},
		{"jpeg", validJPEGMagic, "image/jpeg"},
		{"gif", validGIFMagic, "image/gif"},
		{"webp", validWEBPMagic, "image/webp"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, atts := ExtractRawBase64Images("header\n" + c.payload + "\nfooter")
			if len(atts) != 1 {
				t.Fatalf("expected 1 attachment, got %d", len(atts))
			}
			if atts[0].Mime != c.mime {
				t.Errorf("mime = %q, want %q", atts[0].Mime, c.mime)
			}
		})
	}
}

func TestExtractRawBase64Images_NotAloneOnLine(t *testing.T) {
	// Inline base64 mixed with prose should NOT be
	// lifted — false-positive guard for short
	// base64-looking tokens embedded in text.
	in := "看这张图 " + validPNGPayload + " 完毕"
	cleaned, atts := ExtractRawBase64Images(in)
	if len(atts) != 0 {
		t.Errorf("inline base64 should not be lifted, got %d attachments", len(atts))
	}
	if cleaned != in {
		t.Errorf("content should pass through unchanged, got %q", cleaned)
	}
}

func TestExtractRawBase64Images_TooShort(t *testing.T) {
	// A short base64 string that happens to start
	// with PNG magic must NOT be lifted. (Most
	// "image" tokens in a hash chain or token are
	// < 100 chars.)
	short := "iVBORw0KGgoAAAA" // 16 chars
	_, atts := ExtractRawBase64Images("prefix\n" + short + "\nsuffix")
	if len(atts) != 0 {
		t.Errorf("short base64 should not be lifted, got %d attachments", len(atts))
	}
}

func TestExtractRawBase64Images_NotBase64Alphabet(t *testing.T) {
	// A 200-char string starting with PNG magic but
	// containing a non-base64 char should be left
	// alone — the line-scan guard rejects the whole
	// line.
	bad := "iVBORw0KGgoAAA!@#$%^&*()_+AAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="
	if isRawImagePayload(bad) {
		t.Error("non-base64 chars in payload should reject the line")
	}
}

func TestExtractRawBase64Images_HashOrToken(t *testing.T) {
	// A long base64 string that does NOT start
	// with an image magic byte (e.g. a hash, a
	// token) should be left alone.
	hash := strings.Repeat("A", 200)
	_, atts := ExtractRawBase64Images(hash)
	if len(atts) != 0 {
		t.Errorf("long non-image base64 should not be lifted, got %d", len(atts))
	}
}

// TestToMarkdown_BareBase64LiftedFromContent is the
// end-to-end regression for the user-reported "image
// shows as base64 string with no data: prefix" bug.
// The assistant's Msg.Content contains a bare PNG
// payload (the LLM dropped the data: header). The
// exporter must lift it out and render an
// `![…](data:image/png;base64,…)` markdown image.
func TestToMarkdown_BareBase64LiftedFromContent(t *testing.T) {
	conv := &memory.Conversation{ID: "c", Title: "T"}
	// Prose on its own line, payload on the next
	// line, prose again. We avoid `:` etc. in the
	// surrounding text so renderBody doesn't fence
	// the whole block in a code fence (which would
	// defeat the test of "the payload renders as
	// an image").
	in := "看这张图\n" + validPNGPayload + "\n结束"
	msgs := toFullMFs([]llm.Message{
		{Role: "assistant", Content: in},
	})
	got := ToMarkdown(conv, msgs)
	// Must contain the data: URL wrapping the
	// payload — that's the markdown image syntax
	// the exporter produces. The exact attachment
	// name ("raw-N" for the line number) is
	// internal; the user-visible contract is that
	// the PNG renders as an image, not as a
	// 100-char base64 wall.
	want := "data:image/png;base64," + validPNGPayload
	if !strings.Contains(got, want) {
		t.Errorf("expected bare PNG inlined as data: URL, got:\n%s", got)
	}
	// The bare base64 must not appear outside image
	// syntax anywhere in the output. We do a
	// rough check by removing any matched image
	// lines and asserting the payload doesn't
	// reappear as a code-fence body.
	if strings.Contains(got, "\n"+validPNGPayload+"\n") {
		t.Errorf("raw payload should not survive as standalone line, got:\n%s", got)
	}
	// Surrounding prose should still be present.
	if !strings.Contains(got, "看这张图") {
		t.Errorf("prose prefix should be preserved, got:\n%s", got)
	}
	if !strings.Contains(got, "结束") {
		t.Errorf("prose suffix should be preserved, got:\n%s", got)
	}
}
