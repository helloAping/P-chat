package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMediaRecognizeRoutesResolvedAttachment(t *testing.T) {
	ctx := WithSessionID(context.Background(), "session-1")
	ctx = WithMediaResolver(ctx, func(_ context.Context, sessionID, uploadID string) (MediaRecognitionAsset, error) {
		if sessionID != "session-1" || uploadID != "upl-audio" {
			t.Fatalf("resolve %q/%q", sessionID, uploadID)
		}
		return MediaRecognitionAsset{UploadID: uploadID, Name: "voice.mp3", Kind: "audio", MIME: "audio/mpeg", Data: []byte("audio")}, nil
	})
	ctx = WithMediaRecognizer(ctx, func(_ context.Context, req MediaRecognitionRequest) (string, error) {
		if req.Asset.Kind != "audio" || req.Question != "transcribe" {
			t.Fatalf("request = %#v", req)
		}
		return "hello", nil
	})

	result, err := handleMediaRecognize(ctx, json.RawMessage(`{"upload_id":"upl-audio","question":"transcribe"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.Content == "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestMediaRecognizeAcceptsStorageNeutralInputRef(t *testing.T) {
	ctx := WithSessionID(context.Background(), "session-1")
	ctx = WithMediaResolver(ctx, func(_ context.Context, sessionID, inputRef string) (MediaRecognitionAsset, error) {
		if sessionID != "session-1" || inputRef != "asset-1" {
			t.Fatalf("resolve %q/%q", sessionID, inputRef)
		}
		return MediaRecognitionAsset{UploadID: inputRef, Name: "browser-screenshot.jpg", Kind: "image", MIME: "image/jpeg", Data: []byte("image")}, nil
	})
	ctx = WithMediaRecognizer(ctx, func(_ context.Context, req MediaRecognitionRequest) (string, error) {
		return "visible page", nil
	})
	result, err := handleMediaRecognize(ctx, json.RawMessage(`{"input_ref":"asset-1"}`))
	if err != nil || result.IsError || !strings.Contains(result.Content, "asset-1") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestMediaRecognizeAppendsResolvedContext(t *testing.T) {
	ctx := WithSessionID(context.Background(), "session-1")
	ctx = WithMediaResolver(ctx, func(_ context.Context, sessionID, inputRef string) (MediaRecognitionAsset, error) {
		if sessionID != "session-1" || inputRef != "upl-image" {
			t.Fatalf("resolve %q/%q", sessionID, inputRef)
		}
		return MediaRecognitionAsset{UploadID: inputRef, Name: "table.png", Kind: "image", MIME: "image/png", Data: []byte("image")}, nil
	})
	ctx = WithMediaContextResolver(ctx, func(_ context.Context, sessionID string, req MediaContextResolveRequest) (MediaContextExpansion, error) {
		if sessionID != "session-1" || req.ContextMode != MediaContextModeContinue || strings.Join(req.ContextRefs, ",") != "mctx_1" {
			t.Fatalf("context req = %#v session=%q", req, sessionID)
		}
		return MediaContextExpansion{IDs: []string{"mctx_1"}, Text: "context_id=mctx_1\nresult_excerpt:\n     first column: A,B"}, nil
	})
	ctx = WithMediaRecognizer(ctx, func(_ context.Context, req MediaRecognitionRequest) (string, error) {
		if !strings.Contains(req.Question, "补充第二列") || !strings.Contains(req.Question, "first column: A,B") {
			t.Fatalf("question did not include resolved context:\n%s", req.Question)
		}
		return "second column: 1,2", nil
	})

	result, err := handleMediaRecognize(ctx, json.RawMessage(`{"input_ref":"upl-image","question":"补充第二列","context_mode":"continue","context_refs":["mctx_1"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || strings.Join(result.ContextRefs, ",") != "mctx_1" {
		t.Fatalf("result = %#v", result)
	}
}

func TestMediaRecognizeSummarizeContextWithoutInput(t *testing.T) {
	calledRecognizer := false
	ctx := WithSessionID(context.Background(), "session-1")
	ctx = WithMediaResolver(ctx, func(_ context.Context, _, _ string) (MediaRecognitionAsset, error) {
		t.Fatal("resolver should not read fresh media")
		return MediaRecognitionAsset{}, nil
	})
	ctx = WithMediaRecognizer(ctx, func(_ context.Context, _ MediaRecognitionRequest) (string, error) {
		calledRecognizer = true
		return "", nil
	})
	ctx = WithMediaContextResolver(ctx, func(_ context.Context, sessionID string, req MediaContextResolveRequest) (MediaContextExpansion, error) {
		if sessionID != "session-1" || req.ContextMode != MediaContextModeSummarize {
			t.Fatalf("context req = %#v session=%q", req, sessionID)
		}
		return MediaContextExpansion{IDs: []string{"mctx_1", "mctx_2"}, Text: "merged prior recognition rows"}, nil
	})

	result, err := handleMediaRecognize(ctx, json.RawMessage(`{"context_mode":"summarize","context_refs":["mctx_1","mctx_2"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || calledRecognizer || !strings.Contains(result.Content, "no fresh media was read") || strings.Join(result.ContextRefs, ",") != "mctx_1,mctx_2" {
		t.Fatalf("result = %#v called=%v", result, calledRecognizer)
	}
}
