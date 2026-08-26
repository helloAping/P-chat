package export

import (
	"fmt"
	"html"
	"strings"

	"github.com/p-chat/pchat/internal/memory"
)

// ToHTML renders a conversation as a self-contained, print-friendly
// HTML document. It intentionally shares the same content extraction
// path as ToMarkdown/ToJSON so inline screenshots, attachments and
// assistant parts behave consistently across export formats.
func ToHTML(conv *memory.Conversation, msgs []memory.MessageFull) string {
	var b strings.Builder
	title := conv.Title
	if title == "" {
		title = "(untitled)"
	}

	b.WriteString("<!doctype html>\n<html lang=\"zh-CN\">\n<head>\n")
	b.WriteString("<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	fmt.Fprintf(&b, "<title>%s</title>\n", html.EscapeString(title))
	b.WriteString(exportHTMLCSS())
	b.WriteString("</head>\n<body>\n")
	b.WriteString("<main class=\"export-shell\">\n")
	b.WriteString("<header class=\"cover\">\n")
	b.WriteString("<div class=\"brand-row\"><span class=\"brand-mark\">P</span><span>P-Chat Export</span></div>\n")
	fmt.Fprintf(&b, "<h1>%s</h1>\n", html.EscapeString(title))
	visible := visibleExportMessages(msgs)
	fmt.Fprintf(&b, "<dl class=\"meta\"><div><dt>Session</dt><dd>%s</dd></div><div><dt>Created</dt><dd>%s</dd></div><div><dt>Updated</dt><dd>%s</dd></div><div><dt>Messages</dt><dd>%d</dd></div></dl>\n",
		html.EscapeString(conv.ID),
		html.EscapeString(conv.CreatedAt.Format("2006-01-02 15:04:05")),
		html.EscapeString(conv.UpdatedAt.Format("2006-01-02 15:04:05")),
		len(visible))
	b.WriteString("</header>\n<section class=\"timeline\">\n")

	for i, mf := range visible {
		role := normalizedRole(mf.Msg.Role)
		fmt.Fprintf(&b, "<article class=\"message role-%s\">\n", role)
		fmt.Fprintf(&b, "<header class=\"message-head\"><span class=\"role-chip\">%s</span><span class=\"msg-index\">#%d</span></header>\n",
			html.EscapeString(strings.ToUpper(role)), i+1)
		contentText, attachments := extractContentAttachments(mf)
		if len(attachments) > 0 {
			b.WriteString("<div class=\"attachments\">\n")
			for _, att := range attachments {
				b.WriteString(attachmentToHTML(att))
			}
			b.WriteString("</div>\n")
		}
		if len(mf.Parts) > 0 {
			if parts, ok := DecodeMessageParts(mf.Parts); ok {
				b.WriteString("<div class=\"parts\">\n")
				for _, p := range parts {
					b.WriteString(partToHTML(p, 0))
				}
				b.WriteString("</div>\n")
			} else {
				b.WriteString(renderTextHTML(contentText))
			}
		} else {
			b.WriteString(renderTextHTML(contentText))
		}
		if mf.Thinking != "" {
			fmt.Fprintf(&b, "<details class=\"thinking\"><summary>Thinking</summary>%s</details>\n", renderTextHTML(mf.Thinking))
		}
		if mf.Msg.Name != "" || mf.Msg.ToolCallID != "" {
			b.WriteString("<footer class=\"message-foot\">")
			if mf.Msg.Name != "" {
				fmt.Fprintf(&b, "<span>tool: <code>%s</code></span>", html.EscapeString(mf.Msg.Name))
			}
			if mf.Msg.ToolCallID != "" {
				fmt.Fprintf(&b, "<span>tool_call_id: <code>%s</code></span>", html.EscapeString(mf.Msg.ToolCallID))
			}
			b.WriteString("</footer>\n")
		}
		b.WriteString("</article>\n")
	}

	b.WriteString("</section>\n</main>\n</body>\n</html>\n")
	return b.String()
}

func visibleExportMessages(msgs []memory.MessageFull) []memory.MessageFull {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]memory.MessageFull, 0, len(msgs))
	for _, mf := range msgs {
		if messageHasRenderableContent(mf) {
			out = append(out, mf)
		}
	}
	return out
}

