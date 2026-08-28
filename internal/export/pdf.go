package export

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/p-chat/pchat/internal/memory"
)

const (
	pdfPageW = 595.0
	pdfPageH = 842.0
	pdfM     = 44.0
)

// ToPDF renders a conversation to a lightweight PDF document. The HTML
// export remains the high-fidelity styled output; this PDF path focuses
// on a stable, readable archive that works without external converters.
func ToPDF(conv *memory.Conversation, msgs []memory.MessageFull) []byte {
	title := conv.Title
	if title == "" {
		title = "(untitled)"
	}
	r := newPDFRenderer(title)
	visible := visibleExportMessages(msgs)
	r.titlePage(conv, len(visible))
	for i, mf := range visible {
		role := titleCase(normalizedRole(mf.Msg.Role))
		r.sectionHeader(fmt.Sprintf("%s  #%d", role, i+1), rolePDFColor(mf.Msg.Role))
		for _, line := range messagePlainLines(mf) {
			r.paragraph(line)
		}
		r.gap(8)
	}
	return r.bytes()
}

func rolePDFColor(role string) [3]float64 {
	switch role {
	case "user":
		return [3]float64{0.25, 0.26, 0.90}
	case "assistant":
		return [3]float64{0.43, 0.32, 0.85}
	case "tool":
		return [3]float64{0.08, 0.48, 0.30}
	case "system":
		return [3]float64{0.61, 0.36, 0.00}
	default:
		return [3]float64{0.42, 0.44, 0.52}
	}
}

func messagePlainLines(mf memory.MessageFull) []string {
	contentText, attachments := extractContentAttachments(mf)
	var lines []string
	for _, att := range attachments {
		name := att.Name
		if name == "" {
			name = att.Type
		}
		kind := att.Kind
		if kind == "" {
			kind = kindFromWireType(att.Type)
		}
		lines = append(lines, fmt.Sprintf("[%s attachment] %s", kind, name))
	}
	if len(mf.Parts) > 0 {
		if parts, ok := DecodeMessageParts(mf.Parts); ok {
			for _, p := range parts {
				lines = append(lines, partPlainLines(p, 0)...)
			}
		} else {
			lines = appendTextLines(lines, contentText)
		}
	} else {
		lines = appendTextLines(lines, contentText)
	}
	if mf.Thinking != "" {
		lines = append(lines, "[thinking]")
		lines = appendTextLines(lines, mf.Thinking)
	}
	if mf.Msg.Name != "" {
		lines = append(lines, "tool: "+mf.Msg.Name)
	}
	if mf.Msg.ToolCallID != "" {
		lines = append(lines, "tool_call_id: "+mf.Msg.ToolCallID)
	}
	if len(lines) == 0 {
		return []string{"(empty message)"}
	}
	return lines
}

func partPlainLines(p MessagePart, depth int) []string {
	prefix := strings.Repeat("  ", depth)
	switch p.Kind {
	case "text":
		return appendTextLines(nil, prefix+p.Text)
	case "thinking":
		return appendTextLines([]string{prefix + "[thinking]"}, p.Text)
	case "tool":
		lines := []string{fmt.Sprintf("%s[tool] %s - %s", prefix, defaultStr(p.Name, "tool"), defaultStr(p.Status, "start"))}
		if p.Args != "" {
			lines = append(lines, prefix+"arguments:")
			lines = appendTextLines(lines, p.Args)
		}
		if p.Result != "" {
			lines = append(lines, prefix+"result:")
			lines = appendTextLines(lines, p.Result)
		}
		if p.Error != "" {
			lines = append(lines, prefix+"error: "+normalizeExportText(p.Error))
		}
		return lines
	case "sub_agent":
		lines := []string{fmt.Sprintf("%s[sub-agent] %s - %s", prefix, p.Task, p.Status)}
		for _, inner := range p.Parts {
			lines = append(lines, partPlainLines(inner, depth+1)...)
		}
		return lines
	case "question":
		return []string{prefix + "[question] " + defaultStr(p.QuestionStatus, "open")}
	default:
		return nil
	}
}

func appendTextLines(lines []string, text string) []string {
	text = normalizeExportText(text)
	text = strings.TrimSpace(text)
	if text == "" {
		return lines
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, strings.TrimRight(line, "\r"))
	}
	return lines
}

type pdfRenderer struct {
	title string
	pages []string
	buf   strings.Builder
	runes map[rune]struct{}
	y     float64
	page  int
}

func newPDFRenderer(title string) *pdfRenderer {
	r := &pdfRenderer{title: title, runes: make(map[rune]struct{})}
	r.newPage()
	return r
}

func (r *pdfRenderer) titlePage(conv *memory.Conversation, count int) {
	r.text(pdfM, r.y, 22, [3]float64{0.12, 0.14, 0.22}, conv.Title)
	if conv.Title == "" {
		r.text(pdfM, r.y, 22, [3]float64{0.12, 0.14, 0.22}, "(untitled)")
	}
	r.y -= 28
	meta := []string{
		"Session: " + conv.ID,
		"Created: " + conv.CreatedAt.Format("2006-01-02 15:04:05"),
		"Updated: " + conv.UpdatedAt.Format("2006-01-02 15:04:05"),
		fmt.Sprintf("Messages: %d", count),
	}
	for _, line := range meta {
		r.text(pdfM, r.y, 10, [3]float64{0.42, 0.44, 0.52}, line)
		r.y -= 15
	}
	r.y -= 16
}

