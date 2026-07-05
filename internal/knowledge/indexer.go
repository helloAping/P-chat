package knowledge

import (
	"strings"
)

// IndexEntryResult is the structured output of the LLM-based indexer.
type IndexEntryResult struct {
	Overview    string // 内容概览
	Keywords    string // 关键词 (comma-separated)
	SearchHints string // 搜索匹配
}

// ParseIndexEntry parses the LLM's 3-line plain-text response into
// structured fields. Returns nil if parsing fails.
func ParseIndexEntry(raw string) *IndexEntryResult {
	r := &IndexEntryResult{}
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "内容概览：") || strings.HasPrefix(line, "内容概览:"):
			r.Overview = strings.TrimPrefix(strings.TrimPrefix(line, "内容概览："), "内容概览:")
		case strings.HasPrefix(line, "关键词：") || strings.HasPrefix(line, "关键词:"):
			r.Keywords = strings.TrimPrefix(strings.TrimPrefix(line, "关键词："), "关键词:")
		case strings.HasPrefix(line, "搜索匹配：") || strings.HasPrefix(line, "搜索匹配:"):
			r.SearchHints = strings.TrimPrefix(strings.TrimPrefix(line, "搜索匹配："), "搜索匹配:")
		}
	}
	if r.Overview == "" && r.Keywords == "" && r.SearchHints == "" {
		return nil
	}
	return r
}

// FormatIndexEntry formats a parsed entry back into the canonical
// 3-line format for storage.
func FormatIndexEntry(r *IndexEntryResult) string {
	var b strings.Builder
	if r.Overview != "" {
		b.WriteString("内容概览：")
		b.WriteString(r.Overview)
		b.WriteString("\n")
	}
	if r.Keywords != "" {
		b.WriteString("关键词：")
		b.WriteString(r.Keywords)
		b.WriteString("\n")
	}
	if r.SearchHints != "" {
		b.WriteString("搜索匹配：")
		b.WriteString(r.SearchHints)
	}
	return b.String()
}

// BuildIndexPrompt constructs the LLM user prompt for indexing a
// single heading node with its aggregated content.
func BuildIndexPrompt(nodeTitle, parentTitle, aggregatedContent string) string {
	var b strings.Builder
	if parentTitle != "" {
		b.WriteString("这是「")
		b.WriteString(parentTitle)
		b.WriteString("」下的一个章节。\n\n")
	}
	b.WriteString("请为以下内容生成索引条目：\n\n")
	b.WriteString(aggregatedContent)
	return b.String()
}
