package knowledge

import (
	"path"
	"path/filepath"
	"strings"
)

// ExcludedByPatterns reports whether rel matches a knowledge-base exclude pattern.
func ExcludedByPatterns(rel string, patterns []string) bool {
	rel = normalizeExcludePath(rel)
	if rel == "" {
		return false
	}
	baseName := path.Base(rel)
	for _, pat := range patterns {
		pat = normalizeExcludePath(pat)
		if pat == "" {
			continue
		}
		if strings.HasSuffix(pat, "/**") {
			prefix := strings.TrimSuffix(pat, "/**")
			if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
				return true
			}
		}
		if strings.HasSuffix(pat, "/") {
			prefix := strings.TrimSuffix(pat, "/")
			if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
				return true
			}
		}
		if matched, _ := path.Match(pat, rel); matched {
			return true
		}
		if !strings.Contains(pat, "/") {
			if matched, _ := path.Match(pat, baseName); matched {
				return true
			}
		}
	}
	return false
}

func normalizeExcludePath(s string) string {
	s = strings.TrimSpace(s)
	s = filepath.ToSlash(s)
	s = strings.ReplaceAll(s, "\\", "/")
	for strings.HasPrefix(s, "./") {
		s = strings.TrimPrefix(s, "./")
	}
	return strings.TrimPrefix(s, "/")
}
