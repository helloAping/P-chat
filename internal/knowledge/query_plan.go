package knowledge

import (
	"regexp"
	"strings"
)

var querySplitRe = regexp.MustCompile(`[\s,，、;；|]+`)
var pathLikeRe = regexp.MustCompile(`[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+`)

// QueryPlan is KB-03's lightweight query decomposition result.
type QueryPlan struct {
	Original string   `json:"original"`
	Queries  []string `json:"queries"`
}

// PlanQueries derives a small set of search queries from a user query.
// It is intentionally rule-based: no extra LLM call, deterministic, and
// cheap enough to run inside every knowledge search.
func PlanQueries(query string) QueryPlan {
	original := strings.TrimSpace(query)
	if original == "" {
		return QueryPlan{}
	}
	seen := map[string]bool{}
	var out []string
	add := func(q string) {
		q = strings.TrimSpace(strings.Trim(q, "`'\"()[]{}<>"))
		if q == "" {
			return
		}
		key := strings.ToLower(q)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, q)
	}

	add(original)
	for _, term := range extractBacktickTerms(original) {
		add(term)
	}
	for _, term := range pathLikeRe.FindAllString(original, -1) {
		add(term)
	}
	for _, term := range extractSymbolTerms(original) {
		add(term)
	}
	for _, part := range querySplitRe.Split(original, -1) {
		if usefulToken(part) {
			add(part)
		}
	}
	if len(out) > 5 {
		out = out[:5]
	}
	return QueryPlan{Original: original, Queries: out}
}

func extractBacktickTerms(s string) []string {
	var out []string
	for {
		start := strings.IndexByte(s, '`')
		if start < 0 {
			return out
		}
		s = s[start+1:]
		end := strings.IndexByte(s, '`')
		if end < 0 {
			return out
		}
		out = append(out, s[:end])
		s = s[end+1:]
	}
}

func extractSymbolTerms(s string) []string {
	var out []string
	fields := querySplitRe.Split(s, -1)
	for _, f := range fields {
		f = strings.Trim(f, "`'\"()[]{}<>：:")
		if looksLikeSymbol(f) {
			out = append(out, f)
		}
	}
	return out
}

func usefulToken(s string) bool {
	s = strings.TrimSpace(strings.Trim(s, "`'\"()[]{}<>：:"))
	if s == "" {
		return false
	}
	if looksLikeSymbol(s) {
		return true
	}
	// Keep CJK terms and longer English words; drop glue words.
	if hasCJK(s) {
		return len([]rune(s)) >= 2
	}
	return len(s) >= 4 && !isStopWord(strings.ToLower(s))
}

func looksLikeSymbol(s string) bool {
	if len(s) < 2 {
		return false
	}
	return strings.ContainsAny(s, "._-/") || hasCamelOrDigit(s)
}

func hasCamelOrDigit(s string) bool {
	var hasUpper, hasLower, hasDigit bool
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
	}
	return hasDigit || (hasUpper && hasLower)
}

func hasCJK(s string) bool {
	for _, r := range s {
		if (r >= '一' && r <= '鿿') || (r >= '㐀' && r <= '䶿') {
			return true
		}
	}
	return false
}

func isStopWord(s string) bool {
	switch s {
	case "what", "where", "when", "with", "from", "into", "about", "this", "that", "there", "their", "does", "have", "using", "如何", "怎么", "什么", "哪里":
		return true
	}
	return false
}
