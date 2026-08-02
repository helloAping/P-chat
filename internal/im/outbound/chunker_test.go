package outbound

import "testing"

func TestSplitTextKeepsShortTextWhole(t *testing.T) {
	got := SplitText("hello", 10)
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("chunks = %+v, want [hello]", got)
	}
}

func TestSplitTextPrefersParagraphAndLineBreaks(t *testing.T) {
	text := "alpha beta\nsecond line\n\nthird block"
	got := SplitText(text, 12)
	want := []string{"alpha beta\n", "second line\n\n", "third block"}
	assertChunks(t, got, want)
}

func TestSplitTextPrefersSentenceBoundary(t *testing.T) {
	text := "first sentence. second sentence. third"
	got := SplitText(text, 18)
	want := []string{"first sentence.", " second sentence.", " third"}
	assertChunks(t, got, want)
}

func TestSplitTextSupportsChineseSentenceBoundary(t *testing.T) {
	text := "\u7b2c\u4e00\u53e5\u3002\u7b2c\u4e8c\u53e5\u3002\u7b2c\u4e09\u53e5"
	got := SplitText(text, 20)
	want := []string{
		"\u7b2c\u4e00\u53e5\u3002",
		"\u7b2c\u4e8c\u53e5\u3002",
		"\u7b2c\u4e09\u53e5",
	}
	assertChunks(t, got, want)
}

func TestSplitTextUsesUTF8Bytes(t *testing.T) {
	got := SplitText("\u4f60\u597d\u4e16\u754c", 6)
	want := []string{"\u4f60\u597d", "\u4e16\u754c"}
	assertChunks(t, got, want)
}

func TestSplitTextPreservesWhitespace(t *testing.T) {
	got := SplitText("  hello\nworld", 8)
	want := []string{"  hello\n", "world"}
	assertChunks(t, got, want)
}

func TestSplitTextHardSplitsLongWord(t *testing.T) {
	got := SplitText("abcdefghij", 4)
	want := []string{"abcd", "efgh", "ij"}
	assertChunks(t, got, want)
}

func assertChunks(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("chunks = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chunk[%d] = %q, want %q (all=%+v)", i, got[i], want[i], got)
		}
	}
}
