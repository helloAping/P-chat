package search

import (
	"strings"
	"time"
)

func clampCount(n int) int {
	if n <= 0 {
		return 5
	}
	if n > 10 {
		return 10
	}
	return n
}

func cleanSite(site string) string {
	site = strings.TrimSpace(site)
	if site == "" || strings.ContainsAny(site, " \t\"'") {
		return ""
	}
	return site
}

// withSearchHints appends site:, lang:, and after: operators that
// DeepSeek understands and that hosted search models can pass through.
func withSearchHints(query string, q Query) string {
	base := strings.TrimSpace(query)
	var hints []string
	if site := cleanSite(q.Site); site != "" && !strings.Contains(base, "site:"+site) {
		hints = append(hints, "site:"+site)
	}
	switch strings.ToLower(strings.TrimSpace(q.Language)) {
	case "zh", "en":
		hints = append(hints, "lang:"+strings.ToLower(strings.TrimSpace(q.Language)))
	}
	if q.RecencyDays > 0 {
		day := time.Now().UTC().AddDate(0, 0, -q.RecencyDays).Format("2006-01-02")
		hints = append(hints, "after:"+day)
	}
	if len(hints) == 0 {
		return base
	}
	return strings.TrimSpace(base + " " + strings.Join(hints, " "))
}

// limitRunes keeps the operator suffix and trims the query text so the
// whole string stays within max Unicode characters.
func limitRunes(query string, q Query, max int) string {
	base := strings.TrimSpace(query)
	hinted := withSearchHints(base, q)
	suffix := ""
	if strings.HasPrefix(hinted, base) {
		suffix = hinted[len(base):]
	}
	if max <= 0 {
		max = 128
	}
	room := max - len([]rune(suffix))
	baseRunes := []rune(base)
	if room < 1 {
		return truncateRunes(hinted, max)
	}
	if len(baseRunes) > room {
		baseRunes = baseRunes[:room]
	}
	return strings.TrimSpace(string(baseRunes) + suffix)
}
