package im

import (
	"context"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
)

type fakeAdapter struct {
	started bool
	stopped bool
}

func (f *fakeAdapter) Platform() string   { return "telegram" }
func (f *fakeAdapter) Variants() []string { return []string{"polling"} }
func (f *fakeAdapter) Start(ctx context.Context, in chan<- IMEvent) error {
	f.started = true
	return nil
}
func (f *fakeAdapter) Stop(ctx context.Context) error {
	f.stopped = true
	return nil
}
func (f *fakeAdapter) Health() HealthStatus {
	return HealthStatus{Status: "ok"}
}

func TestGatewayStartMarksMissingAdapterUnavailable(t *testing.T) {
	cfg := config.DefaultIMConfig()
	cfg.Enabled = true
	cfg.Platforms = []config.IMPlatformConfig{{Type: "telegram", Variant: "polling", Enabled: true}}
	g := NewGateway(cfg)

	if err := g.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	health := g.Health()
	if !health.Enabled || !health.Running {
		t.Fatalf("health = %+v, want enabled running", health)
	}
	if len(health.Platforms) != 1 {
		t.Fatalf("platform count = %d, want 1", len(health.Platforms))
	}
	if health.Platforms[0].Status != "unavailable" {
		t.Fatalf("status = %q, want unavailable", health.Platforms[0].Status)
	}
	if health.Platforms[0].Error == "" {
		t.Fatal("missing adapter should carry an error")
	}
}

func TestGatewayStartsAndStopsRegisteredAdapter(t *testing.T) {
	cfg := config.DefaultIMConfig()
	cfg.Enabled = true
	cfg.Platforms = []config.IMPlatformConfig{{Type: "telegram", Variant: "polling", Enabled: true}}
	g := NewGateway(cfg)
	adapter := &fakeAdapter{}
	g.RegisterAdapter(adapter)

	if err := g.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !adapter.started {
		t.Fatal("adapter was not started")
	}
	if got := g.TestConnection("telegram", "polling"); !got.OK || got.Status != "registered" {
		t.Fatalf("test result = %+v, want registered ok", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := g.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !adapter.stopped {
		t.Fatal("adapter was not stopped")
	}
}

func TestGatewayDrainsInboundBusAndBroadcastsLifecycle(t *testing.T) {
	cfg := config.DefaultIMConfig()
	cfg.Enabled = true
	g := NewGateway(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subA := g.Subscribe(ctx)
	subB := g.Subscribe(ctx)
	if err := g.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	g.Inbound() <- IMEvent{ID: "m-1", Platform: "telegram", Variant: "polling"}

	assertInbound := func(name string, ch <-chan LifecycleEvent) {
		t.Helper()
		select {
		case ev := <-ch:
			if ev.Type != "inbound_received" || ev.Platform != "telegram" || ev.Message != "m-1" {
				t.Fatalf("%s event = %+v, want inbound telegram m-1", name, ev)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s timed out waiting for lifecycle event", name)
		}
	}
	assertInbound("subA", subA)
	assertInbound("subB", subB)
}
