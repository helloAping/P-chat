package browser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/tool"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// BR-05: simulated-extension harness.
//
// These tests do not need a real Chrome process. They stand up a local
// HTTP/WebSocket server that acts as the P-Chat hub side, dial a fake
// extension that answers JSON-RPC commands, and drive browser_* tools
// through the real handlers. Failures should make it obvious whether
// the break is in protocol, hub routing, policy, or tool wrapping.

// fakeExtension is a stateful mock of the Chrome extension:
// it tracks tabs, answers browser/* commands, and can be dropped to
// exercise disconnect/reconnect.
type fakeExtension struct {
	mu       sync.Mutex
	tabs     []TabInfo
	prefID   int
	lastCmds []string // method names, oldest first
	conn     *websocket.Conn
	closed   atomic.Bool
}

func newFakeExtension() *fakeExtension {
	return &fakeExtension{
		tabs: []TabInfo{
			{ID: 1, Index: 0, Title: "Home", URL: "https://example.com/", Active: true, Preferred: true},
			{ID: 2, Index: 1, Title: "Docs", URL: "https://docs.example.com/guide", Active: false},
		},
		prefID: 1,
	}
}

func (f *fakeExtension) preferred() *TabInfo {
	for i := range f.tabs {
		if f.tabs[i].ID == f.prefID {
			t := f.tabs[i]
			t.Preferred = true
			return &t
		}
	}
	if len(f.tabs) == 0 {
		return nil
	}
	t := f.tabs[0]
	t.Preferred = true
	return &t
}

