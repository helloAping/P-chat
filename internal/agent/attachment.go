package agent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/tool"
	openai "github.com/sashabaranov/go-openai"
)

// Attachment describes a user-uploaded file that hangs off a
// user message. The HTTP upload endpoint (internal/server/upload.go)
// produces one of these from a multipart file; the agent then
// expands it into a content block before sending to the LLM.
//
// Kind is one of "image", "audio", "video", "text", "file" — see the
// expandAttachments switch for the per-kind handling.
type Attachment struct {
	// ID is the server-side upload id (legacy path; the handler
	// resolves it through AttachmentResolver). Optional when
	// Data is set.
	ID string `json:"id"`
	// UploadID is the upload id the SPA posts for attachments.
	// The server stores "upl://<UploadID>" in the
	// message row instead of base64, so SQLite holds a reference while
	// the current native-media path or a scoped tool reads disk on demand.
	// Mirrored into ID by
	// UnmarshalJSON so the resolver path works unchanged.
	UploadID string `json:"upload_id,omitempty"`
	// Name is the original filename, used for the message bubble
	// and for the system prompt's "## Uploaded Attachments"
	// section.
	Name string `json:"name"`
	// Size is the file size in bytes. Computed from Data when
	// the attachment is inlined.
	Size int64 `json:"size,omitempty"`
	// Kind is "image" | "audio" | "video" | "text" | "file". Drives the
	// multi-content part type and the system prompt section.
	Kind string `json:"kind"`
	// MIME is the file's MIME type, when known.
	MIME string `json:"mime,omitempty"`
	// Data is the inline base64 (or text) fallback payload. When set, it
	// takes precedence over ID. The SPA normally sends UploadID so files
	// remain session-scoped and can be read again in later turns.
	//
	// The frontend often sends the inline payload as `url`
	// (the same field the LLM wire format uses, e.g. a
	// "data:image/png;base64,..." string for an image_url
	// part). We accept either; resolveInline() picks whichever
	// is non-empty.
	Data string `json:"data,omitempty"`
	Text string `json:"text,omitempty"`
	URL  string `json:"url,omitempty"`
}

// UnmarshalJSON mirrors UploadID into ID so the resolver path
// (which keys off a.ID) works for the SPA's upload_id payload
// without every call site branching on both fields.
func (a *Attachment) UnmarshalJSON(b []byte) error {
	type alias Attachment
	var raw alias
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*a = Attachment(raw)
	if a.ID == "" && a.UploadID != "" {
		a.ID = a.UploadID
	}
	return nil
}

// AttachmentResolver turns an Attachment into the actual file
// contents. The default implementation reads from
// ~/.p-chat/uploads/. Tests can swap in a no-op resolver that
// returns synthetic bytes.
type AttachmentResolver interface {
	// Resolve returns the on-disk file path and size for an
	// attachment, or "" + 0 if the attachment id is unknown.
	Resolve(a Attachment) (path string, size int64)
}

// DiskAttachmentResolver looks files up under baseDir/<id>-<name>.
// baseDir defaults to ~/.p-chat/uploads in production; tests pass
// a temp dir.
type DiskAttachmentResolver struct {
	BaseDir string
}

func (r *DiskAttachmentResolver) Resolve(a Attachment) (string, int64) {
	if a.ID == "" {
		return "", 0
	}
	entries, err := os.ReadDir(r.BaseDir)
	if err != nil {
		return "", 0
	}
	prefix := a.ID + "-"
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return "", 0
		}
		return filepath.Join(r.BaseDir, e.Name()), info.Size()
	}
	return "", 0
}

// --- New ChatMessage-based attachment expansion ---

