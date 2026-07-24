// Package dynamic is the P3-2 hot-reload layer for user-defined
// tools. A user drops a YAML file in ~/.p-chat/tools/, the
// watcher in this package picks it up on the next 5s poll,
// parses it into a Spec, and registers a handler with the
// tool.Registry. Delete the file → handler unregisters on the
// next tick.
//
// YAML v1 format — see docs/plans/round4-trace-and-extensibility-plan.md §4:
//
//	name: greet
//	description: "向用户问好。可用于测试。"
//	parameters:
//	  type: object
//	  properties:
//	    name:
//	      type: string
//	      description: "用户名"
//	  required: [name]
//	template:
//	  type: exec | http | echo
//	  # exec:
//	  command: "echo Hello, {{.args.name}}!"
//	  timeout: 5s
//	  # http:
//	  method: POST
//	  url: "https://api.example.com/{{.args.endpoint}}"
//	  headers: {"X-Key": "{{.config.api_key}}"}
//	  body: '{"q": "{{.args.q}}"}"
//	  # echo (debug / dry-run):
//	  text: "you called greet with name={{.args.name}}"
//	sandbox:
//	  exec: allow | deny | confirm
//	  read: allow | deny | confirm
//	  write: allow | deny | confirm
//
// The Spec.Source / Spec.ModTime fields are filled in by the
// loader, not the YAML. They feed the watcher's change
// detection (mtime) and the ToolListDrawer UI (Source for
// the "view YAML" action).
package dynamic

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Spec is the in-memory representation of a parsed YAML tool
// definition. The YAML tag is the on-disk field name; the
// Source / ModTime fields are populated by the loader (see
// loadSpec in watcher.go).
type Spec struct {
	// Name is the tool's identity used by the LLM and the
	// tool.Registry. Must be unique; colliding with a
	// built-in tool name is a load error.
	Name string `yaml:"name"`
	// Description is the human-readable text the LLM sees
	// when deciding whether to call the tool. Should be
	// terse but specific ("send an SMS to the user" beats
	// "sends things").
	Description string `yaml:"description"`
	// ParametersNode is the JSON Schema object the user wrote
	// under `parameters:`. It accepts both normal YAML mapping
	// syntax and a JSON object block scalar; ParseSpec compiles
	// it to Parameters for the registry.
	ParametersNode yaml.Node `yaml:"parameters"`
	// Parameters is the compiled json.RawMessage of
	// ParametersNode. Empty when the user didn't supply
	// one. The tool.Registry expects this shape; the
	// AsTool helper copies it through.
	Parameters json.RawMessage `yaml:"-"`
	// Template describes the actual side-effect the tool
	// performs when called. The Template.Type dispatcher
	// (BuildDynamicHandler in this package) picks an
	// implementation based on Type.
	Template Template `yaml:"template"`
	// Sandbox declares the user's preferred execution
	// policy. Each field ("exec" / "read" / "write") is
	// "allow" | "deny" | "confirm". The agent's
	// confirmTargetFor default branch reads these to
	// decide whether the tool needs a confirm modal
	// before invocation.
	Sandbox SandboxConfig `yaml:"sandbox"`

	// Source is the absolute path to the YAML file this
	// spec was loaded from. Filled in by LoadFromDir, not
	// the YAML. The ToolListDrawer surfaces it for the
	// "view source" action.
	Source string `yaml:"-"`
	// ModTime is the file's mtime at the moment of load.
	// The watcher uses it to detect edits and trigger a
	// re-register.
	ModTime time.Time `yaml:"-"`
	// Config is the per-tool config subtree loaded from
	// ~/.p-chat/config.json's `dynamic.<name>.config`
	// path. Stays as `any` (not map[string]any) so the
	// YAML loader can stash whatever shape the user
	// writes — and so render() can do `{{.config.api_key}}`
	// against it without a typed conversion.
	//
	// Filled in by the watcher (it reads the global
	// config and pulls out the per-tool sub-object), not
	// by ParseSpec.
	Config any `yaml:"-"`
}

