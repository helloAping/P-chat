package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
)

// TestChatWithTools_IncludesCurrentDocumentContentsInFirstRequest locks down
// the current-turn document contract: the main model receives bounded text for
// every readable upload before it has an opportunity to call a tool.
func TestChatWithTools_IncludesCurrentDocumentContentsInFirstRequest(t *testing.T) {
	var (
		mu   sync.Mutex
		body string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode LLM request: %v", err)
		}
		raw, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		body = string(raw)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEContent(t, w, "已收到文档。")
	}))
	t.Cleanup(srv.Close)

	uploadDir := t.TempDir()
	for _, fixture := range []struct{ id, name, text string }{
		{"doc-one-12345678", "one.docx", "DOCUMENT ONE: launch criteria"},
		{"doc-two-12345678", "two.docx", "DOCUMENT TWO: rollback criteria"},
	} {
		writeTestDOCX(t, filepath.Join(uploadDir, fixture.id+"-"+fixture.name), fixture.text)
	}
	writeTestPDF(t, filepath.Join(uploadDir, "pdf-one-12345678-one.pdf"), "PDF ONE: security criteria")
	writeTestPDF(t, filepath.Join(uploadDir, "pdf-two-12345678-two.pdf"), "PDF TWO: release criteria")

	cfg := &config.Config{LLM: config.LLMConfig{Default: "test", Providers: []config.ProviderConfig{{
		Name: "test", Protocol: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test-model",
	}}}, Limits: config.LimitsConfig{MaxRounds: 1}}
	client, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	tool.RegisterBuiltin(registry)
	agt := New(cfg, client, nil, nil, registry)
	agt.SetAttachmentResolver(&DiskAttachmentResolver{BaseDir: uploadDir})

	for chunk := range agt.ChatWithTools(context.Background(), ChatRequest{
		Style: style.Off, Provider: "test", Model: "test-model", MaxRounds: 1,
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Type: llm.TypeText, Content: "记住这些文档", SubmitToLLM: 1}},
		Attachments: []Attachment{
			{UploadID: "doc-one-12345678", Name: "one.docx", Kind: "file", MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
			{UploadID: "doc-two-12345678", Name: "two.docx", Kind: "file", MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
			{UploadID: "pdf-one-12345678", Name: "one.pdf", Kind: "file", MIME: "application/pdf"},
			{UploadID: "pdf-two-12345678", Name: "two.pdf", Kind: "file", MIME: "application/pdf"},
		},
	}) {
		if chunk.Error != "" {
			t.Fatalf("agent error: %s", chunk.Error)
		}
	}
	mu.Lock()
	got := body
	mu.Unlock()
	for _, want := range []string{"DOCUMENT ONE: launch criteria", "DOCUMENT TWO: rollback criteria", "PDF ONE: security criteria", "PDF TWO: release criteria", "untrusted document content"} {
		if !strings.Contains(got, want) {
			t.Fatalf("first LLM request missing %q:\n%s", want, got)
		}
	}
}

