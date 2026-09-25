// Package serverproc auto-starts pchat-server as a subprocess and
// waits for it to accept HTTP connections. Used by pchat (CLI) and
// pchat-gui (Wails) so the user only ever runs one command.
package serverproc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/p-chat/pchat/internal/paths"
	"github.com/p-chat/pchat/runtimeprofile"
)

// Server wraps a pchat-server subprocess.
type Server struct {
	Cmd         *exec.Cmd
	BaseURL     string // http://127.0.0.1:NNNN
	Port        int
	Profile     runtimeprofile.Profile
	InstanceID  string
	runtimeFile string

	// Auto-restart bookkeeping. All fields below are guarded by
	// mu and only relevant when opts.MaxRestarts > 0.
	mu           sync.Mutex
	stopped      bool // set by Stop() to suppress restart
	restartCount int
	opts         Options
}

// Options configures a server launch.
type Options struct {
	// Port 是 server 绑定端口；0 表示由系统分配临时端口，正数端口被占用时
	// 直接失败。
	// Port binds the server; 0 requests an ephemeral OS-assigned port, while a
	// positive value is strict.
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

// Start 启动 pchat-server，并等待启动公告与 HTTP health 的 profile、instance
// 和 PID 均匹配；调用方负责对返回句柄调用 Stop。
// Start launches pchat-server and waits for both startup identity checks.
func Start(ctx context.Context, opts Options) (*Server, error) {
	if opts.ServerBin == "" {
		return nil, errors.New("serverproc: ServerBin is required")
	}
	if opts.PingTimeout == 0 {
		opts.PingTimeout = 15 * time.Second
	}

	profile, err := runtimeprofile.Current(paths.GlobalDir())
	if err != nil {
		return nil, err
	}
	instanceID, err := runtimeprofile.NewInstanceID()
	if err != nil {
		return nil, err
	}
	runtimeFile, err := runtimeprofile.NewAnnouncementPath()
	if err != nil {
		return nil, err
	}
	cleanupRuntimeFile := true
	defer func() {
		if cleanupRuntimeFile {
			_ = os.Remove(runtimeFile)
		}
	}()

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
	childEnv := []string{
		fmt.Sprintf("PCHAT_PORT=%d", opts.Port),
		// PCHAT_DATA_HOME (not PCHAT_HOME) — PCHAT_HOME is the
		// install root set by install.ps1 -AddToPath. Reading
		// it for the data dir would cause memory / config to
		// land in the install directory. Pass the resolved
		// data dir explicitly so the child server agrees with
		// us regardless of the user's PCHAT_HOME value.
		"PCHAT_DATA_HOME=" + profile.DataHome,
		runtimeprofile.ProfileEnv + "=" + profile.Name,
		runtimeprofile.InstanceEnv + "=" + instanceID,
		runtimeprofile.RuntimeFileEnv + "=" + runtimeFile,
	}
	if opts.WebDir != "" {
		childEnv = append(childEnv, "PCHAT_WEB_DIR="+opts.WebDir)
	}
	cmd.Env = overrideEnvironment(os.Environ(), childEnv...)
	cmd.Stderr = opts.Stderr
	cmd.Stdout = opts.Stdout
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start server: %w", err)
	}

	srv := &Server{
		Cmd:         cmd,
		Profile:     profile,
		InstanceID:  instanceID,
		runtimeFile: runtimeFile,
		opts:        opts,
	}
	if err := srv.waitAnnouncement(ctx, opts.PingTimeout); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	if err := srv.waitReady(ctx, opts.PingTimeout); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	cleanupRuntimeFile = false

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
	s.mu.Lock()
	cmd := s.Cmd
	instanceID := s.InstanceID
	s.mu.Unlock()
	expectedPID := 0
	if cmd != nil && cmd.Process != nil {
		expectedPID = cmd.Process.Pid
	}
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("server did not become ready within %v", timeout)
		}
		// If the child already exited, give up early.
		if cmd != nil && cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			return fmt.Errorf("server exited before becoming ready (code %d)", cmd.ProcessState.ExitCode())
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", healthURL, nil)
		resp, err := client.Do(req)
		if err == nil {
			if resp.StatusCode == 200 {
				var health struct {
					Status     string `json:"status"`
					ProfileID  string `json:"profile_id"`
					InstanceID string `json:"instance_id"`
					PID        int    `json:"pid"`
				}
				decodeErr := json.NewDecoder(resp.Body).Decode(&health)
				_ = resp.Body.Close()
				if decodeErr == nil && health.Status == "ok" && health.ProfileID == s.Profile.ID &&
					health.InstanceID == instanceID && health.PID == expectedPID {
					return nil
				}
				if decodeErr == nil {
					return fmt.Errorf("server identity mismatch: got status=%q profile=%q instance=%q pid=%d, want status=%q profile=%q instance=%q pid=%d",
						health.Status, health.ProfileID, health.InstanceID, health.PID,
						"ok", s.Profile.ID, instanceID, expectedPID)
				}
			} else {
				_ = resp.Body.Close()
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

func (s *Server) waitAnnouncement(ctx context.Context, timeout time.Duration) error {
	s.mu.Lock()
	cmd := s.Cmd
	instanceID := s.InstanceID
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return errors.New("serverproc: child process is unavailable")
	}
	announcement, port, err := runtimeprofile.WaitAnnouncement(ctx, s.runtimeFile, s.Profile, instanceID, cmd.Process.Pid, timeout)
	if err != nil {
		return err
	}
	s.BaseURL = announcement.BaseURL
	s.Port = port
	return nil
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
		_ = os.Remove(s.runtimeFile)
		return
	case <-time.After(3 * time.Second):
		_ = s.Cmd.Process.Kill()
		<-done
		_ = os.Remove(s.runtimeFile)
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

		instanceID, err := runtimeprofile.NewInstanceID()
		if err != nil {
			log.Printf("[serverproc] restart %d: create instance identity: %v", count, err)
			continue
		}
		cmd := s.buildCommand(ctx, instanceID)
		_ = os.Remove(s.runtimeFile)
		if err := cmd.Start(); err != nil {
			log.Printf("[serverproc] restart %d: start failed: %v", count, err)
			continue
		}
		s.mu.Lock()
		s.Cmd = cmd
		s.InstanceID = instanceID
		s.mu.Unlock()

		if err := s.waitAnnouncement(ctx, s.opts.PingTimeout); err != nil {
			log.Printf("[serverproc] restart %d: announcement failed: %v", count, err)
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			continue
		}
		if err := s.waitReady(ctx, s.opts.PingTimeout); err != nil {
			log.Printf("[serverproc] restart %d: health check failed: %v", count, err)
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			continue
		}

		log.Printf("[serverproc] restart %d succeeded (PID=%d)", count, cmd.Process.Pid)
		_ = cmd.Wait()
	}
}

