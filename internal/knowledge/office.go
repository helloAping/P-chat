package knowledge

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// ExtractOfficeText reads a .docx, .xlsx, or .pptx file and extracts
// plain text. Uses only Go stdlib (archive/zip + encoding/xml), zero
// external dependencies. Returns empty string + nil if the file is
// readable but contains no extractable text.
func ExtractOfficeText(path string) (string, error) {
	ext := strings.ToLower(strings.TrimPrefix(path[strings.LastIndex(path, "."):], ""))
	// normalize
	if dot := strings.LastIndex(path, "."); dot >= 0 {
		ext = strings.ToLower(path[dot:])
	}

	r, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	switch {
	case ext == ".docx" || ext == ".docm":
		return extractDocx(r), nil
	case ext == ".xlsx" || ext == ".xlsm":
		return extractXlsx(r), nil
	case ext == ".pptx" || ext == ".pptm":
		return extractPptx(r), nil
	default:
		return "", fmt.Errorf("unsupported office format: %s", ext)
	}
}

// extractDocx reads word/document.xml and strips XML tags.
func extractDocx(r *zip.ReadCloser) string {
	f, err := r.Open("word/document.xml")
	if err != nil {
		return ""
	}
	defer f.Close()
	return stripXMLTags(f)
}

// extractXlsx reads shared strings and sheet data.
func extractXlsx(r *zip.ReadCloser) string {
	var b strings.Builder

	// Extract shared strings table.
	if sf, err := r.Open("xl/sharedStrings.xml"); err == nil {
		defer sf.Close()
		b.WriteString(strings.Join(parseSharedStrings(sf), " "))
		b.WriteString("\n")
	}

	// Extract inline text from each sheet.
	for _, f := range r.File {
		if !strings.HasPrefix(f.Name, "xl/worksheets/sheet") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		text := stripXMLTags(rc)
		rc.Close()
		b.WriteString(text)
		b.WriteString("\n")
	}
	return b.String()
}

// extractPptx reads ppt/slides/slide*.xml and strips XML tags.
func extractPptx(r *zip.ReadCloser) string {
	var b strings.Builder
	for _, f := range r.File {
		if !strings.HasPrefix(f.Name, "ppt/slides/slide") || !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		b.WriteString(stripXMLTags(rc))
		b.WriteString("\n\n")
		rc.Close()
	}
	return b.String()
}

// stripXMLTags reads an XML stream and returns plain text by removing
// all elements between < and >, plus normalizing whitespace.
func stripXMLTags(r io.Reader) string {
	data, err := io.ReadAll(r)
	if err != nil {
		return ""
	}
	s := string(data)
	var b strings.Builder
	inTag := false
	for _, ch := range s {
		if ch == '<' {
			inTag = true
			continue
		}
		if ch == '>' {
			inTag = false
			b.WriteByte(' ')
			continue
		}
		if !inTag {
			b.WriteRune(ch)
		}
	}
	// Collapse whitespace
	result := strings.Join(strings.Fields(b.String()), " ")
	return result
}

// parseSharedStrings reads xl/sharedStrings.xml and returns the string
// table used by .xlsx to store shared cell values.
func parseSharedStrings(r io.Reader) []string {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil
	}
	// Fast path: use xml.Decoder to extract <t> text nodes.
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	var out []string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if el, ok := tok.(xml.StartElement); ok && el.Name.Local == "t" {
			var content strings.Builder
			for {
				inner, err := dec.Token()
				if err != nil {
					break
				}
				if cd, ok := inner.(xml.CharData); ok {
					content.Write(cd)
				}
				if _, ok := inner.(xml.EndElement); ok {
					break
				}
			}
			out = append(out, content.String())
		}
	}
	return out
}
