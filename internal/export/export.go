// Package export renders a session's messages to markdown
// or JSON. It is the shared rendering core for both the
// CLI /export slash command and the HTTP /export endpoint.
//
// Why a dedicated package: the rendering rules
// (attachment handling, parts[] walking, tool-result
// sniffing, envelope schema) are non-trivial and would
// otherwise be duplicated between the CLI binary and
// the HTTP server. The CLI and the server must produce
// byte-identical output for a given session, so the
// logic lives in one place.
//
// Pure functions only — no DB access, no HTTP, no file
// I/O. Callers (CLI / server) supply the data via
// []memory.MessageFull and the exported conversation
// metadata.
package export

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/p-chat/pchat/internal/memory"
)

// Format identifies the output format. Same enum used
// by the CLI and the HTTP handler.
type Format string

const (
	FormatMarkdown Format = "markdown"
	FormatJSON     Format = "json"
)

// Schema versions for the envelope. Bumped when the
// output shape changes in a non-backward-compatible
// way. v2 added per-message `attachments` and surfaced
// the assistant's `parts` array on the JSON path.
const (
	JSONSchemaVersion = "pchat-export/2"
)

// MessagePart is the wire shape used by both the JSON
// envelope (re-marshalled verbatim) and the markdown
// renderer (decoded for part-by-part walking).
//
// We duplicate the agent.MessagePart / server.MessagePart
// shape here on purpose: the export package must not
// import the agent or server packages (the latter would
// pull gin into the CLI binary; the former would create
// a chain dependency). The fields the export actually
// reads are a strict subset of the wire shape — anything
// not present in this struct is dropped on the round
// trip, so adding a new field to the agent's part
// definition is a deliberate, reviewable action.
type MessagePart struct {
	Kind     string        `json:"kind"`
	Text     string        `json:"text,omitempty"`
	Name     string        `json:"name,omitempty"`
	Args     string        `json:"args,omitempty"`
	Status   string        `json:"status,omitempty"`
	Result   string        `json:"result,omitempty"`
	Error    string        `json:"error,omitempty"`
	Task     string        `json:"task,omitempty"`
	Parts    []MessagePart `json:"parts,omitempty"`
	// QuestionStatus is preserved so the markdown
	// writer can render a brief "the LLM asked this"
	// marker and the JSON envelope stays round-trippable.
	QuestionStatus string `json:"question_status,omitempty"`
}

// ====================================================================
// Markdown rendering
// ====================================================================

// AttachmentToMarkdown renders a single user-uploaded
// file (sourced from the message's multi_content
// metadata) as a markdown block.
//
//   - image_url  → ![name](data:image/...) inline; https
//     URLs are kept as link text since we can't inline
//     remote content
//   - audio_url / video_url → link with a 🔊/🎬 prefix
//     (markdown has no native media syntax)
//   - text       → fenced code block, MIME-tagged when
//     we can guess a language (json, csv, …)
func AttachmentToMarkdown(att memory.Attachment) string {
	if att.URL == "" {
		return ""
	}
	name := att.Name
	if name == "" {
		name = att.Type
	}
	if att.Type == "image_url" {
		if strings.HasPrefix(att.URL, "data:") || strings.HasPrefix(att.URL, "http") {
			return fmt.Sprintf("![%s](%s)\n\n", name, att.URL)
		}
		return fmt.Sprintf("[🖼 %s](%s)\n\n", name, att.URL)
	}
	if att.Type == "audio_url" {
		return fmt.Sprintf("[🔊 %s](%s)\n\n", name, att.URL)
	}
	if att.Type == "video_url" {
		return fmt.Sprintf("[🎬 %s](%s)\n\n", name, att.URL)
	}
	if att.Type == "text" {
		// The text body lives in the URL field for
		// type=text attachments (see
		// memory.AttachmentsFromMultiContent). Use the
		// MIME as a code-block language hint where it
		// maps cleanly to a known syntax.
		lang := langForMime(att.Mime)
		body := att.URL
		if body == "" {
			return fmt.Sprintf("*_(empty file: %s)_*\n\n", name)
		}
		return fmt.Sprintf("```%s\n%s\n```\n\n*— %s*\n\n", lang, body, name)
	}
	return fmt.Sprintf("[📎 %s](%s)\n\n", name, att.URL)
}

