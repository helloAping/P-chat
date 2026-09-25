package tool

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadAttachmentExtractsSessionValidatedPresentation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slides.pptx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	slide, err := zw.Create("ppt/slides/slide1.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := slide.Write([]byte(`<p:sld><a:t>Quarterly revenue grew</a:t></p:sld>`)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	ctx := WithSessionID(context.Background(), "session-1")
	ctx = WithAttachmentReadResolver(ctx, func(_ context.Context, sessionID, uploadID string) (AttachmentReadAsset, error) {
		if sessionID != "session-1" || uploadID != "upload-1" {
			t.Fatalf("resolve %q/%q", sessionID, uploadID)
		}
		return AttachmentReadAsset{UploadID: uploadID, Name: "slides.pptx", MIME: "application/vnd.openxmlformats-officedocument.presentationml.presentation", Path: path}, nil
	})
	result, err := handleReadAttachment(ctx, json.RawMessage(`{"upload_id":"upload-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Content, "Quarterly revenue grew") {
		t.Fatalf("result = %#v", result)
	}
}

func TestReadAttachmentRejectsArbitraryBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.zip")
	if err := os.WriteFile(path, []byte("binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := WithSessionID(context.Background(), "session-1")
	ctx = WithAttachmentReadResolver(ctx, func(context.Context, string, string) (AttachmentReadAsset, error) {
		return AttachmentReadAsset{UploadID: "upload-1", Name: "archive.zip", MIME: "application/zip", Path: path}, nil
	})
	result, err := handleReadAttachment(ctx, json.RawMessage(`{"upload_id":"upload-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Content, "unsupported attachment format") {
		t.Fatalf("result = %#v, want unsupported format", result)
	}
}
