package serverproc

import (
	"context"
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
		"":         0,
		"0":        0, // strconv.Atoi returns 0; we don't treat as "not set"
		"18960":    18960,
		"not a num": 0,
	}
	for input, want := range cases {
		t.Setenv("PCHAT_PORT", input)
		if got := PortFromEnv(); got != want {
			t.Errorf("PortFromEnv(%q) = %d, want %d", input, got, want)
		}
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

	// Need a config file so pchat-server can start.
	cfg := `llm:
  default: ollama
  providers:
    - name: ollama
      protocol: openai
      base_url: http://localhost:11434/v1
      api_key: ollama
      model: llama3
`
	if err := os.MkdirAll(filepath.Join(tmp, ".p-chat"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".p-chat", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	srv, err := Start(context.Background(), Options{
		ServerBin:   bin,
		ConfigPath:  filepath.Join(tmp, ".p-chat", "config.yaml"),
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
