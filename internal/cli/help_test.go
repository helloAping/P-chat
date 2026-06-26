package cli

import (
	"strings"
	"testing"
)

func TestMatchCommand(t *testing.T) {
	cases := []struct {
		input    string
		wantCmd  string
		wantArgs string
		wantOK   bool
	}{
		{"/help", "/help", "", true},
		{"/h", "/help", "", true},
		{"/model gpt-4o", "/model", "gpt-4o", true},
		{"/m", "/model", "", true},
		{"/unknown", "", "", false},
		{"/style cute", "/style", "cute", true},
		{"/s", "/style", "", true},
		{"", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			cmd, args := matchCommand(c.input)
			if c.wantOK {
				if cmd == nil {
					t.Fatalf("expected match, got nil")
				}
				if cmd.Name != c.wantCmd {
					t.Errorf("name = %q, want %q", cmd.Name, c.wantCmd)
				}
				if args != c.wantArgs {
					t.Errorf("args = %q, want %q", args, c.wantArgs)
				}
			} else if cmd != nil {
				t.Errorf("expected nil, got %q", cmd.Name)
			}
		})
	}
}

func TestSlashCmdIndex_Aliases(t *testing.T) {
	// Every alias should point to a command.
	for name, cmd := range slashCmdIndex {
		if cmd == nil {
			t.Errorf("nil cmd for %q", name)
		}
	}
	// Every command name and alias should be indexed.
	for i := range slashCmds {
		c := &slashCmds[i]
		if got := slashCmdIndex[c.Name]; got != c {
			t.Errorf("name %q not in index or wrong cmd", c.Name)
		}
		for _, a := range c.Aliases {
			if got := slashCmdIndex[a]; got != c {
				t.Errorf("alias %q not in index or wrong cmd", a)
			}
		}
	}
}

func TestHelpOne_Unknown(t *testing.T) {
	// Just ensure it doesn't crash on unknown commands.
	if err := helpOne("/nonexistent"); err != nil {
		t.Errorf("helpOne on unknown: %v", err)
	}
}

func TestHelpOne_All(t *testing.T) {
	// Every command should be queryable by name.
	for _, c := range slashCmds {
		t.Run(c.Name, func(t *testing.T) {
			if err := helpOne(c.Name); err != nil {
				t.Errorf("helpOne(%q): %v", c.Name, err)
			}
		})
		// And by alias (if any).
		for _, alias := range c.Aliases {
			t.Run(c.Name+"_alias_"+alias, func(t *testing.T) {
				if err := helpOne(alias); err != nil {
					t.Errorf("helpOne(%q): %v", alias, err)
				}
			})
		}
	}
}

func TestCommandsHaveDescription(t *testing.T) {
	// Every command must have a non-empty description (shown in /help list).
	for _, c := range slashCmds {
		if strings.TrimSpace(c.Description) == "" {
			t.Errorf("command %s has empty description", c.Name)
		}
	}
}

func TestCommandsHaveUsage(t *testing.T) {
	// Commands meant to be discoverable should have Usage strings. The
	// ones that currently don't (if any) are tracked here.
	noUsageAllowed := map[string]bool{
		"/help": true, // /help is itself the help command
	}
	for _, c := range slashCmds {
		if noUsageAllowed[c.Name] {
			continue
		}
		if strings.TrimSpace(c.Usage) == "" {
			t.Errorf("command %s missing Usage string", c.Name)
		}
	}
}

// TestCommandsHaveExamples enforces that every user-facing command
// has at least one example so /help output is actually useful.
func TestCommandsHaveExamples(t *testing.T) {
	for _, c := range slashCmds {
		if len(c.Examples) == 0 {
			t.Errorf("command %s has no examples; add at least one", c.Name)
		}
	}
}

// TestArgsParseable ensures the Args string (free-form) starts at
// column 0 with reasonable line width. This is a smoke test, not a
// strict parser, but catches wildly malformed help.
func TestArgsParseable(t *testing.T) {
	for _, c := range slashCmds {
		for _, line := range strings.Split(c.Args, "\n") {
			if line == "" {
				continue
			}
			if len(line) > 120 {
				t.Errorf("command %s has overlong Args line (%d chars): %q", c.Name, len(line), line)
			}
		}
	}
}
