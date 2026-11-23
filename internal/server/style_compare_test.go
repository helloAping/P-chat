package server

import (
	"testing"

	"github.com/p-chat/pchat/internal/style"
)

func TestResolveCompareStyles(t *testing.T) {
	all := []style.Style{style.Cute, style.Guofeng, style.Tech, style.Style("warm")}
	got, err := ResolveCompareStyles("cute", nil, all)
	if err != nil || len(got) != 2 || got[0] != style.Guofeng || got[1] != style.Tech {
		t.Fatalf("default = %#v %v", got, err)
	}
	got, err = ResolveCompareStyles("cute", []string{"cute"}, all)
	if err != nil || len(got) != 1 || got[0] != style.Cute {
		t.Fatalf("current style = %#v %v", got, err)
	}
	if _, err = ResolveCompareStyles("cute", []string{"missing"}, all); err == nil {
		t.Fatal("unknown style should fail")
	}
	if _, err = ResolveCompareStyles("cute", []string{"off"}, all); err == nil {
		t.Fatal("off should fail")
	}
	if _, err = ResolveCompareStyles("cute", []string{"tech", "warm", "guofeng"}, all); err == nil {
		t.Fatal("three styles should fail")
	}
}
