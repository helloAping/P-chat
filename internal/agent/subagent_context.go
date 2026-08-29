package agent

import (
	"context"
	"strings"

	"github.com/p-chat/pchat/internal/tool"
)

// SubagentModelPreference is a per-session default model override for child
// agents. It is weaker than task-call and specialized-agent model overrides.
type SubagentModelPreference struct {
	Enabled  bool
	Provider string
	Model    string
}

// Normalize trims the selected provider/model while preserving the switch.
func (p SubagentModelPreference) Normalize() SubagentModelPreference {
	p.Provider = strings.TrimSpace(p.Provider)
	p.Model = strings.TrimSpace(p.Model)
	return p
}

// Active reports whether the preference is complete and should influence a
// child agent run.
func (p SubagentModelPreference) Active() bool {
	p = p.Normalize()
	return p.Enabled && p.Provider != "" && p.Model != ""
}

type subagentModelPreferenceCtxKey struct{}

// WithSubagentModelPreference stores the parent turn's per-session child model
// preference for the task tool.
func WithSubagentModelPreference(ctx context.Context, pref SubagentModelPreference) context.Context {
	if !pref.Active() {
		return ctx
	}
	return context.WithValue(ctx, subagentModelPreferenceCtxKey{}, pref.Normalize())
}

// GetSubagentModelPreference returns the per-session child model preference.
func GetSubagentModelPreference(ctx context.Context) (SubagentModelPreference, bool) {
	if v, ok := ctx.Value(subagentModelPreferenceCtxKey{}).(SubagentModelPreference); ok && v.Active() {
		return v.Normalize(), true
	}
	return SubagentModelPreference{}, false
}

// SharedImageRecognition carries a parent session's image resolver into a
// child agent. The resolver remains scoped to the parent session and must
// validate upload ids before returning bytes.
type SharedImageRecognition struct {
	SessionID          string
	HasImageRefs       bool
	UseConfiguredModel bool
	Resolver           tool.ImageResolver
}

// Available reports whether a child agent can expose image_recognize against
// the parent session's image refs.
func (s SharedImageRecognition) Available() bool {
	return strings.TrimSpace(s.SessionID) != "" && s.HasImageRefs && s.Resolver != nil
}

type sharedImageRecognitionCtxKey struct{}

// WithSharedImageRecognition stores parent-scoped image-recognition access for
// a child agent spawned through the task tool.
func WithSharedImageRecognition(ctx context.Context, shared SharedImageRecognition) context.Context {
	if !shared.Available() {
		return ctx
	}
	return context.WithValue(ctx, sharedImageRecognitionCtxKey{}, shared)
}

// GetSharedImageRecognition returns parent-scoped image recognition access.
func GetSharedImageRecognition(ctx context.Context) (SharedImageRecognition, bool) {
	if v, ok := ctx.Value(sharedImageRecognitionCtxKey{}).(SharedImageRecognition); ok && v.Available() {
		return v, true
	}
	return SharedImageRecognition{}, false
}
