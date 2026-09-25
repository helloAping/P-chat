package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseGitBranchLine(t *testing.T) {
	tests := map[string]string{
		"## main...origin/main":             "main",
		"## feat/ui [ahead 1]":              "feat/ui",
		"## No commits yet on main":         "main",
		"## HEAD (no branch)":               "detached",
		"## release/v1...origin/release/v1": "release/v1",
	}
	for line, want := range tests {
		if got := parseGitBranchLine(line); got != want {
			t.Fatalf("parseGitBranchLine(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestProjectResponseKeepsCleanDirtyFlag(t *testing.T) {
	clean := false
	payload, err := json.Marshal(projectResponse{
		Name:   "P-Chat",
		Path:   "D:/develop/project/P-chat",
		Branch: "main",
		Dirty:  &clean,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := string(payload)
	if !strings.Contains(body, `"branch":"main"`) {
		t.Fatalf("expected branch in project response, got %s", body)
	}
	if !strings.Contains(body, `"dirty":false`) {
		t.Fatalf("expected clean dirty flag in project response, got %s", body)
	}
}