func (f *fakeExtension) handle(req Request) Response {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastCmds = append(f.lastCmds, req.Method)

	params, _ := req.Params.(map[string]any)
	if params == nil {
		// Params may arrive as json.RawMessage depending on decode path.
		if raw, ok := req.Params.(json.RawMessage); ok {
			_ = json.Unmarshal(raw, &params)
		}
		if params == nil {
			// Last resort: re-marshal then unmarshal.
			if b, err := json.Marshal(req.Params); err == nil {
				_ = json.Unmarshal(b, &params)
			}
		}
		if params == nil {
			params = map[string]any{}
		}
	}

	var result any
	switch req.Method {
	case "browser/tabs":
		action, _ := params["action"].(string)
		switch strings.ToLower(action) {
		case "list", "":
			pref := f.prefID
			tabs := make([]TabInfo, len(f.tabs))
			copy(tabs, f.tabs)
			for i := range tabs {
				tabs[i].Preferred = tabs[i].ID == pref
				tabs[i].Active = tabs[i].ID == pref
			}
			result = TabsListResult{PreferredTabID: &pref, Tabs: tabs}
		case "select":
			id := intFromAny(params["tab_id"])
			if id == 0 {
				idx := intFromAny(params["index"])
				if idx >= 0 && idx < len(f.tabs) {
					id = f.tabs[idx].ID
				}
			}
			if id != 0 {
				f.prefID = id
			}
			p := f.preferred()
			if p == nil {
				result = map[string]any{"ok": true}
			} else {
				result = map[string]any{
					"id":               p.ID,
					"title":            p.Title,
					"url":              p.URL,
					"preferred_tab_id": p.ID,
				}
			}
		case "new":
			url, _ := params["url"].(string)
			if url == "" {
				url = "about:blank"
			}
			id := 100 + len(f.tabs)
			f.tabs = append(f.tabs, TabInfo{ID: id, Index: len(f.tabs), Title: "New", URL: url})
			f.prefID = id
			result = map[string]any{
				"id":               id,
				"title":            "New",
				"url":              url,
				"preferred_tab_id": id,
			}
		case "close":
			id := intFromAny(params["tab_id"])
			kept := f.tabs[:0]
			for _, t := range f.tabs {
				if t.ID != id {
					kept = append(kept, t)
				}
			}
			f.tabs = kept
			if len(f.tabs) == 0 {
				f.prefID = 0
				result = map[string]any{"preferred_tab_id": nil}
			} else {
				f.prefID = f.tabs[0].ID
				pid := f.prefID
				result = map[string]any{"preferred_tab_id": pid}
			}
		default:
			return Response{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: -32601, Message: "unknown tabs action"}}
		}
	case "browser/navigate":
		url, _ := params["url"].(string)
		if url == "" {
			return Response{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: -32602, Message: "url required"}}
		}
		tid := intFromAny(params["tab_id"])
		if tid == 0 {
			tid = f.prefID
		}
		for i := range f.tabs {
			if f.tabs[i].ID == tid {
				f.tabs[i].URL = url
				f.tabs[i].Title = "Navigated"
				break
			}
		}
		result = map[string]any{"ok": true, "url": url, "tab_id": tid}
	case "browser/click":
		ref, _ := params["ref"].(string)
		result = map[string]any{"ok": true, "ref": ref}
	case "browser/type":
		ref, _ := params["ref"].(string)
		text, _ := params["text"].(string)
		result = map[string]any{"ok": true, "ref": ref, "text": text}
	case "browser/screenshot":
		// Minimal valid JPEG data URL (1x1 pixel) for tool path coverage.
		result = map[string]any{
			"image": "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wAARCAABAAEDASIAAhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAn/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/8QAFQEBAQAAAAAAAAAAAAAAAAAAAAX/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oADAMBAAIQAxAAAAGcP//EABQQAQAAAAAAAAAAAAAAAAAAAAD/2gAIAQEAAQUCf//EABQRAQAAAAAAAAAAAAAAAAAAAAD/2gAIAQMBAT8Bf//EABQRAQAAAAAAAAAAAAAAAAAAAAD/2gAIAQIBAT8Bf//EABQQAQAAAAAAAAAAAAAAAAAAAAD/2gAIAQEABj8Cf//EABQQAQAAAAAAAAAAAAAAAAAAAAD/2gAIAQEAAT8hf//Z",
		}
	case "browser/snapshot":
		p := f.preferred()
		url, title := "", ""
		if p != nil {
			url, title = p.URL, p.Title
		}
		result = map[string]any{
			"url":      url,
			"title":    title,
			"elements": []map[string]string{{"ref": "button-1", "role": "button", "name": "OK"}},
		}
	case "browser/extract":
		p := f.preferred()
		url, title := "", ""
		if p != nil {
			url, title = p.URL, p.Title
		}
		result = map[string]any{
			"url":          url,
			"title":        title,
			"visible_text": "Hello from fake page",
		}
	case "browser/scroll", "browser/hover", "browser/press_key",
		"browser/select_option", "browser/file_upload", "browser/drag",
		"browser/evaluate", "browser/find":
		result = map[string]any{"ok": true, "method": req.Method}
	default:
		return Response{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: -32601, Message: "method not found: " + req.Method}}
	}

	raw, _ := json.Marshal(result)
	return Response{JSONRPC: "2.0", ID: req.ID, Result: raw}
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

// e2eEnv is a fully wired hub + fake extension + tool registry.
type e2eEnv struct {
	t       *testing.T
	hub     *BridgeHub
	reg     *tool.Registry
	ext     *fakeExtension
	srv     *httptest.Server
	browserID string
	conn    *websocket.Conn
	cancel  context.CancelFunc
}

func startE2E(t *testing.T, policy PolicyConfig) *e2eEnv {
	t.Helper()
	hub := NewHub()
	go hub.Run()
	reg := tool.NewRegistry()
	ext := newFakeExtension()

	env := &e2eEnv{t: t, hub: hub, reg: reg, ext: ext}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		ctx := r.Context()
		var hello HelloParams
		rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
		defer rcancel()
		if err := wsjson.Read(rctx, conn, &hello); err != nil {
			conn.Close(websocket.StatusProtocolError, "hello failed")
			return
		}
		id := hello.ID
		if id == "" {
			id = "e2e-browser-01"
		}
		wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
		_ = wsjson.Write(wctx, conn, HelloResponse{
			Type:            "hello-ok",
			BrowserID:       id,
			ProtocolVersion: ProtocolVersion,
		})
		wcancel()

		client := NewBrowserClient(id, hello.BrowserName, conn, hello)
		// Seed preferred tab from fake extension state.
		if p := ext.preferred(); p != nil {
			client.SetActiveTabMeta(p.ID, p.Title, p.URL, len(ext.tabs))
		}
		go client.StartReadPump(ctx)
		hub.Register(client)
		<-client.Done()
	})
	env.srv = httptest.NewServer(handler)

	// Register tools with live policy.
	policyFn := func() PolicyConfig { return policy }
	RegisterBrowserTools(reg, hub, policyFn)

	env.connect(t, "e2e-browser-01")
	return env
}

