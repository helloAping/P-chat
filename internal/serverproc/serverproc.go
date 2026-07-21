// Package serverproc auto-starts pchat-server as a subprocess and
// waits for it to accept HTTP connections. Used by pchat (CLI) and
// pchat-gui (Wails) so the user only ever runs one command.
package serverproc

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/p-chat/pchat/internal/paths"
)

// Server wraps a pchat-server subprocess.
type Server struct {
	Cmd     *exec.Cmd
	BaseURL string // http://127.0.0.1:NNNN
	Port    int

	// Auto-restart bookkeeping. All fields below are guarded by
	// mu and only relevant when opts.MaxRestarts > 0.
	mu           sync.Mutex
	stopped      bool // set by Stop() to suppress restart
	restartCount int
	opts         Options
}

// Options configures a server launch.
type Options struct {
	// Port to bind the server on. 0 = pick from P-Chat's preferred
	// local range (15150-15159), falling back to an OS-assigned port.
	Port int
	// ConfigPath is the path passed as --config. Empty = use
	// the data dir's config.json (preferred) or config.yaml
	// (legacy); both are decided by internal/paths via the
	// PCHAT_DATA_HOME / sibling / $HOME fallback chain.
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
	// MaxRestarts is the maximum number of times to relaunch
	// the server if it exits unexpectedly after becoming healthy.
	// 0 (default) = no auto-restart, preserves the original
	// behaviour. Each restart waits RestartBackOff before
	// relaunching. Stop() always suppresses the restart loop.
	MaxRestarts int
	// RestartBackOff is the delay between an unexpected exit
	// and the next launch attempt. Zero defaults to 5s.
	RestartBackOff time.Duration
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
		var err error
		port, err = PickPreferredPort()
		if err != nil {
			return nil, fmt.Errorf("pick free port: %w", err)
		}
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
		// Note: the data dir itself is decided by internal/paths
		// (PCHAT_DATA_HOME / sibling / $HOME fallback). The GUI
		// explicitly passes PCHAT_DATA_HOME = <resolved dir> on
		// the child env, so the child sees the same data dir
		// we did.
	}

	cmd := exec.CommandContext(ctx, opts.ServerBin, args...)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PCHAT_PORT=%d", port),
		// PCHAT_DATA_HOME (not PCHAT_HOME) — PCHAT_HOME is the
		// install root set by install.ps1 -AddToPath. Reading
		// it for the data dir would cause memory / config to
		// land in the install directory. Pass the resolved
		// data dir explicitly so the child server agrees with
		// us regardless of the user's PCHAT_HOME value.
		"PCHAT_DATA_HOME="+paths.GlobalDir(),
	)
	if opts.WebDir != "" {
		cmd.Env = append(cmd.Env, "PCHAT_WEB_DIR="+opts.WebDir)
	}
	cmd.Stderr = opts.Stderr
	cmd.Stdout = opts.Stdout
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start server: %w", err)
	}

	srv := &Server{
		Cmd:     cmd,
		BaseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		Port:    port,
		opts:    opts,
	}
	if err := srv.waitReady(ctx, opts.PingTimeout); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}

	// Launch auto-restart watcher if enabled. The goroutine
	// blocks on cmd.Wait() and, if Stop() hasn't been called,
	// relaunches the server up to opts.MaxRestarts times with
	// a back-off in between.
	if opts.MaxRestarts > 0 {
		srv.mu.Lock()
		srv.restartCount = 0
		srv.mu.Unlock()
		go srv.watchAndRestart(ctx)
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

// Stop tells the restart watcher (if any) that the shutdown is
// intentional, then terminates the subprocess. After Stop returns
// the watcher goroutine will exit without relaunching.
func (s *Server) Stop() {
	if s == nil || s.Cmd == nil || s.Cmd.Process == nil {
		return
	}
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()

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

// watchAndRestart blocks on Cmd.Wait(). If the exit was not
// intentional (Stop() not called), it relaunches the server
// up to opts.MaxRestarts times with RestartBackOff between
// attempts. The loop exits when Stop() is called, max restarts
// exhausted, or a relaunch fails.
func (s *Server) watchAndRestart(ctx context.Context) {
	backOff := s.opts.RestartBackOff
	if backOff <= 0 {
		backOff = 5 * time.Second
	}
	_ = s.Cmd.Wait()

	for {
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return
		}
		if s.restartCount >= s.opts.MaxRestarts {
			s.mu.Unlock()
			log.Printf("[serverproc] max restarts (%d) reached, giving up", s.opts.MaxRestarts)
			return
		}
		s.restartCount++
		count := s.restartCount
		s.mu.Unlock()

		log.Printf("[serverproc] restarting pchat-server (attempt %d/%d) in %v",
			count, s.opts.MaxRestarts, backOff)
		time.Sleep(backOff)

		cmd := s.buildCommand(ctx)
		if err := cmd.Start(); err != nil {
			log.Printf("[serverproc] restart %d: start failed: %v", count, err)
			continue
		}
		old := s.Cmd
		s.mu.Lock()
		s.Cmd = cmd
		s.mu.Unlock()

		if err := s.waitReady(ctx, s.opts.PingTimeout); err != nil {
			log.Printf("[serverproc] restart %d: health check failed: %v", count, err)
			_ = cmd.Process.Kill()
			s.mu.Lock()
			s.Cmd = old
			s.mu.Unlock()
			continue
		}

		log.Printf("[serverproc] restart %d succeeded (PID=%d)", count, cmd.Process.Pid)
		_ = cmd.Wait()
	}
}

// buildCommand constructs an exec.Cmd from the saved opts,
// reusing the already-assigned port. Meant for restart watcher.
func (s *Server) buildCommand(ctx context.Context) *exec.Cmd {
	opts := s.opts
	args := []string{"--config", opts.ConfigPath}
	if opts.ConfigPath == "" {
		jsonPath := paths.GlobalConfig()
		yamlPath := paths.GlobalConfigYAML()
		switch {
		case fileExists(jsonPath):
			args = []string{"--config", jsonPath}
		case fileExists(yamlPath):
			args = []string{"--config", yamlPath}
		default:
			args = nil
		}
	}
	cmd := exec.CommandContext(ctx, opts.ServerBin, args...)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PCHAT_PORT=%d", s.Port),
		"PCHAT_DATA_HOME="+paths.GlobalDir(),
	)
	if opts.WebDir != "" {
		cmd.Env = append(cmd.Env, "PCHAT_WEB_DIR="+opts.WebDir)
	}
	cmd.Stderr = opts.Stderr
	cmd.Stdout = opts.Stdout
	return cmd
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
