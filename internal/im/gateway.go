package im

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/im/outbound"
)

// HealthStatus 描述一个 IM adapter 的健康状态。
// HealthStatus describes the health of one IM adapter.
type HealthStatus struct {
	Platform  string    `json:"platform"`
	Variant   string    `json:"variant,omitempty"`
	Enabled   bool      `json:"enabled"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
}

// GatewayHealth 是 Gateway 对外暴露的健康快照。
// GatewayHealth is the external health snapshot of the Gateway.
type GatewayHealth struct {
	Enabled   bool           `json:"enabled"`
	Running   bool           `json:"running"`
	Platforms []HealthStatus `json:"platforms"`
}

// TestResult 是一次平台连接自检的结果。
// TestResult is the result of one platform self-test.
type TestResult struct {
	OK       bool   `json:"ok"`
	Platform string `json:"platform"`
	Variant  string `json:"variant,omitempty"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

// Gateway 管理所有 IM adapter 和 outbound renderer 的生命周期。
// Gateway manages the lifecycle of all IM adapters and outbound renderers.
type Gateway struct {
	mu        sync.RWMutex
	cfg       config.IMConfig
	adapters  map[string]Adapter
	renderers map[string]OutboundRenderer
	factories map[string]RendererFactory
	generated map[string]OutboundRenderer
	dispatch  map[string]*outbound.Dispatcher
	processor InboundProcessor
	in        chan IMEvent
	subs      map[chan LifecycleEvent]struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	running   bool
	startedAt time.Time
	health    map[string]HealthStatus
}

// NewGateway 创建一个 IM Gateway。
// NewGateway creates an IM Gateway.
func NewGateway(cfg config.IMConfig) *Gateway {
	cfg.Normalize()
	return &Gateway{
		cfg:       cfg,
		adapters:  map[string]Adapter{},
		renderers: map[string]OutboundRenderer{},
		factories: map[string]RendererFactory{},
		generated: map[string]OutboundRenderer{},
		dispatch:  map[string]*outbound.Dispatcher{},
		in:        make(chan IMEvent, 64),
		subs:      map[chan LifecycleEvent]struct{}{},
		health:    map[string]HealthStatus{},
	}
}

// RegisterAdapter 注册一个平台 adapter。
// RegisterAdapter registers one platform adapter.
func (g *Gateway) RegisterAdapter(adapter Adapter) {
	if adapter == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	variants := adapter.Variants()
	if len(variants) == 0 {
		variants = []string{""}
	}
	for _, variant := range variants {
		g.adapters[adapterKey(adapter.Platform(), variant)] = adapter
	}
}

// RegisterRenderer 注册一个平台出站 renderer。
// RegisterRenderer registers one platform outbound renderer.
func (g *Gateway) RegisterRenderer(platform, variant string, renderer OutboundRenderer) {
	if renderer == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.renderers[adapterKey(platform, variant)] = renderer
	g.dispatch = map[string]*outbound.Dispatcher{}
}

// RegisterRendererFactory 注册一个按配置创建 renderer 的工厂。
// RegisterRendererFactory registers a factory that builds renderers from config.
func (g *Gateway) RegisterRendererFactory(platform string, factory RendererFactory) {
	if factory == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.factories[platform] = factory
	g.generated = map[string]OutboundRenderer{}
	g.dispatch = map[string]*outbound.Dispatcher{}
}

// UpdateConfig 更新 Gateway 持有的 IM 配置。
// UpdateConfig updates the IM config held by the Gateway.
// SetInboundProcessor 挂载 IM 入站消息处理器。
// SetInboundProcessor wires the normalized inbound message processor.
func (g *Gateway) SetInboundProcessor(processor InboundProcessor) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.processor = processor
}

// UpdateConfig updates the IM config held by the Gateway.
func (g *Gateway) UpdateConfig(cfg config.IMConfig) {
	cfg.Normalize()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cfg = cfg
	g.health = map[string]HealthStatus{}
	g.generated = map[string]OutboundRenderer{}
	g.dispatch = map[string]*outbound.Dispatcher{}
}