func (e *e2eEnv) connect(t *testing.T, id string) {
	t.Helper()
	wsURL := "ws" + e.srv.URL[4:]
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	conn, _, err := websocket.Dial(dialCtx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	e.conn = conn
	e.browserID = id

	// Pump: read commands from server, answer via fake extension.
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	go func() {
		// hello
		wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
		_ = wsjson.Write(wctx, conn, HelloParams{
			BrowserName:      "E2EChrome",
			TabsCount:        len(e.ext.tabs),
			ExtensionVersion: "1.0.0-e2e",
			ProtocolVersion:  ProtocolVersion,
			ID:               id,
		})
		wcancel()
		var helloResp HelloResponse
		rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
		if err := wsjson.Read(rctx, conn, &helloResp); err != nil {
			rcancel()
			return
		}
		rcancel()

		for {
			var req Request
			rctx, rcancel := context.WithTimeout(ctx, 30*time.Second)
			err := wsjson.Read(rctx, conn, &req)
			rcancel()
			if err != nil {
				return
			}
			// Params arrive as raw JSON from wsjson — rehydrate.
			if raw, ok := req.Params.(json.RawMessage); !ok || len(raw) == 0 {
				// nhooyr may decode into map[string]any already.
			} else {
				var m map[string]any
				if json.Unmarshal(raw, &m) == nil {
					req.Params = m
				}
			}
			// When params is still interface{} from default decode:
			if m, ok := req.Params.(map[string]any); ok {
				req.Params = m
			} else if req.Params != nil {
				b, _ := json.Marshal(req.Params)
				var m map[string]any
				_ = json.Unmarshal(b, &m)
				req.Params = m
			}
			resp := e.ext.handle(req)
			wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
			_ = wsjson.Write(wctx, conn, resp)
			wcancel()
		}
	}()

	// Wait for hub registration.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if e.hub.HasConnections() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("hub did not register client in time")
}

func (e *e2eEnv) close() {
	if e.cancel != nil {
		e.cancel()
	}
	if e.conn != nil {
		_ = e.conn.Close(websocket.StatusNormalClosure, "test done")
	}
	time.Sleep(100 * time.Millisecond)
	e.hub.Stop()
	if e.srv != nil {
		e.srv.Close()
	}
	UnregisterBrowserTools(e.reg)
}

func (e *e2eEnv) callTool(name string, args map[string]any) *tool.CallResult {
	e.t.Helper()
	h, ok := e.reg.Get(name)
	if !ok {
		e.t.Fatalf("tool %q not registered", name)
	}
	raw, _ := json.Marshal(args)
	// permission=full so BR-04 confirm does not block the suite;
	// policy hard-block tests use a separate path.
	ctx := tool.WithPermissionLevel(context.Background(), tool.PermissionFull)
	ctx = tool.WithSessionID(ctx, "e2e-session")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := h(ctx, raw)
	if err != nil {
		e.t.Fatalf("%s handler error: %v", name, err)
	}
	return res
}

func (e *e2eEnv) callToolAsk(name string, args map[string]any) *tool.CallResult {
	e.t.Helper()
	h, ok := e.reg.Get(name)
	if !ok {
		e.t.Fatalf("tool %q not registered", name)
	}
	raw, _ := json.Marshal(args)
	ctx := tool.WithPermissionLevel(context.Background(), tool.PermissionAsk)
	ctx = tool.WithSessionID(ctx, "e2e-session")
	// Auto-approve confirms so high-risk tools still exercise the
	// confirm path without a live GUI.
	ctx = tool.WithConfirmEmitter(ctx, func(req tool.ConfirmRequest) {
		// Deliver approval asynchronously so WaitForConfirm can park first.
		go func() {
			time.Sleep(20 * time.Millisecond)
			tool.SubmitConfirm("e2e-session", true)
		}()
		_ = req
	})
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := h(ctx, raw)
	if err != nil {
		e.t.Fatalf("%s handler error: %v", name, err)
	}
	return res
}

// --- Tests ---

func TestE2E_ConnectNavigateClickScreenshot(t *testing.T) {
	env := startE2E(t, PolicyConfig{RequireConfirm: "never"})
	defer env.close()

	if env.hub.Count() != 1 {
		t.Fatalf("expected 1 connection, got %d", env.hub.Count())
	}
	list := env.hub.List()
	if list[0].Name != "E2EChrome" {
		t.Errorf("browser name = %q", list[0].Name)
	}
	if list[0].ActiveTabURL == "" {
		t.Error("expected seeded ActiveTabURL")
	}

	// navigate
	nav := env.callTool("browser_navigate", map[string]any{"url": "https://example.com/docs"})
	if nav.IsError {
		t.Fatalf("navigate error: %s", nav.Content)
	}
	if !strings.Contains(nav.Content, "example.com/docs") && !strings.Contains(nav.Content, `"ok":true`) {
		// Accept either full result or ok flag.
		if !strings.Contains(nav.Content, "ok") {
			t.Errorf("navigate result unexpected: %s", nav.Content)
		}
	}

	// click
	click := env.callTool("browser_click", map[string]any{"ref": "button-1"})
	if click.IsError {
		t.Fatalf("click error: %s", click.Content)
	}

	// type (high risk, but require_confirm=never)
	typ := env.callTool("browser_type", map[string]any{"ref": "input-1", "text": "hello"})
	if typ.IsError {
		t.Fatalf("type error: %s", typ.Content)
	}

	// screenshot → Image + RawFull path
	shot := env.callTool("browser_screenshot", map[string]any{})
	if shot.IsError {
		t.Fatalf("screenshot error: %s", shot.Content)
	}
	if shot.Image == nil || shot.Image.Data == "" {
		t.Error("screenshot should carry Image data")
	}
	if shot.RawFull == "" || !strings.HasPrefix(shot.RawFull, "data:image/") {
		t.Errorf("screenshot RawFull missing data URL: %q", shot.RawFull)
	}

	// snapshot / extract
	snap := env.callTool("browser_snapshot", map[string]any{})
	if snap.IsError {
		t.Fatalf("snapshot error: %s", snap.Content)
	}
	ext := env.callTool("browser_extract", map[string]any{})
	if ext.IsError {
		t.Fatalf("extract error: %s", ext.Content)
	}
	if !strings.Contains(ext.Content, "Hello from fake page") {
		t.Errorf("extract content = %s", ext.Content)
	}

	// Ensure fake extension saw the core methods.
	env.ext.mu.Lock()
	cmds := append([]string(nil), env.ext.lastCmds...)
	env.ext.mu.Unlock()
	wantAny := []string{"browser/navigate", "browser/click", "browser/type", "browser/screenshot"}
	for _, w := range wantAny {
		found := false
		for _, c := range cmds {
			if c == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected command %q in %v", w, cmds)
		}
	}
}

func TestE2E_TabsSelectUpdatesPreferred(t *testing.T) {
	env := startE2E(t, PolicyConfig{RequireConfirm: "never"})
	defer env.close()

	// Select tab 2 via tool.
	res := env.callTool("browser_tabs", map[string]any{"action": "select", "tab_id": 2})
	if res.IsError {
		t.Fatalf("tabs select error: %s", res.Content)
	}

	// Hub cache should now point at tab 2.
	c, err := env.hub.getClient(env.browserID)
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}
	if c.ActiveTabID() != 2 {
		// Some implementations only set preferred_tab_id; accept URL match.
		if !strings.Contains(c.ActiveTabURL(), "docs.example.com") {
			t.Errorf("after select: tabID=%d url=%q", c.ActiveTabID(), c.ActiveTabURL())
		}
	}
}

