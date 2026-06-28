package tool

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// readDocx extracts the plain text content from a .docx file.
// A .docx is a ZIP archive containing word/document.xml; we parse
// that XML and extract all <w:t> elements (the actual text runs).
func readDocx(path string) (string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("open docx: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		if f.Name == "word/document.xml" {
			rc, err := f.Open()
			if err != nil {
				return "", fmt.Errorf("open document.xml: %w", err)
			}
			defer rc.Close()
			return parseDocxXML(rc)
		}
	}
	return "", fmt.Errorf("docx has no word/document.xml entry")
}

// parseDocxXML streams the document.xml and collects all <w:t>
// text content. Paragraph boundaries become newlines.
func parseDocxXML(r io.Reader) (string, error) {
	decoder := xml.NewDecoder(r)
	var (
		sb      strings.Builder
		inT     bool
		inP     bool
	)
	for {
		tok, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", fmt.Errorf("parse document.xml: %w", err)
		}
		switch el := tok.(type) {
		case xml.StartElement:
			switch el.Name.Local {
			case "t":
				inT = true
			case "p":
				if inP {
					sb.WriteByte('\n')
				}
				inP = true
			}
		case xml.EndElement:
			switch el.Name.Local {
			case "t":
				inT = false
			case "p":
				if sb.Len() > 0 && sb.String()[sb.Len()-1] != '\n' {
					sb.WriteByte('\n')
				}
				inP = false
			}
		case xml.CharData:
			if inT {
				s := string(el)
				sb.WriteString(s)
			}
		}
	}
	return strings.TrimSpace(sb.String()), nil
}