// langForMime maps a MIME type to a fenced-code
// language tag. Empty string means "no language" (renders
// as plain preformatted text in most viewers).
func langForMime(mime string) string {
	m := strings.ToLower(mime)
	switch m {
	case "application/json":
		return "json"
	case "text/markdown", "text/x-markdown":
		return "markdown"
	case "text/yaml", "application/x-yaml":
		return "yaml"
	case "text/xml", "application/xml":
		return "xml"
	case "text/html":
		return "html"
	case "text/csv":
		return "csv"
	case "application/javascript", "text/javascript":
		return "javascript"
	case "text/x-go":
		return "go"
	case "text/x-python", "application/x-python":
		return "python"
	case "text/x-rust":
		return "rust"
	case "text/x-shellscript", "application/x-sh":
		return "bash"
	case "text/x-sql":
		return "sql"
	}
	if strings.HasSuffix(m, "+json") {
		return "json"
	}
	if strings.HasPrefix(m, "text/") {
		return "text"
	}
	return ""
}

// PartToMarkdown renders one MessagePart as markdown.
// The recursion handles sub_agent.Parts (nested) and the
// `depth` argument indents each level so a deeply-nested
// sub-agent run reads cleanly.
func PartToMarkdown(p MessagePart, depth int) string {
	indent := strings.Repeat("  ", depth)
	switch p.Kind {
	case "text":
		return p.Text
	case "thinking":
		// Indent subsequent lines so the details
		// block doesn't get re-flowed by markdown
		// list/quote rules.
		body := strings.ReplaceAll(p.Text, "\n", "\n"+indent)
		return fmt.Sprintf("%s<details><summary>💭 thinking</summary>\n\n%s%s\n\n%s</details>\n\n",
			indent, indent, body, indent)
	case "tool":
		head := fmt.Sprintf("%s🔧 **%s** — `%s`", indent, p.Name, p.Status)
		if p.Args != "" {
			head += fmt.Sprintf("\n\n%s```json\n%s%s\n%s```",
				indent, indent, p.Args, indent)
		}
		if p.Result != "" {
			// Use the same sniff heuristic as the
			// frontend so base64 images render as
			// <img>, JSON as json-fenced, etc.
			head += "\n\n" + ResultBlockToMarkdown(p.Result, indent)
		}
		if p.Error != "" {
			head += fmt.Sprintf("\n\n%s> ❌ %s", indent, p.Error)
		}
		return head + "\n\n"
	case "sub_agent":
		head := fmt.Sprintf("%s### 🤖 sub-agent: %s (%s)\n\n",
			indent, p.Task, p.Status)
		var body strings.Builder
		for _, inner := range p.Parts {
			body.WriteString(PartToMarkdown(inner, depth+1))
		}
		return head + body.String()
	case "question":
		return fmt.Sprintf("%s> ❓ question (%s)\n\n",
			indent, defaultStr(p.QuestionStatus, "open"))
	default:
		return ""
	}
}

// ResultBlockToMarkdown is the export's mirror of the
// frontend's resultSniff.ts: a small heuristic that
// picks the right code-fence language for a tool result
// string. Base64 data: URLs are inlined as markdown
// images so the reader sees the screenshot directly.
func ResultBlockToMarkdown(s, indent string) string {
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "data:image/") {
		return fmt.Sprintf("%s![tool result](%s)\n\n", indent, s)
	}
	// A raw PNG signature (iVBORw0KGgo) is a
	// common LLM-fingerprint: many models copy the
	// base64 payload verbatim without the data:
	// prefix when echoing a tool result. Wrap it
	// so the markdown viewer can render it.
	if isRawPNGPayload(s) {
		return fmt.Sprintf("%s![tool result](data:image/png;base64,%s)\n\n", indent, s)
	}
	if IsJSON(s) {
		return fmt.Sprintf("%s```json\n%s%s\n%s```\n\n", indent, indent, s, indent)
	}
	if LooksLikeCode(s) {
		return fmt.Sprintf("%s```\n%s%s\n%s```\n\n", indent, indent, s, indent)
	}
	if strings.Contains(s, "\n") {
		// Multi-line prose → blockquote.
		var b strings.Builder
		for _, line := range strings.Split(s, "\n") {
			b.WriteString(fmt.Sprintf("%s> %s\n", indent, line))
		}
		b.WriteString("\n")
		return b.String()
	}
	return indent + s + "\n\n"
}

