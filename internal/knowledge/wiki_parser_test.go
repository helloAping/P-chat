package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseWiki_H1Heading(t *testing.T) {
	text := "# Main Title\n\nContent under H1."
	sections := ParseWiki(text, "doc.md", 3)
	if len(sections) == 0 {
		t.Fatal("expected at least one section for H1")
	}
	if sections[0].Title != "Main Title" {
		t.Errorf("title = %q, want %q", sections[0].Title, "Main Title")
	}
}

func TestParseWiki_MixedHeadings(t *testing.T) {
	text := "# Top\n\ntop content\n\n## Section A\n\nA content\n\n### Sub\n\nsub content"
	sections := ParseWiki(text, "doc.md", 3)
	if len(sections) != 3 {
		t.Fatalf("want 3 sections, got %d", len(sections))
	}
	if sections[0].Title != "Top" {
		t.Errorf("section 0 = %q", sections[0].Title)
	}
	if sections[1].Title != "Section A" {
		t.Errorf("section 1 = %q", sections[1].Title)
	}
	if sections[2].Title != "Sub" {
		t.Errorf("section 2 = %q", sections[2].Title)
	}
}

func TestParseWiki_NoHeadings(t *testing.T) {
	text := "Just some text without any markdown headings."
	sections := ParseWiki(text, "doc.md", 3)
	if len(sections) != 0 {
		t.Fatalf("want 0 sections for heading-less text, got %d", len(sections))
	}
}

func TestParseWiki_PreambleContent(t *testing.T) {
	text := "intro text\nmore intro\n\n## Section\n\nbody"
	sections := ParseWiki(text, "doc.md", 3)
	if len(sections) < 1 {
		t.Fatalf("want at least 1 section, got %d", len(sections))
	}
	// "Section" heading should be captured as a section.
	found := false
	for _, s := range sections {
		if s.Title == "Section" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'Section' heading: %v", sections)
	}
}

func TestParseWiki_IncludeLevel(t *testing.T) {
	text := "# H1\n\nh1 content\n\n## H2\n\nh2 content\n\n### H3\n\nh3 content"
	// includeLevel=1: only H1
	sections := ParseWiki(text, "doc.md", 1)
	if len(sections) != 1 {
		t.Fatalf("level 1: want 1 section (H1 only), got %d", len(sections))
	}
	if sections[0].Title != "H1" {
		t.Errorf("level 1: title = %q", sections[0].Title)
	}

	// includeLevel=2: H1 and H2
	sections = ParseWiki(text, "doc.md", 2)
	if len(sections) != 2 {
		t.Fatalf("level 2: want 2 sections, got %d", len(sections))
	}
}

func TestParseWiki_CJKHeadings(t *testing.T) {
	text := "## 你好世界\n\n中文内容。\n\n## 日本語\n\n日本語の内容。"
	sections := ParseWiki(text, "doc.md", 3)
	if len(sections) != 2 {
		t.Fatalf("want 2 sections, got %d", len(sections))
	}
	if sections[0].Title != "你好世界" {
		t.Errorf("title 0 = %q", sections[0].Title)
	}
	if sections[1].Title != "日本語" {
		t.Errorf("title 1 = %q", sections[1].Title)
	}
}

func TestParseWiki_CodeFenceWithHash(t *testing.T) {
	text := "## Real Section\n\nreal content\n\n```\n# this is not a heading\n```\n\nmore content"
	sections := ParseWiki(text, "doc.md", 3)
	if len(sections) != 1 {
		t.Fatalf("want 1 section, got %d", len(sections))
	}
	if sections[0].Title != "Real Section" {
		t.Errorf("title = %q", sections[0].Title)
	}
	if !strings.Contains(sections[0].Content, "# this is not a heading") {
		t.Errorf("code fence content should be preserved in section: %q", sections[0].Content)
	}
}

func TestParseWikiFile_NoHeadingsFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.txt")
	os.WriteFile(path, []byte("plain text content"), 0o644)

	sections, err := ParseWikiFile(path, "plain.txt", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 {
		t.Fatalf("want 1 fallback section, got %d", len(sections))
	}
	// ParseWiki returns nil for headingless text, ParseWikiFile fallback uses source as title.
	if sections[0].Title != "plain.txt" {
		t.Errorf("title = %q, want 'plain.txt'", sections[0].Title)
	}
}

func TestParseWikiFile_WithHeadings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")
	os.WriteFile(path, []byte("## Section 1\n\ncontent 1\n\n## Section 2\n\ncontent 2"), 0o644)

	sections, err := ParseWikiFile(path, "doc.md", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 2 {
		t.Fatalf("want 2 sections, got %d", len(sections))
	}
}

func TestParseWiki_EmptyText(t *testing.T) {
	sections := ParseWiki("", "empty.md", 3)
	if len(sections) != 0 {
		t.Fatalf("want 0 sections for empty text, got %d", len(sections))
	}
}

func TestParseWiki_OnlyWhitespace(t *testing.T) {
	sections := ParseWiki("   \n  \n  ", "ws.md", 3)
	if len(sections) != 0 {
		t.Fatalf("want 0 sections for whitespace-only text, got %d", len(sections))
	}
}