// buildCommand 根据保存的选项和本次进程身份构造 exec.Cmd；当 opts.Port 为 0
// 时，每次重启均由子进程重新分配临时端口。
// buildCommand constructs an exec.Cmd for one restart attempt.
func (s *Server) buildCommand(ctx context.Context, instanceID string) *exec.Cmd {
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
	childEnv := []string{
		fmt.Sprintf("PCHAT_PORT=%d", opts.Port),
		"PCHAT_DATA_HOME=" + s.Profile.DataHome,
		runtimeprofile.ProfileEnv + "=" + s.Profile.Name,
		runtimeprofile.InstanceEnv + "=" + instanceID,
		runtimeprofile.RuntimeFileEnv + "=" + s.runtimeFile,
	}
	if opts.WebDir != "" {
		childEnv = append(childEnv, "PCHAT_WEB_DIR="+opts.WebDir)
	}
	cmd.Env = overrideEnvironment(os.Environ(), childEnv...)
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

func overrideEnvironment(environment []string, overrides ...string) []string {
	keys := make([]string, 0, len(overrides))
	for _, entry := range overrides {
		key, _, _ := strings.Cut(entry, "=")
		keys = append(keys, key)
	}
	filtered := make([]string, 0, len(environment)+len(overrides))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		replaced := false
		for _, overrideKey := range keys {
			if strings.EqualFold(key, overrideKey) {
				replaced = true
				break
			}
		}
		if !replaced {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered, overrides...)
}