// IsJSON reports whether s parses as a JSON object or
// array. Used to pick the right code-fence language for
// tool results. Empty / non-bracketed strings return
// false.
func IsJSON(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	first, last := t[0], t[len(t)-1]
	if (first != '{' && first != '[') || (last != '}' && last != ']') {
		return false
	}
	var v any
	return json.Unmarshal([]byte(t), &v) == nil
}

// isRawPNGPayload is a cheap heuristic for the
// "LLM echoed a PNG without the data: prefix" case: a
// long string that starts with the PNG base64 magic
// bytes (\x89PNG = iVBORw0KGgo) and is at least
// 100 chars of base64 alphabet. False positives are
// rare because the magic prefix is unambiguous — no
// other common file format starts with those eight
// characters.
func isRawPNGPayload(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 100 {
		return false
	}
	if !strings.HasPrefix(s, "iVBORw0KGgo") {
		return false
	}
	for _, c := range s {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=') {
			return false
		}
	}
	return true
}

// dataURLRegex matches a `data:image/<mime>;base64,<payload>`
// substring inside a longer text. The character class
// for the MIME is conservative (a-z, 0-9, +, -, .) so
// `image/svg+xml` and `image/x-icon` both match.
// Payload is the standard base64 alphabet.
var dataURLRegex = regexp.MustCompile(`data:image/[a-zA-Z0-9.+-]+;base64,[A-Za-z0-9+/=]+`)

// ExtractDataURLsFromContent finds every data:image/...
// URL embedded in the content string and returns the
// cleaned content (with the URLs removed) plus a list
// of memory.Attachment entries to render alongside the
// regular attachments. The cleaned content preserves
// surrounding text so a message that read "看这张图
// data:image/png;base64,..." still reads as "看这张图
// " after the URL is lifted out.
//
// This is the fix for the "image shows as base64 string"
// bug: when the LLM pastes a screenshot into its
// response as inline text (instead of returning it via
// a tool / attachment), the URL was previously dumped
// verbatim into the markdown body. Now the exporter
// recognises the pattern, lifts the URL out, and
// renders it as a proper markdown image.
func ExtractDataURLsFromContent(content string) (string, []memory.Attachment) {
	matches := dataURLRegex.FindAllString(content, -1)
	if len(matches) == 0 {
		return content, nil
	}
	atts := make([]memory.Attachment, 0, len(matches))
	for i, url := range matches {
		mime := "image/png"
		// Parse the MIME out of the data URL header
		// (e.g. "data:image/jpeg;base64,..." →
		// "image/jpeg"). Falls back to image/png
		// when the header is malformed.
		if colon := strings.Index(url, ":"); colon > 0 {
			if semi := strings.Index(url[colon:], ";"); semi > 0 {
				mime = url[colon+1 : colon+semi]
			}
		}
		atts = append(atts, memory.Attachment{
			Type: "image_url",
			Kind: "image",
			URL:  url,
			Name: fmt.Sprintf("inline-%d", i+1),
			Mime: mime,
		})
	}
	cleaned := dataURLRegex.ReplaceAllString(content, "")
	// Tidy up: collapse runs of whitespace left
	// behind when a long URL is removed, so the
	// reader doesn't see a 20-space gap in the
	// prose.
	cleaned = strings.TrimSpace(cleaned)
	return cleaned, atts
}

// imageMagicPrefixes maps the base64-encoded magic
// bytes of the four image formats the LLM most often
// echoes into its reply text. PNG / JPEG / GIF / WebP
// all have unambiguous 3-8 byte signatures that
// survive the round-trip to base64 unchanged; the
// probability of a random base64 string matching
// any of these is small enough (~1/64^3 for JPEG) to
// make false positives in ordinary text negligible.
var imageMagicPrefixes = []struct {
	prefix string
	mime   string
}{
	{"iVBORw0KGgo", "image/png"},  // \x89PNG\r\n\x1a\n
	{"/9j/", "image/jpeg"},        // \xff\xd8\xff
	{"R0lGOD", "image/gif"},       // GIF87a / GIF89a
	{"UklGR", "image/webp"},       // RIFF....WEBP
}

