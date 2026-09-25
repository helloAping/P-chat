package agent

import "testing"

func TestNormalizeTurnModePolicyMapsLegacyPlanMode(t *testing.T) {
	if got := NormalizeTurnModePolicy("", true); got != TurnModePlan {
		t.Fatalf("legacy true = %q, want plan", got)
	}
	if got := NormalizeTurnModePolicy("", false); got != TurnModeBuild {
		t.Fatalf("legacy false = %q, want build", got)
	}
	if got := NormalizeTurnModePolicy("auto", true); got != TurnModeAuto {
		t.Fatalf("explicit auto = %q, want auto", got)
	}
}

func TestDeterministicTurnModeHonorsExplicitSignals(t *testing.T) {
	mode, _, ok := deterministicTurnMode("梳理为文档，然后开始实现")
	if !ok || mode != TurnModeBuild {
		t.Fatalf("start implement signal = (%q,%v), want build,true", mode, ok)
	}

	mode, _, ok = deterministicTurnMode("先只梳理方案，不要实现")
	if !ok || mode != TurnModePlan {
		t.Fatalf("plan-only signal = (%q,%v), want plan,true", mode, ok)
	}
}

func TestParseAutoTurnModeDecision(t *testing.T) {
	got, ok := parseAutoTurnModeDecision("```json\n{\"mode\":\"plan\",\"reason\":\"跨模块改动\",\"confidence\":0.86}\n```")
	if !ok {
		t.Fatal("parseAutoTurnModeDecision returned !ok")
	}
	if got.Mode != TurnModePlan || got.Reason != "跨模块改动" || got.Confidence != 0.86 {
		t.Fatalf("decision = %+v", got)
	}

	if _, ok := parseAutoTurnModeDecision(`{"mode":"auto"}`); ok {
		t.Fatal("auto is not a valid effective decision")
	}
}
