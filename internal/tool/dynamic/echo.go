package dynamic

import (
	"context"
	"encoding/json"

	"github.com/p-chat/pchat/internal/tool"
)

// MakeEchoHandler builds a tool.ToolHandler for a spec whose
// template.type == "echo". The handler renders the static
// text/template body and returns the result — no side
// effect, no external call, no I/O. Useful for smoke-testing
// the wiring (does my YAML parse? does my template render
// without errors?) without poking at the network or the
// filesystem.
func MakeEchoHandler(spec Spec) tool.ToolHandler {
	return func(ctx context.Context, args json.RawMessage) (*tool.CallResult, error) {
		var argMap map[string]any
		if len(args) > 0 {
			if err := json.Unmarshal(args, &argMap); err != nil {
				return &tool.CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
			}
		}
		rendered, err := render(spec.Template.Text, RenderCtx{Args: argMap, Config: spec.Config})
		if err != nil {
			return &tool.CallResult{Content: "template render: " + err.Error(), IsError: true}, nil
		}
		return &tool.CallResult{Content: rendered}, nil
	}
}
