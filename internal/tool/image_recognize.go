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
	Images   []ImageRecognitionImage
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
	UploadID  string   `json:"upload_id"`
	UploadIDs []string `json:"upload_ids,omitempty"`
	Question  string   `json:"question,omitempty"`
}

func handleImageRecognize(ctx context.Context, argsRaw json.RawMessage) (*CallResult, error) {
	var args imageRecognizeArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return &CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
	}
	uploadIDs := normalizeImageUploadIDs(args.UploadID, args.UploadIDs)
	if len(uploadIDs) == 0 {
		return &CallResult{Content: "upload_id or upload_ids is required", IsError: true}, nil
	}
	if args.Question == "" {
		if len(uploadIDs) == 1 {
			args.Question = "Describe the image in detail. Include visible text, objects, layout, and anything relevant to the user's request."
		} else {
			args.Question = "Describe all images in order. Compare them when relevant, include visible text, objects, layout, and anything relevant to the user's request."
		}
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
	images := make([]ImageRecognitionImage, 0, len(uploadIDs))
	for _, uploadID := range uploadIDs {
		img, err := resolver(ctx, sid, uploadID)
		if err != nil {
			return &CallResult{Content: "image_recognize failed to resolve image: " + err.Error(), IsError: true}, nil
		}
		images = append(images, img)
	}
	text, err := recognizer(ctx, ImageRecognitionRequest{
		Image:    images[0],
		Images:   images,
		Question: strings.TrimSpace(args.Question),
	})
	if err != nil {
		return &CallResult{Content: "image_recognize failed: " + err.Error(), IsError: true}, nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return &CallResult{Content: "image_recognize returned an empty description", IsError: true}, nil
	}
	names := make([]string, 0, len(images))
	for _, img := range images {
		names = append(names, img.Name)
	}
	if len(images) == 1 {
		img := images[0]
		return &CallResult{
			Content: fmt.Sprintf("Image %s (%s, upload_id=%s) recognition result:\n%s", img.Name, img.MIME, img.UploadID, text),
			Summary: "Recognized image " + img.Name,
		}, nil
	}
	return &CallResult{
		Content: fmt.Sprintf("Images %s (upload_ids=%s) recognition result:\n%s", strings.Join(names, ", "), strings.Join(uploadIDs, ","), text),
		Summary: fmt.Sprintf("Recognized %d images", len(images)),
	}, nil
}

func normalizeImageUploadIDs(single string, many []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 1+len(many))
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	add(single)
	for _, id := range many {
		add(id)
	}
	return out
}