// isRawImagePayload reports whether s is a bare
// base64-encoded image (no `data:` URL wrapper) of a
// known image format. Used by extractRawBase64Images
// to detect the common LLM-quirk where a model
// echoes a screenshot's base64 payload verbatim into
// its reply, dropping the `data:image/...;base64,`
// header. False-positive guard: every byte must be
// in the base64 alphabet (no spaces, punctuation, or
// other text), and the string must be long enough
// (~100 chars) to be a real image rather than a hash
// or a short token.
func isRawImagePayload(s string) bool {
	if len(s) < 100 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'A' && c <= 'Z') ||
			(c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') ||
			c == '+' || c == '/' || c == '=') {
			return false
		}
	}
	for _, p := range imageMagicPrefixes {
		if strings.HasPrefix(s, p.prefix) {
			return true
		}
	}
	return false
}

// sniffImageMime returns the MIME type matching a
// bare-base64 image payload's leading magic bytes.
// Defaults to image/png when the magic doesn't match
// any known format (the caller has already verified
// isRawImagePayload returned true, so this branch
// only fires on future format additions).
func sniffImageMime(s string) string {
	for _, p := range imageMagicPrefixes {
		if strings.HasPrefix(s, p.prefix) {
			return p.mime
		}
	}
	return "image/png"
}

// ExtractRawBase64Images finds lines in content that
// consist of a bare base64 image payload (no `data:`
// URL wrapper) and returns the content with those
// lines removed plus a list of image attachments
// synthesised from them.
//
// The LLM-quirk this fixes: many models echo a
// screenshot's base64 bytes verbatim into the reply
// text, dropping the `data:image/png;base64,` header
// that ExtractDataURLsFromContent looks for. The
// result is a 50-200 KB blob of pure base64 sitting
// in the assistant's `Msg.Content`, which used to be
// dumped as a code block in the export. The reader
// saw a 100KB wall of `iVBORw0KGgoAAAANSUhEUg…` and
// no picture.
//
// Heuristic:
//   - Split content by newlines, examine each line
//     independently (so a code-block base64 in the
//     middle of prose is left alone — only a line
//     that IS the base64 payload gets lifted).
//   - The line, after trim, must be pure base64
//     alphabet (no surrounding text, no spaces, no
//     punctuation) — this is the false-positive guard.
//   - The line must be ≥ 100 chars and start with a
//     known image magic prefix.
//
// What this does NOT do (and why):
//   - Inline base64 mixed with prose (e.g. "这是图片
//     iVBORw0KGgoAAA…") is left as text. The LLM
//     usually puts images on their own line, so the
//     line-based scan catches the common case. Adding
//     a regex with word boundaries would catch more
//     but raise the false-positive rate on
//     base64-encoded config / token strings; we'd
//     rather miss an inline image than garble a hash.
func ExtractRawBase64Images(content string) (string, []memory.Attachment) {
	if content == "" {
		return content, nil
	}
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, len(lines))
	var atts []memory.Attachment
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			kept = append(kept, line)
			continue
		}
		if isRawImagePayload(trimmed) {
			mime := sniffImageMime(trimmed)
			atts = append(atts, memory.Attachment{
				Type: "image_url",
				Kind: "image",
				URL:  "data:" + mime + ";base64," + trimmed,
				Name: fmt.Sprintf("raw-%d", i+1),
				Mime: mime,
			})
			// Drop the line from the body.
			continue
		}
		kept = append(kept, line)
	}
	if len(atts) == 0 {
		return content, nil
	}
	return strings.Join(kept, "\n"), atts
}

// LooksLikeCode is the simple heuristic the markdown
// renderer uses to decide whether to fence a body in
// a generic code block. Multi-line + at least one of
// the tell-tale characters (`= ( { ; :`) = code.
func LooksLikeCode(s string) bool {
	if !strings.Contains(s, "\n") {
		return false
	}
	for _, c := range []string{"=", "(", "{", ";", ":"} {
		if strings.Contains(s, c) {
			return true
		}
	}
	return false
}

func defaultStr(s, dflt string) string {
	if s == "" {
		return dflt
	}
	return s
}