// ExpandAttachmentsCM 将附件展开为消息；识图模式只保留显示/持久化数据，
// 并用包含 upload_id 的 media_recognize 引用替代主模型图片输入。
// ExpandAttachmentsCM converts a list of Attachment objects into
// separate llm.ChatMessage entries (one text msg + one per
// attachment). Media may be submitted natively or referenced through a
// recognition tool; documents become TypeFile display rows plus a
// conversation-scoped read_attachment reference.
//
// visionCapable is an optional callback; when it returns false,
// image attachments are replaced with a text marker. When
// useImageRecognition is true, images are kept for display/storage but
// withheld from the main LLM; a system reference tells the model to call
// media_recognize with the corresponding upload_id.
func ExpandAttachmentsCM(protocol string, msgs []llm.ChatMessage, atts []Attachment, r AttachmentResolver, visionCapable func() bool, useImageRecognition bool) []llm.ChatMessage {
	capabilities := []config.MediaKind(nil)
	if useImageRecognition {
		capabilities = []config.MediaKind{config.MediaImage}
	}
	return ExpandAttachmentsForCapabilitiesCM(protocol, msgs, atts, r, func(kind config.MediaKind) bool {
		return kind == config.MediaImage && visionCapable != nil && visionCapable()
	}, capabilities)
}

// ExpandAttachmentsForCapabilitiesCM routes attachments either to the active
// model or to an enabled, independently configured media-recognition tool.
func ExpandAttachmentsForCapabilitiesCM(protocol string, msgs []llm.ChatMessage, atts []Attachment, r AttachmentResolver, modelCapable func(config.MediaKind) bool, recognitionCapabilities []config.MediaKind) []llm.ChatMessage {
	if len(atts) == 0 || r == nil {
		return msgs
	}
	recognitionEnabled := make(map[config.MediaKind]bool, len(recognitionCapabilities))
	for _, kind := range recognitionCapabilities {
		recognitionEnabled[kind] = true
	}

	// Find the last user message and split text + attachments
	// into separate ChatMessage rows. All user text stays
	// first, then each attachment appends after.
	var result []llm.ChatMessage
	for _, m := range msgs {
		result = append(result, m)
	}

	for _, a := range atts {
		data, _ := resolveAttachmentData(a, r)
		if data == nil {
			continue
		}
		if len(data) == 0 {
			result = append(result, llm.ChatMessage{
				Role:        llm.RoleUser,
				Type:        llm.TypeText,
				Content:     fmt.Sprintf("(attached file %s is empty; upload bytes were not available)", a.Name),
				MsgType:     llm.MsgTypeText,
				SubmitToLLM: 1,
			})
			continue
		}

		// Non-media attachments stay in the upload store and are read
		// through one conversation-scoped tool. This keeps project roots,
		// arbitrary path permissions, and Office/PDF extraction out of the
		// upload expansion layer.
		if a.Kind == "text" || tool.SupportsAttachmentRead(a.Name, a.MIME) {
			result = append(result, attachmentReadMessages(a, data)...)
			continue
		}

		switch a.Kind {
		case "image":
			mime := imageMIME(a.Name, a.MIME)
			if recognitionEnabled[config.MediaImage] {
				result = append(result, llm.ChatMessage{
					Role:        llm.RoleUser,
					Type:        llm.TypeImage,
					Content:     base64.StdEncoding.EncodeToString(data),
					Name:        a.Name,
					MimeType:    mime,
					UploadID:    a.UploadID,
					MsgType:     llm.MsgTypeImage,
					SubmitToLLM: 0,
				})
				if a.UploadID != "" {
					result = append(result, llm.ChatMessage{
						Role: llm.RoleSystem,
						Type: llm.TypeText,
						Content: fmt.Sprintf(
							"Uploaded image available for tool-based recognition: name=%q, upload_id=%q, size=%d bytes, MIME=%s. The main model cannot see this image directly in this session. If image details are needed, call media_recognize with this upload_id, or use upload_ids to analyze multiple images in one call.",
							a.Name, a.UploadID, len(data), mime,
						),
					})
				} else {
					result = append(result, llm.ChatMessage{
						Role:    llm.RoleSystem,
						Type:    llm.TypeText,
						Content: fmt.Sprintf("An image named %q was uploaded, but it has no upload_id; media_recognize cannot access it. Ask the user to upload it again if image details are required.", a.Name),
					})
				}
				continue
			}
			if modelCapable != nil && !modelCapable(config.MediaImage) {
				result = append(result, llm.ChatMessage{
					Role:        llm.RoleUser,
					Type:        llm.TypeText,
					Content:     fmt.Sprintf("[%s: %d bytes — 当前模型不支持视觉输入，图片已上传但未发送]", a.Name, len(data)),
					MsgType:     llm.MsgTypeText,
					SubmitToLLM: 1,
				})
				continue
			}
			result = append(result, llm.ChatMessage{
				Role:        llm.RoleUser,
				Type:        llm.TypeImage,
				Content:     base64.StdEncoding.EncodeToString(data),
				Name:        a.Name,
				MimeType:    mime,
				UploadID:    a.UploadID,
				MsgType:     llm.MsgTypeImage,
				SubmitToLLM: 1,
			})
		case "audio":
			if recognitionEnabled[config.MediaAudio] {
				result = append(result, mediaRecognitionMessages(a, data, llm.TypeAudio, llm.MsgTypeAudio)...)
				continue
			}
			if modelCapable != nil && modelCapable(config.MediaAudio) {
				result = append(result, llm.ChatMessage{Role: llm.RoleUser, Type: llm.TypeAudio, Content: base64.StdEncoding.EncodeToString(data), Name: a.Name, MimeType: a.MIME, UploadID: a.UploadID, MsgType: llm.MsgTypeAudio, SubmitToLLM: 1})
				continue
			}
			result = append(result, llm.ChatMessage{
				Role:        llm.RoleUser,
				Type:        llm.TypeText,
				Content:     fmt.Sprintf("(attached audio: %s, %d bytes, MIME=%s)", a.Name, len(data), a.MIME),
				MsgType:     llm.MsgTypeText,
				SubmitToLLM: 1,
			})
		case "video":
			if recognitionEnabled[config.MediaVideo] {
				result = append(result, mediaRecognitionMessages(a, data, llm.TypeVideo, llm.MsgTypeVideo)...)
				continue
			}
			if modelCapable != nil && modelCapable(config.MediaVideo) {
				result = append(result, llm.ChatMessage{Role: llm.RoleUser, Type: llm.TypeVideo, Content: base64.StdEncoding.EncodeToString(data), Name: a.Name, MimeType: a.MIME, UploadID: a.UploadID, MsgType: llm.MsgTypeVideo, SubmitToLLM: 1})
				continue
			}
			result = append(result, llm.ChatMessage{
				Role:        llm.RoleUser,
				Type:        llm.TypeText,
				Content:     fmt.Sprintf("(attached video: %s, %d bytes, MIME=%s)", a.Name, len(data), a.MIME),
				MsgType:     llm.MsgTypeText,
				SubmitToLLM: 1,
			})
		default:
			result = append(result, unsupportedAttachmentMessages(a, data)...)
		}
	}
	return result
}

