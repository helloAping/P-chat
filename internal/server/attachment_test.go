// Package server — attachment flow tests.
//
// The full streaming end-to-end is covered by the LLM client
// unit tests (internal/llm/anthropic_test.go +
// internal/llm/model_test.go). These tests focus on the
// server-side boundary: upload persistence, JSON shape, and
// request acceptance.
package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/llm"
)

// TestSendMessageRequest_AcceptsUploadID verifies the SPA wire
// field: an attachment posted with "upload_id" (no inline bytes)
// maps onto agent.Attachment.ID so the resolver path works, and
// UploadID is preserved for the upl:// persistence layer.
func TestSendMessageRequest_AcceptsUploadID(t *testing.T) {
	in := `{
        "message": "hi",
        "attachments": [
            {"upload_id": "abcd1234567890ab", "name": "a.png", "kind": "image", "mime": "image/png"}
        ]
    }`
	var r SendMessageRequest
	if err := json.Unmarshal([]byte(in), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Attachments) != 1 {
		t.Fatalf("Attachments = %d, want 1", len(r.Attachments))
	}
	a := r.Attachments[0]
	if a.UploadID != "abcd1234567890ab" {
		t.Errorf("UploadID = %q", a.UploadID)
	}
	if a.ID != "abcd1234567890ab" {
		t.Errorf("ID = %q, want mirrored from upload_id", a.ID)
	}
}

// TestBuildMessageResponse_UploadRef verifies the history path for
// rows persisted as "upl://<id>": buildMessageResponse must surface
// a relative /api/v1/uploads/<id> URL (not a data: URL) so the
// frontend fetches the bytes on demand, while legacy base64 rows
// keep the data: URL form.
func TestBuildMessageResponse_UploadRef(t *testing.T) {
	id := "abcd1234567890ab"

	ref := buildMessageResponse(llm.ChatMessage{
		Role: llm.RoleUser, Type: llm.TypeImage, Content: "upl://" + id,
		Name: "a.png", MimeType: "image/png", MsgType: llm.MsgTypeImage, SubmitToLLM: 1,
	}, nil, nil, 0, 1, 1, "", false)
	if ref == nil {
		t.Fatal("upload-ref row returned nil")
	}
	if len(ref.Attachments) != 1 {
		t.Fatalf("Attachments = %d, want 1", len(ref.Attachments))
	}
	att := ref.Attachments[0]
	if att.URL != "/api/v1/uploads/"+id {
		t.Errorf("URL = %q, want %q", att.URL, "/api/v1/uploads/"+id)
	}
	if att.Type != "image_url" || att.Kind != "image" || att.MIME != "image/png" || att.Name != "a.png" {
		t.Errorf("attachment meta = %+v", att)
	}

	b64 := base64.StdEncoding.EncodeToString([]byte("legacy-bytes"))
	legacy := buildMessageResponse(llm.ChatMessage{
		Role: llm.RoleUser, Type: llm.TypeImage, Content: b64,
		Name: "old.png", MimeType: "image/png", MsgType: llm.MsgTypeImage, SubmitToLLM: 1,
	}, nil, nil, 0, 2, 2, "", false)
	if legacy == nil || len(legacy.Attachments) != 1 {
		t.Fatalf("legacy row: resp=%v atts=%d", legacy, len(legacy.Attachments))
	}
	if want := "data:image/png;base64," + b64; legacy.Attachments[0].URL != want {
		t.Errorf("legacy URL = %q, want data: URL", legacy.Attachments[0].URL)
	}
}

