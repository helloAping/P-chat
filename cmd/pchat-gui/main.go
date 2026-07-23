// Command pchat-gui launches the P-Chat desktop app.
//
// Architecture:
//   - This is a Wails v2 app that opens a WebView2 window.
//   - On startup, it spawns pchat-server.exe as a child process on a
//     stable preferred port on 127.0.0.1, falling back only if needed.
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
//	%LOCALAPPDATA%\Programs\P-Chat\
//	鈹溾攢鈹€ pchat-gui.exe        (this binary)
//	鈹溾攢鈹€ pchat-server.exe     (the HTTP API + web UI)
//	鈹溾攢鈹€ install.ps1
//	鈹斺攢鈹€ uninstall.ps1
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

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
		Title:             "P-Chat",
		Width:             1280,
		Height:            820,
		MinWidth:          900,
		MinHeight:         600,
		WindowStartState:  options.Normal,
		StartHidden:       true,
		HideWindowOnClose: false,
		BackgroundColour:  &options.RGBA{R: 10, G: 10, B: 10, A: 1},
		AssetServer: &assetserver.Options{
			Handler: app,
		},
		OnStartup:     app.startup,
		OnDomReady:    app.domReady,
		OnShutdown:    app.shutdown,
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
// loading HTML while the child is starting, and a proxy to the child
// once it's healthy.
type App struct {
	ctx           context.Context
	serverCmd     *exec.Cmd
	backendURL    atomic.Pointer[string] // "http://127.0.0.1:PORT"
	serverMu      sync.Mutex
	serverStopped bool
	quitting      atomic.Bool
	ready         atomic.Bool
	tray          *trayHandle
}

// NewApp creates a new App application struct.
func NewApp() *App {
	return &App{}
}

// ServeHTTP routes the request. If pchat-server is healthy and a backend
// URL is set, we forward the request to it. Otherwise we return the
// loading HTML.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	backend := a.backendURL.Load()
	if backend == nil {
		log.Printf("ServeHTTP: %s %s (no backend yet) -> loading", r.Method, r.URL.Path)
		writeLoading(w)
		return
	}
	ww := &statusRecorder{ResponseWriter: w, status: 200}
	a.proxyRequest(ww, r, *backend)
	log.Printf("ServeHTTP: %s %s -> %d", r.Method, r.URL.Path, ww.status)
}

// OpenExplorer opens the OS-native file manager at the given directory path.
func (a *App) OpenExplorer(path string) {
	if err := openExplorer(path); err != nil {
		log.Printf("OpenExplorer %q: %v", path, err)
	}
}

// OpenTerminal opens the OS-native terminal at the given directory path.
func (a *App) OpenTerminal(path string) {
	if err := openTerminal(path); err != nil {
		log.Printf("OpenTerminal %q: %v", path, err)
	}
}

// extractTraceID pulls the optional `trace_id` field out of a
// /sessions/:id/messages body JSON. The Wails binding can't add
// arbitrary request headers from JS, so the webview inlines the
// id in the body and we extract it here to set as the X-Trace-Id
// request header. Returns "" on parse failure or missing field
// — best-effort, never errors.
func extractTraceID(bodyJSON string) string {
	if bodyJSON == "" {
		return ""
	}
	var probe struct {
		TraceID string `json:"trace_id"`
	}
	if err := json.Unmarshal([]byte(bodyJSON), &probe); err != nil {
		return ""
	}
	return probe.TraceID
}

// GetBackendURL returns the pchat-server base URL (e.g.
// "http://127.0.0.1:18960") for the webview to use as a direct
// connection point. Returns "" if the child server hasn't
// announced its port yet — callers should retry.
//
// The webview uses this for the SSE message endpoint because the
// Wails AssetServer's response writer buffers the entire body and
// only flushes when the request handler returns, which is
// incompatible with an SSE stream that may park for minutes
// waiting on the user (the `question` tool flow).
func (a *App) GetBackendURL() string {
	v := a.backendURL.Load()
	if v == nil {
		return ""
	}
	return *v
}