func hasReadableAttachmentContext(msgs []llm.ChatMessage, atts []Attachment) bool {
	for _, message := range msgs {
		if message.Type == llm.TypeFile && strings.TrimSpace(message.UploadID) != "" &&
			tool.SupportsAttachmentRead(message.Name, message.MimeType) {
			return true
		}
	}
	for _, attachment := range atts {
		if strings.TrimSpace(attachment.UploadID) == "" {
			continue
		}
		if attachment.Kind == "text" || tool.SupportsAttachmentRead(attachment.Name, attachment.MIME) {
			return true
		}
	}
	return false
}

func attachmentDisplayMessage(a Attachment, data []byte) llm.ChatMessage {
	mimeType := strings.TrimSpace(a.MIME)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return llm.ChatMessage{
		Role: llm.RoleUser, Type: llm.TypeFile, Content: base64.StdEncoding.EncodeToString(data),
		Name: a.Name, MimeType: mimeType, UploadID: a.UploadID, MsgType: llm.MsgTypeText, SubmitToLLM: 0,
	}
}

func attachmentReadMessages(a Attachment, data []byte) []llm.ChatMessage {
	display := attachmentDisplayMessage(a, data)
	if a.UploadID == "" {
		return []llm.ChatMessage{display, {
			Role: llm.RoleSystem, Type: llm.TypeText, SubmitToLLM: 1,
			Content: fmt.Sprintf("Attachment %q has no upload_id, so read_attachment cannot access it. Ask the user to upload it again if its contents are required.", a.Name),
		}}
	}
	return []llm.ChatMessage{display, {
		Role: llm.RoleSystem, Type: llm.TypeText, SubmitToLLM: 1,
		Content: fmt.Sprintf("Uploaded attachment available through read_attachment: name=%q, upload_id=%q, size=%d bytes, MIME=%s. Call read_attachment with this upload_id when its contents are needed; do not pass a filesystem path.", a.Name, a.UploadID, len(data), display.MimeType),
	}}
}

