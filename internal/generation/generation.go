// Package generation defines the stable execution seam between model-facing
// media tools and vendor adapters.
package generation

import (
	"context"
	"strings"

	"github.com/p-chat/pchat/internal/config"
)

// PromptMode controls whether the caller's prompt is preserved or may be
// enriched before dispatch. Prompt enrichment is orchestrated above adapters.
type PromptMode string

const (
	PromptModeAuto   PromptMode = "auto"
	PromptModeRaw    PromptMode = "raw"
	PromptModeAssist PromptMode = "assist"
)

// NormalizePromptMode returns auto for an empty value.
func NormalizePromptMode(mode PromptMode) PromptMode {
	switch mode {
	case PromptModeRaw, PromptModeAssist:
		return mode
	default:
		return PromptModeAuto
	}
}

// Status is the vendor-neutral lifecycle state of a generation job.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// Asset is a generated media reference returned to the conversation layer.
// URL is an adapter result and should be materialized by the host before it is
// considered durable.
type Asset struct {
	ID       string           `json:"id,omitempty"`
	Kind     config.MediaKind `json:"kind"`
	MIMEType string           `json:"mime_type,omitempty"`
	Name     string           `json:"name,omitempty"`
	URL      string           `json:"url,omitempty"`
}

// Dispatch is the resolved provider/model route. It is injected by the host
// after configuration validation and is never accepted from model tool args.
type Dispatch struct {
	Target          config.GenerationModelTarget
	Vendor          string
	BaseURL         string
	APIKey          string
	Adapter         string
	OperationConfig config.GenerationOperationConfig
}

// Request is the complete vendor-neutral execution request. InputRefs are
// opaque upload or generated-asset ids; tools never pass base64 or local paths.
type Request struct {
	SessionID  string                       `json:"session_id"`
	ToolName   string                       `json:"tool_name"`
	Operation  config.GenerationOperation   `json:"operation"`
	Target     config.GenerationModelTarget `json:"target"`
	Prompt     string                       `json:"prompt"`
	PromptMode PromptMode                   `json:"prompt_mode"`
	InputRefs  []string                     `json:"input_refs,omitempty"`
	Options    map[string]any               `json:"options,omitempty"`
	Dispatch   Dispatch                     `json:"-"`
}

// MaxGenerationInputBytes caps the aggregate bytes materialized for one media
// transformation before base64 expansion.
const MaxGenerationInputBytes int64 = 32 << 20

// MaxInputRefs returns the bounded number of media inputs accepted by an
// operation. Reference-image workflows may use a small set; video and audio
// transformations use one source asset.
func MaxInputRefs(operation config.GenerationOperation) int {
	if operation == config.GenerationImageToImage {
		return 4
	}
	if operation.RequiredInputKind().IsValid() {
		return 1
	}
	return 0
}

// Result is returned by an adapter after it creates or completes a task.
type Result struct {
	JobID   string  `json:"job_id,omitempty"`
	Status  Status  `json:"status"`
	Assets  []Asset `json:"assets,omitempty"`
	Message string  `json:"message,omitempty"`
}

// Executor dispatches one authorized request to a configured vendor adapter.
type Executor interface {
	Generate(context.Context, Request) (Result, error)
}

// Access is immutable request-scoped generation authority. Enabled is kept
// separate from Targets so a stale/missing model selection is distinguishable
// from an operation the user explicitly turned off.
type Access struct {
	Enabled      map[config.GenerationOperation]bool
	Targets      map[config.GenerationOperation]config.GenerationModelTarget
	Dispatches   map[config.GenerationOperation]Dispatch
	PromptAssist bool
	Executor     Executor
}

// IsEnabled reports whether the conversation explicitly enabled an operation.
func (a Access) IsEnabled(operation config.GenerationOperation) bool {
	return a.Enabled != nil && a.Enabled[operation]
}

// Target returns a complete selected target for the operation.
func (a Access) Target(operation config.GenerationOperation) (config.GenerationModelTarget, bool) {
	target, ok := a.Targets[operation]
	return target, ok && target.Valid()
}

// Dispatch returns the trusted, resolved provider route for an operation.
func (a Access) Dispatch(operation config.GenerationOperation) (Dispatch, bool) {
	dispatch, ok := a.Dispatches[operation]
	return dispatch, ok && dispatch.Target.Valid()
}

type accessContextKey struct{}

// WithAccess attaches request-scoped generation authority to a tool context.
func WithAccess(ctx context.Context, access Access) context.Context {
	return context.WithValue(ctx, accessContextKey{}, access)
}

// AccessFrom returns request-scoped generation authority when present.
func AccessFrom(ctx context.Context) (Access, bool) {
	if ctx == nil {
		return Access{}, false
	}
	access, ok := ctx.Value(accessContextKey{}).(Access)
	return access, ok
}

// NormalizeInputRefs trims, removes empty values, and preserves first-seen
// order. Reference ownership is validated later by the executor.
func NormalizeInputRefs(refs []string) []string {
	out := make([]string, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	return out
}