func (r *pdfRenderer) sectionHeader(text string, color [3]float64) {
	r.ensure(34)
	r.rect(pdfM, r.y-18, pdfPageW-pdfM*2, 24, [3]float64{0.94, 0.95, 0.98})
	r.rect(pdfM, r.y-18, 4, 24, color)
	r.text(pdfM+12, r.y-2, 12, color, text)
	r.y -= 34
}

func (r *pdfRenderer) paragraph(text string) {
	if strings.TrimSpace(text) == "" {
		r.gap(7)
		return
	}
	for _, line := range wrapForPDF(text, 82) {
		r.ensure(15)
		r.text(pdfM, r.y, 10, [3]float64{0.10, 0.12, 0.18}, line)
		r.y -= 14
	}
}

func (r *pdfRenderer) gap(v float64) {
	r.ensure(v)
	r.y -= v
}

func (r *pdfRenderer) ensure(need float64) {
	if r.y-need < pdfM {
		r.newPage()
	}
}

func (r *pdfRenderer) newPage() {
	if r.buf.Len() > 0 {
		r.footer()
		r.pages = append(r.pages, r.buf.String())
		r.buf.Reset()
	}
	r.page++
	r.y = pdfPageH - pdfM
}

func (r *pdfRenderer) footer() {
	r.text(pdfM, 24, 8, [3]float64{0.55, 0.57, 0.64}, fmt.Sprintf("P-Chat Export - page %d", r.page))
}

func (r *pdfRenderer) text(x, y, size float64, color [3]float64, text string) {
	text = normalizePDFText(text)
	if strings.TrimSpace(text) == "" {
		return
	}
	collectPDFRunes(r.runes, text)
	fmt.Fprintf(&r.buf, "BT %.3f %.3f %.3f rg /F1 %.1f Tf %.1f %.1f Td <%s> Tj ET\n",
		color[0], color[1], color[2], size, x, y, utf16Hex(text))
}

func (r *pdfRenderer) rect(x, y, w, h float64, color [3]float64) {
	fmt.Fprintf(&r.buf, "q %.3f %.3f %.3f rg %.1f %.1f %.1f %.1f re f Q\n",
		color[0], color[1], color[2], x, y, w, h)
}

func (r *pdfRenderer) bytes() []byte {
	if r.buf.Len() > 0 {
		r.footer()
		r.pages = append(r.pages, r.buf.String())
		r.buf.Reset()
	}
	return buildPDF(r.pages, r.runes)
}

func wrapForPDF(s string, max int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	width := 0
	for _, rr := range s {
		w := 1
		if rr > 0x7f {
			w = 2
		}
		if width+w > max && cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
			width = 0
		}
		cur.WriteRune(rr)
		width += w
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func utf16Hex(s string) string {
	var b strings.Builder
	for _, rr := range s {
		fmt.Fprintf(&b, "%04X", rr)
	}
	return b.String()
}

func normalizePDFText(s string) string {
	var b strings.Builder
	for _, rr := range s {
		if rr == '\t' {
			rr = ' '
		}
		if rr < 0 || rr > 0xffff {
			rr = '?'
		}
		b.WriteRune(rr)
	}
	return b.String()
}

func collectPDFRunes(dst map[rune]struct{}, s string) {
	for _, rr := range s {
		if rr >= 0 && rr <= 0xffff {
			dst[rr] = struct{}{}
		}
	}
}

func buildPDF(pageContents []string, used map[rune]struct{}) []byte {
	if font, err := loadPDFUnicodeFont(used); err == nil {
		return buildPDFWithEmbeddedFont(pageContents, font)
	}
	return buildPDFWithFallbackFont(pageContents)
}

func buildPDFWithFallbackFont(pageContents []string) []byte {
	var objects [][]byte
	add := func(s string) int {
		objects = append(objects, []byte(s))
		return len(objects)
	}
	catalogID := add("")
	pagesID := add("")
	fontID := add("<< /Type /Font /Subtype /Type0 /BaseFont /STSong-Light /Encoding /UniGB-UCS2-H /DescendantFonts [4 0 R] >>")
	cidFontID := add("<< /Type /Font /Subtype /CIDFontType0 /BaseFont /STSong-Light /CIDSystemInfo << /Registry (Adobe) /Ordering (GB1) /Supplement 2 >> /DW 1000 >>")
	_ = catalogID
	_ = fontID
	_ = cidFontID

	pageIDs := make([]int, 0, len(pageContents))
	for _, content := range pageContents {
		contentID := add(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len([]byte(content)), content))
		pageID := add(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %.0f %.0f] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
			pagesID, pdfPageW, pdfPageH, fontID, contentID))
		pageIDs = append(pageIDs, pageID)
	}
	var kids strings.Builder
	for _, id := range pageIDs {
		fmt.Fprintf(&kids, "%d 0 R ", id)
	}
	objects[catalogID-1] = []byte(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesID))
	objects[pagesID-1] = []byte(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids.String(), len(pageIDs)))

	return writePDFObjects(objects, catalogID)
}

func writePDFObjects(objects [][]byte, catalogID int) []byte {
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n%\xE2\xE3\xCF\xD3\n")
	offsets := make([]int, len(objects)+1)
	for i, obj := range objects {
		offsets[i+1] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n", i+1)
		out.Write(obj)
		out.WriteString("\nendobj\n")
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n", len(objects)+1)
	out.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&out, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, catalogID, xref)
	return out.Bytes()
}
