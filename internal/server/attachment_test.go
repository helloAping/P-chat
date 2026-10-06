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
	"strings"
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

func TestBuildMessageResponse_FileUploadRef(t *testing.T) {
	id := "abcd1234567890ab"
	resp := buildMessageResponse(llm.ChatMessage{
		Role: llm.RoleUser, Type: llm.TypeFile, Content: "upl://" + id,
		Name: "report.docx", MimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", SubmitToLLM: 0,
	}, nil, nil, 0, 1, 1, "", false)
	if resp == nil || len(resp.Attachments) != 1 {
		t.Fatalf("response = %#v, want one file attachment", resp)
	}
	att := resp.Attachments[0]
	if att.Type != "text" || att.Kind != "file" || att.URL != "/api/v1/uploads/"+id || att.Name != "report.docx" {
		t.Fatalf("attachment = %#v", att)
	}
	if resp.Content != "" {
		t.Fatalf("content = %q, want hidden upl reference", resp.Content)
	}
}

func TestBuildLLMMessagesKeepsAllUploadReferences(t *testing.T) {
	refs := []llm.ChatMessage{
		{Role: llm.RoleUser, Type: llm.TypeImage, UploadID: "image-ref", SubmitToLLM: 0},
		{Role: llm.RoleUser, Type: llm.TypeAudio, UploadID: "audio-ref", SubmitToLLM: 0},
		{Role: llm.RoleUser, Type: llm.TypeVideo, UploadID: "video-ref", SubmitToLLM: 0},
		{Role: llm.RoleUser, Type: llm.TypeFile, UploadID: "file-ref", SubmitToLLM: 0},
	}
	out := buildLLMMessages(refs)
	if len(out) != len(refs) {
		t.Fatalf("len(out) = %d, want %d attachment references", len(out), len(refs))
	}
}