// extractFromToolResult scans a tool-call part's result
// string for image data (data: URLs and bare base64)
// and returns the attachments it found. The result
// string itself is NOT modified — the caller decides
// whether to mutate the part (the markdown writer
// rewrites; the JSON writer preserves the raw part).
//
// Why this exists: the browser_screenshot tool (and
// any other tool that returns an image) puts the
// base64 / data: URL in `parts[].result`, NOT in
// `parts[].attachments` (there is no such field) and
// NOT in `Msg.Content`. Without this scan, the JSON
// export would carry the data in a part-result string
// with no type info — the user would see a base64
// wall and no way to know it's a picture.
func extractFromToolResult(result string) []memory.Attachment {
	if result == "" {
		return nil
	}
	// The result might be a single image or a JSON
	// envelope. Try data: URL first (the common
	// shape from browser_screenshot), then fall
	// back to bare-base64 line scan.
	cleaned, atts := ExtractDataURLsFromContent(result)
	if len(atts) > 0 {
		return atts
	}
	// If the result is a JSON envelope, the image
	// is usually under {"image": "data:..."} or
	// similar — but those are not common in the
	// current tool set, so we don't unpack JSON
	// here. Bare base64 in a single-line result
	// is also unusual; skip if so.
	if strings.Contains(cleaned, "\n") {
		_, rawAtts := ExtractRawBase64Images(cleaned)
		if len(rawAtts) > 0 {
			return rawAtts
		}
	}
	return nil
}

// extractContentAttachments runs every extractor on
// the message's content + parts and returns the
// combined list of image attachments it found, plus
// the cleaned content (with images lifted out). The
// cleaned content is the `Msg.Content` value the
// exporter should render / serialise — the original
// is never exposed once extraction runs.
//
// Used by both ToMarkdown and ToJSON so the two
// formats agree on what counts as an image:
//   - data: URLs in content (ExtractDataURLsFromContent)
//   - bare base64 lines in content (ExtractRawBase64Images)
//   - data: URLs / bare base64 in parts[].result
//     (extractFromToolResult)
//
// The structured `mf.Attachments` (from multi_content)
// is appended LAST so the per-attachment `type` /
// `kind` / `mime` set by the store's extraction path
// take precedence over what we infer from the URL
// prefix — a multi_content image_url entry was already
// classified at storage time.
func extractContentAttachments(mf memory.MessageFull) (string, []memory.Attachment) {
	cleaned, atts := ExtractDataURLsFromContent(mf.Msg.Content)
	cleaned, rawAtts := ExtractRawBase64Images(cleaned)
	atts = append(atts, rawAtts...)

	// Walk parts[] for tool results that carry
	// inline image data. We only touch the result
	// string of `kind: "tool"` parts (sub-agents
	// get their own walk; their tool calls are
	// nested under their own parts[].result).
	if len(mf.Parts) > 0 {
		var parts []MessagePart
		if err := json.Unmarshal(mf.Parts, &parts); err == nil {
			for _, p := range parts {
				if p.Kind == "tool" {
					toolAtts := extractFromToolResult(p.Result)
					atts = append(atts, toolAtts...)
				}
				// Sub-agents: recurse into the nested
				// parts array so a sub-agent's tool
				// result images are also lifted.
				if p.Kind == "sub_agent" {
					toolAtts := extractFromNestedParts(p.Parts)
					atts = append(atts, toolAtts...)
				}
			}
		}
	}

	// Append the structured attachments last
	// (multi_content path) so the writer can rely
	// on the order: extracted → structured. The
	// summary fields don't care about order, so
	// this is purely a presentation preference.
	atts = append(atts, mf.Attachments...)
	return cleaned, atts
}

// extractFromNestedParts walks a sub_agent's nested
// parts for tool-result images, the same way the
// top-level walk does. Exported as a separate helper
// for the same recursive-pattern reason
// `extractContentAttachments` is its own function:
// the call site is a sub-agent boundary, not the
// main message.
func extractFromNestedParts(parts []MessagePart) []memory.Attachment {
	var out []memory.Attachment
	for _, p := range parts {
		if p.Kind == "tool" {
			out = append(out, extractFromToolResult(p.Result)...)
		}
		if p.Kind == "sub_agent" {
			out = append(out, extractFromNestedParts(p.Parts)...)
		}
	}
	return out
}

// renderBody applies the same "wrap code in a fence"
// heuristic the original /export used. Kept as a small
// helper so the main loop reads cleanly.
func renderBody(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	if LooksLikeCode(body) {
		return "```\n" + body + "\n```\n\n"
	}
	return body + "\n\n"
}