// Reconfigure 应用新配置并按 enabled 状态重启 Gateway。
// Reconfigure applies new config and restarts the Gateway according to enabled state.
func (g *Gateway) Reconfigure(ctx context.Context, cfg config.IMConfig) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := g.Stop(ctx); err != nil {
		return err
	}
	g.UpdateConfig(cfg)
	if !cfg.Enabled {
		return nil
	}
	return g.Start(ctx)
}

// Start 启动所有已配置且已注册的 adapter。
// Start starts all configured and registered adapters.
func (g *Gateway) Start(ctx context.Context) error {
	g.mu.Lock()
	if g.running {
		g.mu.Unlock()
		return nil
	}
	g.ctx, g.cancel = context.WithCancel(ctx)
	g.running = g.cfg.Enabled
	g.startedAt = time.Now()
	cfg := g.cfg
	g.mu.Unlock()

	if !cfg.Enabled {
		g.emit(LifecycleEvent{Type: "disabled", Message: "IM bridge disabled"})
		return nil
	}
	go g.run(g.ctx)

	for _, platform := range cfg.Platforms {
		if !platform.Enabled {
			continue
		}
		key := adapterKey(platform.Type, platform.Variant)
		g.mu.RLock()
		adapter := g.adapters[key]
		g.mu.RUnlock()
		if adapter == nil {
			if isAuthenticatedWeChat(platform) {
				g.setHealth(key, HealthStatus{
					Platform:  platform.Type,
					Variant:   platform.Variant,
					Enabled:   true,
					Status:    "authenticated",
					StartedAt: g.startedAt,
				})
				g.emit(LifecycleEvent{Type: "adapter_authenticated", Platform: platform.Type, Variant: platform.Variant})
				continue
			}
			g.setHealth(key, HealthStatus{
				Platform: platform.Type,
				Variant:  platform.Variant,
				Enabled:  true,
				Status:   "unavailable",
				Error:    "adapter not registered",
			})
			g.emit(LifecycleEvent{Type: "adapter_unavailable", Platform: platform.Type, Variant: platform.Variant, Error: "adapter not registered"})
			continue
		}
		if err := adapter.Start(g.ctx, g.in); err != nil {
			g.setHealth(key, HealthStatus{
				Platform: platform.Type,
				Variant:  platform.Variant,
				Enabled:  true,
				Status:   "error",
				Error:    err.Error(),
			})
			g.emit(LifecycleEvent{Type: "adapter_error", Platform: platform.Type, Variant: platform.Variant, Error: err.Error()})
			continue
		}
		health := adapter.Health()
		health.Platform = platform.Type
		health.Variant = platform.Variant
		health.Enabled = true
		if health.Status == "" {
			health.Status = "ok"
		}
		if health.StartedAt.IsZero() {
			health.StartedAt = g.startedAt
		}
		g.setHealth(key, health)
		g.emit(LifecycleEvent{Type: "adapter_started", Platform: platform.Type, Variant: platform.Variant})
	}
	return nil
}

func (g *Gateway) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-g.in:
			g.emit(LifecycleEvent{
				Type:     "inbound_received",
				Platform: ev.Platform,
				Variant:  ev.Variant,
				Message:  ev.ID,
			})
			g.mu.RLock()
			processor := g.processor
			g.mu.RUnlock()
			if processor != nil {
				go g.processInbound(ctx, processor, ev)
			}
		}
	}
}

func (g *Gateway) processInbound(ctx context.Context, processor InboundProcessor, ev IMEvent) {
	g.emit(LifecycleEvent{
		Type:     "inbound_processing",
		Platform: ev.Platform,
		Variant:  ev.Variant,
		Message:  ev.ID,
	})
	if err := processor.ProcessIMEvent(ctx, ev); err != nil {
		g.emit(LifecycleEvent{
			Type:     "inbound_error",
			Platform: ev.Platform,
			Variant:  ev.Variant,
			Message:  ev.ID,
			Error:    err.Error(),
		})
		return
	}
	g.emit(LifecycleEvent{
		Type:     "inbound_ok",
		Platform: ev.Platform,
		Variant:  ev.Variant,
		Message:  ev.ID,
	})
}

