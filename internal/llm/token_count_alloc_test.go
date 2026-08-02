package llm

import "testing"

func TestEstimateTokensToolsZeroAlloc(t *testing.T) {
	tools := make([]ToolDef, 0, 31)
	for i := 0; i < 31; i++ {
		tools = append(tools, ToolDef{
			Name:        "tool_" + string(rune('a'+i)),
			Description: "some tool description with CJK 中文 and ascii content to estimate",
			Parameters:  []byte(`{"type":"object","properties":{"cmd":{"type":"string"},"flag":{"type":"boolean"}}}`),
		})
	}
	want := EstimateTokensTools(tools)
	got := testing.AllocsPerRun(100, func() {
		_ = EstimateTokensTools(tools)
	})
	if got != 0 {
		t.Fatalf("EstimateTokensTools allocs = %v, want 0 (string(t.Parameters) copies were removed)", got)
	}
	_ = want
}