// StreamMessages proxies a chat-completion request to pchat-server
// and re-broadcasts each SSE event to the webview via the
// `stream:event` Wails event. This is the SSE path the Wails
// AssetServer cannot serve: the AssetServer's response writer
// buffers the entire body and only flushes when the request
// handler returns, which doesn't happen for a 5-minute question
// tool block. Going through the Go side avoids the Wails proxy
// (and the CORS/Private-Network-Access headers it would otherwise
// need) entirely.
//
// `body` is the JSON request payload the webview would normally
// POST to /api/v1/sessions/:id/messages. Returns the number of
// events forwarded (or an error if the child server is
// unreachable / the request fails up front).
func (a *App) StreamMessages(sessionID string, bodyJSON string) (int, error) {
	backend := a.GetBackendURL()
	if backend == "" {
		return 0, fmt.Errorf("pchat-server not ready (no backend URL)")
	}
	url := fmt.Sprintf("%s/api/v1/sessions/%s/messages", backend, sessionID)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(bodyJSON))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	// P3-3: pull the trace id out of the body JSON and forward
	// it as the X-Trace-Id header. The Wails binding can't add
	// arbitrary request headers from JS, so the frontend
	// includes the id in the body instead and we extract it
	// here. Best-effort: malformed JSON / missing field just
	// means the server mints its own id (no request fails).
	if tid := extractTraceID(bodyJSON); tid != "" {
		req.Header.Set("X-Trace-Id", tid)
	}

	client := &http.Client{
		// No overall timeout — the `question` tool parks the
		// stream for up to 5 minutes. The frontend cancels via
		// AbortController which closes the response body and
		// unblocks the Read below.
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("stream POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("stream POST %s: HTTP %d: %s", url, resp.StatusCode, string(t))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var (
		eventName string
		dataBuf   strings.Builder
		count     int
	)
	emit := func() {
		if dataBuf.Len() == 0 {
			eventName = ""
			return
		}
		// Stamp the session id on every event so the frontend
		// can route concurrent streams to the right session.
		// EventsOn is process-global, so without this two
		// parallel SendMessage calls would interleave events
		// and Vue's reactive map (state.sessionMessages) would
		// see chunks for the wrong conversation.
		payload := map[string]interface{}{
			"session": sessionID,
			"event":   eventName,
			"data":    dataBuf.String(),
		}
		if b, err := json.Marshal(payload); err == nil {
			if a.ctx != nil {
				wailsruntime.EventsEmit(a.ctx, "stream:event", string(b))
			}
			count++
		}
		eventName = ""
		dataBuf.Reset()
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			emit()
		case strings.HasPrefix(line, "event: "):
			eventName = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if dataBuf.Len() > 0 {
				dataBuf.WriteByte('\n')
			}
			dataBuf.WriteString(strings.TrimPrefix(line, "data: "))
		}
	}
	emit() // flush trailing data block, if any
	if err := scanner.Err(); err != nil {
		log.Printf("StreamMessages: scanner error: %v", err)
	}
	// Signal end-of-stream so the frontend can finalize state.
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "stream:end", sessionID)
	}
	return count, nil
}

// CancelStream aborts an in-flight StreamMessages call. The
// webview calls this when the user hits Stop, closes the session,
// or navigates away. Implementation: closes the body of the
// current request, which makes the scanner return from Read and
// the goroutine in StreamMessages exit.
//
// We don't track the active *http.Response here — closing the
// request context (passed via the AbortController on the JS side)
// is enough; the goroutine running StreamMessages sees the body
// return an error and unwinds. This stub exists so the frontend
// can call something on Stop and so future refinement (per-call
// cancellation, ctx propagation) has a place to live.
func (a *App) CancelStream(sessionID string) {
	log.Printf("CancelStream: session=%s", sessionID)
}

// openExplorer opens the OS file manager at the given path.
func openExplorer(path string) error {
	stat, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("path not accessible: %w", err)
	}
	if !stat.IsDir() {
		return fmt.Errorf("not a directory: %s", path)
	}
	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer", path).Start()
	default:
		return fmt.Errorf("file explorer not supported on %s", runtime.GOOS)
	}
}

