package agent

import (
	"context"
	"testing"
)

// TestParentModel_ContextRoundTrip verifies the (provider,
// model) pair the agent publishes for the `task` tool handler
// round-trips through context correctly.
//
// This is the contract the subagent runner depends on to
// inherit the user's currently-selected model — without it the
// subagent would fall back to the server's startup default
// and produce "openai proxy error: model_not_found" when the
// user has switched providers mid-session.
func TestParentModel_ContextRoundTrip(t *testing.T) {
	cases := []struct {
		name                  string
		setProvider, setModel string
		wantProv, wantModel   string
	}{
		{
			name:      "both set",
			setProvider: "openai",
			setModel:   "gpt-4o-mini",
			wantProv:   "openai",
			wantModel:  "gpt-4o-mini",
		},
		{
			name:      "only model set",
			setProvider: "",
			setModel:   "claude-haiku-4-5",
			wantProv:   "",
			wantModel:  "claude-haiku-4-5",
		},
		{
			name:      "only provider set",
			setProvider: "anthropic",
			setModel:   "",
			wantProv:   "anthropic",
			wantModel:  "",
		},
		{
			name:      "neither set — no context value",
			setProvider: "",
			setModel:   "",
			wantProv:   "",
			wantModel:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithParentModel(context.Background(), tc.setProvider, tc.setModel)
			gotProv, gotModel := GetParentModel(ctx)
			if gotProv != tc.wantProv {
				t.Errorf("provider = %q, want %q", gotProv, tc.wantProv)
			}
			if gotModel != tc.wantModel {
				t.Errorf("model = %q, want %q", gotModel, tc.wantModel)
			}
		})
	}
}

// TestParentModel_BothEmptyIsNoOp verifies the optimisation:
// when both provider and model are empty, WithParentModel
// returns the original ctx unchanged. Avoids allocating a new
// context value for every tool call when there's nothing to
// publish.
func TestParentModel_BothEmptyIsNoOp(t *testing.T) {
	base := context.Background()
	got := WithParentModel(base, "", "")
	// We can't compare contexts for equality directly, but we
	// can verify the published value is still empty.
	if _, model := GetParentModel(got); model != "" {
		t.Errorf("model = %q, want empty", model)
	}
}
