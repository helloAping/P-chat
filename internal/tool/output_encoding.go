package tool

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// DecodeCommandOutput decodes command output bytes as UTF-8, falling back to GBK.
func DecodeCommandOutput(data []byte) string {
	data = bytes.TrimRight(data, "\x00")
	if len(data) == 0 {
		return ""
	}
	if utf8.Valid(data) {
		return string(data)
	}
	if decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(data); err == nil && utf8.Valid(decoded) {
		return string(decoded)
	}
	return strings.ToValidUTF8(string(data), "�")
}