// openTerminal opens a new terminal window at the given path.
func openTerminal(path string) error {
	stat, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("path not accessible: %w", err)
	}
	if !stat.IsDir() {
		return fmt.Errorf("not a directory: %s", path)
	}
	switch runtime.GOOS {
	case "windows":
		script := fmt.Sprintf(`Start-Process powershell -ArgumentList '-NoExit','-Command','Set-Location ''%s'''`, path)
		return exec.Command("powershell", "-NoProfile", "-Command", script).Start()
	default:
		return fmt.Errorf("terminal not supported on %s", runtime.GOOS)
	}
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

// Flush pushes buffered data to the client immediately. SSE events
// (especially the "question" tool event which blocks the stream for
// minutes) must reach the WebView2 without delay, otherwise the
// frontend stalls at the loading cursor forever.
//
// Wails v2.12.0's AssetServer wraps the response writer in
// contentTypeSniffer (which stores the inner writer in an unexported
// "rw" field) and bodyRecorder. Neither implements http.Flusher, and
// neither provides Unwrap(). A simple type-assertion on the top-level
// writer silently fails. This method walks through both standard
// Unwrap() chains and unexported struct fields (via reflect+unsafe)
// to find the underlying http.Flusher.
func (s *statusRecorder) Flush() {
	if fl := findFlusher(s.ResponseWriter); fl != nil {
		fl.Flush()
	} else {
		// No flusher found in the response writer chain. SSE
		// events will be buffered in Go's bufio.Writer and only
		// reach WebView2 when the buffer fills (~4KB) or the
		// response ends. The question tool blocks for up to
		// 5 minutes, so small events (like the question payload)
		// never get through. Log this so we can diagnose.
		log.Printf("[wails] Flush: no http.Flusher found in response writer chain (type=%T) — SSE events may be buffered", s.ResponseWriter)
	}
}

// findFlusher searches for an http.Flusher by walking through
// http.ResponseWriter wrappers. It tries the standard Unwrap()
// chain first, then falls back to reflection (including unexported
// fields via unsafe) to handle opaque wrappers like Wails'
// contentTypeSniffer.
func findFlusher(w http.ResponseWriter) http.Flusher {
	type unwrapper interface{ Unwrap() http.ResponseWriter }

	rw := w
	for {
		if fl, ok := rw.(http.Flusher); ok {
			return fl
		}
		if u, ok := rw.(unwrapper); ok {
			rw = u.Unwrap()
			continue
		}
		break
	}
	return findFlusherReflect(reflect.ValueOf(w))
}

func findFlusherReflect(v reflect.Value) (fl http.Flusher) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[wails] findFlusherReflect: panic: %v (v.Kind=%s, v.Type=%s)", r, v.Kind(), v.Type())
		}
	}()
	// Unwrap interfaces and pointers until we reach a struct.
	// http.ResponseWriter is an interface whose dynamic type is
	// usually a pointer (e.g. *contentTypeSniffer → *http.response).
	// Each .Elem() on a pointer/interface makes the next value
	// addressable, which is required for UnsafeAddr() below.
	for {
		switch v.Kind() {
		case reflect.Interface:
			if v.IsNil() {
				return nil
			}
			v = v.Elem()
		case reflect.Ptr:
			if v.IsNil() {
				return nil
			}
			v = v.Elem()
		default:
			goto process
		}
	}
process:
	if v.Kind() != reflect.Struct {
		return nil
	}

	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := v.Field(i)
		if !f.IsValid() {
			continue
		}

		var iface interface{}
		if f.CanInterface() {
			iface = f.Interface()
		} else {
			// Unexported field — use reflect.NewAt+unsafe to
			// create an addressable copy that bypasses the
			// unexported restriction. This is necessary to
			// reach contentTypeSniffer.rw.
			rv := reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr()))
			iface = rv.Elem().Interface()
		}

		if iface == nil {
			continue
		}
		if f, ok := iface.(http.Flusher); ok {
			return f
		}
		if rw, ok := iface.(http.ResponseWriter); ok {
			if fl := findFlusherReflect(reflect.ValueOf(rw)); fl != nil {
				return fl
			}
		}
	}
	return nil
}

