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

func TestPreferredTrayIconResourceIDs_IncludesWailsGeneratedGroupIcon(t *testing.T) {
	ids := preferredTrayIconResourceIDs()
	var hasOne, hasWails bool
	for _, id := range ids {
		if id == 1 {
			hasOne = true
		}
		if id == 3 {
			hasWails = true
		}
	}
	if !hasOne || !hasWails {
		t.Fatalf("preferred tray icon resource IDs = %v, want 1 and 3", ids)
	}
}
