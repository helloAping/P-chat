package browser

import (
	"encoding/json"
	"testing"

	"github.com/p-chat/pchat/internal/tool"
)

func TestBuildToolDefs_Count(t *testing.T) {
	defs := buildToolDefs()
	handlers := buildHandlers(NewHub(), nil)
	if len(defs) != len(handlers) {
		t.Fatalf("defs (%d) and handlers (%d) count mismatch", len(defs), len(handlers))
	}
	if len(defs) != len(browserToolNames) {
		t.Fatalf("defs count %d doesn't match browserToolNames count %d", len(defs), len(browserToolNames))
	}
}

func TestBrowserToolNames_MatchDefs(t *testing.T) {
	defs := buildToolDefs()
	defNames := make(map[string]bool)
	for _, d := range defs {
		defNames[d.Name] = true
	}
	for _, name := range browserToolNames {
		if !defNames[name] {
			t.Errorf("browserToolNames has %q but it's not in buildToolDefs()", name)
		}
	}
}

func TestRegisterBrowserTools_AddsToRegistry(t *testing.T) {
	reg := tool.NewRegistry()
	hub := NewHub()

	RegisterBrowserTools(reg, hub, nil)

	// All browser tools should be in the registry.
	for _, name := range browserToolNames {
		h, ok := reg.Get(name)
		if !ok {
			t.Errorf("tool %q should be registered", name)
			continue
		}
		if h == nil {
			t.Errorf("tool %q handler should not be nil", name)
		}
	}
}

func TestUnregisterBrowserTools_RemovesFromRegistry(t *testing.T) {
	reg := tool.NewRegistry()
	hub := NewHub()

	RegisterBrowserTools(reg, hub, nil)
	UnregisterBrowserTools(reg)

	// No browser tools should remain.
	for _, name := range browserToolNames {
		if _, ok := reg.Get(name); ok {
			t.Errorf("tool %q should be unregistered", name)
		}
	}
}

func TestToolSchemas_ValidJSON(t *testing.T) {
	defs := buildToolDefs()
	for _, d := range defs {
		if d.Name == "" {
			t.Error("tool with empty name")
			continue
		}
		if d.Description == "" {
			t.Errorf("tool %q has empty description", d.Name)
		}
		// Parameters should be valid JSON if present.
		if len(d.Parameters) > 0 {
			var v any
			if err := json.Unmarshal(d.Parameters, &v); err != nil {
				t.Errorf("tool %q has invalid Parameters JSON: %v", d.Name, err)
			}
		}
	}
}

func TestToolSchemas_RequiredFields(t *testing.T) {
	defs := buildToolDefs()

	// browser_navigate should require "url".
	for _, d := range defs {
		if d.Name != "browser_navigate" {
			continue
		}
		var schema struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(d.Parameters, &schema); err != nil {
			t.Fatalf("unmarshal schema: %v", err)
		}
		found := false
		for _, r := range schema.Required {
			if r == "url" {
				found = true
				break
			}
		}
		if !found {
			t.Error("browser_navigate should have 'url' in required fields")
		}
	}

	// browser_click should require "ref".
	for _, d := range defs {
		if d.Name != "browser_click" {
			continue
		}
		var schema struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(d.Parameters, &schema); err != nil {
			t.Fatalf("unmarshal schema: %v", err)
		}
		found := false
		for _, r := range schema.Required {
			if r == "ref" {
				found = true
				break
			}
		}
		if !found {
			t.Error("browser_click should have 'ref' in required fields")
		}
	}
}

func TestToolSchemas_IncludeTabID(t *testing.T) {
	defs := buildToolDefs()
	need := map[string]bool{
		"browser_navigate": true,
		"browser_click":    true,
		"browser_tabs":     true,
		"browser_snapshot": true,
	}
	for _, d := range defs {
		if !need[d.Name] {
			continue
		}
		var schema struct {
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(d.Parameters, &schema); err != nil {
			t.Fatalf("%s unmarshal: %v", d.Name, err)
		}
		if _, ok := schema.Properties["tab_id"]; !ok {
			t.Errorf("%s missing tab_id property", d.Name)
		}
	}
}
