package dynamic

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/p-chat/pchat/internal/tool"
)

// BuildDynamicHandler is the type-dispatching factory called
// by the watcher when it has parsed a YAML spec. Returns a
// tool.ToolHandler that wraps the appropriate template
// implementation (exec / http / echo). For an unknown
// template.type, returns a no-op handler that always reports
// the unknown type as a tool error — better than panicking
// in the agent loop.
func BuildDynamicHandler(spec Spec) tool.ToolHandler {
	switch spec.Template.Type {
	case "exec":
		return MakeExecHandler(spec)
	case "http":
		return MakeHTTPHandler(spec)
	case "echo":
		return MakeEchoHandler(spec)
	}
	// Unreachable: ParseSpec already validates the type
	// against this same set. Defensive: if a future spec
	// sneaks past validation, the LLM sees a clear error
	// rather than a stack trace.
	unknownType := spec.Template.Type
	return func(ctx context.Context, args json.RawMessage) (*tool.CallResult, error) {
		return &tool.CallResult{
			Content: fmt.Sprintf("unknown dynamic template.type %q (want exec|http|echo)", unknownType),
			IsError: true,
		}, nil
	}
}

// AsTool converts a Spec into the tool.Tool value the
// tool.Registry.Register call expects. The handler side is
// the one BuildDynamicHandler returned; this function only
// assembles the metadata (Name / Description / Parameters)
// and leaves the actual call logic to the handler.
func (s Spec) AsTool() tool.Tool {
	return tool.Tool{
		Name:        s.Name,
		Description: s.Description,
		Parameters:  s.Parameters,
	}
}

// specs is a process-global map of currently-loaded
// dynamic-tool specs. The watcher writes here on every
// register / unregister; the agent's confirmTargetFor
// reads from it to convert the user's YAML `sandbox.exec`
// into a real SandboxDecision.
//
// We use a process-global (with a mutex) rather than
// threading the registry through the agent loop because
// the agent's chat path is already 40+ layers deep and
// adding another argument is more disruptive than the
// small encapsulation loss here. Confirmed against the
// existing sessionLocks / summarizer globals for
// pattern-fit.
var (
	specsMu sync.RWMutex
	specs   = map[string]Spec{}
)

// SetSpecs atomically replaces the entire spec table. The
// watcher calls this on every reload so the agent's
// confirmTargetFor sees the same set of tools the
// tool.Registry has. Read-conflicts with the agent loop
// are safe because we copy-on-write.
func SetSpecs(all map[string]Spec) {
	specsMu.Lock()
	defer specsMu.Unlock()
	specs = make(map[string]Spec, len(all))
	for k, v := range all {
		specs[k] = v
	}
}

// LookupSpec returns the spec for the given tool name plus
// an ok flag. Used by the agent's confirmTargetFor to
// resolve a dynamic tool's sandbox policy at confirm time.
func LookupSpec(name string) (Spec, bool) {
	specsMu.RLock()
	defer specsMu.RUnlock()
	s, ok := specs[name]
	return s, ok
}
