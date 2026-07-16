package dynamic

import (
	"bytes"
	"fmt"
	"text/template"
)

// RenderCtx is the data passed into every text/template
// expansion in a dynamic tool. Two top-level keys:
//
//	.args   — the LLM-supplied call arguments, after
//	          json.Unmarshal into a map[string]any. Empty
//	          fields resolve to the zero value rather
//	          than "<no value>" (text/template's
//	          missingkey=zero option).
//	.config — per-tool config read from
//	          ~/.p-chat/config.json's "dynamic.<name>.config"
//	          subtree. Lets the user stash an API key once
//	          and reference it across tool definitions.
//
// Both values may be nil; the helper handles that. We
// convert to a map[string]any at render time so the user
// can write `{{.args.foo}}` (lowercase) instead of having
// to type `{{.Args.foo}}` (the Go field name).
type RenderCtx struct {
	Args   map[string]any
	Config any
}

// asMap flattens RenderCtx into a map[string]any with the
// `args` and `config` keys the user's templates reference.
// Returning a fresh map per call is cheap (these are small)
// and keeps the field-name aliasing logic in one place.
func (rc RenderCtx) asMap() map[string]any {
	return map[string]any{
		"args":   rc.Args,
		"config": rc.Config,
	}
}

// render expands the text/template string with the given
// context. Returns the rendered string and a non-nil error
// on parse / execute failure. Errors are intentionally
// non-graceful — the user wrote the template, they'll want
// to see the line number if it's broken.
func render(s string, rc RenderCtx) (string, error) {
	if s == "" {
		return "", nil
	}
	// missingkey=zero turns {{.args.foo}} into "<no value>"
	// when foo is absent — better than an execution error
	// for a casual user writing YAML.
	t, err := template.New("dyn").Option("missingkey=zero").Parse(s)
	if err != nil {
		return "", fmt.Errorf("template parse: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, rc.asMap()); err != nil {
		return "", fmt.Errorf("template execute: %w", err)
	}
	return buf.String(), nil
}
