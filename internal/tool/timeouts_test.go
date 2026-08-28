package tool

import (
	"context"
	"testing"
	"time"
)

func TestInteractiveContextFromFallsBackToCtx(t *testing.T) {
	ctx := context.Background()
	if InteractiveContextFrom(ctx) != ctx {
		t.Fatal("missing interactive context should fall back to ctx")
	}
	toolCtx, cancel := context.WithTimeout(ctx, time.Millisecond)
	defer cancel()
	turn := context.Background()
	wrapped := WithInteractiveContext(toolCtx, turn)
	if InteractiveContextFrom(wrapped) != turn {
		t.Fatal("InteractiveContextFrom did not return the published turn context")
	}
}
