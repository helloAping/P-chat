package knowledge

import (
	"os"
	"regexp"
	"strings"
)

var headingRe = regexp.MustCompile(`^(#{2,3})\s+(.+)$`)

// ParseWiki splits text into sections by markdown ## / ### headings.
// If no headings found, returns nil so the caller can fall back to
// treating the whole file as a single section.
func ParseWiki(text, source string, includeLevel int) []WikiSection {
	if includeLevel <= 0 {
		includeLevel = 3
	}

	var sections []WikiSection
	var currentTitle string
	var currentContent strings.Builder
	flushCurrent := func() {
		if currentTitle != "" {
			sections = append(sections, WikiSection{
				Title:   currentTitle,
				Content: strings.TrimSpace(currentContent.String()),
				Source:  source,
			})
			currentContent.Reset()
		}
	}

	for _, line := range strings.Split(text, "\n") {
		m := headingRe.FindStringSubmatch(line)
		if m != nil {
			flushCurrent()
			currentTitle = strings.TrimSpace(m[2])
			continue
		}
		if currentTitle != "" {
			currentContent.WriteString(line + "\n")
		}
	}
	flushCurrent()
	return sections
}

// ParseWikiFile reads and parses a markdown file. Falls back to
// single-section mode if no headings found.
func ParseWikiFile(path, source string, includeLevel int) ([]WikiSection, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := string(data)
	sections := ParseWiki(text, source, includeLevel)
	if len(sections) == 0 {
		title := source
		if idx := strings.LastIndex(source, "/"); idx >= 0 {
			title = source[idx+1:]
		}
		sections = []WikiSection{{
			Title:   title,
			Content: strings.TrimSpace(text),
			Source:  source,
		}}
	}
	return sections, nil
}