// Stop 停止所有 adapter。
// Stop stops all adapters.
func (g *Gateway) Stop(ctx context.Context) error {
	g.mu.Lock()
	if !g.running {
		g.mu.Unlock()
		return nil
	}
	if g.cancel != nil {
		g.cancel()
	}
	adapters := make(map[Adapter]struct{})
	for _, adapter := range g.adapters {
		adapters[adapter] = struct{}{}
	}
	g.running = false
	g.mu.Unlock()

	var firstErr error
	for adapter := range adapters {
		if err := adapter.Stop(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	g.emit(LifecycleEvent{Type: "stopped", Message: "IM bridge stopped"})
	return firstErr
}

// Health 返回 Gateway 当前健康状态。
// Health returns the current Gateway health.
func (g *Gateway) Health() GatewayHealth {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := GatewayHealth{
		Enabled: g.cfg.Enabled,
		Running: g.running,
	}
	for _, platform := range g.cfg.Platforms {
		key := adapterKey(platform.Type, platform.Variant)
		if !platform.Enabled {
			out.Platforms = append(out.Platforms, HealthStatus{
				Platform: platform.Type,
				Variant:  platform.Variant,
				Enabled:  false,
				Status:   configuredStatus(false),
			})
			continue
		}
		if adapter := g.adapters[key]; adapter != nil {
			health := adapter.Health()
			health.Platform = platform.Type
			health.Variant = platform.Variant
			health.Enabled = platform.Enabled
			if health.Status == "" {
				health.Status = configuredStatus(platform.Enabled)
			}
			out.Platforms = append(out.Platforms, health)
			continue
		}
		if health, ok := g.health[key]; ok {
			out.Platforms = append(out.Platforms, health)
			continue
		}
		out.Platforms = append(out.Platforms, HealthStatus{
			Platform: platform.Type,
			Variant:  platform.Variant,
			Enabled:  platform.Enabled,
			Status:   configuredStatus(platform.Enabled),
		})
	}
	if out.Platforms == nil {
		out.Platforms = []HealthStatus{}
	}
	return out
}

// TestConnection 执行平台连接自检的骨架逻辑。
// TestConnection runs the skeleton platform self-test.
func (g *Gateway) TestConnection(platformType, variant string) TestResult {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, platform := range g.cfg.Platforms {
		if platform.Type != platformType {
			continue
		}
		if variant != "" && platform.Variant != variant {
			continue
		}
		key := adapterKey(platform.Type, platform.Variant)
		if g.adapters[key] == nil {
			if isAuthenticatedWeChat(platform) {
				return TestResult{
					OK:       true,
					Platform: platform.Type,
					Variant:  platform.Variant,
					Status:   "authenticated",
				}
			}
			return TestResult{
				OK:       false,
				Platform: platform.Type,
				Variant:  platform.Variant,
				Status:   "not_implemented",
				Error:    "adapter not registered",
			}
		}
		return TestResult{
			OK:       true,
			Platform: platform.Type,
			Variant:  platform.Variant,
			Status:   "registered",
		}
	}
	return TestResult{OK: false, Platform: platformType, Variant: variant, Status: "not_configured", Error: "platform not configured"}
}

// Submit 将规范化入站事件送入 Gateway bus。
// Submit sends a normalized inbound event into the Gateway bus.
func (g *Gateway) Submit(ctx context.Context, ev IMEvent) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case g.in <- ev:
		return nil
	default:
		return errors.New("im gateway inbound bus is full")
	}
}

// Inbound 返回 Gateway 的入站总线。
// Inbound returns the Gateway inbound bus.
func (g *Gateway) Inbound() chan<- IMEvent {
	return g.in
}

