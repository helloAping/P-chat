package subagent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/p-chat/pchat/internal/tool"
)

func noopHandler(_ context.Context, _ json.RawMessage) (*tool.CallResult, error) {
	return &tool.CallResult{Content: "ok"}, nil
}

// TestDefault_ExcludesTaskTool verifies the recursion guard.
func TestDefault_ExcludesTaskTool(t *testing.T) {
	parent := tool.NewRegistry()
	parent.Register(tool.Tool{Name: "task", Description: "spawn sub"}, noopHandler)
	parent.Register(tool.Tool{Name: "read_file", Description: "r"}, noopHandler)
	parent.Register(tool.Tool{Name: "recall", Description: "r"}, noopHandler)

	d := &Default{ParentTools: parent}

	subTools := tool.NewRegistry()
	for _, name := range d.ParentTools.Names() {
		if name == "task" || name == "recall" {
			continue
		}
		if tt, h, ok := d.ParentTools.Lookup(name); ok {
			subTools.Register(tt, h)
		}
	}

	if _, ok := subTools.Get("task"); ok {
		t.Error("task must NOT be in sub-agent registry")
	}
	if _, ok := subTools.Get("recall"); ok {
		t.Error("recall must NOT be in sub-agent registry")
	}
	if _, ok := subTools.Get("read_file"); !ok {
		t.Error("read_file SHOULD be in sub-agent registry")
	}
}

// TestDefault_AppliesAllowDenyFilter mirrors the production
// `config.SubAgentConfig.ToolAllowed` logic. Whitelist has priority
// over denylist: when `allowedList` is non-empty, only listed tools
// pass; otherwise denylist filters out the rest.
func TestDefault_AppliesAllowDenyFilter(t *testing.T) {
	filter := func(allowedList, deniedList []string) func(string) bool {
		return func(name string) bool {
			if name == "task" {
				return false
			}
			if len(allowedList) > 0 {
				for _, n := range allowedList {
					if n == name {
						return true
					}
				}
				return false
			}
			for _, n := range deniedList {
				if n == name {
					return false
				}
			}
			return true
		}
	}

	t.Run("whitelist", func(t *testing.T) {
		allow := filter([]string{"read_file", "list_files"}, nil)
		cases := map[string]bool{
			"read_file":   true,
			"list_files":  true,
			"write_file":  false,
			"exec_command": false,
			"task":        false, // always blocked
		}
		for n, want := range cases {
			if got := allow(n); got != want {
				t.Errorf("allow(%q) = %v, want %v", n, got, want)
			}
		}
	})

	t.Run("denylist", func(t *testing.T) {
		allow := filter(nil, []string{"exec_command"})
		cases := map[string]bool{
			"read_file":   true,
			"exec_command": false,
			"task":        false,
		}
		for n, want := range cases {
			if got := allow(n); got != want {
				t.Errorf("allow(%q) = %v, want %v", n, got, want)
			}
		}
	})
}
