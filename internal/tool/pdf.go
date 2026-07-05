package tool

import "github.com/p-chat/pchat/internal/knowledge"

// readPdf extracts plain text from a PDF file.
func readPdf(path string) (string, error) {
	return knowledge.ExtractPDFText(path)
}
