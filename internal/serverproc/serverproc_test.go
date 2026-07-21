package serverproc

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStart_NoServerBin(t *testing.T) {
	_, err := Start(context.Background(), Options{})
	if err == nil {
		t.Error("expected error when ServerBin is empty")
	}
}

func TestStart_InvalidBin(t *testing.T) {
	_, err := Start(context.Background(), Options{
		ServerBin:   "/no/such/file",
		PingTimeout: 200 * time.Millisecond,
	})
	if err == nil {
		t.Error("expected error when ServerBin path is invalid")
	}
}

func TestStop_NilSafe(t *testing.T) {
	// Calling Stop on a zero Server must not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Stop(nil) panicked: %v", r)
		}
	}()
	(*Server)(nil).Stop()
	var s *Server
	s.Stop()
}

func TestPortFromEnv(t *testing.T) {
	cases := map[string]int{
		"":          0,
		"0":         0, // strconv.Atoi returns 0; we don't treat as "not set"
		"18960":     18960,
		"not a num": 0,
	}
	for input, want := range cases {
		t.Setenv("PCHAT_PORT", input)
		if got := PortFromEnv(); got != want {
			t.Errorf("PortFromEnv(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestWebDirFromEnv(t *testing.T) {
	cases := map[string]string{
		"":                           "",
		"web":                        "web",
		`C:\Users\me\src\p-chat\web`: `C:\Users\me\src\p-chat\web`,
		"/home/me/p-chat/web":        "/home/me/p-chat/web",
	}
	for input, want := range cases {
		t.Setenv("PCHAT_WEB_DIR", input)
		if got := WebDirFromEnv(); got != want {
			t.Errorf("WebDirFromEnv(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPickPreferredPort_FirstPreferredWhenAvailable(t *testing.T) {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", PreferredPortStart))
	if err != nil {
		t.Skipf("preferred port %d is already occupied: %v", PreferredPortStart, err)
	}
	_ = l.Close()

	port, err := PickPreferredPort()
	if err != nil {
		t.Fatal(err)
	}
	if port != PreferredPortStart {
		t.Fatalf("PickPreferredPort() = %d, want %d", port, PreferredPortStart)
	}
}

func TestPickPreferredPort_SkipsOccupiedPreferredPorts(t *testing.T) {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", PreferredPortStart))
	if err != nil {
		t.Skipf("preferred port %d is already occupied: %v", PreferredPortStart, err)
	}
	defer l.Close()

	port, err := PickPreferredPort()
	if err != nil {
		t.Fatal(err)
	}
	if port == PreferredPortStart {
		t.Fatalf("PickPreferredPort() reused occupied port %d", PreferredPortStart)
	}
	if port < PreferredPortStart || port > PreferredPortEnd {
		t.Fatalf("PickPreferredPort() = %d, want next port in %d-%d", port, PreferredPortStart+1, PreferredPortEnd)
	}
}

// TestStart_RealBinary builds a real pchat-server binary in a temp
// dir, starts it as a subprocess, waits for /health, then stops it.
// This is the only end-to-end check that the launch plumbing works
// (port allocation, PCHAT_PORT forwarding, /health polling, kill
// on shutdown).
func TestStart_RealBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping end-to-end subprocess test in -short mode")
	}

	bin := buildServerBinary(t)
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)

	// Need a config file so pchat-server can start. Since the
	// config format moved to JSON in 0.10, write JSON.
	cfg := `{
  "llm": {
    "default": "ollama",
    "providers": [
      {
        "name": "ollama",
        "protocol": "openai",
        "base_url": "http://localhost:11434/v1",
        "api_key": "ollama",
        "model": "llama3"
      }
    ]
  }
}`
	if err := os.MkdirAll(filepath.Join(tmp, ".p-chat"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".p-chat", "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	srv, err := Start(context.Background(), Options{
		ServerBin:   bin,
		ConfigPath:  filepath.Join(tmp, ".p-chat", "config.json"),
		PingTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Stop()

	if srv.BaseURL == "" {
		t.Fatal("BaseURL not set")
	}

	// Hit /health directly with the real http client.
	resp, err := http.Get(srv.BaseURL + "/api/v1/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("health = %d, want 200", resp.StatusCode)
	}
}

// buildServerBinary compiles pchat-server into a temp file and
// returns its path.
func buildServerBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "pchat-server.exe")
	cmd := commandGo("build", "-o", bin, "./cmd/pchat-server")
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build pchat-server: %v\n%s", err, out)
	}
	return bin
}
