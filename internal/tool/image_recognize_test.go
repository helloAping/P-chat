package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestHandleImageRecognizeRequiresSession(t *testing.T) {
	res, err := handleImageRecognize(context.Background(), json.RawMessage(`{"upload_id":"abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "session") {
		t.Fatalf("res = %#v, want session error", res)
	}
}

func TestHandleImageRecognizeCallsRecognizer(t *testing.T) {
	ctx := WithSessionID(context.Background(), "s1")
	ctx = WithImageResolver(ctx, func(ctx context.Context, sessionID, uploadID string) (ImageRecognitionImage, error) {
		if sessionID != "s1" || uploadID != "upl1" {
			t.Fatalf("resolver got session=%q upload=%q", sessionID, uploadID)
		}
		return ImageRecognitionImage{
			UploadID: uploadID,
			Name:     "a.png",
			MIME:     "image/png",
			Data:     []byte("png"),
		}, nil
	})
	ctx = WithImageRecognizer(ctx, func(ctx context.Context, req ImageRecognitionRequest) (string, error) {
		if req.Image.UploadID != "upl1" {
			t.Fatalf("recognizer upload = %q", req.Image.UploadID)
		}
		if req.Question != "read visible text" {
			t.Fatalf("question = %q", req.Question)
		}
		return "visible text: hello", nil
	})

	res, err := handleImageRecognize(ctx, json.RawMessage(`{"upload_id":"upl1","question":"read visible text"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("res is error: %#v", res)
	}
	if !strings.Contains(res.Content, "visible text: hello") || !strings.Contains(res.Content, "upload_id=upl1") {
		t.Fatalf("content = %q", res.Content)
	}
}