func TestE2E_DisconnectAndReconnect(t *testing.T) {
	env := startE2E(t, PolicyConfig{RequireConfirm: "never"})
	defer env.close()

	if !env.hub.HasConnections() {
		t.Fatal("expected connection before disconnect")
	}

	// Drop the extension connection.
	_ = env.conn.Close(websocket.StatusNormalClosure, "simulate crash")
	if env.cancel != nil {
		env.cancel()
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !env.hub.HasConnections() {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if env.hub.HasConnections() {
		t.Fatal("hub still has connections after disconnect")
	}

	// Commands should fail while disconnected.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := env.hub.SendCommand(ctx, "", "browser/snapshot", nil, 2*time.Second)
	if err == nil {
		t.Fatal("expected SendCommand error while disconnected")
	}

	// Reconnect with same browser id (extension reconnect path).
	env.connect(t, "e2e-browser-01")
	if !env.hub.HasConnections() {
		t.Fatal("expected connection after reconnect")
	}

	// Tools should work again.
	res := env.callTool("browser_snapshot", map[string]any{})
	if res.IsError {
		t.Fatalf("snapshot after reconnect failed: %s", res.Content)
	}
}

func TestE2E_PolicyBlocksBlockedHost(t *testing.T) {
	env := startE2E(t, PolicyConfig{
		RequireConfirm: "dangerous",
		BlockedHosts:   []string{"evil.example"},
	})
	defer env.close()

	// permission=full still cannot override blocked_hosts (gateBrowserCall
	// runs Decide first and returns Block before RequireConfirm).
	res := env.callTool("browser_navigate", map[string]any{"url": "https://evil.example/steal"})
	if !res.IsError {
		t.Fatalf("expected blocked navigate, got: %s", res.Content)
	}
	if !strings.Contains(res.Content, "E_BROWSER_POLICY") && !strings.Contains(res.Content, "blocked") {
		t.Errorf("expected policy block message, got: %s", res.Content)
	}

	// Normal host still works.
	ok := env.callTool("browser_navigate", map[string]any{"url": "https://example.com/ok"})
	if ok.IsError {
		t.Fatalf("normal navigate should pass: %s", ok.Content)
	}
}

func TestE2E_HighRiskConfirmAutoApprove(t *testing.T) {
	env := startE2E(t, PolicyConfig{
		RequireConfirm: "dangerous",
		// No allowlist → type must confirm.
	})
	defer env.close()

	// Seed page URL so policy has a host.
	_ = env.callTool("browser_navigate", map[string]any{"url": "https://example.com/login"})

	res := env.callToolAsk("browser_type", map[string]any{"ref": "pwd", "text": "secret"})
	if res.IsError {
		t.Fatalf("type after confirm should succeed: %s", res.Content)
	}
}

func TestE2E_ManagerToolsRegisterOnConnect(t *testing.T) {
	// Drive the Manager lifecycle the way production does: tools
	// appear only after a connection, disappear after disconnect.
	cfg := config.BrowserConfig{
		Enabled:        true,
		RequireConfirm: "never",
	}
	reg := tool.NewRegistry()
	m := NewManager(cfg, reg)
	m.Start()
	defer m.Stop()

	// No tools yet.
	for _, name := range browserToolNames {
		if _, ok := reg.Get(name); ok {
			t.Fatalf("%s registered before any connection", name)
		}
	}

	// Use a lightweight hub registration (no full WS) to flip tools on.
	// Manager watches hub.SetOnToolsChange; Register from a real client
	// with nil conn still fires the callback.
	client := NewBrowserClient("mgr-e2e", "MgrChrome", nil, HelloParams{
		BrowserName: "MgrChrome",
		TabsCount:   1,
	})
	// Register via channel — need hub.Run (started by Manager.Start).
	m.Hub().Register(client)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := reg.Get("browser_navigate"); ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := reg.Get("browser_navigate"); !ok {
		t.Fatal("browser tools should register after connection")
	}

	m.Hub().Unregister("mgr-e2e")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := reg.Get("browser_navigate"); !ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("browser tools should unregister after last connection lost")
}

