package knowledge

import (
	"fmt"

	"github.com/ledongthuc/pdf"
)

// ExtractPDFText reads a PDF file and extracts plain text from all pages.
func ExtractPDFText(path string) (string, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return "", fmt.Errorf("open pdf: %w", err)
	}
	defer f.Close()

	var result string
	for pageNum := 1; pageNum <= r.NumPage(); pageNum++ {
		p := r.Page(pageNum)
		if p.V.IsNull() {
			continue
		}
		text, err := p.GetPlainText(nil)
		if err != nil {
			result += fmt.Sprintf("\n[page %d error: %v]\n", pageNum, err)
			continue
		}
		result += text
		if pageNum < r.NumPage() {
			result += "\n\n"
		}
	}
	return result, nil
}
