package serverproc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/runtimeprofile"
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

func TestOverrideEnvironmentReplacesInheritedRuntimeValues(t *testing.T) {
	got := overrideEnvironment(
		[]string{"PATH=keep", "PCHAT_PORT=15150", "pchat_profile=prod"},
		"PCHAT_PORT=0", "PCHAT_PROFILE=dev",
	)
	want := []string{"PATH=keep", "PCHAT_PORT=0", "PCHAT_PROFILE=dev"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("overrideEnvironment() = %#v, want %#v", got, want)
	}
}

func TestListenFromEnvZeroAtomicallyOwnsEphemeralPort(t *testing.T) {
	t.Setenv("PCHAT_PORT", "0")
	t.Setenv("PCHAT_PORT_RANGE", "")

	listener, err := Listen("127.0.0.1", PreferredPortStart)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	address := listener.Addr().String()
	if listener.Addr().(*net.TCPAddr).Port == 0 {
		t.Fatalf("listener address %q still has port zero", address)
	}
	second, err := net.Listen("tcp", address)
	if err == nil {
		second.Close()
		t.Fatalf("allocated address %q was not held atomically", address)
	}
}

func TestListenRangeFallsBackAtomicallyWhenRangeIsOccupied(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	occupiedPort := occupied.Addr().(*net.TCPAddr).Port

	t.Setenv("PCHAT_PORT", "")
	t.Setenv("PCHAT_PORT_RANGE", fmt.Sprintf("%d-%d", occupiedPort, occupiedPort))
	listener, err := Listen("127.0.0.1", PreferredPortStart)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if got := listener.Addr().(*net.TCPAddr).Port; got == occupiedPort || got == 0 {
		t.Fatalf("fallback port = %d, occupied=%d", got, occupiedPort)
	}
}

func TestWaitReadyRejectsAnotherProcessPID(t *testing.T) {
	profile, err := runtimeprofile.Resolve(filepath.Join(t.TempDir(), ".p-chat"), "test")
	if err != nil {
		t.Fatal(err)
	}
	const instanceID = "instance-test"
	expectedPID := os.Getpid()
	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":      "ok",
			"profile_id":  profile.ID,
			"instance_id": instanceID,
			"pid":         expectedPID + 1,
		})
	}))
	defer healthServer.Close()

	srv := &Server{
		Cmd:        &exec.Cmd{Process: &os.Process{Pid: expectedPID}},
		BaseURL:    healthServer.URL,
		Profile:    profile,
		InstanceID: instanceID,
	}
	err = srv.waitReady(context.Background(), time.Second)
	if err == nil || !strings.Contains(err.Error(), "pid=") {
		t.Fatalf("waitReady error = %v, want PID identity mismatch", err)
	}
}

func TestBuildCommandUsesPerAttemptInstanceIdentity(t *testing.T) {
	profile, err := runtimeprofile.Resolve(filepath.Join(t.TempDir(), ".p-chat"), "test")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		Profile:     profile,
		runtimeFile: filepath.Join(t.TempDir(), "runtime.json"),
		opts: Options{
			ServerBin: "pchat-server",
			Port:      0,
		},
	}

	for _, instanceID := range []string{"first-instance", "restart-instance"} {
		cmd := srv.buildCommand(context.Background(), instanceID)
		if !strings.Contains(strings.Join(cmd.Env, "\n"), runtimeprofile.InstanceEnv+"="+instanceID) {
			t.Fatalf("buildCommand(%q) did not pass the attempt identity", instanceID)
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
	dataHome := filepath.Join(tmp, ".p-chat")
	t.Setenv("PCHAT_DATA_HOME", dataHome)
	t.Setenv("PCHAT_PROFILE", "test")

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
	if err := os.MkdirAll(dataHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataHome, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	srv, err := Start(context.Background(), Options{
		ServerBin:   bin,
		ConfigPath:  filepath.Join(dataHome, "config.json"),
		PingTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Stop()

	if srv.BaseURL == "" {
		t.Fatal("BaseURL not set")
	}
	if srv.InstanceID == "" {
		t.Fatal("InstanceID not set from startup announcement")
	}
	if srv.Profile.Name != "test" || srv.Profile.DataHome != dataHome {
		t.Fatalf("profile = %#v, want test profile at %q", srv.Profile, dataHome)
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
	var health struct {
		ProfileID  string `json:"profile_id"`
		InstanceID string `json:"instance_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if health.ProfileID != srv.Profile.ID || health.InstanceID != srv.InstanceID {
		t.Fatalf("health identity = (%q, %q), want (%q, %q)", health.ProfileID, health.InstanceID, srv.Profile.ID, srv.InstanceID)
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
