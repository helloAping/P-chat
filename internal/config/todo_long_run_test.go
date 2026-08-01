package config

import "testing"

func TestTodoLongRunModeNormalizationAndRoundBypass(t *testing.T) {
	if got := NormalizeTodoLongRunMode(""); got != TodoLongRunAdaptive {
		t.Fatalf("empty mode = %q, want adaptive", got)
	}
	if TodoLongRunOff.AllowsUnlimitedRounds(true) {
		t.Fatal("off mode bypassed the round cap")
	}
	if TodoLongRunAdaptive.AllowsUnlimitedRounds(false) {
		t.Fatal("adaptive mode bypassed the cap without active todos")
	}
	if !TodoLongRunAdaptive.AllowsUnlimitedRounds(true) {
		t.Fatal("adaptive mode did not bypass the cap for active todos")
	}
	if !TodoLongRunUnlimited.AllowsUnlimitedRounds(false) {
		t.Fatal("unlimited mode did not bypass the cap")
	}
}

func TestDefaultSetsAnExplicitRoundCap(t *testing.T) {
	cfg := Default()
	if cfg.Limits.MaxRounds != 300 {
		t.Fatalf("default max rounds = %d, want 300", cfg.Limits.MaxRounds)
	}
	if cfg.Limits.TodoLongRunMode != TodoLongRunAdaptive {
		t.Fatalf("default todo long run mode = %q, want adaptive", cfg.Limits.TodoLongRunMode)
	}
}