// ToMarkdown renders the conversation as human-readable
// markdown. The output is byte-stable for a given
// conversation + style + provider combo (so re-exports
// produce diff-friendly files).
//
//   - Attachments are inlined as markdown images /
//     links / code blocks at the top of each message
//   - The assistant message's `parts` array
//     (thinking / tool / sub_agent / question) is
//     rendered when available; falls back to the legacy
//     `content` field for messages that pre-date the
//     parts snapshot
func ToMarkdown(conv *memory.Conversation, msgs []memory.MessageFull) string {
	var b strings.Builder

	title := conv.Title
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "- **Session ID**: `%s`\n", conv.ID)
	fmt.Fprintf(&b, "- **Created**: %s\n", conv.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(&b, "- **Updated**: %s\n", conv.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(&b, "- **Messages**: %d\n\n", len(msgs))
	fmt.Fprintln(&b, "---")
	fmt.Fprintln(&b)

	for i, mf := range msgs {
		m := mf.Msg
		roleIcon := roleEmoji(m.Role)
		fmt.Fprintf(&b, "## %s %s · msg #%d\n\n", roleIcon, titleCase(m.Role), i+1)
		// Run the shared content + parts extraction
		// (data: URLs in content, bare base64 lines
		// in content, and tool-result images in
		// parts[]). `contentText` is the cleaned
		// body with image payloads lifted out;
		// `attachments` is the combined list we
		// render below. Doing this in one place
		// keeps the markdown and JSON exports
		// consistent — they both apply the same
		// "what counts as an image" rules.
		contentText, attachments := extractContentAttachments(mf)
		// Attachments first (so the reader sees the
		// upload before the question / response that
		// references it). Extracted-from-content
		// attachments (returned first by
		// extractContentAttachments) come BEFORE
		// the structured multi_content attachments
		// because the LLM typically dumps a
		// screenshot at the end of its reply, after
		// the prose.
		for _, att := range attachments {
			b.WriteString(AttachmentToMarkdown(att))
		}
		// Parts (assistant) when present; legacy
		// `content` field otherwise.
		if len(mf.Parts) > 0 {
			var parts []MessagePart
			if err := json.Unmarshal(mf.Parts, &parts); err == nil {
				for _, p := range parts {
					b.WriteString(PartToMarkdown(p, 0))
				}
			} else {
				// Fall back to content when
				// the parts blob is
				// unparseable — better to emit
				// *something* than nothing.
				b.WriteString(renderBody(contentText))
			}
		} else {
			b.WriteString(renderBody(contentText))
		}
		if m.Name != "" {
			fmt.Fprintf(&b, "*tool: `%s`*\n\n", m.Name)
		}
		if m.ToolCallID != "" {
			fmt.Fprintf(&b, "*tool_call_id: `%s`*\n\n", m.ToolCallID)
		}
		// Thinking persisted as a top-level field
		// (older rows) gets a details block so the
		// reader can still see it.
		if mf.Thinking != "" {
			fmt.Fprintf(&b, "<details><summary>💭 thinking</summary>\n\n%s\n\n</details>\n\n",
				mf.Thinking)
		}
	}
	return b.String()
}

func roleEmoji(role string) string {
	switch role {
	case "user":
		return "🧑"
	case "assistant":
		return "🤖"
	case "system":
		return "⚙️"
	case "tool":
		return "🔧"
	default:
		return "•"
	}
}

func titleCase(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// SanitizeFilename replaces filesystem-unsafe
// characters with `-`. Mirrors the frontend's
// suggestFilename so an export triggered from the UI
// gets the same name the SPA would have produced.
//
// Lives in the export package (not the HTTP handler)
// because the rule set is universal: it must produce
// the same output whether the caller is the CLI, the
// HTTP server, or a future embedder. Keeping it next
// to the other render helpers makes the contract
// obvious to readers.
func SanitizeFilename(s string) string {
	r := strings.NewReplacer(
		`\`, "-",
		`/`, "-",
		`:`, "-",
		`*`, "-",
		`?`, "-",
		`"`, "-",
		`<`, "-",
		`>`, "-",
		`|`, "-",
		" ", "-",
	)
	out := r.Replace(s)
	// Drop any leftover control characters.
	cleaned := make([]rune, 0, len(out))
	for _, c := range out {
		if c >= 0x20 && c != 0x7f {
			cleaned = append(cleaned, c)
		}
	}
	return string(cleaned)
}

// URLEncodeFilename encodes a filename per RFC 5987
// for the `filename*=UTF-8''…` Content-Disposition
// parameter. Per spec the value MUST be percent-encoded
// (raw UTF-8 bytes are not legal in this parameter,
// and some HTTP stacks — notably WebView2 in the
// Wails desktop app — will mangle them into `?` or
// other replacement characters).
//
// The "safe" set that passes through unescaped is the
// RFC 5987 `attr-char` set: ASCII alphanumeric plus
// `! # $ & + - . ^ _ ` | } ~`. We use the conservative
// RFC 3986 unreserved set (alphanumeric + `- . _`) —
// enough for typical filenames, and the rest of the
// special characters can break out of a header value
// anyway.
//
// The previous implementation (write the rune
// directly when r >= 0x80) shipped raw UTF-8 bytes for
// Chinese / emoji titles. The end-to-end test passed
// because Go's `url.QueryUnescape` tolerates the raw
// bytes, but WebView2 does not — the user saw a
// garbled download name. This rewrite iterates byte
// by byte and percent-encodes anything outside the
// safe set, including the multi-byte UTF-8
// representation of non-ASCII runes.
func URLEncodeFilename(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		// Decode the rune to its UTF-8 bytes; we
		// percent-encode per byte, not per rune, so
		// `调` (U+8C03, E8 B0 83 in UTF-8) becomes
		// %E8%B0%83.
		utf8 := []byte(string(r))
		for _, c := range utf8 {
			if isUnreserved(c) {
				b.WriteByte(c)
			} else {
				fmt.Fprintf(&b, "%%%02X", c)
			}
		}
	}
	return b.String()
}

// isUnreserved reports whether b is in the RFC 3986
// unreserved set: ASCII alphanumeric + `- . _`. These
// are the only bytes that survive percent-encoding in
// the `filename*=UTF-8''…` value per RFC 5987 §3.2.
func isUnreserved(b byte) bool {
	return (b >= '0' && b <= '9') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= 'a' && b <= 'z') ||
		b == '-' || b == '.' || b == '_'
}

