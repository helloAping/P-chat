package dynamic

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

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

// Preview renders a dynamic tool's template without performing the
// side-effect. It powers the DT-02 GUI trial panel: users can confirm
// the final command / URL / echo text before choosing a real run.
func Preview(spec Spec, args json.RawMessage) (*tool.CallResult, error) {
	var argMap map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return &tool.CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
		}
	}
	rc := RenderCtx{Args: argMap, Config: spec.Config}
	switch spec.Template.Type {
	case "exec":
		cmd, err := render(spec.Template.Command, rc)
		if err != nil {
			return &tool.CallResult{Content: "template render: " + err.Error(), IsError: true}, nil
		}
		return &tool.CallResult{Content: fmt.Sprintf("[dry-run] would execute dynamic tool %q\ncommand: %s\ntimeout: %s\n\n(no command was actually run)", spec.Name, cmd, spec.Template.Timeout.Std())}, nil
	case "http":
		urlStr, err := render(spec.Template.URL, rc)
		if err != nil {
			return &tool.CallResult{Content: "url render: " + err.Error(), IsError: true}, nil
		}
		body, err := render(spec.Template.Body, rc)
		if err != nil {
			return &tool.CallResult{Content: "body render: " + err.Error(), IsError: true}, nil
		}
		method := strings.ToUpper(strings.TrimSpace(spec.Template.Method))
		if method == "" {
			method = "GET"
		}
		headers := make([]string, 0, len(spec.Template.Headers))
		for k, v := range spec.Template.Headers {
			rendered, rerr := render(v, rc)
			if rerr != nil {
				return &tool.CallResult{Content: fmt.Sprintf("header %q render: %v", k, rerr), IsError: true}, nil
			}
			if rendered != "" {
				headers = append(headers, k+": <set>")
			}
		}
		sort.Strings(headers)
		content := fmt.Sprintf("[dry-run] would request dynamic tool %q\nmethod: %s\nurl: %s\ntimeout: %s", spec.Name, method, urlStr, spec.Template.Timeout.Std())
		if len(headers) > 0 {
			content += "\nheaders:\n  " + strings.Join(headers, "\n  ")
		}
		if body != "" {
			content += "\nbody:\n" + body
		}
		content += "\n\n(no HTTP request was sent)"
		return &tool.CallResult{Content: content}, nil
	case "echo":
		rendered, err := render(spec.Template.Text, rc)
		if err != nil {
			return &tool.CallResult{Content: "template render: " + err.Error(), IsError: true}, nil
		}
		return &tool.CallResult{Content: fmt.Sprintf("[dry-run] would return dynamic tool %q\n%s", spec.Name, rendered)}, nil
	default:
		return &tool.CallResult{Content: fmt.Sprintf("unknown dynamic template.type %q", spec.Template.Type), IsError: true}, nil
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
	specs   = map[string]map[string]Spec{}

	diagnosticsMu sync.RWMutex
	diagnostics   = map[string]map[string]LoadDiagnostic{}
)

// LoadDiagnostic is one row of dynamic-tool loader state.
// It includes both valid and invalid YAML files so the GUI can
// explain why a custom tool did not appear in the registry.
type LoadDiagnostic struct {
	Source      string    `json:"source"`
	Name        string    `json:"name,omitempty"`
	Status      string    `json:"status"` // loaded | error
	Error       string    `json:"error,omitempty"`
	Scope       string    `json:"scope,omitempty"`
	ProjectRoot string    `json:"project_root,omitempty"`
	ModTime     time.Time `json:"-"`
	ModAt       string    `json:"mod_at,omitempty"`
}

func scopeKey(scope tool.ToolOriginScope, projectRoot string) string {
	return string(scope) + "\x00" + projectRoot
}

func formatDiagnostic(d LoadDiagnostic, scope tool.ToolOriginScope, projectRoot string) LoadDiagnostic {
	if !d.ModTime.IsZero() {
		d.ModAt = d.ModTime.Format(time.RFC3339)
	}
	if d.Scope == "" {
		d.Scope = string(scope)
	}
	if d.ProjectRoot == "" {
		d.ProjectRoot = projectRoot
	}
	return d
}

// SetSpecs atomically replaces the global dynamic spec table. It is kept as a
// compatibility wrapper for tests and the global watcher.
func SetSpecs(all map[string]Spec) {
	SetSpecsForRoot(tool.ToolOriginGlobal, "", all)
}

// SetSpecsForRoot replaces the dynamic spec table for one origin scope.
func SetSpecsForRoot(scope tool.ToolOriginScope, projectRoot string, all map[string]Spec) {
	specsMu.Lock()
	defer specsMu.Unlock()
	key := scopeKey(scope, projectRoot)
	if len(all) == 0 {
		delete(specs, key)
		return
	}
	copyMap := make(map[string]Spec, len(all))
	for k, v := range all {
		copyMap[k] = v
	}
	specs[key] = copyMap
}

// SetDiagnostics atomically replaces the global loader diagnostics table.
func SetDiagnostics(all map[string]LoadDiagnostic) {
	SetDiagnosticsForRoot(tool.ToolOriginGlobal, "", all)
}

// SetDiagnosticsForRoot replaces the loader diagnostics table for one origin scope.
func SetDiagnosticsForRoot(scope tool.ToolOriginScope, projectRoot string, all map[string]LoadDiagnostic) {
	diagnosticsMu.Lock()
	defer diagnosticsMu.Unlock()
	key := scopeKey(scope, projectRoot)
	if len(all) == 0 {
		delete(diagnostics, key)
		return
	}
	copyMap := make(map[string]LoadDiagnostic, len(all))
	for k, v := range all {
		copyMap[k] = formatDiagnostic(v, scope, projectRoot)
	}
	diagnostics[key] = copyMap
}

// DiagnosticsSnapshot returns the global loader diagnostics in a stable order.
func DiagnosticsSnapshot() []LoadDiagnostic {
	return DiagnosticsSnapshotForRoot("")
}

// DiagnosticsSnapshotForRoot returns global diagnostics plus the current
// project's diagnostics. Other projects stay hidden from this session view.
func DiagnosticsSnapshotForRoot(projectRoot string) []LoadDiagnostic {
	diagnosticsMu.RLock()
	defer diagnosticsMu.RUnlock()
	out := []LoadDiagnostic{}
	appendScope := func(scope tool.ToolOriginScope, root string) {
		for _, d := range diagnostics[scopeKey(scope, root)] {
			out = append(out, d)
		}
	}
	appendScope(tool.ToolOriginGlobal, "")
	if projectRoot != "" {
		appendScope(tool.ToolOriginProject, projectRoot)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Status != out[j].Status {
			return out[i].Status == "error"
		}
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].Source < out[j].Source
	})
	return out
}

// LookupSpec returns the global spec for the given tool name plus an ok flag.
func LookupSpec(name string) (Spec, bool) {
	return LookupSpecForRoot(name, "")
}

// LookupSpecForRoot resolves a dynamic spec in the same order the tool registry
// resolves handlers: project tools override global tools for that session.
func LookupSpecForRoot(name, projectRoot string) (Spec, bool) {
	specsMu.RLock()
	defer specsMu.RUnlock()
	if projectRoot != "" {
		if m := specs[scopeKey(tool.ToolOriginProject, projectRoot)]; m != nil {
			if s, ok := m[name]; ok {
				return s, true
			}
		}
	}
	m := specs[scopeKey(tool.ToolOriginGlobal, "")]
	s, ok := m[name]
	return s, ok
}
