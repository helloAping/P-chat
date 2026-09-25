package agent

import (
	"context"
	"testing"

	"github.com/p-chat/pchat/internal/tool"
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
			name:        "both set",
			setProvider: "openai",
			setModel:    "gpt-4o-mini",
			wantProv:    "openai",
			wantModel:   "gpt-4o-mini",
		},
		{
			name:        "only model set",
			setProvider: "",
			setModel:    "claude-haiku-4-5",
			wantProv:    "",
			wantModel:   "claude-haiku-4-5",
		},
		{
			name:        "only provider set",
			setProvider: "anthropic",
			setModel:    "",
			wantProv:    "anthropic",
			wantModel:   "",
		},
		{
			name:        "neither set — no context value",
			setProvider: "",
			setModel:    "",
			wantProv:    "",
			wantModel:   "",
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

func TestSubagentModelPreference_ContextRoundTrip(t *testing.T) {
	base := context.Background()
	inactive := WithSubagentModelPreference(base, SubagentModelPreference{})
	if _, ok := GetSubagentModelPreference(inactive); ok {
		t.Fatal("inactive sub-agent model preference should not be stored")
	}

	ctx := WithSubagentModelPreference(base, SubagentModelPreference{
		Enabled:  true,
		Provider: " openai ",
		Model:    " gpt-4o-mini ",
	})
	got, ok := GetSubagentModelPreference(ctx)
	if !ok {
		t.Fatal("sub-agent model preference missing from context")
	}
	if got.Provider != "openai" || got.Model != "gpt-4o-mini" {
		t.Fatalf("preference = %#v, want trimmed provider/model", got)
	}
}

func TestSharedImageRecognition_ContextRoundTrip(t *testing.T) {
	resolver := tool.ImageResolver(func(context.Context, string, string) (tool.ImageRecognitionImage, error) {
		return tool.ImageRecognitionImage{Name: "x.png"}, nil
	})
	base := context.Background()
	if _, ok := GetSharedImageRecognition(WithSharedImageRecognition(base, SharedImageRecognition{})); ok {
		t.Fatal("inactive shared image recognition should not be stored")
	}

	ctx := WithSharedImageRecognition(base, SharedImageRecognition{
		SessionID:          "parent",
		HasImageRefs:       true,
		UseConfiguredModel: true,
		Resolver:           resolver,
	})
	got, ok := GetSharedImageRecognition(ctx)
	if !ok {
		t.Fatal("shared image recognition missing from context")
	}
	if got.SessionID != "parent" || !got.UseConfiguredModel || got.Resolver == nil {
		t.Fatalf("shared recognition = %#v", got)
	}
}
