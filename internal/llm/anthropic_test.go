package llm

import (
	"encoding/json"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

// TestSplitDataURL covers the data: URL → (mime, data, ok)
// decomposition used by the Anthropic adapter when translating
// OpenAI image_url parts into Anthropic image content blocks.
func TestSplitDataURL(t *testing.T) {
	cases := []struct {
		in        string
		mime      string
		data      string
		ok        bool
	}{
		{"data:image/png;base64,AAAA", "image/png", "AAAA", true},
		{"data:image/jpeg;base64,/9j/4AAQ", "image/jpeg", "/9j/4AAQ", true},
		{"data:application/pdf;base64,JVBERi0", "application/pdf", "JVBERi0", true},
		{"https://example.com/cat.png", "", "", false},
		{"", "", "", false},
		{"data:image/png,AAAA", "", "", false}, // missing ;base64,
		{"data:image/png;base64,", "image/png", "", true}, // empty payload is still valid
	}
	for _, c := range cases {
		mime, data, ok := splitDataURL(c.in)
		if ok != c.ok || mime != c.mime || data != c.data {
			t.Errorf("splitDataURL(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.in, mime, data, ok, c.mime, c.data, c.ok)
		}
	}
}

// TestOpenAIToAnthropicContent_TextOnly verifies the simplest
// case: a plain-text message → a single Anthropic text block
// serialised as a JSON string (the MarshalJSON shortcut).
func TestOpenAIToAnthropicContent_TextOnly(t *testing.T) {
	msg := Message{Role: "user", Content: "hello"}
	blocks := openAIToAnthropicContent(msg)
	if len(blocks) != 1 || blocks[0].Type != "text" {
		t.Fatalf("expected 1 text block, got %+v", blocks)
	}
	if blocks[0].Text != "hello" {
		t.Errorf("Text = %q, want hello", blocks[0].Text)
	}
	// Round-trip through MarshalJSON to verify the wire form.
	out, err := blocks.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `"hello"` {
		t.Errorf("MarshalJSON = %s, want \"hello\"", string(out))
	}
}

// TestOpenAIToAnthropicContent_ImageFromDataURL is the key
// regression test: an OpenAI image_url part with a data: URL
// must become an Anthropic image block with a base64 source
// (not a stringified "data:..." URL inside the request body,
// which is what would break Claude).
func TestOpenAIToAnthropicContent_ImageFromDataURL(t *testing.T) {
	msg := Message{
		Role: "user",
		MultiContent: []openai.ChatMessagePart{
			{Type: "text", Text: "what's this?"},
			{Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{
				URL: "data:image/png;base64,iVBORw0KGgo=",
			}},
		},
	}
	blocks := openAIToAnthropicContent(msg)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	// First block: text
	if blocks[0].Type != "text" || blocks[0].Text != "what's this?" {
		t.Errorf("blocks[0] = %+v", blocks[0])
	}
	// Second block: image with base64 source
	img := blocks[1]
	if img.Type != "image" {
		t.Errorf("blocks[1].Type = %q, want image", img.Type)
	}
	if img.Source == nil {
		t.Fatal("blocks[1].Source is nil")
	}
	if img.Source.Type != "base64" {
		t.Errorf("source.type = %q, want base64", img.Source.Type)
	}
	if img.Source.MediaType != "image/png" {
		t.Errorf("source.media_type = %q, want image/png", img.Source.MediaType)
	}
	if img.Source.Data != "iVBORw0KGgo=" {
		t.Errorf("source.data = %q, want iVBORw0KGgo=", img.Source.Data)
	}
	// Round-trip: MarshalJSON should produce an array (not a
	// string) because there are multiple blocks.
	out, err := blocks.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "[") {
		t.Errorf("MarshalJSON should emit a JSON array for multi-block content, got %s", string(out))
	}
	// And the data URL must NOT appear in the output —
	// Anthropic wants {type, source:{type, media_type, data}}.
	if strings.Contains(string(out), "data:image") {
		t.Errorf("output still contains the data: URL: %s", string(out))
	}
}

// TestOpenAIToAnthropicContent_ImageFromHTTPSURL covers the
// remote-URL branch: when the OpenAI image_url is an https://
// link, the Anthropic side should forward it as a URL source
// (Anthropic's Messages API supports URL sources for vision).
func TestOpenAIToAnthropicContent_ImageFromHTTPSURL(t *testing.T) {
	msg := Message{
		Role: "user",
		MultiContent: []openai.ChatMessagePart{
			{Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{
				URL: "https://example.com/cat.png",
			}},
		},
	}
	blocks := openAIToAnthropicContent(msg)
	if len(blocks) != 1 || blocks[0].Type != "image" {
		t.Fatalf("expected 1 image block, got %+v", blocks)
	}
	if blocks[0].Source == nil || blocks[0].Source.Type != "url" {
		t.Fatalf("expected url source, got %+v", blocks[0].Source)
	}
	if blocks[0].Source.URL != "https://example.com/cat.png" {
		t.Errorf("source.url = %q", blocks[0].Source.URL)
	}
}

// TestAnthropicMessageMarshal_EmptyString guards against a
// regression where a message with empty Content crashes the
// serialiser. Empty content must still produce a valid JSON
// string (""), not a null or an error.
func TestAnthropicMessageMarshal_EmptyString(t *testing.T) {
	var b anthropicBlocksRaw
	out, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `""` {
		t.Errorf("empty blocks MarshalJSON = %s, want \"\"", string(out))
	}
}
