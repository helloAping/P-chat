package knowledge

import (
	"os"
	"regexp"
	"strings"
)

// headingRe matches H1-H3 markdown headings. Level is controlled by includeLevel.
var headingRe = regexp.MustCompile(`^(#{1,3})\s+(.+)$`)

// ParseWiki splits text into sections by markdown headings.
// includeLevel controls max heading depth: 1=H1 only, 2=H1-H2, 3=H1-H3 (default).
// H4+ headings are treated as regular content.
// If no headings found, returns nil so the caller can fall back to
// treating the whole file as a single section.
func ParseWiki(text, source string, includeLevel int) []WikiSection {
	if includeLevel <= 0 {
		includeLevel = 3
	}

	var sections []WikiSection
	var currentTitle string
	var currentContent strings.Builder
	preambleDone := false

	flushCurrent := func() {
		body := strings.TrimSpace(currentContent.String())
		if currentTitle != "" && body != "" {
			sections = append(sections, WikiSection{
				Title:   currentTitle,
				Content: body,
				Source:  source,
			})
		}
		currentContent.Reset()
	}

	inFence := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		}

		m := headingRe.FindStringSubmatch(line)
		if m != nil && !inFence {
			level := len(m[1])
			if level <= includeLevel {
				flushCurrent()
				if !preambleDone && currentContent.Len() == 0 {
					// First heading: any content accumulated before it is the preamble.
					// The preamble will be flushed at the end with the source name as title.
					preambleDone = true
				}
				preambleDone = true
				currentTitle = strings.TrimSpace(m[2])
				continue
			}
		}
		if currentTitle != "" {
			currentContent.WriteString(line + "\n")
		}
	}

	// Flush preamble if it exists: content before first heading, or all content if no headings.
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