func TestBuildRegenerateMessagesKeepsFileAttachment(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: llm.RoleUser, Type: llm.TypeText, Content: "remember this", MsgType: llm.MsgTypeText, SubmitToLLM: 1},
		{Role: llm.RoleUser, Type: llm.TypeFile, Content: "upl://abcd1234567890ab", UploadID: "abcd1234567890ab", Name: "report.pdf", SubmitToLLM: 0},
	}
	out, historyCount, ok := buildRegenerateMessages(msgs, []int64{10, 11}, 10)
	if !ok || historyCount != 0 || len(out) != 2 {
		t.Fatalf("ok=%v history=%d messages=%#v", ok, historyCount, out)
	}
	if out[1].Type != llm.TypeFile || out[1].UploadID != "abcd1234567890ab" {
		t.Fatalf("file attachment was dropped: %#v", out[1])
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
		{Role: llm.RoleUser, Type: llm.TypeImage, Content: "upl://" + id, Name: "a.png", MimeType: "image/png", UploadID: id, MsgType: llm.MsgTypeImage, SubmitToLLM: 1},
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

func TestResolveHistoryUploads_EmptyUploadDegradesToText(t *testing.T) {
	dir := t.TempDir()
	id := "abcd1234567890ab"
	if err := os.WriteFile(filepath.Join(dir, id+"-empty.png"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := &agent.DiskAttachmentResolver{BaseDir: dir}

	msgs := []llm.ChatMessage{
		{Role: llm.RoleUser, Type: llm.TypeImage, Content: "upl://" + id, Name: "empty.png", MimeType: "image/png", MsgType: llm.MsgTypeImage, SubmitToLLM: 1},
	}
	out := resolveHistoryUploads(msgs, resolver)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0].Type != llm.TypeText {
		t.Fatalf("Type = %q, want text marker", out[0].Type)
	}
	if out[0].Content == "" || out[0].Content == "upl://"+id {
		t.Fatalf("Content = %q, want non-empty marker", out[0].Content)
	}
}

func TestResolveHistoryUploads_PreservesFileReference(t *testing.T) {
	dir := t.TempDir()
	id := "abcd1234567890ab"
	if err := os.WriteFile(filepath.Join(dir, id+"-report.pdf"), []byte("large document bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := &agent.DiskAttachmentResolver{BaseDir: dir}
	out := resolveHistoryUploads([]llm.ChatMessage{{
		Role: llm.RoleUser, Type: llm.TypeFile, Content: "upl://" + id,
		Name: "report.pdf", SubmitToLLM: 0,
	}}, resolver)
	if len(out) != 1 || out[0].Type != llm.TypeFile || out[0].Content != "upl://"+id || out[0].UploadID != id {
		t.Fatalf("document was not preserved as upload ref: %#v", out)
	}
}

// TestUploadAndSend_Image uploads a tiny PNG via /api/v1/uploads
// and verifies the file is on disk + the response carries the
// metadata the web UI uses to attach it to the next message.
func TestUploadAndSend_Image(t *testing.T) {
	s, _ := newTestServerWithConfig(t, richTestConfigJSON)
	sessionID, _ := s.handler.store.NewConversation()

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
	if err := mw.WriteField("session_id", sessionID); err != nil {
		t.Fatal(err)
	}
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

	missingSession := httptest.NewRecorder()
	s.engine.ServeHTTP(missingSession, httptest.NewRequest(http.MethodGet, "/api/v1/uploads/"+up.ID, nil))
	if missingSession.Code != http.StatusBadRequest {
		t.Fatalf("download without session status=%d, want 400", missingSession.Code)
	}
	otherSession, _ := s.handler.store.NewConversation()
	wrongSession := httptest.NewRecorder()
	s.engine.ServeHTTP(wrongSession, httptest.NewRequest(http.MethodGet, "/api/v1/uploads/"+up.ID+"?session_id="+otherSession, nil))
	if wrongSession.Code != http.StatusForbidden {
		t.Fatalf("cross-session download status=%d, want 403", wrongSession.Code)
	}
	ownerSession := httptest.NewRecorder()
	s.engine.ServeHTTP(ownerSession, httptest.NewRequest(http.MethodGet, "/api/v1/uploads/"+up.ID+"?session_id="+sessionID, nil))
	if ownerSession.Code != http.StatusOK {
		t.Fatalf("owner download status=%d body=%s", ownerSession.Code, ownerSession.Body.String())
	}
}

func TestUploadRequiresKnownSession(t *testing.T) {
	s, _ := newTestServerWithConfig(t, richTestConfigJSON)
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	if _, err := mw.CreateFormFile("file", "notes.pdf"); err != nil {
		t.Fatal(err)
	}
	_ = mw.Close()

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/uploads", body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	s.engine.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestValidateAttachmentOwnershipRejectsOtherSession(t *testing.T) {
	s, _ := newTestServer(t)
	owner, _ := s.handler.store.NewConversation()
	other, _ := s.handler.store.NewConversation()
	id := "abcd1234567890ab"
	if err := os.MkdirAll(UploadDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(UploadDir(), id+"-report.pdf"), []byte("pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeUploadOwner(id, owner); err != nil {
		t.Fatal(err)
	}
	if err := s.handler.validateAttachmentOwnership(other, []agent.Attachment{{UploadID: id, Name: "report.pdf", Kind: "file"}}); err == nil {
		t.Fatal("cross-session upload was accepted")
	}
	if err := s.handler.validateAttachmentOwnership(owner, []agent.Attachment{{UploadID: id, Name: "report.pdf", Kind: "file"}}); err != nil {
		t.Fatalf("owner upload rejected: %v", err)
	}
}

func TestMaterializeInlineFileAttachmentBindsOwner(t *testing.T) {
	s, _ := newTestServer(t)
	sessionID, _ := s.handler.store.NewConversation()
	atts := []agent.Attachment{{
		Name: "report.pdf", Kind: "file", MIME: "application/pdf",
		Data: "data:application/pdf;base64," + base64.StdEncoding.EncodeToString([]byte("pdf bytes")),
	}}
	if err := s.handler.materializeInlineFileAttachments(sessionID, atts); err != nil {
		t.Fatal(err)
	}
	if atts[0].UploadID == "" || atts[0].ID != atts[0].UploadID || atts[0].Data != "" {
		t.Fatalf("inline attachment was not materialized: %+v", atts[0])
	}
	owner, found, err := readUploadOwner(atts[0].UploadID)
	if err != nil || !found || owner != sessionID {
		t.Fatalf("owner = %q found=%v err=%v", owner, found, err)
	}
}

func TestMaterializeInlineTextAttachmentBindsOwner(t *testing.T) {
	s, _ := newTestServer(t)
	sessionID, _ := s.handler.store.NewConversation()
	atts := []agent.Attachment{{Name: "notes.txt", Kind: "text", MIME: "text/plain", Data: "纯文本内容"}}
	if err := s.handler.materializeInlineFileAttachments(sessionID, atts); err != nil {
		t.Fatal(err)
	}
	if atts[0].UploadID == "" || atts[0].Data != "" {
		t.Fatalf("inline text was not materialized: %+v", atts[0])
	}
}

func TestMaterializeInlineAttachmentsRollsBackBatchOnFailure(t *testing.T) {
	s, _ := newTestServer(t)
	sessionID, _ := s.handler.store.NewConversation()
	before, _ := os.ReadDir(UploadDir())
	atts := []agent.Attachment{
		{Name: "notes.txt", Kind: "text", MIME: "text/plain", Data: "first succeeds"},
		{Name: "broken.pdf", Kind: "file", MIME: "application/pdf", Data: "not-a-data-url"},
	}
	if err := s.handler.materializeInlineFileAttachments(sessionID, atts); err == nil {
		t.Fatal("invalid second attachment was accepted")
	}
	after, _ := os.ReadDir(UploadDir())
	if len(after) != len(before) {
		t.Fatalf("failed batch left upload artifacts: before=%d after=%d", len(before), len(after))
	}
	if atts[0].UploadID != "" || atts[0].Data != "first succeeds" {
		t.Fatalf("failed batch mutated caller attachments: %+v", atts[0])
	}
}

func TestLatestUserDocumentAttachmentsSkipsAutoResumeNudge(t *testing.T) {
	got := latestUserDocumentAttachments([]llm.ChatMessage{
		{Role: llm.RoleUser, Type: llm.TypeText, Content: "remember"},
		{Role: llm.RoleUser, Type: llm.TypeFile, Content: "upl://aaaaaaaaaaaaaaaa", UploadID: "aaaaaaaaaaaaaaaa", Name: "a.pdf", MimeType: "application/pdf"},
		{Role: llm.RoleAssistant, Type: llm.TypeText, Content: "working"},
		{Role: llm.RoleUser, Type: llm.TypeText, Content: "continue", Meta: map[string]any{"origin": "auto_resume"}},
	})
	if len(got) != 1 || got[0].UploadID != "aaaaaaaaaaaaaaaa" {
		t.Fatalf("resume documents = %#v", got)
	}
}

func TestValidateAttachmentOwnershipRejectsMissingBytes(t *testing.T) {
	s, _ := newTestServer(t)
	sessionID, _ := s.handler.store.NewConversation()
	id := "1111222233334444"
	if err := os.MkdirAll(UploadDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeUploadOwner(id, sessionID); err != nil {
		t.Fatal(err)
	}
	if err := s.handler.validateAttachmentOwnership(sessionID, []agent.Attachment{{UploadID: id, Name: "gone.pdf", Kind: "file"}}); err == nil {
		t.Fatal("owner sidecar without upload bytes was accepted")
	}
}

func TestMergeUserAttachmentRowsRespectsParentMessage(t *testing.T) {
	msgs := []MessageResponse{
		{ID: 10, SourceMessageIDs: []int64{10}, Role: "user", Content: "first"},
		{ID: 20, SourceMessageIDs: []int64{20}, Role: "user", Content: "second"},
		{ID: 21, SourceMessageIDs: []int64{21}, Role: "user", Attachments: []AttachmentPart{{Name: "first.pdf"}}, AttachmentParentUserID: 10},
	}
	got := mergeUserAttachmentRows(msgs)
	if len(got) != 3 {
		t.Fatalf("attachment was merged into adjacent wrong turn: %#v", got)
	}
}

func TestUploadRejectsEmptyFile(t *testing.T) {
	s, _ := newTestServerWithConfig(t, richTestConfigJSON)
	sessionID, _ := s.handler.store.NewConversation()

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	if err := mw.WriteField("session_id", sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := mw.CreateFormFile("file", "empty.png"); err != nil {
		t.Fatal(err)
	}
	_ = mw.Close()

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/uploads", body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	s.engine.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("upload: status=%d body=%s, want 400", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "empty file") {
		t.Fatalf("body = %q, want empty file error", w.Body.String())
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

func TestQueuedUploadReferencePreventsPruneAndSweep(t *testing.T) {
	s, _ := newTestServer(t)
	convID, _ := s.handler.store.NewConversation()
	id := "abcdefabcdefabcd"
	dir := UploadDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+"-queued.pdf")
	if err := os.WriteFile(path, []byte("queued"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"message":"queued","client_msg_id":1730000000001002,"attachments":[{"upload_id":"abcdefabcdefabcd"}]}`)
	item, err := s.handler.store.CreateTurnQueueItem(convID, payload, "queued", 1730000000001002, 1)
	if err != nil {
		t.Fatal(err)
	}
	s.handler.pruneUploadFiles([]string{id})
	if removed := s.handler.sweepOrphanUploads(24 * time.Hour); removed != 0 {
		t.Fatalf("sweep removed %d queued upload(s)", removed)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("queued upload was pruned: %v", err)
	}
	if _, ok, err := s.handler.store.CancelTurnQueueItem(convID, item.ID); err != nil || !ok {
		t.Fatalf("cancel queue item: ok=%v err=%v", ok, err)
	}
	s.handler.pruneUploadFiles([]string{id})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unreferenced upload still exists after queue cancellation: %v", err)
	}
}
