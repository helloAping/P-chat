package knowledge

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/ledongthuc/pdf"
)

// ValidatePDF 在接收附件时检查 PDF 结构是否可由实际提取器打开。
// ValidatePDF checks that the PDF can be opened by the same parser used for extraction.
func ValidatePDF(path string) error {
	f, _, err := openPDF(path)
	if err != nil {
		return fmt.Errorf("open pdf: %w", err)
	}
	return f.Close()
}

// ExtractPDFText reads a PDF file and extracts plain text from all pages.
func ExtractPDFText(path string) (string, error) {
	f, r, err := openPDF(path)
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

// openPDF allows a bounded non-PDF suffix after a structurally valid final EOF.
// openPDF 允许结构完整的 PDF 在结束标记后带有长度受限的非 PDF 尾部。
func openPDF(path string) (*os.File, *pdf.Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	r, err := pdf.NewReader(f, info.Size())
	if err == nil {
		return f, r, nil
	}
	// Some published PDFs append an HTML page after a complete %%EOF. The
	// parser checks only the last 100 bytes, so retry with a logical EOF while
	// leaving the original upload untouched. A missing or broken xref still fails.
	// 部分已发布 PDF 在完整 %%EOF 后附加 HTML；仅缩短解析视图，不改原文件。
	if strings.Contains(err.Error(), "missing %%EOF") {
		const maxTrailingBytes = 64 << 10
		tailSize := min(info.Size(), int64(maxTrailingBytes))
		if tailSize > 0 {
			tail := make([]byte, tailSize)
			if _, readErr := f.ReadAt(tail, info.Size()-tailSize); readErr == nil {
				if i := bytes.LastIndex(tail, []byte("%%EOF")); i >= 0 {
					logicalEnd := info.Size() - tailSize + int64(i+len("%%EOF"))
					if logicalEnd < info.Size() {
						if recovered, recoverErr := pdf.NewReader(f, logicalEnd); recoverErr == nil {
							return f, recovered, nil
						}
					}
				}
			}
		}
	}
	_ = f.Close()
	return nil, nil, err
}
