package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ImageRecognitionImage is the resolved image payload for image_recognize.
// 中文：工具只接收已校验的会话图片；英文：the tool receives only a
// session-validated image payload.
type ImageRecognitionImage struct {
	UploadID string
	Name     string
	MIME     string
	Data     []byte
}

// ImageRecognitionRequest is the bounded request sent to the external
// multimodal recognizer.
type ImageRecognitionRequest struct {
	Image    ImageRecognitionImage
	Question string
}

// ImageResolver resolves an upload id only if it is referenced by the
// current session.
type ImageResolver func(ctx context.Context, sessionID, uploadID string) (ImageRecognitionImage, error)

// ImageRecognizer calls the configured multimodal model and returns text.
type ImageRecognizer func(ctx context.Context, req ImageRecognitionRequest) (string, error)

type imageResolverKey struct{}
type imageRecognizerKey struct{}

// WithImageResolver attaches the current session's image resolver.
func WithImageResolver(ctx context.Context, r ImageResolver) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, imageResolverKey{}, r)
}

// WithImageRecognizer attaches the configured multimodal recognizer.
func WithImageRecognizer(ctx context.Context, r ImageRecognizer) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, imageRecognizerKey{}, r)
}

func imageResolverFromCtx(ctx context.Context) ImageResolver {
	if v, ok := ctx.Value(imageResolverKey{}).(ImageResolver); ok {
		return v
	}
	return nil
}

func imageRecognizerFromCtx(ctx context.Context) ImageRecognizer {
	if v, ok := ctx.Value(imageRecognizerKey{}).(ImageRecognizer); ok {
		return v
	}
	return nil
}

type imageRecognizeArgs struct {
	UploadID string `json:"upload_id"`
	Question string `json:"question,omitempty"`
}

func handleImageRecognize(ctx context.Context, argsRaw json.RawMessage) (*CallResult, error) {
	var args imageRecognizeArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return &CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
	}
	args.UploadID = strings.TrimSpace(args.UploadID)
	if args.UploadID == "" {
		return &CallResult{Content: "upload_id is required", IsError: true}, nil
	}
	if args.Question == "" {
		args.Question = "Describe the image in detail. Include visible text, objects, layout, and anything relevant to the user's request."
	}
	sid, _ := ctx.Value(SessionIDKey{}).(string)
	if sid == "" {
		return &CallResult{Content: "image_recognize requires a session context", IsError: true}, nil
	}
	resolver := imageResolverFromCtx(ctx)
	if resolver == nil {
		return &CallResult{Content: "image_recognize is not available in this session", IsError: true}, nil
	}
	recognizer := imageRecognizerFromCtx(ctx)
	if recognizer == nil {
		return &CallResult{Content: "image_recognize is not configured", IsError: true}, nil
	}
	img, err := resolver(ctx, sid, args.UploadID)
	if err != nil {
		return &CallResult{Content: "image_recognize failed to resolve image: " + err.Error(), IsError: true}, nil
	}
	text, err := recognizer(ctx, ImageRecognitionRequest{
		Image:    img,
		Question: strings.TrimSpace(args.Question),
	})
	if err != nil {
		return &CallResult{Content: "image_recognize failed: " + err.Error(), IsError: true}, nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return &CallResult{Content: "image_recognize returned an empty description", IsError: true}, nil
	}
	return &CallResult{
		Content: fmt.Sprintf("Image %s (%s, upload_id=%s) recognition result:\n%s", img.Name, img.MIME, img.UploadID, text),
		Summary: "Recognized image " + img.Name,
	}, nil
}
