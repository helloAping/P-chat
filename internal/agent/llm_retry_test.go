package agent

import (
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
)

func TestRetryBackoffDuration(t *testing.T) {
	steps := []int{5, 60, 180, 300, 600}
	cases := []struct {
		name string
		k    int
		want time.Duration
	}{
		{"first retry waits 5s", 1, 5 * time.Second},
		{"second retry waits 60s", 2, 60 * time.Second},
		{"fifth retry waits 600s", 5, 600 * time.Second},
		{"out-of-range clamps to last step", 9, 600 * time.Second},
	}
	for _, c := range cases {
		if got := retryBackoffDuration(steps, c.k); got != c.want {
			t.Errorf("retryBackoffDuration(%v, %d) = %v, want %v", steps, c.k, got, c.want)
		}
	}
}

func TestRetryBackoffDuration_EmptyTable(t *testing.T) {
	if got := retryBackoffDuration(nil, 1); got != 0 {
		t.Errorf("empty table should yield a zero wait, got %v", got)
	}
	if got := retryBackoffDuration([]int{}, 2); got != 0 {
		t.Errorf("empty table should yield a zero wait, got %v", got)
	}
}

func TestRetryBackoffDuration_NegativeClampsToZero(t *testing.T) {
	if got := retryBackoffDuration([]int{-3}, 1); got != 0 {
		t.Errorf("negative step should yield a zero wait, got %v", got)
	}
}

func TestLLMRetryBackoffs_ReadsFromConfig(t *testing.T) {
	if got := llmRetryBackoffs(nil); got != nil {
		t.Errorf("nil config should yield nil backoffs, got %v", got)
	}
	cfg := &config.Config{
		Limits: config.LimitsConfig{LLMRetryBackoffs: []int{1, 2, 3}},
	}
	got := llmRetryBackoffs(cfg)
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Errorf("llmRetryBackoffs = %v, want [1 2 3]", got)
	}
}
