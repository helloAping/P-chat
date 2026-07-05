package knowledge

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ==============================
// BuildHeadingTree tests
// ==============================

func TestBuildHeadingTree_H1H2H3(t *testing.T) {
	text := "# Top\n\ntop text\n\n## Middle\n\nmiddle text\n\n### Deep\n\ndeep text"
	roots := BuildHeadingTree(text, 3)
	if len(roots) != 1 {
		t.Fatalf("want 1 root, got %d", len(roots))
	}
	top := roots[0]
	if top.Title != "Top" {
		t.Errorf("root title = %q", top.Title)
	}
	if !strings.Contains(top.OwnText, "top text") {
		t.Errorf("root OwnText: %q", top.OwnText)
	}
	if len(top.Children) != 1 {
		t.Fatalf("want 1 child, got %d", len(top.Children))
	}
	middle := top.Children[0]
	if middle.Title != "Middle" {
		t.Errorf("middle title = %q", middle.Title)
	}
	if middle.Parent != top {
		t.Error("middle.Parent should be top")
	}
	if len(middle.Children) != 1 {
		t.Fatalf("want 1 grandchild, got %d", len(middle.Children))
	}
	deep := middle.Children[0]
	if deep.Title != "Deep" {
		t.Errorf("deep title = %q", deep.Title)
	}
	if deep.Parent != middle {
		t.Error("deep.Parent should be middle")
	}
}

func TestBuildHeadingTree_MultipleH1(t *testing.T) {
	text := "# A\n\na\n\n# B\n\nb"
	roots := BuildHeadingTree(text, 3)
	if len(roots) != 2 {
		t.Fatalf("want 2 roots, got %d", len(roots))
	}
	if roots[0].Title != "A" || roots[1].Title != "B" {
		t.Errorf("titles: %q, %q", roots[0].Title, roots[1].Title)
	}
}

func TestBuildHeadingTree_EmptyNodeFiltered(t *testing.T) {
	text := "# Top\n\n## Middle\n\nmiddle text"
	roots := BuildHeadingTree(text, 3)
	top := roots[0]
	if !top.HasContent() {
		t.Error("top should have content (has children)")
	}
	if !top.Children[0].HasContent() {
		t.Error("middle should have content (has text)")
	}
}

func TestBuildHeadingTree_NoHeadings(t *testing.T) {
	text := "Just some text without any headings."
	roots := BuildHeadingTree(text, 3)
	if len(roots) == 0 {
		return // acceptable — no sections to index
	}
}

func TestAggregatedContent_Hierarchy(t *testing.T) {
	text := "# Users\n\nUser management overview\n\n## Auth\n\nJWT auth details\n\n### OAuth\n\nGoogle OAuth setup"
	roots := BuildHeadingTree(text, 3)
	if len(roots) != 1 {
		t.Fatal("want 1 root")
	}
	agg := roots[0].AggregatedContent()
	if !strings.Contains(agg, "Users") {
		t.Errorf("missing root title: %s", agg)
	}
	if !strings.Contains(agg, "User management") {
		t.Errorf("missing root text: %s", agg)
	}
	if !strings.Contains(agg, "Auth") {
		t.Errorf("missing child title: %s", agg)
	}
	if !strings.Contains(agg, "JWT") {
		t.Errorf("missing child text: %s", agg)
	}
	if !strings.Contains(agg, "OAuth") {
		t.Errorf("missing grandchild: %s", agg)
	}
}

func TestWalkHeadingTree(t *testing.T) {
	text := "# A\n\n## B\n\n## C\n\n### D"
	roots := BuildHeadingTree(text, 3)
	var titles []string
	WalkHeadingTree(roots, func(n *HeadingNode) {
		titles = append(titles, n.Title)
	})
	if len(titles) != 4 {
		t.Fatalf("want 4 nodes, got %d: %v", len(titles), titles)
	}
}

// ==============================
// Indexer tests
// ==============================

func TestParseIndexEntry_Valid(t *testing.T) {
	raw := "内容概览：JWT认证实现\n关键词：jwt, token, auth\n搜索匹配：token生成与验证"
	r := ParseIndexEntry(raw)
	if r == nil {
		t.Fatal("expected parsed entry")
	}
	if r.Overview != "JWT认证实现" {
		t.Errorf("overview = %q", r.Overview)
	}
	if r.Keywords != "jwt, token, auth" {
		t.Errorf("keywords = %q", r.Keywords)
	}
	if r.SearchHints != "token生成与验证" {
		t.Errorf("hints = %q", r.SearchHints)
	}
}

func TestParseIndexEntry_Partial(t *testing.T) {
	raw := "内容概览：Only overview here"
	r := ParseIndexEntry(raw)
	if r == nil {
		t.Fatal("expected partial entry")
	}
	if r.Overview != "Only overview here" {
		t.Errorf("overview = %q", r.Overview)
	}
}

func TestParseIndexEntry_Invalid(t *testing.T) {
	raw := "  \n  \n"
	r := ParseIndexEntry(raw)
	if r != nil {
		t.Error("expected nil for empty/whitespace")
	}
}

func TestFormatIndexEntry_Roundtrip(t *testing.T) {
	r := &IndexEntryResult{
		Overview:    "概览内容",
		Keywords:    "k1, k2",
		SearchHints: "搜索意图",
	}
	formatted := FormatIndexEntry(r)
	parsed := ParseIndexEntry(formatted)
	if parsed == nil {
		t.Fatal("roundtrip failed")
	}
	if parsed.Overview != r.Overview || parsed.Keywords != r.Keywords || parsed.SearchHints != r.SearchHints {
		t.Errorf("roundtrip mismatch: got %+v, want %+v", parsed, r)
	}
}

func TestBuildIndexPrompt(t *testing.T) {
	prompt := BuildIndexPrompt("Auth", "Users", "Auth content here")
	if !strings.Contains(prompt, "Auth") {
		t.Errorf("missing node title: %s", prompt)
	}
	if !strings.Contains(prompt, "Users") {
		t.Errorf("missing parent title: %s", prompt)
	}
}

// ==============================
// Office extraction tests
// ==============================

func TestExtractOfficeText_Docx(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.docx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("word/document.xml")
	io.WriteString(w, `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Hello World from docx</w:t></w:r></w:p></w:body></w:document>`)
	zw.Close()
	f.Close()

	text, err := ExtractOfficeText(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Hello World from docx") {
		t.Errorf("expected 'Hello World from docx', got %q", text)
	}
}

func TestExtractOfficeText_Pptx(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.pptx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("ppt/slides/slide1.xml")
	io.WriteString(w, `<?xml version="1.0"?><p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>Slide 1 Title</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`)
	zw.Close()
	f.Close()

	text, err := ExtractOfficeText(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Slide 1 Title") {
		t.Errorf("expected 'Slide 1 Title', got %q", text)
	}
}

func TestExtractOfficeText_Xlsx(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.xlsx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	ss, _ := zw.Create("xl/sharedStrings.xml")
	io.WriteString(ss, `<?xml version="1.0"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>Cell A1</t></si><si><t>Cell B1</t></si></sst>`)
	sheet, _ := zw.Create("xl/worksheets/sheet1.xml")
	io.WriteString(sheet, `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c></row></sheetData></worksheet>`)
	zw.Close()
	f.Close()

	text, err := ExtractOfficeText(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Cell A1") {
		t.Errorf("expected 'Cell A1', got %q", text)
	}
}