// DispatchOutbound 将出站 chunk 派发给对应平台 renderer。
// DispatchOutbound dispatches an outbound chunk to the matching platform renderer.
func (g *Gateway) DispatchOutbound(ctx context.Context, chunk IMOutChunk) error {
	if ctx == nil {
		ctx = context.Background()
	}
	platform, variant := outboundRoute(chunk)
	g.emit(LifecycleEvent{Type: "outbound_start", Platform: platform, Variant: variant, Message: chunk.Kind})

	renderer, key, err := g.resolveRenderer(platform, variant)
	if err != nil {
		g.emit(LifecycleEvent{Type: "outbound_error", Platform: platform, Variant: variant, Message: chunk.Kind, Error: err.Error()})
		return err
	}
	dispatcher := g.outboundDispatcher(key, renderer)
	if err := dispatcher.Dispatch(ctx, toOutboundChunk(chunk)); err != nil {
		g.emit(LifecycleEvent{Type: "outbound_error", Platform: platform, Variant: variant, Message: chunk.Kind, Error: err.Error()})
		return err
	}
	g.emit(LifecycleEvent{Type: "outbound_ok", Platform: platform, Variant: variant, Message: chunk.Kind})
	return nil
}

// Subscribe 订阅 Gateway 生命周期事件。
// Subscribe subscribes to Gateway lifecycle events.
func (g *Gateway) Subscribe(ctx context.Context) <-chan LifecycleEvent {
	if ctx == nil {
		ctx = context.Background()
	}
	ch := make(chan LifecycleEvent, 16)
	g.mu.Lock()
	g.subs[ch] = struct{}{}
	g.mu.Unlock()
	go func() {
		<-ctx.Done()
		g.mu.Lock()
		delete(g.subs, ch)
		close(ch)
		g.mu.Unlock()
	}()
	return ch
}

func (g *Gateway) resolveRenderer(platform, variant string) (OutboundRenderer, string, error) {
	if platform == "" {
		return nil, "", errors.New("im outbound requires platform")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.cfg.Enabled {
		return nil, "", errors.New("im gateway disabled")
	}
	if renderer, key := g.rendererByKeyLocked(platform, variant); renderer != nil {
		return renderer, key, nil
	}
	platformCfg, ok := g.matchPlatformConfigLocked(platform, variant)
	if !ok {
		return nil, "", fmt.Errorf("im platform not configured: %s", adapterKey(platform, variant))
	}
	key := adapterKey(platformCfg.Type, platformCfg.Variant)
	if renderer := g.renderers[key]; renderer != nil {
		return renderer, key, nil
	}
	if renderer := g.generated[key]; renderer != nil {
		return renderer, key, nil
	}
	factory := g.factories[platformCfg.Type]
	if factory == nil {
		return nil, "", fmt.Errorf("renderer not registered: %s", adapterKey(platform, variant))
	}
	renderer, err := factory(platformCfg)
	if err != nil {
		return nil, "", fmt.Errorf("create outbound renderer: %w", err)
	}
	if renderer == nil {
		return nil, "", fmt.Errorf("renderer factory returned nil: %s", key)
	}
	g.generated[key] = renderer
	return renderer, key, nil
}

func (g *Gateway) rendererByKeyLocked(platform, variant string) (OutboundRenderer, string) {
	keys := []string{adapterKey(platform, variant)}
	if variant != "" {
		keys = append(keys, platform)
	}
	for _, key := range keys {
		if renderer := g.renderers[key]; renderer != nil {
			return renderer, key
		}
		if renderer := g.generated[key]; renderer != nil {
			return renderer, key
		}
	}
	return nil, ""
}

func (g *Gateway) outboundDispatcher(key string, renderer OutboundRenderer) *outbound.Dispatcher {
	g.mu.Lock()
	defer g.mu.Unlock()
	if dispatcher := g.dispatch[key]; dispatcher != nil {
		return dispatcher
	}
	dispatcher := outbound.NewDispatcher(outboundRendererAdapter{renderer: renderer})
	g.dispatch[key] = dispatcher
	return dispatcher
}

func (g *Gateway) matchPlatformConfigLocked(platform, variant string) (config.IMPlatformConfig, bool) {
	for _, platformCfg := range g.cfg.Platforms {
		if platformCfg.Type != platform || !platformCfg.Enabled {
			continue
		}
		if variant == "" || platformCfg.Variant == variant {
			return platformCfg, true
		}
	}
	return config.IMPlatformConfig{}, false
}

func (g *Gateway) setHealth(key string, health HealthStatus) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.health[key] = health
}

