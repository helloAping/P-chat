package subagent

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestParseAgentFile_Valid covers the happy path: well-formed
// frontmatter with all optional fields plus a body.
func TestParseAgentFile_Valid(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/explore.md"
	src := `---
name: explore
description: Fast read-only file search specialist.
model: gpt-4o-mini
color: "#44BA81"
tools: [read_file, list_files, exec_command]
hidden: false
---
You are a file search specialist.

Always return absolute paths.
Use the grep tool for regex searches.
`
	if err := writeFile(path, src); err != nil {
		t.Fatalf("write: %v", err)
	}
	a, err := ParseAgentFile(path)
	if err != nil {
		t.Fatalf("ParseAgentFile: %v", err)
	}
	if a.Name != "explore" {
		t.Errorf("Name = %q, want explore", a.Name)
	}
	if a.Description != "Fast read-only file search specialist." {
		t.Errorf("Description = %q", a.Description)
	}
	if a.Model != "gpt-4o-mini" {
		t.Errorf("Model = %q, want gpt-4o-mini", a.Model)
	}
	if a.Color != "#44BA81" {
		t.Errorf("Color = %q", a.Color)
	}
	if got, want := strings.Join(a.Tools, ","), "read_file,list_files,exec_command"; got != want {
		t.Errorf("Tools = %q, want %q", got, want)
	}
	if a.Hidden {
		t.Errorf("Hidden = true, want false")
	}
	if !strings.Contains(a.Prompt, "file search specialist") {
		t.Errorf("Prompt missing body: %q", a.Prompt)
	}
	if !strings.Contains(a.Prompt, "Always return absolute paths.") {
		t.Errorf("Prompt missing second line: %q", a.Prompt)
	}
}

// TestParseAgentFile_Defaults verifies that the file stem is
// used as the agent name when no `name:` is given, and that
// missing optional fields use zero-value defaults.
func TestParseAgentFile_Defaults(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/my-agent.md"
	src := `---
description: A test agent.
---
Just a prompt.
`
	if err := writeFile(path, src); err != nil {
		t.Fatalf("write: %v", err)
	}
	a, err := ParseAgentFile(path)
	if err != nil {
		t.Fatalf("ParseAgentFile: %v", err)
	}
	if a.Name != "my-agent" {
		t.Errorf("Name = %q, want my-agent (from file stem)", a.Name)
	}
	if a.Model != "" {
		t.Errorf("Model = %q, want empty", a.Model)
	}
	if a.Hidden {
		t.Errorf("Hidden = true, want false")
	}
	if len(a.Tools) != 0 {
		t.Errorf("Tools = %v, want empty", a.Tools)
	}
}

// TestParseAgentFile_QuotedValues checks that single and
// double-quoted YAML values are unwrapped correctly.
func TestParseAgentFile_QuotedValues(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/quoted.md"
	src := `---
name: "quoted name"
description: 'single quoted description'
model: gpt-4o
---
Prompt body.
`
	if err := writeFile(path, src); err != nil {
		t.Fatalf("write: %v", err)
	}
	a, err := ParseAgentFile(path)
	if err != nil {
		t.Fatalf("ParseAgentFile: %v", err)
	}
	if a.Name != "quoted name" {
		t.Errorf("Name = %q, want 'quoted name'", a.Name)
	}
	if a.Description != "single quoted description" {
		t.Errorf("Description = %q", a.Description)
	}
}

// TestParseAgentFile_NoFrontmatter covers the bare-markdown
// case: the file has no frontmatter, only a body. Without
// a `description:` line, the loader cannot tell the parent
// LLM when to use this agent — so we reject the file with
// a clear error rather than silently misregistering it.
func TestParseAgentFile_NoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/bare.md"
	src := "Just a prompt, no frontmatter.\n"
	if err := writeFile(path, src); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := ParseAgentFile(path)
	if err == nil {
		t.Fatalf("expected error for missing description, got nil")
	}
	if !strings.Contains(err.Error(), "description") {
		t.Errorf("error %q does not mention description", err.Error())
	}
}

// TestParseAgentFile_NoFrontmatter_NameFromStem covers a
// related path: the file has frontmatter but no `name:`
// line. The loader falls back to the file stem so the user
// can write a minimal config without naming the agent
// twice (once in filename, once in frontmatter).
func TestParseAgentFile_NoFrontmatter_NameFromStem(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/my-named-agent.md"
	src := `---
description: a test agent
---
body content
`
	if err := writeFile(path, src); err != nil {
		t.Fatalf("write: %v", err)
	}
	a, err := ParseAgentFile(path)
	if err != nil {
		t.Fatalf("ParseAgentFile: %v", err)
	}
	if a.Name != "my-named-agent" {
		t.Errorf("Name = %q, want my-named-agent (from stem)", a.Name)
	}
}