func writeTestPDF(t *testing.T, path, text string) {
	t.Helper()
	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(text))
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, object := range objects {
		offsets[i+1] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for i := 1; i < len(offsets); i++ {
		fmt.Fprintf(&out, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentDocumentContextIncludesPersistedFileRowsForRegenerate(t *testing.T) {
	dir := t.TempDir()
	id := "regen-doc-123456"
	writeTestDOCX(t, filepath.Join(dir, id+"-report.docx"), "REGENERATE DOCUMENT CONTENT")
	contextText := CurrentDocumentContextForMessages(nil, []llm.ChatMessage{{
		Role: llm.RoleUser, Type: llm.TypeFile, Content: "upl://" + id,
		UploadID: id, Name: "report.docx", MimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	}}, &DiskAttachmentResolver{BaseDir: dir})
	if !strings.Contains(contextText, "REGENERATE DOCUMENT CONTENT") {
		t.Fatalf("regenerate document context missing persisted file: %s", contextText)
	}
}

func TestCurrentDocumentContextTruncatesOnUTF8Boundary(t *testing.T) {
	dir := t.TempDir()
	id := "utf8-doc-1234567"
	if err := os.WriteFile(filepath.Join(dir, id+"-notes.txt"), []byte(strings.Repeat("中", 2000)), 0o644); err != nil {
		t.Fatal(err)
	}
	got := CurrentDocumentContext([]Attachment{{UploadID: id, Name: "notes.txt", Kind: "text", MIME: "text/plain"}}, &DiskAttachmentResolver{BaseDir: dir})
	if !utf8.ValidString(got) {
		t.Fatal("document context is not valid UTF-8")
	}
}

func TestCurrentDocumentContextHasHardLimitAndDeduplicatesUploadID(t *testing.T) {
	dir := t.TempDir()
	id := "dedupe-doc-12345"
	if err := os.WriteFile(filepath.Join(dir, id+"-notes.txt"), []byte(strings.Repeat("content ", 1000)), 0o644); err != nil {
		t.Fatal(err)
	}
	longName := strings.Repeat("文档", 5000) + ".txt"
	atts := []Attachment{
		{ID: id, UploadID: id, Name: longName, Kind: "text", MIME: "text/plain"},
		{ID: id, UploadID: id, Name: "alias.txt", Kind: "text", MIME: "text/plain"},
	}
	got := CurrentDocumentContextForMessages(atts, nil, &DiskAttachmentResolver{BaseDir: dir})
	if len(got) > maxCurrentDocumentContextBytes {
		t.Fatalf("context bytes=%d, max=%d", len(got), maxCurrentDocumentContextBytes)
	}
	if count := strings.Count(got, "### Document"); count != 1 {
		t.Fatalf("same upload injected %d times: %s", count, got)
	}
}

func TestChatWithToolsIncludesPersistedDocumentForRegenerateAndAutoResume(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  func(id string) ChatRequest
	}{
		{
			name: "regenerate",
			req: func(id string) ChatRequest {
				return ChatRequest{
					Messages: []llm.ChatMessage{
						{Role: llm.RoleUser, Type: llm.TypeText, Content: "remember", SubmitToLLM: 1},
						{Role: llm.RoleUser, Type: llm.TypeFile, Content: "upl://" + id, UploadID: id, Name: "persisted.pdf", MimeType: "application/pdf"},
					},
					CurrentTurnAlreadyPersisted: true,
				}
			},
		},
		{
			name: "auto_resume",
			req: func(id string) ChatRequest {
				return ChatRequest{
					Messages:                   []llm.ChatMessage{{Role: llm.RoleUser, Type: llm.TypeText, Content: "continue", SubmitToLLM: 1}},
					DocumentContextAttachments: []Attachment{{ID: id, UploadID: id, Name: "persisted.pdf", Kind: "file", MIME: "application/pdf"}},
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				body = string(raw)
				w.Header().Set("Content-Type", "text/event-stream")
				writeSSEContent(t, w, "ok")
			}))
			defer srv.Close()

			uploadDir := t.TempDir()
			id := "persisted-123456"
			writeTestPDF(t, filepath.Join(uploadDir, id+"-persisted.pdf"), "PERSISTED DOCUMENT BODY")
			cfg := &config.Config{LLM: config.LLMConfig{Default: "test", Providers: []config.ProviderConfig{{
				Name: "test", Protocol: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test-model",
			}}}, Limits: config.LimitsConfig{MaxRounds: 1}}
			client, err := llm.NewClient(&cfg.LLM)
			if err != nil {
				t.Fatal(err)
			}
			registry := tool.NewRegistry()
			tool.RegisterBuiltin(registry)
			agt := New(cfg, client, nil, nil, registry)
			agt.SetAttachmentResolver(&DiskAttachmentResolver{BaseDir: uploadDir})
			req := tc.req(id)
			req.Style, req.Provider, req.Model, req.MaxRounds = style.Off, "test", "test-model", 1
			for chunk := range agt.ChatWithTools(context.Background(), req) {
				if chunk.Error != "" {
					t.Fatalf("agent error: %s", chunk.Error)
				}
			}
			if !strings.Contains(body, "PERSISTED DOCUMENT BODY") {
				t.Fatalf("first LLM request missed persisted document: %s", body)
			}
		})
	}
}

func writeTestDOCX(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(w, `<w:document><w:body><w:p><w:r><w:t>%s</w:t></w:r></w:p></w:body></w:document>`, text); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
