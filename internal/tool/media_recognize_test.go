package tool

import (
	"context"
	"encoding/json"
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
