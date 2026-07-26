package outbound

// SplitText 按平台长度限制拆分文本，优先保留段落、换行和句子边界。
// SplitText splits text by platform limits, preferring paragraph, line, and sentence boundaries.
func SplitText(text string, maxBytes int) []string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return []string{text}
	}
	var chunks []string
	remaining := []rune(text)
	for utf8Len(remaining) > maxBytes {
		split := bestSplitIndex(remaining, maxBytes)
		if split <= 0 {
			split = 1
		}
		chunks = append(chunks, string(remaining[:split]))
		remaining = remaining[split:]
	}
	if len(remaining) > 0 || len(chunks) == 0 {
		chunks = append(chunks, string(remaining))
	}
	return chunks
}

func bestSplitIndex(runes []rune, maxBytes int) int {
	if utf8Len(runes) <= maxBytes {
		return len(runes)
	}
	limit := maxRuneIndexByBytes(runes, maxBytes)
	if idx := lastBoundary(runes, limit, paragraphBoundary); idx > 0 {
		return idx
	}
	if idx := lastBoundary(runes, limit, lineBoundary); idx > 0 {
		return idx
	}
	if idx := lastBoundary(runes, limit, sentenceBoundary); idx > 0 {
		return idx
	}
	if idx := lastBoundary(runes, limit, spaceBoundary); idx > 0 {
		return idx
	}
	return limit
}

type boundaryFunc func([]rune, int) (int, bool)

func lastBoundary(runes []rune, maxRunes int, match boundaryFunc) int {
	limit := maxRunes
	if limit > len(runes) {
		limit = len(runes)
	}
	for i := limit - 1; i >= 0; i-- {
		if idx, ok := match(runes, i); ok {
			return idx
		}
	}
	return 0
}

func paragraphBoundary(runes []rune, idx int) (int, bool) {
	if idx+1 >= len(runes) {
		return 0, false
	}
	if runes[idx] == '\n' && runes[idx+1] == '\n' {
		return idx + 2, true
	}
	return 0, false
}

func lineBoundary(runes []rune, idx int) (int, bool) {
	if runes[idx] == '\n' {
		return idx + 1, true
	}
	return 0, false
}

func sentenceBoundary(runes []rune, idx int) (int, bool) {
	switch runes[idx] {
	case '.', '!', '?', ';', ':', '\u3002', '\uff01', '\uff1f', '\uff1b', '\uff1a':
		return idx + 1, true
	default:
		return 0, false
	}
}

func spaceBoundary(runes []rune, idx int) (int, bool) {
	if idx == 0 {
		return 0, false
	}
	if runes[idx] == ' ' || runes[idx] == '\t' {
		return idx, true
	}
	return 0, false
}

func maxRuneIndexByBytes(runes []rune, maxBytes int) int {
	var total int
	for i, r := range runes {
		next := total + len(string(r))
		if next > maxBytes {
			return i
		}
		total = next
	}
	return len(runes)
}

func utf8Len(runes []rune) int {
	return len(string(runes))
}