func (g *Gateway) emit(ev LifecycleEvent) {
	ev.Time = time.Now()
	g.mu.RLock()
	defer g.mu.RUnlock()
	for ch := range g.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func adapterKey(platform, variant string) string {
	if variant == "" {
		return platform
	}
	return fmt.Sprintf("%s:%s", platform, variant)
}

func configuredStatus(enabled bool) string {
	if enabled {
		return "configured"
	}
	return "disabled"
}

func isAuthenticatedWeChat(platform config.IMPlatformConfig) bool {
	return platform.Type == "wechat" && platform.Token != ""
}

func outboundRoute(chunk IMOutChunk) (string, string) {
	platform := chunk.Platform
	if platform == "" {
		platform = chunk.Chat.Platform
	}
	return platform, chunk.Chat.Variant
}

type outboundRendererAdapter struct {
	renderer OutboundRenderer
}

func (a outboundRendererAdapter) Send(ctx context.Context, chunk outbound.Chunk) error {
	return a.renderer.Send(ctx, fromOutboundChunk(chunk))
}

func (a outboundRendererAdapter) Edit(ctx context.Context, ref outbound.ChatRef, msgID string, chunk outbound.Chunk) error {
	return a.renderer.Edit(ctx, fromOutboundChatRef(ref), msgID, fromOutboundChunk(chunk))
}

func (a outboundRendererAdapter) Typing(ctx context.Context, ref outbound.ChatRef) error {
	return a.renderer.Typing(ctx, fromOutboundChatRef(ref))
}

func (a outboundRendererAdapter) MaxTextLen() int {
	return a.renderer.MaxTextLen()
}

func (a outboundRendererAdapter) MarkdownDialect() outbound.MarkdownDialect {
	return outbound.MarkdownDialect(a.renderer.MarkdownDialect())
}

func toOutboundChunk(chunk IMOutChunk) outbound.Chunk {
	return outbound.Chunk{
		TraceID:  chunk.TraceID,
		Platform: chunk.Platform,
		Chat:     toOutboundChatRef(chunk.Chat),
		MsgID:    chunk.MsgID,
		Kind:     chunk.Kind,
		Text:     chunk.Text,
		Parts:    chunk.Parts,
		Done:     chunk.Done,
		Error:    chunk.Error,
		Metadata: chunk.Metadata,
	}
}

func fromOutboundChunk(chunk outbound.Chunk) IMOutChunk {
	return IMOutChunk{
		TraceID:  chunk.TraceID,
		Platform: chunk.Platform,
		Chat:     fromOutboundChatRef(chunk.Chat),
		MsgID:    chunk.MsgID,
		Kind:     chunk.Kind,
		Text:     chunk.Text,
		Parts:    chunk.Parts,
		Done:     chunk.Done,
		Error:    chunk.Error,
		Metadata: chunk.Metadata,
	}
}

func toOutboundChatRef(ref ChatRef) outbound.ChatRef {
	return outbound.ChatRef{
		Platform: ref.Platform,
		Variant:  ref.Variant,
		ChatID:   ref.ChatID,
		ChatType: ref.ChatType,
		ThreadID: ref.ThreadID,
	}
}

func fromOutboundChatRef(ref outbound.ChatRef) ChatRef {
	return ChatRef{
		Platform: ref.Platform,
		Variant:  ref.Variant,
		ChatID:   ref.ChatID,
		ChatType: ref.ChatType,
		ThreadID: ref.ThreadID,
	}
}
