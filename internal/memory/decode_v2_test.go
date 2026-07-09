package memory

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/llm"
)

// TestDecodeChatMessages_V2PartsBlob is the regression test
// for the v2 metadata format fix. Before the fix, rows
// persisted via snapshotStructural (`meta["parts"] =
// "<json of []MessagePart>"` with an empty content column)
// were silently dropped from GetChatMessagesWithMeta* —
// the function only recognised `meta["type"]` and the
// legacy `multi_content` / `tool_calls` shapes. This made
// the LLM context lose every assistant message the moment
// the agent started persisting parts, breaking multi-round
// tool flows and question cards. The fix recognises the
// `parts` shape and emits a single text-typed ChatMessage
// with the content extracted from the parts blob (or an
// empty content for question-only turns, which the LLM
// still needs to see as a placeholder so the tool_result
// round-trips correctly).
func TestDecodeChatMessages_V2PartsBlob(t *testing.T) {
	partsBlob := []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}{
		{Kind: "text", Text: "let me look"},
		{Kind: "text", Text: "I'll read the file"},
		{Kind: "tool", Text: ""}, // tool kind has no Text
	}
	partsJSON, _ := json.Marshal(partsBlob)
	meta := `{"parts": "` + strings.ReplaceAll(string(partsJSON), `"`, `\"`) + `"}`

	msgs := decodeChatMessages("assistant", "", meta, 0, 0)
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d (the v2 format was dropped!)", len(msgs))
	}
	m := msgs[0]
	if m.Role != "assistant" {
		t.Errorf("role = %q, want assistant", m.Role)
	}
	if m.Type != llm.TypeText {
		t.Errorf("type = %q, want text", m.Type)
	}
	// Text parts concatenated with newlines.
	want := "let me look\nI'll read the file"
	if m.Content != want {
		t.Errorf("content = %q, want %q", m.Content, want)
	}
}

// TestDecodeChatMessages_V2EmptyContent covers the
// question-only turn case: content column is empty (the
// question card's text lives entirely in parts), but the
// message must still be returned so the LLM context stays
// complete and the tool_result can be associated with the
// question tool call.
func TestDecodeChatMessages_V2EmptyContent(t *testing.T) {
	// Question part only — no text, no thinking, no tool.
	// qBlob is already a JSON string (the parts array as
	// stored in the DB); we just need to embed it as a
	// string value inside the meta envelope.
	qBlob := `[{"kind":"question","text":"{}","name":"{}","question_status":"ok"}]`
	meta := `{"parts":` + strconvQuote(qBlob) + `}`

	msgs := decodeChatMessages("assistant", "", meta, 0, 0)
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d (empty-content v2 row dropped — LLM context would lose this turn)", len(msgs))
	}
	if msgs[0].Content != "" {
		t.Errorf("content = %q, want empty", msgs[0].Content)
	}
	if msgs[0].Type != llm.TypeText {
		t.Errorf("type = %q, want text", msgs[0].Type)
	}
}

// strconvQuote is json.Marshal's string-encoder behavior in
// one place. Used by the v2 tests to build a properly-
// escaped meta envelope without dragging in json.Marshal
// (which would also require a placeholder "parts": key, not
// the raw string).
func strconvQuote(s string) string {
	// Mirror encoding/json's string encoding: " + escaped
	// chars + ". Only the chars that appear in our test
	// fixtures need handling.
	var b []byte
	b = append(b, '"')
	for _, r := range s {
		switch r {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		default:
			b = append(b, byte(r))
		}
	}
	b = append(b, '"')
	return string(b)
}

// TestDecodeChatMessages_V2PrefersContentColumn verifies
// that when the content column has text AND the parts blob
// also has text parts, the content column wins. The column
// is a denormalized cache the agent maintains during live
// streaming and is the most authoritative for the LLM
// view (matches what the user saw in the text part
// bubble during streaming).
func TestDecodeChatMessages_V2PrefersContentColumn(t *testing.T) {
	partsBlob := `[{"kind":"text","text":"parts text"}]`
	meta := `{"parts": "` + strings.ReplaceAll(partsBlob, `"`, `\"`) + `"}`

	msgs := decodeChatMessages("assistant", "denormalized text", meta, 0, 0)
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d", len(msgs))
	}
	if msgs[0].Content != "denormalized text" {
		t.Errorf("content = %q, want %q (content column should win over parts blob)", msgs[0].Content, "denormalized text")
	}
}

// TestDecodeChatMessages_TrulyEmptyStillDropped ensures
// the genuinely-empty case (no content, no metadata) is
// still dropped. The pre-fix code dropped this AND the v2
// format by accident; the fix only drops this. The `{}` +
// content case is still preserved (a no-op metadata row
// with text content has a clean text-message decode).
func TestDecodeChatMessages_TrulyEmptyStillDropped(t *testing.T) {
	if msgs := decodeChatMessages("assistant", "", "", 0, 0); msgs != nil {
		t.Errorf("want nil for truly empty row, got %+v", msgs)
	}
	// Sanity: `{}` + content still produces a text message
	// (this path was always preserved; the fix only
	// extended the metadata-recognised set).
	if msgs := decodeChatMessages("user", "hi", "{}", 0, 0); len(msgs) != 1 || msgs[0].Content != "hi" {
		t.Errorf("{{}}+content should still produce 1 text message, got %+v", msgs)
	}
}

// TestDecodeChatMessages_NewFormatTypeKeyStillWorks
// pins the v1 (type-key) format path. The v2 (parts blob)
// path is now preferred but the v1 path is still in use
// for explicit-type-key messages and shouldn't regress.
func TestDecodeChatMessages_NewFormatTypeKeyStillWorks(t *testing.T) {
	meta := `{"type":"text","name":"foo.txt"}`
	msgs := decodeChatMessages("user", "hello", meta, 0, 0)
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d", len(msgs))
	}
	if msgs[0].Type != "text" {
		t.Errorf("type = %q, want text", msgs[0].Type)
	}
	if msgs[0].Name != "foo.txt" {
		t.Errorf("name = %q, want foo.txt", msgs[0].Name)
	}
}

// Compile-time check: time import is used elsewhere in the
// package; this is just to anchor the dependency in case
// future refactors prune it.
var _ = time.Now
