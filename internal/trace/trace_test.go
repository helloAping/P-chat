package trace

import (
	"context"
	"strings"
	"testing"
)

func TestNewID_Format(t *testing.T) {
	id := NewID()
	if !strings.HasPrefix(id, prefix) {
		t.Fatalf("NewID() = %q, want prefix %q", id, prefix)
	}
	// 8 hex chars after the prefix.
	if len(id) != len(prefix)+8 {
		t.Fatalf("NewID() = %q, want len %d, got %d", id, len(prefix)+8, len(id))
	}
	for _, r := range id[len(prefix):] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Fatalf("NewID() = %q, contains non-hex char %q", id, r)
		}
	}
}

func TestNewID_Unique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewID()
		if seen[id] {
			t.Fatalf("duplicate id %q after %d iterations", id, i)
		}
		seen[id] = true
	}
}

func TestWithID_FromContext(t *testing.T) {
	ctx := context.Background()
	if got := FromContext(ctx); got != "" {
		t.Fatalf("FromContext(Background) = %q, want \"\"", got)
	}

	ctx = WithID(ctx, "T-12345678")
	if got := FromContext(ctx); got != "T-12345678" {
		t.Fatalf("FromContext = %q, want %q", got, "T-12345678")
	}
}

func TestWithID_EmptyIsNoop(t *testing.T) {
	ctx := WithID(context.Background(), "T-original")
	got := WithID(ctx, "")
	if FromContext(got) != "T-original" {
		t.Fatalf("WithID(ctx, \"\") clobbered original value")
	}
}

func TestFromContext_NilCtx(t *testing.T) {
	if got := FromContext(nil); got != "" {
		t.Fatalf("FromContext(nil) = %q, want \"\"", got)
	}
}
