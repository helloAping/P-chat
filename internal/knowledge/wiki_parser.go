package knowledge

import (
	"os"
	"regexp"
	"strings"
)

// headingRe matches H1-H3 markdown headings. Level is controlled by includeLevel.
var headingRe = regexp.MustCompile(`^(#{1,3})\s+(.+)$`)

// ==============================
// HeadingNode — 层级标题树
// ==============================

// HeadingNode represents a markdown heading and its content subtree.
type HeadingNode struct {
	Title    string         // heading text
	Level    int            // 1=H1, 2=H2, 3=H3
	OwnText  string         // text directly under this heading (before any sub-heading)
	Children []*HeadingNode // sub-headings
	Parent   *HeadingNode   // parent node (nil for root H1)
}

// HasContent reports whether the node should produce an index entry.
// A node has content if it has its own text OR has child headings.
func (n *HeadingNode) HasContent() bool {
	return strings.TrimSpace(n.OwnText) != "" || len(n.Children) > 0
}

// AggregatedContent recursively builds a full-text context string for
// this node, including its own text and all children's titles + text.
// This is sent to the LLM for keyword extraction and summarisation.
func (n *HeadingNode) AggregatedContent() string {
	var b strings.Builder
	b.WriteString(n.Title)
	b.WriteString("\n")
	if strings.TrimSpace(n.OwnText) != "" {
		b.WriteString(n.OwnText)
		b.WriteString("\n")
	}
	for _, child := range n.Children {
		b.WriteString("\n子主题: ")
		b.WriteString(child.Title)
		if strings.TrimSpace(child.OwnText) != "" {
			b.WriteString(" — ")
			b.WriteString(strings.TrimSpace(child.OwnText))
		}
		b.WriteString("\n")
		// Recurse into grandchildren.
		for _, gc := range child.Children {
			b.WriteString("  子主题: ")
			b.WriteString(gc.Title)
			if strings.TrimSpace(gc.OwnText) != "" {
				b.WriteString(" — ")
				b.WriteString(strings.TrimSpace(gc.OwnText))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// BuildHeadingTree parses markdown into a tree of HeadingNode.
// includeLevel controls max heading depth: 1=H1 only, 2=H1-H2, 3=H1-H3.
// H4+ headings are treated as regular text.
func BuildHeadingTree(text string, includeLevel int) []*HeadingNode {
	if includeLevel <= 0 {
		includeLevel = 3
	}

	var roots []*HeadingNode
	var leaf *HeadingNode          // current deepest node collecting text
	var stack []*HeadingNode       // path from root to leaf
	preambleDone := false

	flushLeaf := func() {
		if leaf != nil {
			leaf.OwnText = strings.TrimSpace(leaf.OwnText)
		}
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
				flushLeaf()
				preambleDone = true
				title := strings.TrimSpace(m[2])

				node := &HeadingNode{Title: title, Level: level, OwnText: ""}

				// Find parent: pop stack until we find a shallower level.
				for len(stack) > 0 && stack[len(stack)-1].Level >= level {
					stack = stack[:len(stack)-1]
				}
				if len(stack) == 0 {
					// Top-level heading.
					roots = append(roots, node)
				} else {
					parent := stack[len(stack)-1]
					node.Parent = parent
					parent.Children = append(parent.Children, node)
				}
				stack = append(stack, node)
				leaf = node
				continue
			}
		}

		// Append text to the current leaf node.
		if leaf != nil {
			leaf.OwnText += line + "\n"
		} else if !preambleDone {
			// Preamble text before any heading — attach to a synthetic root.
			if len(roots) == 0 {
				roots = append(roots, &HeadingNode{Title: "_preamble_", Level: 0, OwnText: ""})
			}
			roots[0].OwnText += line + "\n"
		}
	}
	flushLeaf()

	// Strip preamble placeholder — if the only root is a _preamble_ node
	// (no real headings), return nil so caller uses the fallback path.
	if len(roots) == 1 && roots[0].Level == 0 && len(roots[0].Children) == 0 {
		// If preamble has text, keep it as a single synthetic root.
		if strings.TrimSpace(roots[0].OwnText) == "" {
			return nil
		}
	}
	if len(roots) > 1 {
		filtered := make([]*HeadingNode, 0, len(roots))
		for _, r := range roots {
			if r.Level > 0 || r.OwnText != "" {
				filtered = append(filtered, r)
			}
		}
		roots = filtered
	}

	// Trim OwnText on all nodes.
	var trimAll func(n *HeadingNode)
	trimAll = func(n *HeadingNode) {
		n.OwnText = strings.TrimSpace(n.OwnText)
		for _, c := range n.Children {
			trimAll(c)
		}
	}
	for _, r := range roots {
		trimAll(r)
	}

	return roots
}

// WalkHeadingTree calls fn for every node in the tree (depth-first).
func WalkHeadingTree(roots []*HeadingNode, fn func(*HeadingNode)) {
	var walk func(n *HeadingNode)
	walk = func(n *HeadingNode) {
		fn(n)
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
}

// ==============================
// Legacy flat parser (kept for CLI kb.go scanDir compatibility)
// ==============================

// ParseWiki splits text into sections by markdown headings.
// includeLevel controls max heading depth: 1=H1 only, 2=H1-H2, 3=H1-H3 (default).
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

	flushCurrent()
	return sections
}

// ParseWikiFile reads and parses a file for wiki indexing. PDF and
// Office documents have their text extracted first. Falls back to
// single-section mode if no headings found.
func ParseWikiFile(path, source string, includeLevel int) ([]WikiSection, error) {
	ext := strings.ToLower("")
	if dot := strings.LastIndex(path, "."); dot >= 0 {
		ext = strings.ToLower(path[dot:])
	}

	var text string
	switch {
	case ext == ".pdf":
		extracted, err := ExtractPDFText(path)
		if err != nil {
			return nil, err
		}
		text = extracted
	case ext == ".docx" || ext == ".docm" || ext == ".xlsx" || ext == ".xlsm" || ext == ".pptx" || ext == ".pptm":
		extracted, err := ExtractOfficeText(path)
		if err != nil {
			return nil, err
		}
		text = extracted
	default:
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		text = string(data)
	}
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

// ReadFileText reads a file and returns its text content, dispatching
// PDFs and Office documents through their respective extractors.
func ReadFileText(path string) (string, error) {
	ext := strings.ToLower("")
	if dot := strings.LastIndex(path, "."); dot >= 0 {
		ext = strings.ToLower(path[dot:])
	}
	switch {
	case ext == ".pdf":
		return ExtractPDFText(path)
	case ext == ".docx", ext == ".docm", ext == ".xlsx", ext == ".xlsm", ext == ".pptx", ext == ".pptm":
		return ExtractOfficeText(path)
	default:
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
}