// ====================================================================
// JSON rendering
// ====================================================================

// jsonEnvelope is the v2 wire shape. Fields:
//
//   - Version: schema id, always "pchat-export/2"
//   - ExportedAt: RFC3339 UTC timestamp
//   - Session: free-form session metadata (id, title,
//     timestamps) for downstream tooling that wants to
//     re-link the export to the original conversation
//   - Messages: array of jsonMessage (one per row)
//     in oldest-first order
type jsonEnvelope struct {
	Version    string         `json:"version"`
	ExportedAt string         `json:"exported_at"`
	Session    map[string]any `json:"session"`
	Messages   []jsonMessage  `json:"messages"`
}

// jsonMessage is the per-row shape inside the envelope.
// The Parts field carries the raw assistant `parts` JSON
// when present; Attachments carries the typed attachment
// list (always present, may be empty). The two
// summary fields (AttachmentKinds, AttachmentCount)
// let a downstream consumer read the message-level
// "what kind of attachments does this message have"
// in O(1) — they previously had to walk
// `attachments[].type` for every message.
//
// AttachmentKinds is a deduplicated, sorted array of
// the per-attachment `kind` values ("image" / "audio"
// / "video" / "text" / "file"). Always present (never
// null) so consumers can iterate without a nil check;
// empty when the message has no attachments. Same
// length-cap rule as the existing `attachments` field:
// omitted via `omitempty` only when there's nothing to
// say (i.e. the message has zero attachments AND no
// part-attached content).
type jsonMessage struct {
	Index            int                 `json:"index"`
	Role             string              `json:"role"`
	Content          string              `json:"content"`
	Thinking         string              `json:"thinking,omitempty"`
	Parts            json.RawMessage     `json:"parts,omitempty"`
	Attachments      []memory.Attachment `json:"attachments"`
	// AttachmentKinds is the dedup'd, sorted list of
	// attachment kinds ("image" / "audio" / "video"
	// / "text" / "file") for this message. Always
	// serialised as an array (possibly empty) so
	// consumers don't have to nil-check it. We
	// explicitly want an empty array over a missing
	// key here — "no attachments" is information.
	AttachmentKinds []string `json:"attachment_kinds"`
	// AttachmentCount is len(Attachments). Exposed
	// separately so a count-only consumer doesn't
	// have to decode the full array.
	AttachmentCount int `json:"attachment_count"`
	ToolCallID      string `json:"tool_call_id,omitempty"`
	Name            string `json:"name,omitempty"`
	CreatedAt       int64  `json:"created_at,omitempty"`
}

