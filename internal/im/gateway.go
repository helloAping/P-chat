package im

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/p-chat/pchat/internal/config"
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

// Gateway 管理所有 IM adapter 的生命周期。
// Gateway manages the lifecycle of all IM adapters.
type Gateway struct {
	mu        sync.RWMutex
	cfg       config.IMConfig
	adapters  map[string]Adapter
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
		cfg:      cfg,
		adapters: map[string]Adapter{},
		in:       make(chan IMEvent, 64),
		subs:     map[chan LifecycleEvent]struct{}{},
		health:   map[string]HealthStatus{},
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

// UpdateConfig 更新 Gateway 持有的 IM 配置。
// UpdateConfig updates the IM config held by the Gateway.
func (g *Gateway) UpdateConfig(cfg config.IMConfig) {
	cfg.Normalize()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cfg = cfg
	g.health = map[string]HealthStatus{}
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
		}
	}
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

// Inbound 返回 Gateway 的入站总线。
// Inbound returns the Gateway inbound bus.
func (g *Gateway) Inbound() chan<- IMEvent {
	return g.in
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
