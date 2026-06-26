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
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

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