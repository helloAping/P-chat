package cli

import (
	"context"
	"testing"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/subagent"
	"github.com/p-chat/pchat/internal/style"
)

// REPL setter coverage. We don't drive Run() (it reads
// stdin); we just verify the setters store what they're
// given and that NewREPL constructs a usable struct.

func TestNewREPL_Defaults(t *testing.T) {
	r := NewREPL(nil, &config.Config{}, style.Style("tech"), "openai")
	if r == nil {
		t.Fatal("NewREPL returned nil")
	}
	if r.style != style.Style("tech") {
		t.Errorf("style = %q, want tech", r.style)
	}
	if r.provider != "openai" {
		t.Errorf("provider = %q, want openai", r.provider)
	}
	if !r.useTools {
		t.Error("useTools default = false, want true")
	}
	if r.rollbackUndo == nil {
		t.Error("rollbackUndo map not initialised")
	}
	if r.runCtx == nil {
		t.Error("runCtx default = nil, want context.Background()")
	}
}

func TestREPL_SetRunContext(t *testing.T) {
	r := NewREPL(nil, &config.Config{}, style.Style("tech"), "openai")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.SetRunContext(ctx)
	if r.runCtx != ctx {
		t.Error("SetRunContext did not store the ctx")
	}
	// nil should be ignored (defensive).
	r.SetRunContext(nil)
	if r.runCtx != ctx {
		t.Error("SetRunContext(nil) overwrote the stored ctx")
	}
}

func TestREPL_SetSubAgentCache(t *testing.T) {
	r := NewREPL(nil, &config.Config{}, style.Style("tech"), "openai")
	r.SetSubAgentCache(nil)
	if r.subCache != nil {
		t.Error("SetSubAgentCache(nil) did not store nil")
	}
	r.SetSubAgentCache(&subagent.Cache{})
	if r.subCache == nil {
		t.Error("SetSubAgentCache(&Cache{}) stored nil")
	}
}

func TestREPL_SetLLMClient(t *testing.T) {
	r := NewREPL(nil, &config.Config{}, style.Style("tech"), "openai")
	// SetLLMClient with nil is valid (used in tests).
	r.SetLLMClient(nil)
	if r.llm != nil {
		t.Error("SetLLMClient(nil) did not store nil")
	}
	// SetLLMClient with a real (zero-value) client — the
	// test only cares that the pointer is stored, not
	// that the client is functional.
	r.SetLLMClient(&llm.Client{})
	if r.llm == nil {
		t.Error("SetLLMClient(&Client{}) stored nil")
	}
}

func TestREPL_SetKBManager(t *testing.T) {
	r := NewREPL(nil, &config.Config{}, style.Style("tech"), "openai")
	r.SetKBManager(nil)
	if r.kbManager != nil {
		t.Error("SetKBManager(nil) did not store nil")
	}
	r.SetKBManager(&KBManager{})
	if r.kbManager == nil {
		t.Error("SetKBManager(&KBManager{}) stored nil")
	}
}

func TestREPL_AsContext(t *testing.T) {
	r := NewREPL(nil, &config.Config{}, style.Style("tech"), "openai")
	// With r.ctx == nil, asContext returns a fresh
	// &localContext{r: r}. We can only assert it's
	// non-nil and the same REPL backs it.
	c := r.asContext()
	if c == nil {
		t.Fatal("asContext returned nil")
	}
	if lc, ok := c.(*localContext); ok {
		if lc.r != r {
			t.Error("asContext returned a localContext pointing at a different REPL")
		}
	} else {
		t.Errorf("asContext returned unexpected type %T", c)
	}
}