// TestParseAgentFile_InvalidYAML covers the failure paths
// the loader actively checks for.
func TestParseAgentFile_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		src  string
		want string // substring of the error message
	}{
		{
			name: "unterminated frontmatter",
			src: `---
name: x
description: y
missing closing fence
`,
			want: "unterminated frontmatter",
		},
		{
			name: "missing description",
			src: `---
name: x
---
body
`,
			want: "missing 'description'",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := dir + "/" + tc.name + ".md"
			if err := writeFile(path, tc.src); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := ParseAgentFile(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

// TestLoadFromDir_MergesAndSkips verifies that LoadFromDir
// (a) returns the parsed agents, (b) ignores non-.md files,
// and (c) silently skips a missing directory (the common
// "user has not created any agents" case).
func TestLoadFromDir_MergesAndSkips(t *testing.T) {
	dir := t.TempDir()
	must(t, writeFile(dir+"/a.md", "---\nname: a\ndescription: agent a\n---\nbody a\n"))
	must(t, writeFile(dir+"/b.md", "---\nname: b\ndescription: agent b\n---\nbody b\n"))
	must(t, writeFile(dir+"/README.txt", "this is not an agent")) // ignored
	agents, err := LoadFromDir(dir)
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("len(agents) = %d, want 2", len(agents))
	}
	// Sorted by name. (Note: the sort happens in
	// Registry.List, not in LoadFromDir — the loader
	// preserves directory order.)
}

// TestLoadFromDir_MissingDir returns no error and no agents
// for a nonexistent path. Used in main.go at startup so a
// fresh install doesn't need to seed the agent directory.
func TestLoadFromDir_MissingDir(t *testing.T) {
	agents, err := LoadFromDir("/nonexistent/path/that/does/not/exist")
	if err != nil {
		t.Fatalf("LoadFromDir on missing dir: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("len(agents) = %d, want 0", len(agents))
	}
}

// TestRegistry_RegisterAndGet covers the basic
// register/lookup contract.
func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	r.Register(AgentInfo{Name: "a", Description: "agent a"})
	r.Register(AgentInfo{Name: "b", Description: "agent b"})
	if got, ok := r.Get("a"); !ok || got.Description != "agent a" {
		t.Errorf("Get(a) = %+v, %v", got, ok)
	}
	if _, ok := r.Get("nonexistent"); ok {
		t.Errorf("Get(nonexistent) returned ok=true")
	}
	// Registering with a duplicate name silently overwrites.
	r.Register(AgentInfo{Name: "a", Description: "replaced"})
	if got, _ := r.Get("a"); got.Description != "replaced" {
		t.Errorf("after overwrite, Get(a).Description = %q, want 'replaced'", got.Description)
	}
}

// TestRegistry_ListSorted asserts that List returns agents
// in name-sorted order regardless of insertion order. The
// order is observable to the dynamic tool description that
// the parent LLM sees.
func TestRegistry_ListSorted(t *testing.T) {
	r := NewRegistry()
	r.Register(AgentInfo{Name: "zeta"})
	r.Register(AgentInfo{Name: "alpha"})
	r.Register(AgentInfo{Name: "mike"})
	list := r.List()
	if len(list) != 3 {
		t.Fatalf("len(list) = %d, want 3", len(list))
	}
	if list[0].Name != "alpha" || list[1].Name != "mike" || list[2].Name != "zeta" {
		t.Errorf("List order = %v, want alpha, mike, zeta", list)
	}
}

// TestRegistry_Describe verifies the dynamic tool-description
// string. Format mirrors opencode's
// `packages/opencode/src/tool/registry.ts:describeTask`.
func TestRegistry_Describe(t *testing.T) {
	r := NewRegistry()
	r.Register(AgentInfo{Name: "explore", Description: "Fast file search."})
	r.Register(AgentInfo{Name: "plan", Description: "Read-only architect."})
	r.Register(AgentInfo{Name: "internal", Description: "hidden", Hidden: true})
	got := r.Describe()
	if !strings.Contains(got, "- explore: Fast file search.") {
		t.Errorf("Describe missing explore: %q", got)
	}
	if !strings.Contains(got, "- plan: Read-only architect.") {
		t.Errorf("Describe missing plan: %q", got)
	}
	// Hidden agents are excluded from the description so the
	// parent LLM doesn't try to spawn them.
	if strings.Contains(got, "internal") {
		t.Errorf("Describe leaked hidden agent: %q", got)
	}
}

// TestRegistry_MergeFrom covers the project-overlay pattern:
// the per-project registry's entries replace matching names
// in the global registry, leaving other entries alone.
func TestRegistry_MergeFrom(t *testing.T) {
	global := NewRegistry()
	global.Register(AgentInfo{Name: "explore", Description: "global explore"})
	global.Register(AgentInfo{Name: "shared", Description: "global shared"})

	project := NewRegistry()
	project.Register(AgentInfo{Name: "explore", Description: "project explore (overrides)"})

	global.MergeFrom(project)

	if got, _ := global.Get("explore"); got.Description != "project explore (overrides)" {
		t.Errorf("after merge, Get(explore).Description = %q, want project value", got.Description)
	}
	if got, _ := global.Get("shared"); got.Description != "global shared" {
		t.Errorf("after merge, Get(shared).Description = %q, want 'global shared'", got.Description)
	}
}

// TestBuiltins_HasCoreAgents makes sure the three built-in
// agents we ship are present with the right names. Adding a
// fourth built-in would require updating this test and the
// AGENTS.md / SUBAGENT.md docs in lockstep.
func TestBuiltins_HasCoreAgents(t *testing.T) {
	bs := Builtins()
	names := make(map[string]bool, len(bs))
	for _, b := range bs {
		names[b.Name] = true
	}
	for _, want := range []string{"general-purpose", "explore", "plan"} {
		if !names[want] {
			t.Errorf("Builtins missing %q (got %v)", want, names)
		}
	}
	// Each built-in must have a non-empty prompt — a silent
	// zero-value prompt would mean the agent runs with the
	// parent's prompt, defeating the "specialized agent"
	// intent.
	for _, b := range bs {
		if b.Prompt == "" {
			t.Errorf("Builtins %q has empty prompt", b.Name)
		}
		if b.Color == "" {
			t.Errorf("Builtins %q has empty color (required for the card tint)", b.Name)
		}
	}
	// Read-only agents must not include write tools in
	// their whitelist — the per-agent Tools list is the
	// second line of defense after the global
	// subagent.denied_tools. exec_command is INTENTIONALLY
	// allowed for read-only shell commands (ls, grep, cat,
	// find) per the agent's prompt; the prompt is the
	// policy, the whitelist is just a backstop.
	for _, b := range bs {
		if b.Name == "explore" || b.Name == "plan" {
			for _, tn := range b.Tools {
				if tn == "write_file" {
					t.Errorf("read-only agent %q has write tool %q in whitelist", b.Name, tn)
				}
			}
		}
	}
}

// TestCache_ByKeyResume verifies that two calls with the
// same task_id return the cached result of the first call
// without re-running. This is the resume-by-id code path
// the LLM uses to dedupe repeated sub-agent invocations.
func TestCache_ByKeyResume(t *testing.T) {
	c := NewCache(time.Hour)
	r := Result{
		Content:   "first",
		TokensIn:  100,
		TokensOut: 50,
	}
	c.PutByKey("task-1|explore|gpt-4o-mini|default|openai", r)
	got, ok := c.GetByKey("task-1|explore|gpt-4o-mini|default|openai")
	if !ok {
		t.Fatalf("GetByKey returned not-found")
	}
	if got.Content != "first" || got.TokensIn != 100 {
		t.Errorf("GetByKey = %+v, want first/100", got)
	}
	// Different key → not found.
	if _, ok := c.GetByKey("task-2|explore|gpt-4o-mini|default|openai"); ok {
		t.Errorf("GetByKey on different key returned found")
	}
}

// TestCache_ByKeyTTLExpiry checks that the task_id path
// honors the same TTL as the legacy (description, style,
// provider) path. Without this, a stale task_id would
// return yesterday's result.
func TestCache_ByKeyTTLExpiry(t *testing.T) {
	c := NewCache(50 * time.Millisecond)
	c.PutByKey("k", Result{Content: "stale"})
	if _, ok := c.GetByKey("k"); !ok {
		t.Fatalf("GetByKey returned not-found before TTL")
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok := c.GetByKey("k"); ok {
		t.Errorf("GetByKey returned found after TTL")
	}
}

// writeFile is a tiny test helper that writes a string to a
// file and returns the error. Lives here (not in a shared
// testutil) because it's only used in this file.
func writeFile(path, src string) error {
	return os.WriteFile(path, []byte(src), 0o644)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("must: %v", err)
	}
}