// TestResolveHistoryUploads_RehydratesBase64 verifies the LLM
// context path: upl:// rows are re-read from disk into base64 so
// the model sees the image, and a missing file degrades to a text
// marker instead of breaking the turn.
func TestResolveHistoryUploads_RehydratesBase64(t *testing.T) {
	dir := t.TempDir()
	id := "abcd1234567890ab"
	raw := []byte("fake-png-bytes")
	if err := os.WriteFile(filepath.Join(dir, id+"-a.png"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := &agent.DiskAttachmentResolver{BaseDir: dir}

	msgs := []llm.ChatMessage{
		{Role: llm.RoleUser, Type: llm.TypeText, Content: "plain", MsgType: llm.MsgTypeText, SubmitToLLM: 1},
		{Role: llm.RoleUser, Type: llm.TypeImage, Content: "upl://" + id, Name: "a.png", MimeType: "image/png", MsgType: llm.MsgTypeImage, SubmitToLLM: 1},
		{Role: llm.RoleUser, Type: llm.TypeImage, Content: "upl://" + "deadbeefcafebabe", Name: "gone.png", MimeType: "image/png", MsgType: llm.MsgTypeImage, SubmitToLLM: 1},
	}
	out := resolveHistoryUploads(msgs, resolver)
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3", len(out))
	}
	if out[0].Content != "plain" {
		t.Errorf("plain row changed: %q", out[0].Content)
	}
	if out[1].Content != base64.StdEncoding.EncodeToString(raw) {
		t.Errorf("image row not rehydrated: %q", out[1].Content)
	}
	if out[1].Type != llm.TypeImage || out[1].UploadID != id {
		t.Errorf("image row metadata = (%q, %q)", out[1].Type, out[1].UploadID)
	}
	if out[2].Type != llm.TypeText || out[2].Content == "upl://deadbeefcafebabe" {
		t.Errorf("missing file should degrade to a text marker: %+v", out[2])
	}
}

// TestUploadAndSend_Image uploads a tiny PNG via /api/v1/uploads
// and verifies the file is on disk + the response carries the
// metadata the web UI uses to attach it to the next message.
func TestUploadAndSend_Image(t *testing.T) {
	s, _ := newTestServerWithConfig(t, richTestConfigJSON)

	// 1x1 transparent PNG: 8-byte signature + IHDR + IDAT + IEND
	png := []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0d, 'I', 'D', 'A', 'T',
		0x78, 0x9c, 0x62, 0x00, 0x01, 0x00, 0x00, 0x05,
		0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00,
		0x00, 0x00, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
	}
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, err := mw.CreateFormFile("file", "dot.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(fw, bytes.NewReader(png)); err != nil {
		t.Fatal(err)
	}
	_ = mw.Close()

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/uploads", body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	s.engine.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("upload: status=%d body=%s", w.Code, w.Body.String())
	}
	var up UploadMeta
	if err := json.NewDecoder(w.Body).Decode(&up); err != nil {
		t.Fatal(err)
	}
	if up.Kind != "image" {
		t.Errorf("Kind = %q, want image", up.Kind)
	}
	if up.ID == "" {
		t.Error("ID is empty")
	}
	if up.Size != int64(len(png)) {
		t.Errorf("Size = %d, want %d", up.Size, len(png))
	}
	// Sanity: the file is on disk. StoredAs is json:"-", so
	// re-derive the path from the response.
	wantPath := filepath.Join(UploadDir(), up.ID+"-"+up.Name)
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("uploaded file missing at %s: %v", wantPath, err)
	}
}

// TestSendMessageRequest_AcceptsAttachments verifies the JSON
// shape of the new field round-trips through the gin binding.
func TestSendMessageRequest_AcceptsAttachments(t *testing.T) {
	in := `{
        "message": "hi",
        "style": "tech",
        "attachments": [
            {"id": "abc123", "name": "a.png", "kind": "image", "mime": "image/png"},
            {"id": "def456", "name": "b.txt", "kind": "text",  "mime": "text/plain"}
        ]
    }`
	var r SendMessageRequest
	if err := json.Unmarshal([]byte(in), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Attachments) != 2 {
		t.Fatalf("Attachments = %d, want 2", len(r.Attachments))
	}
	if r.Attachments[0].ID != "abc123" || r.Attachments[0].Kind != "image" {
		t.Errorf("attachment[0] = %+v", r.Attachments[0])
	}
	if r.Attachments[1].ID != "def456" || r.Attachments[1].Kind != "text" {
		t.Errorf("attachment[1] = %+v", r.Attachments[1])
	}
}

