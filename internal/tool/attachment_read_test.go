package tool

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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

func TestReadAttachmentReturnsPartialBatchAndValidUTF8(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "valid.txt")
	if err := os.WriteFile(path, []byte("中文内容继续"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := WithSessionID(context.Background(), "session-1")
	ctx = WithAttachmentReadResolver(ctx, func(_ context.Context, _ string, uploadID string) (AttachmentReadAsset, error) {
		if uploadID == "missing" {
			return AttachmentReadAsset{}, fmt.Errorf("not found")
		}
		return AttachmentReadAsset{UploadID: uploadID, Name: "valid.txt", MIME: "text/plain", Path: path}, nil
	})
	result, err := handleReadAttachment(ctx, json.RawMessage(`{"upload_ids":["missing","valid"],"offset":1,"limit":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("partial batch marked as error: %#v", result)
	}
	if !strings.Contains(result.Content, "upload_id=missing") || !strings.Contains(result.Content, "not found") {
		t.Fatalf("missing per-file error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "upload_id=valid") || !utf8.ValidString(result.Content) {
		t.Fatalf("valid attachment missing or invalid UTF-8: %q", result.Content)
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

func TestReadAttachmentReadsMultipleUploadsWithPagination(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "one.txt")
	second := filepath.Join(dir, "two.txt")
	if err := os.WriteFile(first, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("abcdefghij"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := WithSessionID(context.Background(), "session-1")
	ctx = WithAttachmentReadResolver(ctx, func(_ context.Context, sessionID, uploadID string) (AttachmentReadAsset, error) {
		if sessionID != "session-1" {
			t.Fatalf("session = %q", sessionID)
		}
		switch uploadID {
		case "one":
			return AttachmentReadAsset{UploadID: uploadID, Name: "one.txt", MIME: "text/plain", Path: first}, nil
		case "two":
			return AttachmentReadAsset{UploadID: uploadID, Name: "two.txt", MIME: "text/plain", Path: second}, nil
		default:
			t.Fatalf("unexpected upload id %q", uploadID)
			return AttachmentReadAsset{}, nil
		}
	})
	result, err := handleReadAttachment(ctx, json.RawMessage(`{"upload_ids":["one","two"],"offset":2,"limit":4}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("result = %#v", result)
	}
	for _, want := range []string{"upload_id=one", "2345", "upload_id=two", "cdef", "next offset=6"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("result missing %q: %s", want, result.Content)
		}
	}
}