func unsupportedAttachmentMessages(a Attachment, data []byte) []llm.ChatMessage {
	display := attachmentDisplayMessage(a, data)
	return []llm.ChatMessage{display, {
		Role: llm.RoleSystem, Type: llm.TypeText, SubmitToLLM: 1,
		Content: fmt.Sprintf("An attachment named %q was uploaded (%d bytes, MIME=%s), but no content extractor is available for this format. Do not claim to have read it; ask the user for a supported text, PDF, Word, Excel, or PowerPoint file when its contents are required.", a.Name, len(data), display.MimeType),
	}}
}

func mediaRecognitionMessages(a Attachment, data []byte, messageType string, messageTypeID int) []llm.ChatMessage {
	media := llm.ChatMessage{
		Role: llm.RoleUser, Type: messageType, Content: base64.StdEncoding.EncodeToString(data),
		Name: a.Name, MimeType: a.MIME, UploadID: a.UploadID, MsgType: messageTypeID, SubmitToLLM: 0,
	}
	if a.UploadID == "" {
		return []llm.ChatMessage{media, {
			Role: llm.RoleSystem, Type: llm.TypeText, SubmitToLLM: 1,
			Content: fmt.Sprintf("A %s attachment named %q has no upload_id. Ask the user to upload it again before using media_recognize.", messageType, a.Name),
		}}
	}
	return []llm.ChatMessage{media, {
		Role: llm.RoleSystem, Type: llm.TypeText, SubmitToLLM: 1,
		Content: fmt.Sprintf("Uploaded %s available through media_recognize: name=%q, upload_id=%q, size=%d bytes, MIME=%s. The main model cannot inspect it directly in this session; call media_recognize when its contents are needed.", messageType, a.Name, a.UploadID, len(data), a.MIME),
	}}
}

// resolveAttachmentData returns the raw bytes for an attachment.
// Inlined data (URL or Data field) is preferred; otherwise the
// resolver is used to read from disk.
func resolveAttachmentData(a Attachment, r AttachmentResolver) ([]byte, string) {
	// Normalize the SPA's upload_id onto ID so the resolver path
	// works regardless of whether the Attachment was built by
	// UnmarshalJSON (which mirrors the fields) or by a struct
	// literal in tests/callers.
	if a.ID == "" && a.UploadID != "" {
		a.ID = a.UploadID
	}
	inlineRaw := a.Data
	if inlineRaw == "" {
		inlineRaw = a.Text
	}
	if a.URL != "" {
		inlineRaw = a.URL
	}
	if inlineRaw != "" {
		if a.Kind == "text" {
			return []byte(inlineRaw), inlineRaw
		}
		cleaned := strings.TrimPrefix(inlineRaw, "data:")
		if i := strings.Index(cleaned, ";base64,"); i >= 0 {
			cleaned = cleaned[i+len(";base64,"):]
		}
		decoded, err := base64.StdEncoding.DecodeString(cleaned)
		if err != nil {
			return nil, ""
		}
		return decoded, inlineRaw
	}

	path, _ := r.Resolve(a)
	if path == "" {
		return nil, ""
	}
	read, err := os.ReadFile(path)
	if err != nil {
		return nil, ""
	}
	return read, path
}

