//go:build windows

package main

import "testing"

func TestTrayCallbackActionFor_DecodesLegacyAndVersionedEvents(t *testing.T) {
	if got := trayCallbackActionFor(wmRButtonUp); got != trayCallbackMenu {
		t.Fatalf("legacy right-click action = %v, want menu", got)
	}
	versionedContext := uintptr(trayUID<<16) | uintptr(wmContextMenu)
	if got := trayCallbackActionFor(versionedContext); got != trayCallbackMenu {
		t.Fatalf("versioned context action = %v, want menu", got)
	}
	if got := trayCallbackActionFor(wmLButtonDbl); got != trayCallbackOpen {
		t.Fatalf("left double-click action = %v, want open", got)
	}
}
