package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// MediaRecognitionAsset is a session-validated image, video, or audio input.
type MediaRecognitionAsset struct {
	// UploadID retains its historical name for API compatibility; it may hold
	// either a user upload id or a durable tool-asset id.
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

// MediaResolver resolves an opaque media reference only when it belongs to the
// active session.
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
	InputRef    string   `json:"input_ref,omitempty"`
	InputRefs   []string `json:"input_refs,omitempty"`
	ContextRefs []string `json:"context_refs,omitempty"`
	ContextMode string   `json:"context_mode,omitempty"`
	// UploadID/UploadIDs remain accepted for existing conversations and older
	// model prompts. New calls should use the storage-neutral input_ref fields.
	UploadID  string   `json:"upload_id"`
	UploadIDs []string `json:"upload_ids,omitempty"`
	Question  string   `json:"question,omitempty"`
}

func handleMediaRecognize(ctx context.Context, argsRaw json.RawMessage) (*CallResult, error) {
	var args mediaRecognizeArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return &CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
	}
	inputRefs := normalizeImageUploadIDs(args.InputRef, args.InputRefs)
	inputRefs = normalizeImageUploadIDs("", append(inputRefs, normalizeImageUploadIDs(args.UploadID, args.UploadIDs)...))
	sessionID, _ := ctx.Value(SessionIDKey{}).(string)
	resolver, _ := ctx.Value(mediaResolverKey{}).(MediaResolver)
	recognizer, _ := ctx.Value(mediaRecognizerKey{}).(MediaRecognizer)
	if sessionID == "" || resolver == nil || recognizer == nil {
		return &CallResult{Content: "media_recognize is not available in this session", IsError: true}, nil
	}
	contextExpansion, contextMode, err := resolveToolMediaContext(ctx, sessionID, MediaContextResolveRequest{
		ContextRefs:    args.ContextRefs,
		ContextMode:    args.ContextMode,
		ToolName:       "media_recognize",
		PreferredKinds: []string{"recognition"},
		InputRefs:      inputRefs,
	})
	if err != nil {
		return &CallResult{
			Content: fmt.Sprintf("media_recognize context rejected: %v", err),
			IsError: true,
			Status:  CallStatusBlocked,
			Summary: fmt.Sprintf("media_recognize context rejected: %v", err),
		}, nil
	}
	if len(inputRefs) == 0 {
		if contextMode == MediaContextModeSummarize && strings.TrimSpace(contextExpansion.Text) != "" {
			return &CallResult{
				Content:     "Reusable media context summary; no fresh media was read:\n" + strings.TrimSpace(contextExpansion.Text),
				Summary:     fmt.Sprintf("Summarized %d reusable media context(s)", len(contextExpansion.IDs)),
				ContextRefs: contextExpansion.IDs,
			}, nil
		}
		return &CallResult{Content: "input_ref or input_refs is required", IsError: true}, nil
	}
	assets := make([]MediaRecognitionAsset, 0, len(inputRefs))
	kind := ""
	for _, inputRef := range inputRefs {
		asset, err := resolver(ctx, sessionID, inputRef)
		if err != nil {
			return &CallResult{Content: "media_recognize failed to resolve media reference: " + err.Error(), IsError: true}, nil
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
	question = appendMediaContextExpansion(question, contextMode, contextExpansion)
	text, err := recognizer(ctx, MediaRecognitionRequest{Asset: assets[0], Assets: assets, Question: question})
	if err != nil {
		return &CallResult{Content: "media_recognize failed: " + err.Error(), IsError: true}, nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return &CallResult{Content: "media_recognize returned an empty result", IsError: true}, nil
	}
	return &CallResult{
		Content:     fmt.Sprintf("%s recognition result for %s:\n%s", kind, strings.Join(inputRefs, ","), text),
		Summary:     fmt.Sprintf("Recognized %d %s media asset(s)", len(assets), kind),
		ContextRefs: contextExpansion.IDs,
	}, nil
}