// TestUploadRefsAndPrune covers the D3 orphan-upload cleanup: refs
// are collected per conversation, files are pruned only when no row
// references them (across ALL conversations), and the startup sweep
// respects the mtime grace period.
func TestUploadRefsAndPrune(t *testing.T) {
	s, _ := newTestServer(t)
	convA, _ := s.handler.store.NewConversation()
	convB, _ := s.handler.store.NewConversation()

	// Two uploads on disk: one referenced by convA, one orphan.
	upA := "abcd1234567890ab"
	upB := "deadbeefcafebabe"
	dir := UploadDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, upA+"-a.png"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, upB+"-b.png"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	// convA references upA via a message row.
	s.handler.store.AddChatMessageTo(convA, llm.ChatMessage{
		Role: llm.RoleUser, Type: llm.TypeImage, Content: "upl://" + upA,
		MsgType: llm.MsgTypeImage, SubmitToLLM: 1,
	})
	// convB references upB too — so upB is NOT a true orphan yet.
	s.handler.store.AddChatMessageTo(convB, llm.ChatMessage{
		Role: llm.RoleUser, Type: llm.TypeImage, Content: "upl://" + upB,
		MsgType: llm.MsgTypeImage, SubmitToLLM: 1,
	})
	if err := s.handler.store.Flush(); err != nil {
		t.Fatal(err)
	}

	// Ref collection for convA → only upA.
	refs := s.handler.store.UploadRefsForConversation(convA)
	if len(refs) != 1 || refs[0] != upA {
		t.Fatalf("UploadRefsForConversation = %v, want [%s]", refs, upA)
	}
	// Count across all conversations: both referenced → neither pruned.
	s.handler.pruneUploadFiles([]string{upA, upB})
	if _, err := os.Stat(filepath.Join(dir, upA+"-a.png")); err != nil {
		t.Errorf("upA pruned while still referenced by convA: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, upB+"-b.png")); err != nil {
		t.Errorf("upB pruned while still referenced by convB: %v", err)
	}

	// Clear convA → upA now unreferenced → prune removes only upA.
	if err := s.handler.store.ClearMessages(convA); err != nil {
		t.Fatal(err)
	}
	s.handler.pruneUploadFiles([]string{upA})
	if _, err := os.Stat(filepath.Join(dir, upA+"-a.png")); !os.IsNotExist(err) {
		t.Errorf("upA file still exists after convA cleared (want pruned)")
	}
	if _, err := os.Stat(filepath.Join(dir, upB+"-b.png")); err != nil {
		t.Errorf("upB pruned despite still being referenced by convB")
	}

	// Sweep: upB is still referenced, so even past-grace it survives.
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(filepath.Join(dir, upB+"-b.png"), old, old)
	n := s.handler.sweepOrphanUploads(24 * time.Hour)
	if n != 0 {
		t.Errorf("sweep removed %d files, want 0 (upB still referenced)", n)
	}

	// Now drop convB too and add a fresh (young) orphan file that the
	// grace period protects.
	upC := "0123456789abcdef"
	if err := os.WriteFile(filepath.Join(dir, upC+"-c.png"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.handler.store.ClearMessages(convB); err != nil {
		t.Fatal(err)
	}
	// upB referenced nowhere now → pruned by prune (unreferenced), upC young → kept by sweep.
	s.handler.pruneUploadFiles([]string{upB})
	if _, err := os.Stat(filepath.Join(dir, upB+"-b.png")); !os.IsNotExist(err) {
		t.Errorf("upB file still exists after convB cleared")
	}
	n = s.handler.sweepOrphanUploads(24 * time.Hour)
	if n != 0 {
		t.Errorf("sweep removed %d files, want 0 (upC younger than grace)", n)
	}
	// Age upC past grace → sweep removes it.
	old2 := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(filepath.Join(dir, upC+"-c.png"), old2, old2)
	n = s.handler.sweepOrphanUploads(24 * time.Hour)
	if n != 1 {
		t.Errorf("sweep removed %d files, want 1 (aged orphan upC)", n)
	}
}
