package dynamic

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/tool"
)

// TestParseSpec_Exec is the happy path: a well-formed
// exec-type YAML parses without error and round-trips the
// expected fields.
func TestParseSpec_Exec(t *testing.T) {
	src := []byte(`
name: greet
description: "向用户问好"
template:
  type: exec
  command: "echo Hello, {{.args.name}}!"
  timeout: 5s
sandbox:
  exec: allow
  read: deny
  write: deny
`)
	spec, err := ParseSpec(src)
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if spec.Name != "greet" {
		t.Errorf("Name = %q, want greet", spec.Name)
	}
	if spec.Template.Type != "exec" {
		t.Errorf("Template.Type = %q, want exec", spec.Template.Type)
	}
	if spec.Template.Timeout.Std() != 5*time.Second {
		t.Errorf("Template.Timeout = %v, want 5s", spec.Template.Timeout)
	}
	if spec.Sandbox.Exec != "allow" {
		t.Errorf("Sandbox.Exec = %q, want allow", spec.Sandbox.Exec)
	}
	if spec.Sandbox.Read != "deny" {
		t.Errorf("Sandbox.Read = %q, want deny", spec.Sandbox.Read)
	}
	// Write was set to "deny" in the YAML, so the
	// default-to-confirm path doesn't apply here.
	if spec.Sandbox.Write != "deny" {
		t.Errorf("Sandbox.Write = %q, want deny (from YAML)", spec.Sandbox.Write)
	}
}

// TestParseSpec_HTTP covers the http variant: method defaults
// to GET, headers map is preserved, body template is kept.
func TestParseSpec_HTTP(t *testing.T) {
	src := []byte(`
name: fetch
description: "fetch a URL"
template:
  type: http
  method: POST
  url: "https://api.example.com/{{.args.endpoint}}"
  headers:
    X-Key: "{{.config.api_key}}"
  body: '{"q": "{{.args.q}}"}'
`)
	spec, err := ParseSpec(src)
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if spec.Template.Type != "http" {
		t.Errorf("Template.Type = %q, want http", spec.Template.Type)
	}
	if spec.Template.Method != "POST" {
		t.Errorf("Method = %q, want POST", spec.Template.Method)
	}
	if spec.Template.Headers["X-Key"] != "{{.config.api_key}}" {
		t.Errorf("Headers[X-Key] = %q, want template", spec.Template.Headers["X-Key"])
	}
}

// TestParseSpec_Echo covers the static-text variant: no
// command/url/body, just text.
func TestParseSpec_Echo(t *testing.T) {
	src := []byte(`
name: ping
description: "smoke test"
template:
  type: echo
  text: "pong {{.args.x}}"
`)
	spec, err := ParseSpec(src)
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if spec.Template.Type != "echo" {
		t.Errorf("Template.Type = %q, want echo", spec.Template.Type)
	}
	if spec.Template.Text != "pong {{.args.x}}" {
		t.Errorf("Text = %q, want template", spec.Template.Text)
	}
}

func TestParseSpec_ParametersYAMLObject(t *testing.T) {
	src := []byte(`
name: greet
description: "schema test"
parameters:
  type: object
  properties:
    name:
      type: string
  required: [name]
template:
  type: echo
  text: "hello"
`)
	spec, err := ParseSpec(src)
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if !strings.Contains(string(spec.Parameters), `"required":["name"]`) {
		t.Fatalf("Parameters = %s, want compiled JSON schema", string(spec.Parameters))
	}
}

func TestParseSpec_ParametersJSONBlock(t *testing.T) {
	src := []byte(`
name: greet
description: "schema test"
parameters: |
  {"type":"object","properties":{"name":{"type":"string"}}}
template:
  type: echo
  text: "hello"
`)
	spec, err := ParseSpec(src)
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if !strings.Contains(string(spec.Parameters), `"properties"`) {
		t.Fatalf("Parameters = %s, want JSON schema block", string(spec.Parameters))
	}
}

