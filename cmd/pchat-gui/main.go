// Command pchat-gui launches the P-Chat desktop app.
//
// Architecture:
//   - This is a Wails v2 app that opens a WebView2 window.
//   - On startup, it spawns pchat-server.exe as a child process on a free
//     port on 127.0.0.1.
//   - The webview serves ALL content from a reverse proxy: when the user
//     navigates to http://wails.localhost/..., we forward the request to
//     http://127.0.0.1:<port>/... (pchat-server). This keeps the webview
//     on its own origin (wails.localhost) and avoids CSP/cross-origin
//     issues that would otherwise block window.location.replace to
//     127.0.0.1.
//   - Before pchat-server is ready, the proxy is nil and the handler
//     returns a small loading HTML that polls /api/v1/health. As soon as
//     pchat-server becomes healthy, we set the proxy and the webview's
//     next request gets served by pchat-server directly.
//   - StartHidden: true keeps the window invisible until pchat-server is
//     healthy, so the user never sees a "black loading" flash on slow
//     machines. Once pchat-server is up, we WindowShow() and the user
//     sees the real UI.
//   - When the window closes (OnBeforeClose), the child server process is
//     killed.
//
// Layout at runtime (after install):
//
//   %LOCALAPPDATA%\Programs\P-Chat\
//   ├── pchat-gui.exe        (this binary)
//   ├── pchat-server.exe     (the HTTP API + web UI)
//   ├── install.ps1
//   └── uninstall.ps1
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// loadingHTML is served by the AssetServer handler while pchat-server is
// still starting up. The embedded JS polls /api/v1/health (which, once
// pchat-server is up, returns 200 via the reverse proxy). On the first
// 200, the page navigates back to "/" and pchat-server's real UI takes
// over. The window itself stays hidden during this phase thanks to
// StartHidden: true.
const loadingHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>P-Chat</title>
<style>
html,body{height:100%;margin:0;background:#0a0a0a;color:#e0e0e0;
  font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","Microsoft YaHei",sans-serif;
  display:flex;align-items:center;justify-content:center;text-align:center}
