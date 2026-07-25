package im

import (
	"context"
	"errors"
	"strings"
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

type fakeRenderer struct {
	sendChunks []IMOutChunk
	editRefs   []ChatRef
	editIDs    []string
	editChunks []IMOutChunk
	typingRefs []ChatRef
	err        error
}

func (f *fakeRenderer) Send(ctx context.Context, chunk IMOutChunk) error {
	f.sendChunks = append(f.sendChunks, chunk)
	return f.err
}

func (f *fakeRenderer) Edit(ctx context.Context, ref ChatRef, msgID string, chunk IMOutChunk) error {
	f.editRefs = append(f.editRefs, ref)
	f.editIDs = append(f.editIDs, msgID)
	f.editChunks = append(f.editChunks, chunk)
	return f.err
}

func (f *fakeRenderer) Typing(ctx context.Context, ref ChatRef) error {
	f.typingRefs = append(f.typingRefs, ref)
	return f.err
}

func (f *fakeRenderer) MaxTextLen() int {
	return 4096
}

func (f *fakeRenderer) MarkdownDialect() MarkdownDialect {
	return MarkdownPlain
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

func TestGatewayDispatchOutboundSendsThroughRegisteredRenderer(t *testing.T) {
	cfg := config.DefaultIMConfig()
	cfg.Enabled = true
	cfg.Platforms = []config.IMPlatformConfig{{Type: "feishu", Variant: "bot", Enabled: true}}
	g := NewGateway(cfg)
	renderer := &fakeRenderer{}
	g.RegisterRenderer("feishu", "bot", renderer)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := g.Subscribe(ctx)

	chunk := IMOutChunk{
		Platform: "feishu",
		Chat:     ChatRef{Platform: "feishu", Variant: "bot", ChatID: "oc_group"},
		Kind:     "text",
		Text:     "hello",
	}
	if err := g.DispatchOutbound(ctx, chunk); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(renderer.sendChunks) != 1 {
		t.Fatalf("send calls = %d, want 1", len(renderer.sendChunks))
	}
	if renderer.sendChunks[0].Text != "hello" {
		t.Fatalf("sent text = %q, want hello", renderer.sendChunks[0].Text)
	}
	assertLifecycle(t, events, "outbound_start")
	assertLifecycle(t, events, "outbound_ok")
}

func TestGatewayDispatchOutboundRoutesEditAndTyping(t *testing.T) {
	cfg := config.DefaultIMConfig()
	cfg.Enabled = true
	cfg.Platforms = []config.IMPlatformConfig{{Type: "feishu", Variant: "bot", Enabled: true}}
	g := NewGateway(cfg)
	renderer := &fakeRenderer{}
	g.RegisterRenderer("feishu", "bot", renderer)

	ctx := context.Background()
	ref := ChatRef{Platform: "feishu", Variant: "bot", ChatID: "oc_group"}
	if err := g.DispatchOutbound(ctx, IMOutChunk{Platform: "feishu", Chat: ref, Kind: "edit", MsgID: "om_msg", Text: "updated"}); err != nil {
		t.Fatalf("dispatch edit: %v", err)
	}
	if len(renderer.editIDs) != 1 || renderer.editIDs[0] != "om_msg" {
		t.Fatalf("edit ids = %+v, want om_msg", renderer.editIDs)
	}
	if err := g.DispatchOutbound(ctx, IMOutChunk{Platform: "feishu", Chat: ref, Kind: "typing"}); err != nil {
		t.Fatalf("dispatch typing: %v", err)
	}
	if len(renderer.typingRefs) != 1 || renderer.typingRefs[0].ChatID != "oc_group" {
		t.Fatalf("typing refs = %+v, want oc_group", renderer.typingRefs)
	}
}

func TestGatewayDispatchOutboundReportsMissingRenderer(t *testing.T) {
	cfg := config.DefaultIMConfig()
	cfg.Enabled = true
	cfg.Platforms = []config.IMPlatformConfig{{Type: "feishu", Variant: "bot", Enabled: true}}
	g := NewGateway(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := g.Subscribe(ctx)

	err := g.DispatchOutbound(ctx, IMOutChunk{
		Platform: "feishu",
		Chat:     ChatRef{Platform: "feishu", Variant: "bot", ChatID: "oc_group"},
		Kind:     "text",
		Text:     "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "renderer not registered") {
		t.Fatalf("error = %v, want renderer not registered", err)
	}
	assertLifecycle(t, events, "outbound_start")
	got := assertLifecycle(t, events, "outbound_error")
	if got.Error == "" {
		t.Fatal("outbound_error should carry an error")
	}
}

func TestGatewayDispatchOutboundUsesRendererFactoryAfterConfigUpdate(t *testing.T) {
	cfg := config.DefaultIMConfig()
	cfg.Enabled = true
	cfg.Platforms = []config.IMPlatformConfig{{Type: "feishu", Variant: "bot", Enabled: true, AppSecret: "old"}}
	g := NewGateway(cfg)
	var builtWith []string
	g.RegisterRendererFactory("feishu", func(platform config.IMPlatformConfig) (OutboundRenderer, error) {
		builtWith = append(builtWith, platform.AppSecret)
		return &fakeRenderer{}, nil
	})

	chunk := IMOutChunk{
		Platform: "feishu",
		Chat:     ChatRef{Platform: "feishu", Variant: "bot", ChatID: "oc_group"},
		Kind:     "text",
		Text:     "hello",
	}
	if err := g.DispatchOutbound(context.Background(), chunk); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	cfg.Platforms[0].AppSecret = "new"
	g.UpdateConfig(cfg)
	if err := g.DispatchOutbound(context.Background(), chunk); err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	if len(builtWith) != 2 || builtWith[0] != "old" || builtWith[1] != "new" {
		t.Fatalf("factory configs = %+v, want old then new", builtWith)
	}
}

func TestGatewayDispatchOutboundPropagatesRendererError(t *testing.T) {
	cfg := config.DefaultIMConfig()
	cfg.Enabled = true
	cfg.Platforms = []config.IMPlatformConfig{{Type: "feishu", Variant: "bot", Enabled: true}}
	g := NewGateway(cfg)
	g.RegisterRenderer("feishu", "bot", &fakeRenderer{err: errors.New("send failed")})

	err := g.DispatchOutbound(context.Background(), IMOutChunk{
		Platform: "feishu",
		Chat:     ChatRef{Platform: "feishu", Variant: "bot", ChatID: "oc_group"},
		Kind:     "text",
		Text:     "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "send failed") {
		t.Fatalf("error = %v, want send failed", err)
	}
}

func assertLifecycle(t *testing.T, ch <-chan LifecycleEvent, typ string) LifecycleEvent {
	t.Helper()
	select {
	case ev := <-ch:
		if ev.Type != typ {
			t.Fatalf("event type = %q, want %q", ev.Type, typ)
		}
		return ev
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", typ)
		return LifecycleEvent{}
	}
}
