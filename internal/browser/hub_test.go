package browser

import (
	"strings"
	"testing"
	"time"
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

func TestProtocolCompatibility(t *testing.T) {
	if !ProtocolCompatible(ProtocolVersion) {
		t.Fatalf("ProtocolCompatible(%q) = false", ProtocolVersion)
	}
	if ProtocolCompatible("") {
		t.Fatal("empty protocol version should require an update")
	}
	if ProtocolCompatible("legacy") {
		t.Fatal("mismatched protocol version should require an update")
	}
	if msg := UpdateMessage(""); !strings.Contains(msg, "未上报协议版本") {
		t.Fatalf("UpdateMessage(empty) = %q", msg)
	}
}

func TestHubList_IncludesProtocolUpdateState(t *testing.T) {
	hub := NewHub()
	client := NewBrowserClient("browser-test", "Chrome Test", nil, HelloParams{
		BrowserName:      "Chrome Test",
		TabsCount:        2,
		ExtensionVersion: "1.0.0",
		ProtocolVersion:  "legacy",
	})
	hub.clients[client.ID()] = &clientEntry{
		client:      client,
		connectedAt: time.Now(),
	}

	list := hub.List()
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1", len(list))
	}
	got := list[0]
	if got.TabsCount != 2 {
		t.Fatalf("TabsCount = %d, want 2", got.TabsCount)
	}
	if !got.UpdateRequired {
		t.Fatal("UpdateRequired = false, want true for mismatched protocol")
	}
	if got.ProtocolCompatible {
		t.Fatal("ProtocolCompatible = true, want false for mismatched protocol")
	}
	if !strings.Contains(got.UpdateMessage, ProtocolVersion) {
		t.Fatalf("UpdateMessage = %q, want expected protocol version", got.UpdateMessage)
	}
}

func TestHub_Stop_NoRun(t *testing.T) {
	hub := NewHub()
	// Stop without calling Run should not panic.
	hub.Stop()
	// Double stop should also not panic (stopOnce).
	hub.Stop()
}

func TestBrowserClient_SetActiveTabMeta(t *testing.T) {
	client := NewBrowserClient("browser-test", "Chrome Test", nil, HelloParams{
		BrowserName: "Chrome Test",
		TabsCount:   1,
	})
	client.SetActiveTabMeta(42, "Example", "https://example.com", 3)
	snap := client.Snapshot()
	if snap.ActiveTabID != 42 {
		t.Fatalf("ActiveTabID = %d, want 42", snap.ActiveTabID)
	}
	if snap.ActiveTabTitle != "Example" {
		t.Fatalf("ActiveTabTitle = %q", snap.ActiveTabTitle)
	}
	if snap.ActiveTabURL != "https://example.com" {
		t.Fatalf("ActiveTabURL = %q", snap.ActiveTabURL)
	}
	if snap.TabsCount != 3 {
		t.Fatalf("TabsCount = %d, want 3", snap.TabsCount)
	}
	if client.ActiveTabID() != 42 {
		t.Fatalf("ActiveTabID() = %d, want 42", client.ActiveTabID())
	}
}

func TestHubList_IncludesActiveTabMeta(t *testing.T) {
	hub := NewHub()
	client := NewBrowserClient("browser-test", "Chrome Test", nil, HelloParams{
		BrowserName:      "Chrome Test",
		TabsCount:        2,
		ExtensionVersion: "1.1.0",
		ProtocolVersion:  ProtocolVersion,
	})
	client.SetActiveTabMeta(7, "Docs", "https://docs.example", 4)
	hub.clients[client.ID()] = &clientEntry{
		client:      client,
		connectedAt: time.Now(),
	}
	list := hub.List()
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1", len(list))
	}
	got := list[0]
	if got.ActiveTabID != 7 {
		t.Fatalf("ActiveTabID = %d, want 7", got.ActiveTabID)
	}
	if got.ActiveTabTitle != "Docs" {
		t.Fatalf("ActiveTabTitle = %q", got.ActiveTabTitle)
	}
	if got.TabsCount != 4 {
		t.Fatalf("TabsCount = %d, want 4", got.TabsCount)
	}
	if got.UpdateRequired {
		t.Fatal("UpdateRequired should be false for current protocol")
	}
}
