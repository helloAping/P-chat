package browser

import (
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/tool"
)

func TestNewManager_DefaultState(t *testing.T) {
	cfg := config.BrowserConfig{
		Enabled:           true,
		ScreenshotQuality: 80,
	}
	reg := tool.NewRegistry()
	m := NewManager(cfg, reg)

	if !m.IsEnabled() {
		t.Fatal("manager should be enabled from config")
	}
	if m.Hub() == nil {
		t.Fatal("Hub() should not be nil")
	}
	if m.Hub().HasConnections() {
		t.Fatal("new manager should have no connections")
	}
}

func TestManager_Start_Stop_Idempotent(t *testing.T) {
	reg := tool.NewRegistry()
	cfg := config.BrowserConfig{Enabled: false}
	m := NewManager(cfg, reg)

	// Start twice should not panic.
	m.Start()
	m.Start()

	time.Sleep(50 * time.Millisecond) // let hub.Run() start

	// Stop twice should not panic.
	m.Stop()
	m.Stop()
}

func TestManager_Stop_Without_Start(t *testing.T) {
	reg := tool.NewRegistry()
	cfg := config.BrowserConfig{Enabled: false}
	m := NewManager(cfg, reg)

	// Stop without Start should not panic.
	m.Stop()
}

func TestManager_SetEnabled_Toggle(t *testing.T) {
	reg := tool.NewRegistry()
	cfg := config.BrowserConfig{Enabled: true}
	m := NewManager(cfg, reg)

	m.Start()
	defer m.Stop()
	time.Sleep(50 * time.Millisecond)

	m.SetEnabled(false)
	if m.IsEnabled() {
		t.Fatal("SetEnabled(false) did not take effect")
	}

	// No connections, so tools should NOT be registered on re-enable.
	// (SetEnabled only registers if HasConnections is true.)
	m.SetEnabled(true)
	if !m.IsEnabled() {
		t.Fatal("SetEnabled(true) did not take effect")
	}

	// Without any connections, browser tools should not be in the
	// registry even when enabled.
	for _, name := range browserToolNames {
		if _, ok := reg.Get(name); ok {
			t.Errorf("tool %q should NOT be registered without connections", name)
		}
	}
}

func TestManager_Start_DoesNotRegisterToolsWithoutConnection(t *testing.T) {
	reg := tool.NewRegistry()
	cfg := config.BrowserConfig{Enabled: true}
	m := NewManager(cfg, reg)
	m.Start()
	defer m.Stop()
	time.Sleep(50 * time.Millisecond)

	// No browser connected, so no tools should be registered.
	for _, name := range browserToolNames {
		if _, ok := reg.Get(name); ok {
			t.Errorf("tool %q should NOT be registered without connections right after Start", name)
		}
	}
}

func TestManager_Stop_UnregistersTools(t *testing.T) {
	reg := tool.NewRegistry()
	cfg := config.BrowserConfig{Enabled: true}
	m := NewManager(cfg, reg)

	// Manually register tools (simulating a connection had arrived).
	RegisterBrowserTools(reg, m.Hub(), m.Policy)

	m.Start()
	defer m.Stop()
	time.Sleep(50 * time.Millisecond)

	// Verify tools are currently registered.
	for _, name := range browserToolNames {
		if _, ok := reg.Get(name); !ok {
			t.Fatalf("tool %q should be registered before Stop", name)
		}
	}

	m.Stop()

	// After Stop, tools should be unregistered.
	for _, name := range browserToolNames {
		if _, ok := reg.Get(name); ok {
			t.Errorf("tool %q should be unregistered after Stop", name)
		}
	}
}