func normalizedRole(role string) string {
	switch role {
	case "user", "assistant", "system", "tool":
		return role
	default:
		return "other"
	}
}

func attachmentToHTML(att memory.Attachment) string {
	name := att.Name
	if name == "" {
		name = att.Type
	}
	escName := html.EscapeString(name)
	escURL := html.EscapeString(att.URL)
	switch att.Type {
	case "image_url":
		if strings.HasPrefix(att.URL, "data:image/") || strings.HasPrefix(att.URL, "http") {
			return fmt.Sprintf("<figure class=\"attachment attachment-image\"><img src=\"%s\" alt=\"%s\"><figcaption>%s</figcaption></figure>\n", escURL, escName, escName)
		}
	case "audio_url", "video_url":
		return fmt.Sprintf("<a class=\"attachment attachment-link\" href=\"%s\">%s</a>\n", escURL, escName)
	case "text":
		return fmt.Sprintf("<figure class=\"attachment attachment-text\"><figcaption>%s</figcaption><pre><code>%s</code></pre></figure>\n", escName, html.EscapeString(att.URL))
	}
	return fmt.Sprintf("<a class=\"attachment attachment-link\" href=\"%s\">%s</a>\n", escURL, escName)
}

func partToHTML(p MessagePart, depth int) string {
	depthClass := fmt.Sprintf(" depth-%d", depth)
	switch p.Kind {
	case "text":
		return renderTextHTML(p.Text)
	case "thinking":
		return fmt.Sprintf("<details class=\"thinking%s\"><summary>Thinking</summary>%s</details>\n", depthClass, renderTextHTML(p.Text))
	case "tool":
		var b strings.Builder
		fmt.Fprintf(&b, "<section class=\"tool-card%s\"><header><span class=\"tool-name\">%s</span><span class=\"status status-%s\">%s</span></header>\n",
			depthClass, html.EscapeString(defaultStr(p.Name, "tool")), html.EscapeString(p.Status), html.EscapeString(defaultStr(p.Status, "start")))
		if p.Args != "" {
			fmt.Fprintf(&b, "<details class=\"tool-details\"><summary>Arguments</summary><pre><code>%s</code></pre></details>\n", html.EscapeString(p.Args))
		}
		if p.Result != "" {
			b.WriteString(resultBlockToHTML(p.Result))
		}
		if p.Error != "" {
			fmt.Fprintf(&b, "<p class=\"tool-error\">%s</p>\n", html.EscapeString(normalizeExportText(p.Error)))
		}
		b.WriteString("</section>\n")
		return b.String()
	case "sub_agent":
		var b strings.Builder
		fmt.Fprintf(&b, "<section class=\"sub-agent%s\"><header><span>Sub-agent</span><strong>%s</strong><em>%s</em></header>\n",
			depthClass, html.EscapeString(p.Task), html.EscapeString(p.Status))
		for _, inner := range p.Parts {
			b.WriteString(partToHTML(inner, depth+1))
		}
		b.WriteString("</section>\n")
		return b.String()
	case "question":
		return fmt.Sprintf("<blockquote class=\"question\">Question requested: %s</blockquote>\n", html.EscapeString(defaultStr(p.QuestionStatus, "open")))
	default:
		return ""
	}
}

func resultBlockToHTML(s string) string {
	s = normalizeExportText(s)
	if strings.HasPrefix(s, "data:image/") {
		return fmt.Sprintf("<figure class=\"tool-image\"><img src=\"%s\" alt=\"tool result\"><figcaption>Tool result</figcaption></figure>\n", html.EscapeString(s))
	}
	if isRawPNGPayload(s) {
		return fmt.Sprintf("<figure class=\"tool-image\"><img src=\"data:image/png;base64,%s\" alt=\"tool result\"><figcaption>Tool result</figcaption></figure>\n", html.EscapeString(s))
	}
	if IsJSON(s) || LooksLikeCode(s) || strings.Contains(s, "\n") {
		return fmt.Sprintf("<pre class=\"tool-result\"><code>%s</code></pre>\n", html.EscapeString(s))
	}
	return fmt.Sprintf("<p class=\"tool-result-inline\">%s</p>\n", html.EscapeString(s))
}

