package agent

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/p-chat/pchat/internal/llm"
	"github.com/sashabaranov/go-openai"
)

// Attachment describes a user-uploaded file that hangs off a
// user message. The HTTP upload endpoint (internal/server/upload.go)
// produces one of these from a multipart file; the agent then
// expands it into a content block before sending to the LLM.
//
// Kind is one of "image", "audio", "text", "file" — see the
// expandAttachments switch for the per-kind handling.
type Attachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size,omitempty"`
	Kind string `json:"kind"`
	MIME string `json:"mime,omitempty"`
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

// expandAttachments walks req.Attachments, reads each file from
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
func expandAttachments(msgs []llm.Message, atts []Attachment, r AttachmentResolver) []llm.Message {
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
		path, _ := r.Resolve(a)
		if path == "" {
			parts = append(parts, openai.ChatMessagePart{
				Type: openai.ChatMessagePartTypeText,
				Text: fmt.Sprintf("(attachment %s not found on server)", a.Name),
			})
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			parts = append(parts, openai.ChatMessagePart{
				Type: openai.ChatMessagePartTypeText,
				Text: fmt.Sprintf("(failed to read %s: %v)", a.Name, err),
			})
			continue
		}
		switch a.Kind {
		case "image":
			mime := a.MIME
			if mime == "" {
				mime = "image/png"
			}
			parts = append(parts, openai.ChatMessagePart{
				Type: openai.ChatMessagePartTypeImageURL,
				ImageURL: &openai.ChatMessageImageURL{
					URL: dataURL(mime, data),
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
				Text: fmt.Sprintf("(attached audio: %s, %d bytes, MIME=%s —server side: file kept at %s)",
					a.Name, len(data), a.MIME, path),
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
func ExpandAttachments(protocol string, msgs []llm.Message, atts []Attachment, r AttachmentResolver) []llm.Message {
	return expandAttachments(msgs, atts, r)
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