.wrap{padding:24px}
h1{font-weight:500;font-size:18px;letter-spacing:2px;margin:0 0 12px 0;color:#e0e0e0}
p{color:#9aa0a6;font-size:13px;margin:0;min-height:18px}
.dot{display:inline-block;width:6px;height:6px;background:#5b8cff;border-radius:50%;
  margin:0 2px;animation:p 1.2s infinite ease-in-out;vertical-align:middle}
.dot:nth-child(2){animation-delay:.2s}
.dot:nth-child(3){animation-delay:.4s}
@keyframes p{0%,to{opacity:.25}50%{opacity:1}}
.err{color:#ff8080}
</style>
</head>
<body>
<div class="wrap">
<h1>P-Chat</h1>
<p><span id="msg">正在启动后端服务</span><span class="dot"></span><span class="dot"></span><span class="dot"></span></p>
</div>
<script>
(function(){
  var msg = document.getElementById('msg');
  var tries = 0;
  function fail(text){
    msg.className = 'err';
    msg.textContent = text;
  }
  function tick(){
    tries++;
    if (tries > 300) { fail('后端服务启动超时（60秒），请查看 pchat-gui.log / pchat-server.log'); return; }
    fetch('/api/v1/health', {cache:'no-store', credentials:'omit'})
      .then(function(r){
        // We only treat the backend as "ready" when we get a real JSON
        // response from pchat-server. The fallback transport on the
        // proxy returns the loading HTML (text/html) on dial errors, so
        // a text/html response here means pchat-server is still starting.
        var ct = (r.headers.get('content-type') || '').toLowerCase();
        if (!r.ok || ct.indexOf('application/json') < 0) {
          throw new Error('not ready');
        }
        return r.json();
      })
      .then(function(j){
        if (j && j.status === 'ok') {
          window.location.replace('/app/');
          return;
        }
        throw new Error('not ready');
      })
      .catch(function(){
        msg.textContent = '正在启动后端服务';
        setTimeout(tick, 200);
      });
  }
  tick();
})();
</script>
</body>
</html>
`

func main() {
	// Open a debug log on disk so we can diagnose issues even if the
	// webview never appears (e.g. headless session).
	exe, _ := os.Executable()
	logPath := filepath.Join(filepath.Dir(exe), "pchat-gui.log")
	if lf, lfErr := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); lfErr == nil {
		log.SetOutput(lf)
		defer lf.Close()
	}
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("pchat-gui starting; exe=%s", exe)

	app := NewApp()
	err := wails.Run(&options.App{
		Title:            "P-Chat",
		Width:            1280,
		Height:           820,
		MinWidth:         900,
		MinHeight:        600,
		WindowStartState: options.Normal,
		StartHidden:      true,
		HideWindowOnClose: false,
		BackgroundColour: &options.RGBA{R: 10, G: 10, B: 10, A: 1},
		AssetServer: &assetserver.Options{
			Handler: app,
		},
		OnStartup:     app.startup,
		OnDomReady:    app.domReady,
		OnBeforeClose: app.beforeClose,
		Bind:          []interface{}{app},
	})
	if err != nil {
		log.Printf("wails.Run returned: %v", err)
		fmt.Println("Error:", err.Error())
	}
	log.Printf("pchat-gui exiting")
}

// App holds the child server process. It also implements http.Handler so
// the Wails AssetServer can route every webview request through us: a
// loading HTML while the child is starting, and a reverse proxy to the
// child once it's healthy.
type App struct {
	ctx       context.Context
	serverCmd *exec.Cmd
	proxy     atomic.Pointer[httputil.ReverseProxy]
	proxyHost atomic.Pointer[string] // host:port of the backend, used to rewrite Location headers
	mu        sync.Mutex
	ready     atomic.Bool
}

// NewApp creates a new App application struct.
func NewApp() *App {
	return &App{}
}

// ServeHTTP routes the request. If pchat-server is healthy and a proxy is
// installed, we forward the request to it (and rewrite absolute-Location
// redirects back to the webview origin). Otherwise we return the loading
// HTML with HTTP 503.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := a.proxy.Load()
	if p == nil {
		log.Printf("ServeHTTP: %s %s (no proxy yet) -> loading", r.Method, r.URL.Path)
		writeLoading(w)
		return
	}
	ww := &statusRecorder{ResponseWriter: w, status: 200}
	p.ServeHTTP(ww, r)
	log.Printf("ServeHTTP: %s %s -> %d", r.Method, r.URL.Path, ww.status)
}

func writeLoading(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, loadingHTML)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// installProxy builds and stores a reverse proxy to pchat-server running
// on host:port. It also rewrites any absolute "Location" header that
// points at the backend so the webview keeps staying inside the
// wails.localhost origin.
//
// The proxy uses a "dial-failure fallback" transport: if pchat-server
// isn't accepting connections yet, the transport returns the loading
// HTML (200) instead of a 502, so WebView2 doesn't swap in its own error
// page. The loading HTML's JS keeps polling /api/v1/health; once the
// proxy can actually reach the backend, that endpoint returns real JSON,
// the JS detects the Content-Type, and the webview navigates to /app/.
func (a *App) installProxy(host string, port int) {
	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("%s:%d", host, port)}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		originalDirector(r)
		r.Host = target.Host
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		loc := resp.Header.Get("Location")
		if loc == "" {
			return nil
		}
		if u, err := url.Parse(loc); err == nil {
			if u.IsAbs() && strings.EqualFold(u.Host, target.Host) {
				u.Scheme = "http"
				u.Host = "wails.localhost"
				resp.Header.Set("Location", u.String())
			}
		}
		return nil
	}
	proxy.Transport = &dialFallbackTransport{
		inner:    http.DefaultTransport,
		fallback: []byte(loadingHTML),
	}
	a.proxyHost.Store(&target.Host)
	a.proxy.Store(proxy)
}

// dialFallbackTransport wraps another RoundTripper and, on a connection
// error to the backend, returns a synthetic 200 response with the
// loading HTML as the body. This prevents WebView2 from showing its
// built-in error page while pchat-server is still starting up.
type dialFallbackTransport struct {
	inner    http.RoundTripper
	fallback []byte
}

func (t *dialFallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err == nil {
		return resp, nil
	}
	// Connection-level error: backend is not (yet) reachable.
	headers := http.Header{}
	headers.Set("Content-Type", "text/html; charset=utf-8")
	headers.Set("Cache-Control", "no-store")
	headers.Set("X-Pchat-Backend", "unreachable")
	return &http.Response{
		Status:        "200 OK",
		StatusCode:    200,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        headers,
		Body:          io.NopCloser(bytes.NewReader(t.fallback)),
		ContentLength: int64(len(t.fallback)),
		Request:       req,
	}, nil
}

// startup is called when the app starts. We spawn pchat-server.exe in a
// background goroutine and install the reverse proxy once it's healthy,
// at which point we also show the window.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	log.Printf("OnStartup called")
	go a.spawnAndWatch()
}

// domReady is called when the webview's DOM is ready. Wails' default
// window placement on small screens can leave the title bar above the
// monitor (e.g. y = -33 on a 1280x720 laptop), which is what produced
// the original "black screen" complaint. We re-center the window here
// and clamp the size to the primary screen so the title bar and content
// are both visible.
func (a *App) domReady(ctx context.Context) {
	log.Printf("OnDomReady called")
	w, h := wailsruntime.WindowGetSize(a.ctx)
	log.Printf("OnDomReady: initial size = %dx%d", w, h)

	sw, sh, ok := primaryScreenSize(ctx)
	if !ok {
		sw, sh = 1280, 720
	}
	log.Printf("OnDomReady: primary screen = %dx%d", sw, sh)
	maxW := int(float64(sw) * 0.92)
	maxH := int(float64(sh) * 0.85)
	if w > maxW {
		w = maxW
	}
	if h > maxH {
		h = maxH
	}
	if w < 700 {
		w = 700
	}
	if h < 460 {
		h = 460
	}
	log.Printf("OnDomReady: resizing to %dx%d", w, h)
	wailsruntime.WindowSetSize(a.ctx, w, h)
	wailsruntime.WindowCenter(a.ctx)
	wx, wy := wailsruntime.WindowGetPosition(a.ctx)
	log.Printf("OnDomReady: window position after center = (%d, %d)", wx, wy)
}

// primaryScreenSize returns the primary monitor's size in pixels, or
// (0, 0, false) if the screen list isn't available yet.
func primaryScreenSize(ctx context.Context) (int, int, bool) {
	screens, err := wailsruntime.ScreenGetAll(ctx)
	if err != nil || len(screens) == 0 {
		return 0, 0, false
	}
	for _, s := range screens {
		if s.IsPrimary {
			return s.Size.Width, s.Size.Height, true
		}
	}
	return screens[0].Size.Width, screens[0].Size.Height, true
}

// beforeClose is called when the user closes the window. We kill the
// child server process so it does not linger after the GUI exits.
func (a *App) beforeClose(ctx context.Context) bool {
	log.Printf("OnBeforeClose called")
	a.mu.Lock()
	cmd := a.serverCmd
	a.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	return false
}

// ---------- server spawning ----------

// spawnAndWatch locates pchat-server.exe, picks a free port, starts it as
// a child process, installs the reverse proxy, waits for it to be
// healthy, and finally shows the window.
func (a *App) spawnAndWatch() {
	bin, err := findServerBinary()
	if err != nil {
		log.Printf("findServerBinary: %v", err)
		return
	}

	port, err := pickFreePort()
	if err != nil {
		log.Printf("pickFreePort: %v", err)
		return
	}
	log.Printf("picked port %d", port)

	// pchat-server only accepts --config; the bind port is overridden
	// via the PCHAT_PORT env var (see internal/serverproc).
	var args []string
	if cfg := pickConfigPath(); cfg != "" {
		args = append(args, "--config", cfg)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "PCHAT_PORT="+strconv.Itoa(port))
	// Forward child output to the same log file the GUI uses so users
	// can see both pchat-gui and pchat-server logs side by side.
	if lf, lfErr := os.OpenFile(filepath.Join(filepath.Dir(bin), "pchat-server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); lfErr == nil {
		cmd.Stdout = lf
		cmd.Stderr = lf
	} else {
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Start(); err != nil {
		log.Printf("cmd.Start: %v", err)
		return
	}
	a.mu.Lock()
	a.serverCmd = cmd
	a.mu.Unlock()
	log.Printf("spawned pchat-server PID=%d", cmd.Process.Pid)

	// Install the reverse proxy immediately so the loading page can
	// start polling /api/v1/health through us. The child is starting up
	// in the background; the first few polls will get connection
	// refused and the JS will retry.
	a.installProxy("127.0.0.1", port)

	// Wait for the server to be healthy (max 30s).
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/api/v1/health", port)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			log.Printf("pchat-server exited prematurely (code %d)", cmd.ProcessState.ExitCode())
			return
		}
		client := &http.Client{Timeout: 2 * time.Second}
		if resp, herr := client.Get(healthURL); herr == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if a.ctx == nil {
		log.Printf("wails ctx disappeared before health")
		return
	}
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		return
	}
	log.Printf("pchat-server is healthy at %s", healthURL)
	a.ready.Store(true)

	// Show the window. The webview is currently still rendering the
	// loading HTML; on its next health poll it will detect 200 and
	// navigate to "/", which now goes to pchat-server's real UI.
	wailsruntime.WindowShow(a.ctx)
}

// findServerBinary searches common locations for pchat-server.exe, in order:
//   1. The directory of the running pchat-gui.exe
//   2. The repository `bin/` directory (dev mode, two levels up)
//   3. PATH
func findServerBinary() (string, error) {
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		candidates := []string{
			filepath.Join(dir, "pchat-server.exe"),
			filepath.Join(dir, "..", "pchat-server.exe"),
			filepath.Join(dir, "..", "..", "pchat-server.exe"),
		}
		for _, c := range candidates {
			if _, statErr := os.Stat(c); statErr == nil {
				abs, _ := filepath.Abs(c)
				return abs, nil
			}
		}
	}
	if path, err := exec.LookPath("pchat-server.exe"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("pchat-server.exe not found next to pchat-gui.exe and not in PATH")
}

// pickFreePort asks the OS for a free TCP port.
func pickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// pickConfigPath returns the user's p-chat config path. Prefers the
// newer json file, falls back to yaml. Empty string means "use
// pchat-server's built-in default".
func pickConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	jsonPath := filepath.Join(home, ".p-chat", "config.json")
	yamlPath := filepath.Join(home, ".p-chat", "config.yaml")
	if _, err := os.Stat(jsonPath); err == nil {
		return jsonPath
	}
	if _, err := os.Stat(yamlPath); err == nil {
		return yamlPath
	}
	return jsonPath
}
