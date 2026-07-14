package browser

import (
	"strings"
	"testing"
)

func TestNewBrowserID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := NewBrowserID()
		if !strings.HasPrefix(id, "browser-") {
			t.Fatalf("ID should start with 'browser-', got %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate ID: %q", id)
		}
		seen[id] = true
	}
}

func TestRPCError_Error(t *testing.T) {
	e := &RPCError{Code: -32600, Message: "Invalid Request"}
	got := e.Error()
	if got != "rpc error -32600: Invalid Request" {
		t.Fatalf("unexpected Error() string: %q", got)
	}
}

func TestHub_Enabled_Toggle(t *testing.T) {
	hub := NewHub()
	if hub.IsEnabled() {
		t.Fatal("new hub should be disabled")
	}
	hub.SetEnabled(true)
	if !hub.IsEnabled() {
		t.Fatal("SetEnabled(true) did not work")
	}
	hub.SetEnabled(false)
	if hub.IsEnabled() {
		t.Fatal("SetEnabled(false) did not work")
	}
}

func TestHub_Count_Empty(t *testing.T) {
	hub := NewHub()
	if hub.Count() != 0 {
		t.Fatalf("expected 0, got %d", hub.Count())
	}
	if hub.HasConnections() {
		t.Fatal("HasConnections should be false on empty hub")
	}
	list := hub.List()
	if len(list) != 0 {
		t.Fatalf("List() should be empty, got %d", len(list))
	}
}

func TestHub_Stop_NoRun(t *testing.T) {
	hub := NewHub()
	// Stop without calling Run should not panic.
	hub.Stop()
	// Double stop should also not panic (stopOnce).
	hub.Stop()
}