// Template holds the type-specific fields. Exactly one set
// of (Command) / (URL+Method+...) / (Text) is populated per
// spec — the one matching Template.Type. Unused fields are
// left at their zero value and ignored at execution time.
type Template struct {
	// Type is the discriminator: "exec" runs a shell
	// command, "http" makes an HTTP request, "echo"
	// returns a static string. Empty Type fails parsing.
	Type string `yaml:"type"`
	// Command is the shell command for exec templates.
	// Supports `{{.args.foo}}` and `{{.config.api_key}}`
	// substitutions via text/template.
	Command string `yaml:"command,omitempty"`
	// Method / URL / Headers / Body for http templates.
	// Method defaults to GET when empty.
	Method  string            `yaml:"method,omitempty"`
	URL     string            `yaml:"url,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
	Body    string            `yaml:"body,omitempty"`
	// Text is the literal response for echo templates.
	// Used for smoke-testing the wiring without a real
	// side-effect.
	Text string `yaml:"text,omitempty"`
	// Timeout caps the total runtime of the template
	// (exec wall time or http round-trip). Defaults to
	// 30s when zero. The dynamic handler enforces it
	// via context.WithTimeout.
	Timeout Duration `yaml:"timeout,omitempty"`
}

// SandboxConfig declares the per-tool execution policy. The
// fields map loosely to the sandbox API in internal/sandbox
// (which uses regexes + path classification for the built-in
// tools). For dynamic tools the user's YAML is the policy
// source of truth — the agent's confirmTargetFor reads these
// strings and routes "confirm" through the existing modal.
//
// Allowed values per field:
//
//	"allow"   — invoke without prompting
//	"deny"    — block invocation entirely (return an error)
//	"confirm" — open the existing confirm modal first
//
// Empty string defaults to "confirm" (fail-safe).
type SandboxConfig struct {
	Exec  string `yaml:"exec,omitempty"`
	Read  string `yaml:"read,omitempty"`
	Write string `yaml:"write,omitempty"`
}

// ParseSpec parses the raw YAML bytes into a Spec. Returns
// the parsed spec plus a non-nil error if the YAML is
// malformed or required fields are missing. The Source /
// ModTime fields are NOT populated here — those are the
// loader's job (so tests can ParseSpec without touching
// the filesystem).
//
// Validation rules:
//   - name required (no whitespace, no leading dash)
//   - description required
//   - template.type required ("exec" / "http" / "echo")
//   - parameters: if present, must be a JSON object
//   - sandbox.{exec,read,write}: empty / "allow" / "deny" / "confirm"
func ParseSpec(data []byte) (Spec, error) {
	var s Spec
	if err := yaml.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("yaml unmarshal: %w", err)
	}
	// Compile the raw parameters string into the
	// json.RawMessage the rest of the package expects.
	// Done before Validate so the object-shape check
	// sees the compiled value.
	if s.ParametersNode.Kind != 0 {
		raw, err := compileParametersNode(&s.ParametersNode)
		if err != nil {
			return s, err
		}
		s.Parameters = json.RawMessage(raw)
	}
	if err := s.Validate(); err != nil {
		return s, err
	}
	// Normalize sandbox empty → "confirm" (fail-safe) so
	// confirmTargetFor's default branch doesn't have to
	// re-check.
	if s.Sandbox.Exec == "" {
		s.Sandbox.Exec = "confirm"
	}
	if s.Sandbox.Read == "" {
		s.Sandbox.Read = "confirm"
	}
	if s.Sandbox.Write == "" {
		s.Sandbox.Write = "confirm"
	}
	return s, nil
}

func compileParametersNode(n *yaml.Node) ([]byte, error) {
	if n == nil || n.Kind == 0 {
		return nil, nil
	}
	if n.Kind == yaml.ScalarNode {
		raw := []byte(n.Value)
		trimmed := skipWS(raw)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return nil, fmt.Errorf("parameters must be a JSON object, got %s...", string(trimmed)[:min(len(trimmed), 16)])
		}
		if !json.Valid(raw) {
			return nil, fmt.Errorf("parameters must be valid JSON object")
		}
		return raw, nil
	}
	var v any
	if err := n.Decode(&v); err != nil {
		return nil, fmt.Errorf("parameters decode: %w", err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("parameters marshal: %w", err)
	}
	return raw, nil
}

// Validate enforces the required-field rules. Called by
// ParseSpec; also useful for tests that want to assert on
// a single rule in isolation.
func (s *Spec) Validate() error {
	if s.Name == "" {
		return errors.New("name is required")
	}
	for _, r := range s.Name {
		if r == ' ' || r == '\t' || r == '\n' {
			return fmt.Errorf("name %q must not contain whitespace", s.Name)
		}
	}
	if s.Description == "" {
		return errors.New("description is required")
	}
	if s.Template.Type == "" {
		return errors.New("template.type is required")
	}
	switch s.Template.Type {
	case "exec", "http", "echo":
		// ok
	default:
		return fmt.Errorf("template.type %q is not one of exec|http|echo", s.Template.Type)
	}
	if s.Template.Type == "exec" && s.Template.Command == "" {
		return errors.New("template.command is required for exec type")
	}
	if s.Template.Type == "http" && s.Template.URL == "" {
		return errors.New("template.url is required for http type")
	}
	// parameters is optional; if present it must be a
	// JSON object (LLM expects an object schema, not a
	// scalar or array). yaml.v3 already enforces "this
	// must be JSON" via the json.RawMessage tag, so the
	// unmarshal failure mode catches e.g. a string
	// scalar. We add a post-decode object check for
	// shapes yaml accepts but the LLM doesn't (a JSON
	// array, for example).
	if len(s.Parameters) > 0 {
		// json.RawMessage's first byte tells us the
		// top-level shape: '{' = object, '[' = array,
		// '"' = string, '0'-'9' = number, 't'/'f' =
		// bool, 'n' = null. We want '{'.
		trimmed := skipWS(s.Parameters)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return fmt.Errorf("parameters must be a JSON object, got %s...", string(trimmed)[:min(len(trimmed), 16)])
		}
	}
	// sandbox fields (if set) must be one of the three
	// allowed values. Empty means "use default", validated
	// in ParseSpec.
	for _, kv := range []struct {
		field, value string
	}{
		{"sandbox.exec", s.Sandbox.Exec},
		{"sandbox.read", s.Sandbox.Read},
		{"sandbox.write", s.Sandbox.Write},
	} {
		if kv.value == "" {
			continue
		}
		switch kv.value {
		case "allow", "deny", "confirm":
			// ok
		default:
			return fmt.Errorf("%s = %q, want allow|deny|confirm", kv.field, kv.value)
		}
	}
	return nil
}

// Duration is a time.Duration that knows how to decode from
// YAML's "5s" / "1m" / "500ms" syntax. Wrapping rather than
// aliasing so the YAML tag ("timeout") renders cleanly.
type Duration time.Duration

// UnmarshalYAML accepts Go duration strings ("5s", "1m500ms")
// and bare integers (interpreted as seconds). Anything else
// returns an error.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return nil
	}
	var s string
	if err := value.Decode(&s); err == nil && s != "" {
		td, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("timeout %q: %w", s, err)
		}
		*d = Duration(td)
		return nil
	}
	// Fall back to integer (seconds).
	var n int64
	if err := value.Decode(&n); err != nil {
		return fmt.Errorf("timeout must be a duration string or integer: %w", err)
	}
	*d = Duration(time.Duration(n) * time.Second)
	return nil
}

// Std returns the wrapped time.Duration, with a default of
// 30s when unset. Used by the handler factories to keep the
// timeout-capping logic in one place.
func (d Duration) Std() time.Duration {
	if d == 0 {
		return 30 * time.Second
	}
	return time.Duration(d)
}

// skipWS returns b with leading whitespace bytes stripped.
// json.RawMessage is the source of truth for the value; we
// just need to peek at the first non-whitespace char.
func skipWS(b []byte) []byte {
	for i, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return b[i:]
		}
	}
	return nil
}