func renderTextHTML(s string) string {
	s = normalizeExportText(s)
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	var b strings.Builder
	var para []string
	inFence := false
	var fence []string
	flushPara := func() {
		if len(para) == 0 {
			return
		}
		fmt.Fprintf(&b, "<p>%s</p>\n", inlineHTML(strings.Join(para, " ")))
		para = nil
	}
	flushFence := func() {
		fmt.Fprintf(&b, "<pre><code>%s</code></pre>\n", html.EscapeString(strings.Join(fence, "\n")))
		fence = nil
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inFence {
				flushFence()
				inFence = false
			} else {
				flushPara()
				inFence = true
			}
			continue
		}
		if inFence {
			fence = append(fence, line)
			continue
		}
		if trimmed == "" {
			flushPara()
			continue
		}
		if strings.HasPrefix(trimmed, "### ") {
			flushPara()
			fmt.Fprintf(&b, "<h3>%s</h3>\n", inlineHTML(strings.TrimSpace(trimmed[4:])))
			continue
		}
		if strings.HasPrefix(trimmed, "## ") {
			flushPara()
			fmt.Fprintf(&b, "<h2>%s</h2>\n", inlineHTML(strings.TrimSpace(trimmed[3:])))
			continue
		}
		if strings.HasPrefix(trimmed, "# ") {
			flushPara()
			fmt.Fprintf(&b, "<h2>%s</h2>\n", inlineHTML(strings.TrimSpace(trimmed[2:])))
			continue
		}
		para = append(para, trimmed)
	}
	if inFence {
		flushFence()
	}
	flushPara()
	return b.String()
}

func inlineHTML(s string) string {
	escaped := html.EscapeString(s)
	// Minimal inline polish for exported prose. This is deliberately
	// conservative; full Markdown rendering belongs in the live UI.
	for {
		start := strings.Index(escaped, "`")
		if start < 0 {
			return escaped
		}
		end := strings.Index(escaped[start+1:], "`")
		if end < 0 {
			return escaped
		}
		end += start + 1
		escaped = escaped[:start] + "<code>" + escaped[start+1:end] + "</code>" + escaped[end+1:]
	}
}