// summaryKinds returns the deduplicated, sorted list
// of attachment kinds for a message. Empty input →
// empty slice (so the JSON encoder emits "[]" rather
// than "null"). Duplicates are removed because a
// message with three image attachments should show
// `["image"]` not `["image", "image", "image"]` — the
// array answers "what kinds are present", not "how
// many of each".
func summaryKinds(atts []memory.Attachment) []string {
	if len(atts) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(atts))
	for _, a := range atts {
		kind := a.Kind
		if kind == "" {
			// Fall back to the wire type for
			// older rows that predate the Kind
			// field. The mapping is the same
			// one we use at extraction time
			// (image_url → image, etc.).
			kind = kindFromWireType(a.Type)
		}
		if kind == "" {
			continue
		}
		seen[kind] = struct{}{}
	}
	if len(seen) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// kindFromWireType maps the OpenAI ChatMessagePart.Type
// to the human-readable kind. The mapping is
// duplicated here (rather than imported from
// memory.AttachmentsFromMultiContent) to keep the
// export package free of an OpenAI SDK import.
func kindFromWireType(wire string) string {
	switch wire {
	case "image_url":
		return "image"
	case "audio_url":
		return "audio"
	case "video_url":
		return "video"
	case "text":
		return "text"
	}
	return "file"
}

// ToJSON serializes the conversation as JSON. The schema
// is stable and machine-readable. The v2 envelope carries
// parts and attachments so downstream tooling can re-render
// the session with the same fidelity as the chat UI.
func ToJSON(conv *memory.Conversation, msgs []memory.MessageFull) (string, error) {
	env := jsonEnvelope{
		Version:    JSONSchemaVersion,
		ExportedAt: nowRFC3339UTC(),
		Session: map[string]any{
			"id":         conv.ID,
			"title":      conv.Title,
			"created_at": conv.CreatedAt,
			"updated_at": conv.UpdatedAt,
		},
		Messages: make([]jsonMessage, 0, len(msgs)),
	}
	for i, mf := range msgs {
		m := mf.Msg
		// Apply the shared content extractor: data:
		// URLs in content, bare base64 lines in
		// content, and tool-result images in parts
		// all become attachments with type/kind.
		// The `content` field in the JSON is the
		// CLEANED text (base64 lifted out) so
		// downstream tooling never has to deal with
		// a 100KB image string sitting inline. The
		// `parts` field preserves the raw shape for
		// anyone who needs the original tool result
		// verbatim.
		cleanedContent, attachments := extractContentAttachments(mf)
		out := jsonMessage{
			Index:       i + 1,
			Role:        m.Role,
			Content:     cleanedContent,
			Thinking:    mf.Thinking,
			ToolCallID:  m.ToolCallID,
			Name:        m.Name,
			CreatedAt:   mf.CreatedAt,
			Attachments: attachments,
		}
		// Empty (or JSON null) parts get serialised as
		// "null" without the omitempty guard — the
		// shape is the same either way and consumers
		// can rely on `parts` being present on every
		// message.
		if len(mf.Parts) > 0 {
			out.Parts = mf.Parts
		}
		// Guarantee an empty (not nil) Attachments
		// slice so consumers can iterate without a
		// nil check.
		if out.Attachments == nil {
			out.Attachments = []memory.Attachment{}
		}
		// Per-message attachment summary: dedup'd
		// kinds + total count. Both are always
		// emitted (no omitempty) so consumers can
		// rely on the field's presence. The summary
		// reflects the COMBINED list (extracted +
		// structured) — every image in the message,
		// regardless of where it came from, shows up
		// here.
		out.AttachmentKinds = summaryKinds(out.Attachments)
		out.AttachmentCount = len(out.Attachments)
		env.Messages = append(env.Messages, out)
	}
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
