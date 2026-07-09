package server

import (
	"testing"
)

// TestParseCommandFromToolInput pins the exec_command
// tool-input JSON shape. The handler used to call this
// from buildMessageResponse (the line 1766-1770 block
// that was dead code: the result was assigned to
// resp.Name and then the function returned nil for the
// whole row, dropping the assignment). The function
// stays as a reusable helper for future subagent
// flows / non-filtered paths, and these tests document
// the contract.
//
// Returns:
//   - v.Command when the JSON has a "command" field.
//   - "" on JSON parse failure.
//   - "" when "command" is the empty string.
//   - "" when "command" is absent (other fields only).
func TestParseCommandFromToolInput(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "simple command",
			raw:  `{"command":"ls -la"}`,
			want: "ls -la",
		},
		{
			name: "command with timeout",
			raw:  `{"command":"dir","timeout":30}`,
			want: "dir",
		},
		{
			name: "windows path command",
			raw:  `{"command":"cmd.exe /C dir"}`,
			want: "cmd.exe /C dir",
		},
		{
			name: "empty command field",
			raw:  `{"command":""}`,
			want: "",
		},
		{
			name: "no command field",
			raw:  `{"path":"/tmp","timeout":30}`,
			want: "",
		},
		{
			name: "malformed json",
			raw:  `not json at all`,
			want: "",
		},
		{
			name: "empty input",
			raw:  ``,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseCommandFromToolInput(tc.raw)
			if got != tc.want {
				t.Errorf("parseCommandFromToolInput(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