// --- Legacy attachment expansion (kept for backward compat) ---
// disk via the resolver, and returns a NEW user message that
// replaces the trailing user message in req.Messages with a
// multi-part OpenAI content array (text + image_url /
// input_audio / text-blocks). For non-multimodal attachments
// (text/plain) it appends the file contents as a code-fenced
// block under the user's text.
//
// If req.Messages is empty or doesn't end on a user message,
// expandAttachments returns req.Messages unchanged (caller
// decides what to do).
//
// The resolver is required; nil is a programming error.
//
// Both OpenAI and Anthropic paths populate the same MultiContent
// field on the user message. The two wire formats differ, but
// they share the same building blocks:
//
//	OpenAI  image_url  → { type: image_url, image_url: { url: "data:..." } }
//	Anthropic image    → { type: image,     source:   { type: base64, media_type, data } }
//
// The LLM client translates at the wire boundary (see
// internal/llm/anthropic.go), so the agent layer stays
// protocol-agnostic.
//
// If a VisionCapable function is supplied, image parts are only
// emitted for models that return true. Otherwise the image is
// dropped and a text marker ("(image: foo.png, N bytes; the
// current model does not support image input, file is kept at
// <path>)") is appended so the user is told *why* their image
// didn't reach the model — without it the LLM can only answer
// based on the text, leaving the user to wonder what went wrong.
// The check is "no" not "drop silently" so the user can switch
// to a vision-capable model and re-send if needed.
func expandAttachments(msgs []llm.Message, atts []Attachment, r AttachmentResolver, visionCapable func() bool) []llm.Message {
	if len(atts) == 0 || r == nil || len(msgs) == 0 {
		return msgs
	}
	last := msgs[len(msgs)-1]
	if last.Role != openai.ChatMessageRoleUser {
		// Attachments must hang off a user message. If the
		// caller forgot to append one, no-op rather than
		// synthesizing a fake message.
		return msgs
	}

	// Build the content parts in order: original text first,
	// then each attachment in declaration order. We collect into
	// a []any because OpenAI's content field is a union.
	parts := make([]openai.ChatMessagePart, 0, 1+len(atts))
	if last.Content != "" {
		parts = append(parts, openai.ChatMessagePart{
			Type: openai.ChatMessagePartTypeText,
			Text: last.Content,
		})
	}

	for _, a := range atts {
		// Two ways to get the bytes:
		//   - a.Data / a.URL: inlined by the client (the new
		//     SPA path; the message is self-contained, no disk
		//     read needed). URL is preferred for image_url
		//     attachments because the frontend already has the
		//     data: URL in that exact form.
		//   - a.ID: legacy upload-id path; resolve through the
		//     resolver and read from disk. Kept for back-compat
		//     and for non-SPA clients (e.g. the in-tree REPL).
		var data []byte
		var path string
		inlineRaw := a.Data
		if a.URL != "" {
			inlineRaw = a.URL
		}
		if inlineRaw != "" {
			if a.Kind == "text" {
				data = []byte(inlineRaw)
			} else {
				// data: URL → strip the prefix and base64-decode.
				// We expect the same shape dataURL() produces so
				// the round-trip is symmetric.
				cleaned := strings.TrimPrefix(inlineRaw, "data:")
				if i := strings.Index(cleaned, ";base64,"); i >= 0 {
					cleaned = cleaned[i+len(";base64,"):]
				}
				decoded, err := base64.StdEncoding.DecodeString(cleaned)
				if err != nil {
					parts = append(parts, openai.ChatMessagePart{
						Type: openai.ChatMessagePartTypeText,
						Text: fmt.Sprintf("(attachment %s has invalid base64 data: %v)", a.Name, err),
					})
					continue
				}
				data = decoded
			}
		} else {
			path, _ = r.Resolve(a)
			if path == "" {
				parts = append(parts, openai.ChatMessagePart{
					Type: openai.ChatMessagePartTypeText,
					Text: fmt.Sprintf("(attachment %s not found on server)", a.Name),
				})
				continue
			}
			read, err := os.ReadFile(path)
			if err != nil {
				parts = append(parts, openai.ChatMessagePart{
					Type: openai.ChatMessagePartTypeText,
					Text: fmt.Sprintf("(failed to read %s: %v)", a.Name, err),
				})
				continue
			}
			data = read
		}
		if len(data) == 0 {
			parts = append(parts, openai.ChatMessagePart{
				Type: openai.ChatMessagePartTypeText,
				Text: fmt.Sprintf("(attached file %s is empty; upload bytes were not available)", a.Name),
			})
			continue
		}
		switch a.Kind {
		case "image":
			// Skip the image_url part for models that don't accept
			// vision input. We still surface a one-line marker so
			// the user (and the LLM) know the upload happened and
			// can re-send with a vision-capable model.
			if visionCapable != nil && !visionCapable() {
				parts = append(parts, openai.ChatMessagePart{
					Type: openai.ChatMessagePartTypeText,
					Text: fmt.Sprintf("(attached image: %s, %d bytes — current model does not support image input; bytes are kept in the message itself)",
						a.Name, len(data)),
				})
				continue
			}

			mime := imageMIME(a.Name, a.MIME)
			// When the bytes were inlined, we already have a
			// data: URL in a.Data / a.URL; otherwise wrap the raw
			// bytes for the LLM.
			url := inlineRaw
			if url == "" {
				url = dataURL(mime, data)
			}
			parts = append(parts, openai.ChatMessagePart{
				Type: openai.ChatMessagePartTypeImageURL,
				ImageURL: &openai.ChatMessageImageURL{
					URL: url,
				},
			})
		case "audio":
			// The go-openai v1.30 library doesn't model the
			// input_audio content block. Most chat-completions
			// servers (including the ones we use) reject unknown
			// parts, so we don't synthesize a fake one. Instead
			// we just tell the model the user attached an audio
			// file and continue with text. A future upgrade to
			// go-openai v1.41+ can re-enable the real block.
			parts = append(parts, openai.ChatMessagePart{
				Type: openai.ChatMessagePartTypeText,
				Text: fmt.Sprintf("(attached audio: %s, %d bytes, MIME=%s)",
					a.Name, len(data), a.MIME),
			})
		case "text":
			// Cap the text dump at 200 KB to keep requests
			// sane. Larger files just get a marker.
			const maxTextDump = 200 << 10
			body := string(data)
			if len(body) > maxTextDump {
				body = body[:maxTextDump] + "\n... (truncated)"
			}
			parts = append(parts, openai.ChatMessagePart{
				Type: openai.ChatMessagePartTypeText,
				Text: fmt.Sprintf("--- %s ---\n%s", a.Name, body),
			})
		default:
			// kind=file: unknown binary, just announce.
			parts = append(parts, openai.ChatMessagePart{
				Type: openai.ChatMessagePartTypeText,
				Text: fmt.Sprintf("(attached file: %s, %d bytes, kind=%s)", a.Name, len(data), a.Kind),
			})
		}
	}

	// Re-serialize parts. The library has a dedicated
	// MultiContent field for the multi-part form, separate from
	// the plain string Content. Set both: when there's only one
	// text part the server will see just `content`, when there
	// are multiple parts it'll see `content` (empty) + the
	// MultiContent array.
	newMsg := openai.ChatCompletionMessage{
		Role:         openai.ChatMessageRoleUser,
		MultiContent: parts,
	}
	out := make([]llm.Message, 0, len(msgs))
	out = append(out, msgs[:len(msgs)-1]...)
	out = append(out, newMsg)
	return out
}