// proxyRequest forwards an HTTP request to the backend and streams the
// response back. SSE responses (text/event-stream) are copied with
// immediate flush after every write — no buffering. Regular responses
// use standard io.Copy.
func (a *App) proxyRequest(w http.ResponseWriter, r *http.Request, backend string) {
	targetURL := backend + r.RequestURI
	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for key, vals := range r.Header {
		for _, v := range vals {
			req.Header.Add(key, v)
		}
	}

	client := &http.Client{
		Transport: &dialFallbackTransport{
			inner:    http.DefaultTransport,
			fallback: []byte(loadingHTML),
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		writeLoading(w)
		return
	}
	defer resp.Body.Close()

	// Copy headers, rewriting absolute Location headers.
	for key, vals := range resp.Header {
		for _, v := range vals {
			if key == "Location" {
				if u, uErr := url.Parse(v); uErr == nil && u.IsAbs() {
					u.Scheme = "http"
					u.Host = "wails.localhost"
					v = u.String()
				}
			}
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		copyStreamFlush(w, resp.Body)
	} else {
		io.Copy(w, resp.Body)
	}
}

// copyStreamFlush copies r to w, flushing after every write so the
// WebView2 client sees each SSE event immediately — no buffering,
// no delay.
//
// The 4KB-padding fallback exists for response writers that don't
// implement http.Flusher (Wails v2's contentTypeSniffer is the
// canonical offender: it embeds http.ResponseWriter in an unexported
// field and provides no Flush). When the chain has no working flusher,
// Go's bufio.Writer buffers the SSE event in a 4KB buffer and only
// sends it when the buffer fills or the response ends — which never
// happens for the "question" tool event (the stream is parked for
// up to 5 minutes waiting for the user). Padding the write to ≥4KB
// forces an auto-flush: SSE comment lines (":…") are valid in the
// protocol and ignored by clients, including our frontend's
// `data:`-only parser.
func copyStreamFlush(w io.Writer, r io.Reader) {
	// padChunk is a pre-built SSE comment line slightly larger
	// than Go's default bufio.Writer size (4KB). Writing it after
	// any sub-4KB SSE event forces the underlying buffered writer
	// to drain to TCP, so the event reaches WebView2 immediately.
	const padThreshold = 4096
	padChunk := []byte(":" + strings.Repeat(" ", padThreshold) + "\n")
	var padded int64
	buf := make([]byte, 256)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				log.Printf("copyStreamFlush: write error: %v", werr)
				return
			}
			fl, hasFlusher := w.(http.Flusher)
			if hasFlusher {
				fl.Flush()
			} else if n < padThreshold {
				// No flusher reachable. Force a TCP flush by
				// writing a ~4KB SSE comment that overflows
				// the bufio.Writer buffer. The comment is
				// valid SSE and ignored by every conformant
				// client (and by our frontend's `data:`-only
				// parser).
				if _, werr := w.Write(padChunk); werr != nil {
					log.Printf("copyStreamFlush: padding write error: %v", werr)
				}
				padded += int64(len(padChunk))
			}
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("proxyRequest: stream read error: %v", err)
			}
			if padded > 0 {
				log.Printf("copyStreamFlush: used %d bytes of padding to force SSE flushes", padded)
			}
			return
		}
	}
}

// dialFallbackTransport wraps another RoundTripper and, on a connection
// error to the backend, returns a synthetic 200 response with the
// loading HTML as the body.
type dialFallbackTransport struct {
	inner    http.RoundTripper
	fallback []byte
}

func (t *dialFallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err == nil {
		return resp, nil
	}
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
	a.tray = startTray(a)
	go a.spawnAndWatch()
}

