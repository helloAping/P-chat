package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/llm"
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
	}, resolver, func() bool { return true })
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
	}, resolver, func() bool { return true })
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
	}, resolver, func() bool { return true })
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
	out := expandAttachments(in, nil, nil, func() bool { return true })
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

// TestExpandAttachmentsCM_UploadImage verifies the upload-id path:
// an attachment carrying UploadID (no inline Data/URL) is resolved
// from disk into base64 for the LLM, and the ChatMessage carries
// the upload id so the persistence layer can store "upl://<id>"
// instead of the bytes.
func TestExpandAttachmentsCM_UploadImage(t *testing.T) {
	dir := t.TempDir()
	id := "abcd1234567890ab"
	raw := []byte("fake-png-bytes")
	if err := os.WriteFile(filepath.Join(dir, id+"-test.png"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := &DiskAttachmentResolver{BaseDir: dir}

	in := []llm.ChatMessage{
		{Role: llm.RoleUser, Type: llm.TypeText, Content: "look at this", MsgType: llm.MsgTypeText, SubmitToLLM: 1},
	}
	out := ExpandAttachmentsCM("openai", in, []Attachment{
		{UploadID: id, Name: "test.png", Kind: "image", MIME: "image/png"},
	}, resolver, func() bool { return true }, false)
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2 (text + image)", len(out))
	}
	img := out[1]
	if img.Type != llm.TypeImage {
		t.Errorf("img.Type = %q, want image", img.Type)
	}
	if img.UploadID != id {
		t.Errorf("img.UploadID = %q, want %q", img.UploadID, id)
	}
	wantB64 := base64.StdEncoding.EncodeToString(raw)
	if img.Content != wantB64 {
		t.Errorf("img.Content = %q, want base64 %q", img.Content, wantB64)
	}
	if img.Name != "test.png" || img.MimeType != "image/png" {
		t.Errorf("img metadata = (%q, %q), want (test.png, image/png)", img.Name, img.MimeType)
	}
}

func TestExpandAttachmentsCM_EmptyUploadImageDegradesToText(t *testing.T) {
	dir := t.TempDir()
	id := "abcd1234567890ab"
	if err := os.WriteFile(filepath.Join(dir, id+"-empty.png"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := &DiskAttachmentResolver{BaseDir: dir}
	in := []llm.ChatMessage{
		{Role: llm.RoleUser, Type: llm.TypeText, Content: "look at this", MsgType: llm.MsgTypeText, SubmitToLLM: 1},
	}
	out := ExpandAttachmentsCM("openai", in, []Attachment{
		{UploadID: id, Name: "empty.png", Kind: "image", MIME: "image/png"},
	}, resolver, func() bool { return true }, false)
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want text + marker", len(out))
	}
	if out[1].Type != llm.TypeText {
		t.Fatalf("out[1].Type = %q, want text marker", out[1].Type)
	}
	if !strings.Contains(out[1].Content, "empty.png") {
		t.Fatalf("marker missing filename: %#v", out[1])
	}
}

func TestExpandAttachmentsCM_ImageRecognitionMode(t *testing.T) {
	dir := t.TempDir()
	id := "abcd1234567890ab"
	raw := []byte("fake-png-bytes")
	if err := os.WriteFile(filepath.Join(dir, id+"-test.png"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := &DiskAttachmentResolver{BaseDir: dir}
	in := []llm.ChatMessage{
		{Role: llm.RoleUser, Type: llm.TypeText, Content: "look at this", MsgType: llm.MsgTypeText, SubmitToLLM: 1},
	}
	out := ExpandAttachmentsCM("openai", in, []Attachment{
		{UploadID: id, Name: "test.png", Kind: "image", MIME: "image/png"},
	}, resolver, func() bool { return false }, true)
	if len(out) != 3 {
		t.Fatalf("len(out) = %d, want 3 (text + display image + system ref)", len(out))
	}
	img := out[1]
	if img.Type != llm.TypeImage || img.SubmitToLLM != 0 {
		t.Fatalf("image = %#v, want display-only image", img)
	}
	if img.UploadID != id {
		t.Fatalf("img.UploadID = %q, want %q", img.UploadID, id)
	}
	ref := out[2]
	if ref.Role != llm.RoleSystem || !strings.Contains(ref.Content, id) || !strings.Contains(ref.Content, "image_recognize") {
		t.Fatalf("system ref = %#v, want upload_id image_recognize hint", ref)
	}
}

func TestReplaceImagesWithRecognitionRefs(t *testing.T) {
	in := []llm.ChatMessage{
		{Role: llm.RoleUser, Type: llm.TypeText, Content: "compare", MsgType: llm.MsgTypeText, SubmitToLLM: 1},
		{Role: llm.RoleUser, Type: llm.TypeImage, Content: "AAA", Name: "a.png", MimeType: "image/png", UploadID: "upl1", MsgType: llm.MsgTypeImage, SubmitToLLM: 1},
		{Role: llm.RoleUser, Type: llm.TypeImage, Content: "BBB", Name: "b.png", MimeType: "image/png", UploadID: "upl2", MsgType: llm.MsgTypeImage, SubmitToLLM: 1},
	}
	out := replaceImagesWithRecognitionRefs(in)
	if len(out) != 3 {
		t.Fatalf("len(out) = %d, want 3", len(out))
	}
	for i, m := range out {
		if m.Type == llm.TypeImage {
			t.Fatalf("out[%d] is still image: %#v", i, m)
		}
	}
	if !strings.Contains(out[1].Content, "upl1") || !strings.Contains(out[2].Content, "upl2") {
		t.Fatalf("refs missing upload ids: %#v", out)
	}
	if !strings.Contains(out[1].Content, "image_recognize") || !strings.Contains(out[2].Content, "image_recognize") {
		t.Fatalf("refs missing tool hint: %#v", out)
	}
}

func TestReplaceHistoricalImagesWithReuploadPlaceholders(t *testing.T) {
	in := []llm.ChatMessage{
		{Role: llm.RoleSystem, Type: llm.TypeText, Content: "system", MsgType: llm.MsgTypeText, SubmitToLLM: 1},
		{Role: llm.RoleUser, Type: llm.TypeText, Content: "old text", MsgType: llm.MsgTypeText, SubmitToLLM: 1},
		{Role: llm.RoleUser, Type: llm.TypeImage, Content: "OLD_IMAGE_BASE64", Name: "old.png", MimeType: "image/png", UploadID: "old-upl", MsgType: llm.MsgTypeImage, SubmitToLLM: 1},
		{Role: llm.RoleUser, Type: llm.TypeText, Content: "current text", MsgType: llm.MsgTypeText, SubmitToLLM: 1},
		{Role: llm.RoleUser, Type: llm.TypeImage, Content: "CURRENT_IMAGE_BASE64", Name: "current.png", MimeType: "image/png", UploadID: "current-upl", MsgType: llm.MsgTypeImage, SubmitToLLM: 1},
	}

	out := replaceHistoricalImagesWithReuploadPlaceholders(in, 3)
	if len(out) != len(in) {
		t.Fatalf("len(out) = %d, want %d", len(out), len(in))
	}
	histImage := out[2]
	if histImage.Type != llm.TypeText {
		t.Fatalf("historical image Type = %q, want text placeholder", histImage.Type)
	}
	if strings.Contains(histImage.Content, "OLD_IMAGE_BASE64") {
		t.Fatalf("historical image placeholder leaked image bytes: %q", histImage.Content)
	}
	if !strings.Contains(histImage.Content, "old.png") || !strings.Contains(histImage.Content, "重新上传") {
		t.Fatalf("historical image placeholder missing filename/reupload guidance: %q", histImage.Content)
	}
	currentImage := out[4]
	if currentImage.Type != llm.TypeImage || currentImage.Content != "CURRENT_IMAGE_BASE64" {
		t.Fatalf("current image = %#v, want unchanged image payload", currentImage)
	}
}

// TestExpandAttachmentsCM_UploadIDMapsToResolver verifies the
// UnmarshalJSON mirror: a client posting "upload_id" (the SPA
// wire) resolves through the same disk path as a legacy "id".
func TestExpandAttachmentsCM_UploadIDMapsToResolver(t *testing.T) {
	dir := t.TempDir()
	id := "deadbeefcafebabe"
	if err := os.WriteFile(filepath.Join(dir, id+"-a.png"), []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	var a Attachment
	if err := json.Unmarshal([]byte(`{"upload_id":"`+id+`","name":"a.png","kind":"image","mime":"image/png"}`), &a); err != nil {
		t.Fatal(err)
	}
	if a.ID != id {
		t.Errorf("a.ID = %q, want mirrored %q", a.ID, id)
	}
	resolver := &DiskAttachmentResolver{BaseDir: dir}
	data, _ := resolveAttachmentData(a, resolver)
	if string(data) != "bytes" {
		t.Errorf("resolved data = %q, want %q", data, "bytes")
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