// ExpandAttachments is the protocol-aware dispatcher used by the
// agent layer when building the message list for a chat call.
// `protocol` is the value of cfg.LLM.Providers[i].GetProtocol()
// ("openai" or "anthropic"). Unknown protocols fall back to the
// OpenAI shape; the LLM client decides what to do with it.
//
// Both protocols share the same MultiContent representation at
// the agent layer (text + image_url). The LLM client
// serialises per protocol at the wire boundary.
//
// visionCapable is an optional callback that returns whether
// the *current* (provider, model) pair supports image input.
// When nil or returning true, image attachments are inlined as
// image_url parts as before. When false, images are dropped
// and a short text marker is added instead.
func ExpandAttachments(protocol string, msgs []llm.Message, atts []Attachment, r AttachmentResolver, visionCapable func() bool) []llm.Message {
	return expandAttachments(msgs, atts, r, visionCapable)
}

// imageMIME returns the MIME type for an image attachment. Uses
// the stored MIME when it's a valid image type; otherwise falls
// back to extension-based detection. This ensures the data URL
// always carries the correct media type for the LLM.
func imageMIME(name, storedMIME string) string {
	if storedMIME != "" && strings.HasPrefix(storedMIME, "image/") {
		return storedMIME
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".svg":
		return "image/svg+xml"
	default:
		return "image/png"
	}
}

// dataURL is a small helper that builds a data: URL from the
// MIME type and raw bytes. Equivalent to the standard form
// "data:<mime>;base64,<...>".
func dataURL(mime string, data []byte) string {
	var buf bytes.Buffer
	buf.WriteString("data:")
	buf.WriteString(mime)
	buf.WriteString(";base64,")
	enc := base64.NewEncoder(base64.StdEncoding, &buf)
	_, _ = enc.Write(data)
	_ = enc.Close()
	return buf.String()
}