func exportHTMLCSS() string {
	return `<style>
:root {
  --page: #f4f6fb;
  --paper: #ffffff;
  --ink: #1b2030;
  --muted: #697084;
  --line: #d9deea;
  --soft: #eef2f8;
  --brand: #4042e6;
  --assistant: #6d52d9;
  --success: #16794c;
  --warn: #9a5a00;
  --error: #c83535;
  --code: #10131b;
}
* { box-sizing: border-box; }
body {
  margin: 0;
  background: var(--page);
  color: var(--ink);
  font: 14px/1.62 "Inter", "Segoe UI", "Microsoft YaHei", sans-serif;
}
.export-shell { max-width: 960px; margin: 0 auto; padding: 40px 28px 64px; }
.cover {
  background: var(--paper);
  border: 1px solid var(--line);
  border-radius: 18px;
  padding: 30px 34px;
  box-shadow: 0 24px 70px rgba(26, 31, 47, 0.12);
}
.brand-row { display: flex; align-items: center; gap: 10px; color: var(--muted); font-size: 12px; font-weight: 700; text-transform: uppercase; letter-spacing: .08em; }
.brand-mark { display: inline-flex; align-items: center; justify-content: center; width: 26px; height: 26px; border-radius: 8px; background: var(--brand); color: white; letter-spacing: 0; }
h1 { margin: 18px 0 22px; font-size: 32px; line-height: 1.15; letter-spacing: 0; }
.meta { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; margin: 0; }
.meta div { min-width: 0; padding: 10px 12px; background: var(--soft); border-radius: 10px; }
.meta dt { margin: 0 0 2px; color: var(--muted); font-size: 11px; font-weight: 700; text-transform: uppercase; }
.meta dd { margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: "JetBrains Mono", "Cascadia Mono", monospace; font-size: 12px; }
.timeline { margin-top: 24px; display: grid; gap: 16px; }
.message { background: var(--paper); border: 1px solid var(--line); border-radius: 14px; padding: 16px 18px; position: relative; overflow: hidden; }
.message::before { content: ""; position: absolute; inset: 0 auto 0 0; width: 4px; background: var(--muted); }
.role-user::before { background: var(--brand); }
.role-assistant::before { background: var(--assistant); }
.role-tool::before { background: var(--success); }
.role-system::before { background: var(--warn); }
.message-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 12px; }
.role-chip { color: var(--muted); font-size: 11px; font-weight: 800; letter-spacing: .08em; }
.msg-index { color: var(--muted); font-family: "JetBrains Mono", monospace; font-size: 11px; }
p { margin: 0 0 10px; }
h2, h3 { margin: 14px 0 8px; line-height: 1.3; letter-spacing: 0; }
h2 { font-size: 18px; }
h3 { font-size: 15px; }
code { font-family: "JetBrains Mono", "Cascadia Mono", monospace; font-size: .92em; background: var(--soft); border-radius: 5px; padding: 1px 4px; }
pre { margin: 10px 0; padding: 14px 16px; overflow: auto; color: #eef2ff; background: var(--code); border-radius: 12px; line-height: 1.5; white-space: pre-wrap; word-break: break-word; }
pre code { padding: 0; background: transparent; color: inherit; }
.attachments { display: grid; gap: 10px; margin-bottom: 12px; }
.attachment { margin: 0; border: 1px solid var(--line); border-radius: 12px; background: var(--soft); overflow: hidden; }
.attachment img, .tool-image img { display: block; max-width: 100%; height: auto; }
figcaption { padding: 8px 10px; color: var(--muted); font-size: 12px; }
.attachment-link { display: block; padding: 10px 12px; color: var(--brand); text-decoration: none; border: 1px solid var(--line); border-radius: 10px; background: var(--soft); }
.thinking, .tool-card, .sub-agent, .question { margin: 10px 0; border: 1px solid var(--line); border-radius: 12px; background: #fbfcff; }
.thinking { padding: 10px 12px; color: var(--muted); }
.thinking summary { cursor: pointer; color: var(--ink); font-weight: 700; }
.tool-card, .sub-agent { padding: 12px; }
.tool-card header, .sub-agent header { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; color: var(--muted); font-size: 12px; }
.tool-name, .sub-agent strong { color: var(--ink); font-weight: 700; }
.sub-agent em { margin-left: auto; font-style: normal; color: var(--muted); }
.status { margin-left: auto; padding: 2px 7px; border-radius: 999px; background: var(--soft); font-size: 11px; font-weight: 700; }
.status-ok { color: var(--success); }
.status-error, .tool-error { color: var(--error); }
.status-warn { color: var(--warn); }
.tool-error { margin: 8px 0 0; }
.question { padding: 10px 12px; color: var(--muted); }
.message-foot { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 12px; padding-top: 10px; border-top: 1px solid var(--line); color: var(--muted); font-size: 12px; }
@media (max-width: 760px) {
  .export-shell { padding: 18px 12px 40px; }
  .cover { padding: 22px 20px; border-radius: 14px; }
  h1 { font-size: 24px; }
  .meta { grid-template-columns: 1fr 1fr; }
}
@media print {
  body { background: white; }
  .export-shell { max-width: none; padding: 0; }
  .cover, .message { box-shadow: none; break-inside: avoid; }
  .timeline { gap: 12px; }
  pre { white-space: pre-wrap; }
}
</style>
`
}