// shutdown handles process teardown paths that bypass OnBeforeClose.
// shutdown 兜住绕过窗口关闭钩子的退出路径，确保子进程被清理。
func (a *App) shutdown(ctx context.Context) {
	log.Printf("OnShutdown called")
	a.stopServer()
	if a.tray != nil {
		a.tray.stop()
	}
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

// beforeClose is called when the user closes the window.
// beforeClose 根据配置决定隐藏到托盘还是真正退出。
func (a *App) beforeClose(ctx context.Context) bool {
	log.Printf("OnBeforeClose called")
	if shouldPreventClose(a.quitting.Load(), readCloseBehavior()) && a.tray != nil && a.tray.ready() {
		a.hideMainWindow()
		return true
	}
	a.stopServer()
	return false
}

// stopServer stops the pchat-server child started by this GUI.
// stopServer 只停止当前 GUI 持有的子进程，并且可以重复调用。
func (a *App) stopServer() {
	a.serverMu.Lock()
	if a.serverStopped {
		a.serverMu.Unlock()
		return
	}
	cmd := a.serverCmd
	if cmd == nil || cmd.Process == nil {
		a.serverMu.Unlock()
		return
	}
	a.serverStopped = true
	a.serverMu.Unlock()

	log.Printf("stopping pchat-server PID=%d", cmd.Process.Pid)
	if err := cmd.Process.Kill(); err != nil {
		log.Printf("stopServer: kill PID=%d: %v", cmd.Process.Pid, err)
	}
	if _, err := cmd.Process.Wait(); err != nil {
		log.Printf("stopServer: wait PID=%d: %v", cmd.Process.Pid, err)
	}
}

// showMainWindow restores the main Wails window.
// showMainWindow 从托盘恢复主窗口。
func (a *App) showMainWindow() {
	if a.ctx == nil {
		return
	}
	wailsruntime.WindowShow(a.ctx)
	wailsruntime.WindowUnminimise(a.ctx)
}

// hideMainWindow hides the main window without stopping the server.
// hideMainWindow 仅隐藏窗口，后端服务继续运行。
func (a *App) hideMainWindow() {
	if a.ctx == nil {
		return
	}
	wailsruntime.WindowHide(a.ctx)
}

// quitApp performs a real application exit from the tray menu.
// quitApp 用于托盘菜单的真正退出路径。
func (a *App) quitApp() {
	a.quitting.Store(true)
	a.stopServer()
	if a.ctx != nil {
		wailsruntime.Quit(a.ctx)
	}
}

// ---------- server spawning ----------

// spawnAndWatch locates pchat-server.exe, picks a preferred port, starts it
// as a child process, installs the reverse proxy, waits for it to be
// healthy, and finally shows the window.
func (a *App) spawnAndWatch() {
	bin, err := findServerBinary()
	if err != nil {
		log.Printf("findServerBinary: %v", err)
		return
	}

	port, err := pickPreferredPort()
	if err != nil {
		log.Printf("pickPreferredPort: %v", err)
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
	// Forward both PCHAT_PORT and PCHAT_HOME so the child
	// server binds to the port we picked AND uses the same
	// home directory as the GUI (a sibling of the GUI binary
	// when running from bin/ or dev-bin/ — see homepath.go
	// for the resolution rules).
	cmd.Env = append(os.Environ(),
		"PCHAT_PORT="+strconv.Itoa(port),
		// PCHAT_DATA_HOME (not PCHAT_HOME) — PCHAT_HOME is
		// the install root set by install.ps1 -AddToPath.
		// Reading it for the data dir would cause memory /
		// config to land in the install directory. Pass
		// the resolved data dir explicitly so the child
		// server agrees with us regardless of what the
		// user's PCHAT_HOME happens to be.
		"PCHAT_DATA_HOME="+resolveHomeDir(),
	)
	// pchat-gui is a WINDOWS_GUI subsystem binary, but Go's
	// os/exec still allocates a fresh console for child processes
	// unless we set CREATE_NO_WINDOW explicitly. Without this, every
	// launch would pop up a black console window for pchat-server.
	hideChildConsole(cmd)
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
	a.serverMu.Lock()
	a.serverCmd = cmd
	a.serverStopped = false
	a.serverMu.Unlock()
	log.Printf("spawned pchat-server PID=%d", cmd.Process.Pid)

	// Store the backend URL immediately so the loading page can
	// start polling /api/v1/health through us. The child is starting up
	// in the background; the first few polls will get connection
	// refused and the JS will retry.
	beURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	a.backendURL.Store(&beURL)

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

	// Inject the backend URL into the webview as a global. The
	// frontend uses it to open a direct connection to pchat-server
	// for the SSE message endpoint — the Wails AssetServer's
	// response writer buffers the entire body and only flushes when
	// the request handler returns, which doesn't happen while the
	// `question` tool is parked waiting for the user.
	be := beURL
	js := fmt.Sprintf("window.__PCHAT_BACKEND__ = %q; window.__pchat_backend_injected__ = true; console.log('[pchat-gui] injected backend URL:', window.__PCHAT_BACKEND__);", be)
	log.Printf("[pchat-gui] injecting backend URL into webview: %s", be)
	wailsruntime.WindowExecJS(a.ctx, js)

	// Show the window. The webview is currently still rendering the
	// loading HTML; on its next health poll it will detect 200 and
	// navigate to "/", which now goes to pchat-server's real UI.
	a.showMainWindow()
}

// findServerBinary searches common locations for pchat-server.exe, in order:
//  1. The directory of the running pchat-gui.exe
//  2. The repository `bin/` directory (from CWD — covers `wails dev`)
//  3. PATH
func findServerBinary() (string, error) {
	// Resolve from executable directory first.
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates := []string{
			filepath.Join(dir, "pchat-server.exe"),
			filepath.Join(dir, "..", "pchat-server.exe"),
			filepath.Join(dir, "..", "..", "pchat-server.exe"),
		}
		for _, c := range candidates {
			if _, statErr := os.Stat(c); statErr == nil {
				abs, _ := filepath.Abs(c)
				log.Printf("findServerBinary: found at %s (exe=%s)", abs, exe)
				return abs, nil
			}
		}
	}

	// wails dev builds the gui binary to a temp directory, so
	// the exe-relative candidates above miss the project's bin/.
	// Fall back to CWD-relative search.
	if cwd, err := os.Getwd(); err == nil {
		// Walk up to 5 levels looking for bin/pchat-server.exe
		// to cover wails dev (CWD=cmd/pchat-gui) and running
		// from anywhere in the project tree.
		d := cwd
		for i := 0; i < 5; i++ {
			cand := filepath.Join(d, "bin", "pchat-server.exe")
			if _, statErr := os.Stat(cand); statErr == nil {
				abs, _ := filepath.Abs(cand)
				log.Printf("findServerBinary: found at %s (cwd=%s, levels=%d)", abs, cwd, i)
				return abs, nil
			}
			parent := filepath.Dir(d)
			if parent == d {
				break
			}
			d = parent
		}
	}

	if path, err := exec.LookPath("pchat-server.exe"); err == nil {
		log.Printf("findServerBinary: found in PATH: %s", path)
		return path, nil
	}
	return "", fmt.Errorf("pchat-server.exe not found next to pchat-gui.exe and not in PATH")
}

const (
	preferredPortStart = 15150
	preferredPortEnd   = 15159
)

// pickPreferredPort returns the first available port in P-Chat's stable
// local range, falling back to an OS-assigned port only if the range is full.
func pickPreferredPort() (int, error) {
	for port := preferredPortStart; port <= preferredPortEnd; port++ {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			_ = l.Close()
			return port, nil
		}
	}
	return pickFreePort()
}

func pickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// pickConfigPath returns the active p-chat config path. Prefers
// the newer json file, falls back to yaml. Returns "" when
// neither exists (fresh install — pchat-server uses built-in
// defaults).
//
// The home directory itself is decided by resolveHomeDir() in
// homepath.go — the GUI is its own Go module so it can't
// import internal/paths from the root module.
//   - $PCHAT_DATA_HOME if set
//   - sibling of this binary if it lives in bin/ or dev-bin/
//   - $HOME/.p-chat fallback
//
// When the GUI is launched from bin/pchat-gui.exe, the config
// it picks up is bin/.p-chat/config.json — fully isolated
// from the user's real ~/.p-chat.
//
// Note: PCHAT_HOME is NOT consulted. PCHAT_HOME is the install
// root (used in PATH as %PCHAT_HOME%) and must never be the
// data dir, otherwise installs with -AddToPath write their
// memory under the install directory.
func pickConfigPath() string {
	return resolveConfigPath()
}

// hideChildConsole is implemented per-platform in
// hide_child_console_windows.go (sets CREATE_NO_WINDOW so the
// WINDOWS_GUI-subsystem pchat-gui doesn't pop a console for
// pchat-server.exe) and hide_child_console_other.go (no-op
// stub — Linux/macOS GUI processes don't have a stray console
// window to suppress). Splitting by //go:build tag keeps the
// Windows-only syscall.SysProcAttr.CreationFlags field out of
// the non-Windows translation unit, which would otherwise fail
// to compile with `CreationFlags undefined` on Linux/macOS.
