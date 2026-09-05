package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// MediaRecognitionAsset is a session-validated image, video, or audio upload.
type MediaRecognitionAsset struct {
	UploadID string
	Name     string
	Kind     string
	MIME     string
	Data     []byte
}

// MediaRecognitionRequest is sent to the configured route for one media kind.
type MediaRecognitionRequest struct {
	Asset    MediaRecognitionAsset
	Assets   []MediaRecognitionAsset
	Question string
}

// MediaResolver resolves an upload only when it belongs to the active session.
type MediaResolver func(context.Context, string, string) (MediaRecognitionAsset, error)

// MediaRecognizer analyzes resolved assets through the configured media route.
type MediaRecognizer func(context.Context, MediaRecognitionRequest) (string, error)

type mediaResolverKey struct{}
type mediaRecognizerKey struct{}

// WithMediaResolver attaches a media resolver to a tool call context.
func WithMediaResolver(ctx context.Context, resolver MediaResolver) context.Context {
	if resolver == nil {
		return ctx
	}
	return context.WithValue(ctx, mediaResolverKey{}, resolver)
}

// WithMediaRecognizer attaches a media recognizer to a tool call context.
func WithMediaRecognizer(ctx context.Context, recognizer MediaRecognizer) context.Context {
	if recognizer == nil {
		return ctx
	}
	return context.WithValue(ctx, mediaRecognizerKey{}, recognizer)
}

type mediaRecognizeArgs struct {
	UploadID  string   `json:"upload_id"`
	UploadIDs []string `json:"upload_ids,omitempty"`
	Question  string   `json:"question,omitempty"`
}

func handleMediaRecognize(ctx context.Context, argsRaw json.RawMessage) (*CallResult, error) {
	var args mediaRecognizeArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return &CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
	}
	uploadIDs := normalizeImageUploadIDs(args.UploadID, args.UploadIDs)
	if len(uploadIDs) == 0 {
		return &CallResult{Content: "upload_id or upload_ids is required", IsError: true}, nil
	}
	sessionID, _ := ctx.Value(SessionIDKey{}).(string)
	resolver, _ := ctx.Value(mediaResolverKey{}).(MediaResolver)
	recognizer, _ := ctx.Value(mediaRecognizerKey{}).(MediaRecognizer)
	if sessionID == "" || resolver == nil || recognizer == nil {
		return &CallResult{Content: "media_recognize is not available in this session", IsError: true}, nil
	}
	assets := make([]MediaRecognitionAsset, 0, len(uploadIDs))
	kind := ""
	for _, uploadID := range uploadIDs {
		asset, err := resolver(ctx, sessionID, uploadID)
		if err != nil {
			return &CallResult{Content: "media_recognize failed to resolve upload: " + err.Error(), IsError: true}, nil
		}
		if kind == "" {
			kind = asset.Kind
		} else if asset.Kind != kind {
			return &CallResult{Content: "media_recognize requires uploads of the same media type per call", IsError: true}, nil
		}
		assets = append(assets, asset)
	}
	question := strings.TrimSpace(args.Question)
	if question == "" {
		question = fmt.Sprintf("Analyze the attached %s content in detail and return factual observations relevant to the conversation.", kind)
	}
	text, err := recognizer(ctx, MediaRecognitionRequest{Asset: assets[0], Assets: assets, Question: question})
	if err != nil {
		return &CallResult{Content: "media_recognize failed: " + err.Error(), IsError: true}, nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return &CallResult{Content: "media_recognize returned an empty result", IsError: true}, nil
	}
	return &CallResult{
		Content: fmt.Sprintf("%s recognition result for %s:\n%s", kind, strings.Join(uploadIDs, ","), text),
		Summary: fmt.Sprintf("Recognized %d %s upload(s)", len(assets), kind),
	}, nil
}
