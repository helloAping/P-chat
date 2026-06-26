package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sashabaranov/go-openai"
)

// TestExpandAttachments_Image verifies that an image attachment
// produces an image_url part on the trailing user message.
func TestExpandAttachments_Image(t *testing.T) {
	dir := t.TempDir()
	id := "abcd1234567890ab"
	if err := os.WriteFile(filepath.Join(dir, id+"-test.png"), []byte("fake-png"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := &DiskAttachmentResolver{BaseDir: dir}

	msgs := []struct{}{} // unused
	_ = msgs
	in := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "look at this"},
	}
	out := expandAttachments(in, []Attachment{
		{ID: id, Name: "test.png", Kind: "image", MIME: "image/png"},
	}, resolver)
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
	if len(out[0].MultiContent) != 2 {
		t.Fatalf("MultiContent len = %d, want 2 (text + image)", len(out[0].MultiContent))
	}
	if out[0].MultiContent[0].Type != openai.ChatMessagePartTypeText {
		t.Errorf("part[0].Type = %v, want text", out[0].MultiContent[0].Type)
	}
	if out[0].MultiContent[1].Type != openai.ChatMessagePartTypeImageURL {
		t.Errorf("part[1].Type = %v, want image_url", out[0].MultiContent[1].Type)
	}
	if out[0].MultiContent[1].ImageURL == nil {
		t.Fatal("ImageURL is nil")
	}
	if len(out[0].MultiContent[1].ImageURL.URL) < 30 {
		t.Errorf("image URL too short: %q", out[0].MultiContent[1].ImageURL.URL)
	}
	if !containsCheck(out[0].MultiContent[1].ImageURL.URL, "data:image/png;base64,") {
		t.Errorf("image URL not a data: URL: %q", out[0].MultiContent[1].ImageURL.URL)
	}
}

// TestExpandAttachments_Text verifies that a text attachment is
// inlined as a code-fenced block.
func TestExpandAttachments_Text(t *testing.T) {
	dir := t.TempDir()
	id := "ffffffffffffffff"
	body := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(dir, id+"-main.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := &DiskAttachmentResolver{BaseDir: dir}

	in := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "what does this do?"},
	}
	out := expandAttachments(in, []Attachment{
		{ID: id, Name: "main.go", Kind: "text"},
	}, resolver)
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
	parts := out[0].MultiContent
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	if parts[1].Type != openai.ChatMessagePartTypeText {
		t.Errorf("part[1].Type = %v, want text", parts[1].Type)
	}
	if !containsCheck(parts[1].Text, "package main") {
		t.Errorf("part[1].Text = %q, missing 'package main'", parts[1].Text)
	}
	if !containsCheck(parts[1].Text, "main.go") {
		t.Errorf("part[1].Text = %q, missing filename", parts[1].Text)
	}
}

// TestExpandAttachments_AudioDoesNotBreakLib verifies the audio
// path: the go-openai v1.30 library doesn't model input_audio, so
// we fall back to a text marker. The test only asserts that the
// agent still produces a valid message and the model never sees
// an unknown content type.
func TestExpandAttachments_AudioDoesNotBreakLib(t *testing.T) {
	dir := t.TempDir()
	id := "0000aaaa1111bbbb"
	if err := os.WriteFile(filepath.Join(dir, id+"-song.mp3"), []byte("fake-mp3"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := &DiskAttachmentResolver{BaseDir: dir}

	in := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "describe"},
	}
	out := expandAttachments(in, []Attachment{
		{ID: id, Name: "song.mp3", Kind: "audio", MIME: "audio/mpeg"},
	}, resolver)
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
	parts := out[0].MultiContent
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	if parts[1].Type != openai.ChatMessagePartTypeText {
		t.Errorf("audio fallback type = %v, want text", parts[1].Type)
	}
	if !containsCheck(parts[1].Text, "song.mp3") {
		t.Errorf("audio marker missing filename: %q", parts[1].Text)
	}
}

// TestExpandAttachments_NoAttachmentsPassThrough verifies the
// no-op path: empty attachments, the message is returned
// unchanged.
func TestExpandAttachments_NoAttachmentsPassThrough(t *testing.T) {
	in := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "hi"},
	}
	out := expandAttachments(in, nil, nil)
	if len(out) != 1 {
		t.Fatalf("len = %d", len(out))
	}
	if out[0].Content != "hi" {
		t.Errorf("content = %q", out[0].Content)
	}
	if len(out[0].MultiContent) != 0 {
		t.Errorf("MultiContent = %d, want 0", len(out[0].MultiContent))
	}
}

// TestDiskAttachmentResolver_HitAndMiss verifies the resolver
// returns the right path and size for a stored upload, and "" +
// 0 for a missing one.
func TestDiskAttachmentResolver_HitAndMiss(t *testing.T) {
	dir := t.TempDir()
	id := "deadbeefcafebabe"
	if err := os.WriteFile(filepath.Join(dir, id+"-hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &DiskAttachmentResolver{BaseDir: dir}

	gotPath, gotSize := r.Resolve(Attachment{ID: id, Name: "hello.txt", Kind: "text"})
	if gotPath == "" {
		t.Fatal("Resolve hit: empty path")
	}
	if gotSize != 5 {
		t.Errorf("size = %d, want 5", gotSize)
	}

	missPath, missSize := r.Resolve(Attachment{ID: "missingid00000000", Name: "nope"})
	if missPath != "" || missSize != 0 {
		t.Errorf("miss = (%q, %d), want (\"\", 0)", missPath, missSize)
	}
}

func containsCheck(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
