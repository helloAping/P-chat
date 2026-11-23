package server

import "testing"

func TestNormalizeSessionTags(t *testing.T) {
	got, err := normalizeSessionTags([]string{" 排查 ", "前端", "排查", "  "})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "排查" || got[1] != "前端" {
		t.Fatalf("got %#v", got)
	}

	if _, err := normalizeSessionTags([]string{"一二三四五六七八九十一二三四五六七"}); err == nil {
		t.Fatal("expected overlong tag to fail")
	}
	if _, err := normalizeSessionTags([]string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}); err == nil {
		t.Fatal("expected more than 8 tags to fail")
	}
	cleared, err := normalizeSessionTags(nil)
	if err != nil || len(cleared) != 0 {
		t.Fatalf("clear: %#v %v", cleared, err)
	}
}