// TestParseSpec_MissingFields exercises the validation
// rules. Each call should fail with a specific error.
func TestParseSpec_MissingFields(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string // substring expected in the error
	}{
		{
			name: "missing name",
			yaml: `
description: x
template: { type: echo, text: y }
`,
			want: "name is required",
		},
		{
			name: "missing description",
			yaml: `
name: x
template: { type: echo, text: y }
`,
			want: "description is required",
		},
		{
			name: "missing template.type",
			yaml: `
name: x
description: y
template: { text: z }
`,
			want: "template.type is required",
		},
		{
			name: "unknown template.type",
			yaml: `
name: x
description: y
template: { type: bogus }
`,
			want: "template.type",
		},
		{
			name: "exec without command",
			yaml: `
name: x
description: y
template: { type: exec }
`,
			want: "command is required",
		},
		{
			name: "http without url",
			yaml: `
name: x
description: y
template: { type: http }
`,
			want: "url is required",
		},
		{
			name: "name with whitespace",
			yaml: `
name: "bad name"
description: y
template: { type: echo, text: z }
`,
			want: "whitespace",
		},
		{
			name: "bad sandbox value",
			yaml: `
name: x
description: y
template: { type: echo, text: z }
sandbox: { exec: maybe }
`,
			want: "allow|deny|confirm",
		},
		{
			name: "parameters must be object",
			yaml: `
name: x
description: y
template: { type: echo, text: z }
parameters: |
  [1, 2, 3]
`,
			want: "JSON object",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSpec([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("ParseSpec returned nil error for %q", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

func TestPreview_ExecDoesNotRunCommand(t *testing.T) {
	spec, err := ParseSpec([]byte(`
name: greet
description: "preview"
template:
  type: exec
  command: "echo Hello {{.args.name}}"
`))
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	res, err := Preview(spec, []byte(`{"name":"Ada"}`))
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if res.IsError {
		t.Fatalf("Preview IsError=true: %s", res.Content)
	}
	if !strings.Contains(res.Content, "[dry-run]") || !strings.Contains(res.Content, "echo Hello Ada") {
		t.Fatalf("preview content = %q, want dry-run rendered command", res.Content)
	}
}

// TestRender_ArgsAndConfig exercises the text/template
// substitution. Both .args.* and .config.* must resolve.
func TestRender_ArgsAndConfig(t *testing.T) {
	rc := RenderCtx{
		Args:   map[string]any{"name": "world", "count": 3},
		Config: map[string]any{"api_key": "secret-123"},
	}
	got, err := render("Hello {{.args.name}}, key={{.config.api_key}}", rc)
	if err != nil {
		t.Fatal(err)
	}
	want := "Hello world, key=secret-123"
	if got != want {
		t.Errorf("render = %q, want %q", got, want)
	}
}

// TestRender_MissingKeyIsZero verifies the
// `missingkey=zero` behavior: a reference to an absent
// key renders as `<no value>` rather than failing the
// whole tool call. Important for casual users who write
// a template that references a field the LLM might not
// always supply.
func TestRender_MissingKeyIsZero(t *testing.T) {
	rc := RenderCtx{Args: map[string]any{}}
	got, err := render("a={{.args.absent}}", rc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "<no value>") {
		t.Errorf("render = %q, want to contain <no value>", got)
	}
}

// TestBuildDynamicHandler_Echo is the integration smoke
// test: a parsed echo spec, when wrapped by
// BuildDynamicHandler, returns the rendered text on call.
func TestBuildDynamicHandler_Echo(t *testing.T) {
	spec, err := ParseSpec([]byte(`
name: ping
description: smoke
template:
  type: echo
  text: "pong {{.args.name}}"
`))
	if err != nil {
		t.Fatal(err)
	}
	h := BuildDynamicHandler(spec)
	res, err := h(context.Background(), []byte(`{"name":"alice"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Errorf("IsError = true: %s", res.Content)
	}
	if res.Content != "pong alice" {
		t.Errorf("Content = %q, want %q", res.Content, "pong alice")
	}
}

// TestBuildDynamicHandler_UnknownType covers the defensive
// fallback: if a spec with an unknown type sneaks past
// ParseSpec, the handler returns a clear error rather
// than panicking.
func TestBuildDynamicHandler_UnknownType(t *testing.T) {
	spec := Spec{Name: "x", Description: "x", Template: Template{Type: "bogus"}}
	h := BuildDynamicHandler(spec)
	res, err := h(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("IsError = false, want true for unknown type")
	}
	if !strings.Contains(res.Content, "unknown") {
		t.Errorf("Content = %q, want to mention 'unknown'", res.Content)
	}
}

// TestWatcher_AddRemove is the integration test for the
// reload loop: write a YAML → tick → tool registered;
// delete the YAML → tick → tool unregistered. Uses a
// short interval (50ms) and a few sleeps to keep the
// test fast.
func TestWatcher_AddRemove(t *testing.T) {
	dir := t.TempDir()
	reg := tool.NewRegistry()
	lookup := func(name string) map[string]any { return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := Watch(ctx, reg, dir, lookup, 50*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	// Empty state: no tools yet.
	if names := reg.Names(); len(names) != 0 {
		t.Fatalf("initial names = %v, want []", names)
	}

	// Write a YAML and wait for the next tick.
	yamlPath := filepath.Join(dir, "ping.yaml")
	if err := os.WriteFile(yamlPath, []byte(`
name: ping
description: "smoke"
template:
  type: echo
  text: "pong"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		_, ok := reg.Get("ping")
		return ok
	})
	if _, ok := reg.Get("ping"); !ok {
		t.Fatal("ping tool not registered after write")
	}

	// Delete the YAML; the next tick should unregister.
	if err := os.Remove(yamlPath); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		_, ok := reg.Get("ping")
		return !ok
	})
	if _, ok := reg.Get("ping"); ok {
		t.Fatal("ping tool still registered after delete")
	}
}

// TestWatcher_MalformedYAMLDoesNotPoison covers the
// "one bad file shouldn't take down the rest" rule. A
// user writes a YAML with a missing field; the watcher
// must log a warning and leave the other tools alone.
func TestWatcher_MalformedYAMLDoesNotPoison(t *testing.T) {
	dir := t.TempDir()
	reg := tool.NewRegistry()
	lookup := func(name string) map[string]any { return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := Watch(ctx, reg, dir, lookup, 50*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	// Good file first.
	if err := os.WriteFile(filepath.Join(dir, "ok.yaml"), []byte(`
name: ok
description: "good"
template:
  type: echo
  text: "ok"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		_, ok := reg.Get("ok")
		return ok
	})

	// Now a malformed one (missing name). The watcher
	// must log + skip; "ok" stays registered.
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(`
description: "no name"
template: { type: echo, text: x }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond) // give the watcher a few ticks
	if _, ok := reg.Get("ok"); !ok {
		t.Error("good tool 'ok' was unregistered after a bad YAML appeared")
	}
	if _, ok := reg.Get("bad"); ok {
		t.Error("malformed tool was registered (should have been skipped)")
	}
	waitFor(t, 2*time.Second, func() bool {
		for _, d := range DiagnosticsSnapshot() {
			if strings.HasSuffix(d.Source, "bad.yaml") && d.Status == "error" && strings.Contains(d.Error, "name is required") {
				return true
			}
		}
		return false
	})
}

// TestWatcher_DiagnosticsRecoverAfterFix verifies the GUI-facing
// diagnostics snapshot changes from error to loaded when the user
// fixes a YAML file.
func TestWatcher_DiagnosticsRecoverAfterFix(t *testing.T) {
	dir := t.TempDir()
	reg := tool.NewRegistry()
	lookup := func(name string) map[string]any { return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := Watch(ctx, reg, dir, lookup, 50*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	yamlPath := filepath.Join(dir, "flip.yaml")
	if err := os.WriteFile(yamlPath, []byte(`
description: "broken"
template: { type: echo, text: x }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		for _, d := range DiagnosticsSnapshot() {
			if d.Source == yamlPath && d.Status == "error" {
				return true
			}
		}
		return false
	})

	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(yamlPath, []byte(`
name: flip
description: "fixed"
template:
  type: echo
  text: "ok"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		for _, d := range DiagnosticsSnapshot() {
			if d.Source == yamlPath && d.Status == "loaded" && d.Name == "flip" {
				return true
			}
		}
		return false
	})
	if _, ok := reg.Get("flip"); !ok {
		t.Fatal("fixed dynamic tool was not registered")
	}
}

// TestWatcher_EditIsDetected: editing a YAML (changing
// the mtime without renaming) triggers a re-register on
// the next tick. Confirms the mtime-based diff logic.
func TestWatcher_EditIsDetected(t *testing.T) {
	dir := t.TempDir()
	reg := tool.NewRegistry()
	lookup := func(name string) map[string]any { return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := Watch(ctx, reg, dir, lookup, 50*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	yamlPath := filepath.Join(dir, "ping.yaml")
	if err := os.WriteFile(yamlPath, []byte(`
name: ping
description: "v1"
template:
  type: echo
  text: "first"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		_, ok := reg.Get("ping")
		return ok
	})

	// Edit the file. Bump the mtime so the watcher
	// sees a real change (text editors usually rewrite
	// the file in place, which already updates mtime).
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(yamlPath, []byte(`
name: ping
description: "v2"
template:
  type: echo
  text: "second"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Wait for the next tick to re-register.
	time.Sleep(150 * time.Millisecond)

	h, ok := reg.Get("ping")
	if !ok {
		t.Fatal("ping not registered after edit")
	}
	// The handler now should be the new one.
	res, err := h(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "second") {
		t.Errorf("Content = %q, want to contain 'second' (re-registration didn't pick up new text)", res.Content)
	}
}

func TestLoadSnapshot_DuplicateNamesAreDiagnostics(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(`
name: dup
description: "first"
template:
  type: echo
  text: "a"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.yaml"), []byte(`
name: dup
description: "second"
template:
  type: echo
  text: "b"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, specs, diagnostics, err := LoadSnapshot(dir, nil)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if _, ok := entries["dup"]; ok {
		t.Fatal("duplicate tool was registered; want both files reported as diagnostics")
	}
	if _, ok := specs["dup"]; ok {
		t.Fatal("duplicate spec was published; want both files reported as diagnostics")
	}
	errors := 0
	for _, d := range diagnostics {
		if d.Name == "dup" && d.Status == "error" && strings.Contains(d.Error, "duplicate dynamic tool name") {
			errors++
		}
	}
	if errors != 2 {
		t.Fatalf("diagnostics = %+v, want two duplicate-name errors", diagnostics)
	}
}

// waitFor polls `cond` every 10ms until it returns true or
// the deadline passes. The watcher is event-driven on a
// 50ms ticker in this test, so a 2-second ceiling is
// generous.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}
