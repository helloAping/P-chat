// Package serverproc auto-starts pchat-server as a subprocess and
// waits for it to accept HTTP connections. Used by pchat (CLI) and
// pchat-gui (Wails) so the user only ever runs one command.
package serverproc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"github.com/p-chat/pchat/internal/paths"
)

// Server wraps a pchat-server subprocess.
type Server struct {
	Cmd     *exec.Cmd
	BaseURL string // http://127.0.0.1:NNNN
	Port    int
}

// Options configures a server launch.
type Options struct {
	// Port to bind the server on. 0 = pick a free port automatically.
	Port int
	// ConfigPath is the path passed as --config. Empty = default
	// ~/.p-chat/config.yaml.
	ConfigPath string
	// ServerBin is the path to the pchat-server binary. Required.
	ServerBin string
	// Stderr/Stdout for the subprocess; nil = /dev/null (silent).
	Stderr *os.File
	Stdout *os.File
	// WebDir, when non-empty, is forwarded to pchat-server as
	// PCHAT_WEB_DIR so it knows where the web/index.html lives.
	// If empty, pchat-server falls back to its CWD/web.
	WebDir string
	// PingTimeout caps how long we wait for the server to be ready.
	PingTimeout time.Duration
}

// Start launches pchat-server (if ServerBin is set) and blocks until
// the HTTP /health endpoint responds 200 OK. Returns the running
// server handle; caller is responsible for calling Stop.
func Start(ctx context.Context, opts Options) (*Server, error) {
	if opts.ServerBin == "" {
		return nil, errors.New("serverproc: ServerBin is required")
	}
	if opts.PingTimeout == 0 {
		opts.PingTimeout = 15 * time.Second
	}

	port := opts.Port
	if port == 0 {
		// Pick a free port: bind to :0, read the port, close.
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("pick free port: %w", err)
		}
		port = l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
	}

	args := []string{"--config", opts.ConfigPath}
	if opts.ConfigPath == "" {
		// Resolve the active home directory so the child can
		// find its config. pchat config moved from yaml to
		// json in 0.10; we try json first, then fall back to
		// yaml for older installs. The home dir itself is
		// decided by internal/paths (env var / sibling of
		// the binary / $HOME fallback) so a `bin/pchat-server`
		// dev run gets its own .p-chat next to the binary.
		jsonPath := paths.GlobalConfig()
		yamlPath := paths.GlobalConfigYAML()
		switch {
		case fileExists(jsonPath):
			args = []string{"--config", jsonPath}
		case fileExists(yamlPath):
			args = []string{"--config", yamlPath}
		default:
			// Neither file exists yet — fresh install.
			// Don't pass --config so pchat-server uses its
			// built-in defaults. The config file will be
			// created on first save.
			args = nil
		}
	}

	cmd := exec.CommandContext(ctx, opts.ServerBin, args...)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PCHAT_PORT=%d", port),
		"PCHAT_HOME="+paths.GlobalDir(),
	)
	if opts.WebDir != "" {
		cmd.Env = append(cmd.Env, "PCHAT_WEB_DIR="+opts.WebDir)
	}
	// PCHAT_PORT is informational; the real port comes from --config.
	// We also set the listen address via env so the child binds to
	// the port we picked. (pchat-server reads PCHAT_PORT to override
	// the configured port — see cmd/pchat-server/main.go for the
	// corresponding code.)
	cmd.Stderr = opts.Stderr
	cmd.Stdout = opts.Stdout
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start server: %w", err)
	}

	srv := &Server{
		Cmd:     cmd,
		BaseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		Port:    port,
	}
	if err := srv.waitReady(ctx, opts.PingTimeout); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	return srv, nil
}

// waitReady polls /health until it returns 200 OK or the timeout
// expires. If the child process exits, the ping fails and we return
// the error so the caller can show the exit reason.
func (s *Server) waitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	healthURL := s.BaseURL + "/api/v1/health"
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("server did not become ready within %v", timeout)
		}
		// If the child already exited, give up early.
		if s.Cmd.ProcessState != nil && s.Cmd.ProcessState.Exited() {
			return fmt.Errorf("server exited before becoming ready (code %d)", s.Cmd.ProcessState.ExitCode())
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", healthURL, nil)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
			// retry
		}
	}
}

// Stop terminates the subprocess gracefully (SIGTERM on Unix,
// taskkill on Windows). Falls back to Kill after a short timeout.
func (s *Server) Stop() {
	if s == nil || s.Cmd == nil || s.Cmd.Process == nil {
		return
	}
	_ = s.Cmd.Process.Signal(terminateSignal())
	done := make(chan struct{})
	go func() {
		s.Cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-time.After(3 * time.Second):
		_ = s.Cmd.Process.Kill()
		<-done
	}
}

// terminateSignal returns SIGTERM on Unix, os.Kill on Windows
// (Go has no Windows SIGTERM; just use Kill which maps to
// TerminateProcess).
func terminateSignal() os.Signal {
	if runtime.GOOS == "windows" {
		return os.Kill
	}
	return os.Interrupt
}

// PortFromEnv returns the PCHAT_PORT env var (0 if missing). The
// server uses this to override the configured port.
func PortFromEnv() int {
	s := os.Getenv("PCHAT_PORT")
	if s == "" {
		return 0
	}
	p, _ := strconv.Atoi(s)
	return p
}

// WebDirFromEnv returns the PCHAT_WEB_DIR env var (empty if
// missing). The server uses this to find the web/index.html
// static dir when launched as a subprocess by `pchat web`. If
// empty, the server falls back to its CWD/web, which only works
// when the user runs pchat-server from the repo root.
func WebDirFromEnv() string {
	return os.Getenv("PCHAT_WEB_DIR")
}

// fileExists is a small helper to avoid an os.Stat import noise
// at every call site.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
