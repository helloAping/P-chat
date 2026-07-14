package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// TestWebSocket_FullRoundTrip sets up a real HTTP server that
// accepts a WebSocket, performs the hello handshake, sends a
// JSON-RPC command, and receives a response. This is the closest
// we can get to an end-to-end test without an actual Chrome
// extension.
func TestWebSocket_FullRoundTrip(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	defer hub.Stop()

	var mu sync.Mutex
	var registeredID string

	hub.SetOnToolsChange(func(has bool) {
		mu.Lock()
		if has && registeredID == "" {
			list := hub.List()
			if len(list) > 0 {
				registeredID = list[0].ID
			}
		}
		mu.Unlock()
	})

	// Start a test HTTP server that accepts WebSocket upgrades.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			t.Logf("accept error: %v", err)
			return
		}

		ctx := r.Context()

		// Read hello from client.
		var hello HelloParams
		rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
		defer rcancel()
		if err := wsjson.Read(rctx, conn, &hello); err != nil {
			t.Logf("hello read error: %v", err)
			conn.Close(websocket.StatusProtocolError, "hello failed")
			return
		}

		id := "test-browser-01"
		if hello.ID != "" {
			id = hello.ID
		}

		// Send hello-ok.
		resp := HelloResponse{Type: "hello-ok", BrowserID: id}
		wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
		defer wcancel()
		if err := wsjson.Write(wctx, conn, resp); err != nil {
			t.Logf("hello write error: %v", err)
			return
		}

		client := NewBrowserClient(id, hello.BrowserName, conn)

		// Start pump so the client can receive responses.
		go client.StartReadPump(ctx)

		hub.Register(client)
		// Keep handler alive until connection closes.
		<-client.Done()
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	wsURL := "ws" + srv.URL[4:] // http → ws

	// --- Client side: simulate a Chrome extension. ---

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	clientConn, _, err := websocket.Dial(dialCtx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer clientConn.Close(websocket.StatusNormalClosure, "done")

	// Send hello.
	hello := HelloParams{
		BrowserName: "TestChrome",
		TabsCount:   3,
	}
	wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wcancel()
	if err := wsjson.Write(wctx, clientConn, hello); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	// Read hello-ok.
	var helloResp HelloResponse
	rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer rcancel()
	if err := wsjson.Read(rctx, clientConn, &helloResp); err != nil {
		t.Fatalf("read hello-ok: %v", err)
	}
	if helloResp.Type != "hello-ok" {
		t.Fatalf("expected hello-ok, got %q", helloResp.Type)
	}
	if helloResp.BrowserID == "" {
		t.Fatal("expected a non-empty browser ID")
	}

	// Wait for hub to register the client.
	time.Sleep(200 * time.Millisecond)

	if !hub.HasConnections() {
		t.Fatal("hub should have connections after client registers")
	}
	if hub.Count() != 1 {
		t.Fatalf("expected 1 connection, got %d", hub.Count())
	}

	list := hub.List()
	if len(list) != 1 {
		t.Fatalf("List() should have 1 entry, got %d", len(list))
	}
	if list[0].Name != "TestChrome" {
		t.Errorf("expected name TestChrome, got %q", list[0].Name)
	}

	// --- Client loop: read JSON-RPC commands and respond. ---
	go func() {
		for {
			var req Request
			rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := wsjson.Read(rctx, clientConn, &req)
			rcancel()
			if err != nil {
				return
			}
			// Echo back the method name as the result.
			result, _ := json.Marshal(map[string]string{"action": req.Method, "status": "ok"})
			resp := Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  result,
			}
			wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
			wsjson.Write(wctx, clientConn, resp)
			wcancel()
		}
	}()

	// --- Server sends a command via Hub and gets the response. ---
	cmdCtx, cmdCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cmdCancel()
	resp, err := hub.SendCommand(cmdCtx, "", "browser/navigate", map[string]string{"url": "https://example.com"}, 5*time.Second)
	if err != nil {
		t.Fatalf("SendCommand: %v", err)
	}

	var result map[string]string
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v (raw: %s)", err, string(resp.Result))
	}
	if result["action"] != "browser/navigate" {
		t.Errorf("expected action=browser/navigate, got %q", result["action"])
	}
	if result["status"] != "ok" {
		t.Errorf("expected status=ok, got %q", result["status"])
	}

	// --- Test multiple concurrent commands. ---
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		method := fmt.Sprintf("browser/click_%d", i)
		go func(m string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			r, err := hub.SendCommand(ctx, "", m, nil, 5*time.Second)
			if err != nil {
				t.Errorf("concurrent SendCommand(%s): %v", m, err)
				return
			}
			if r == nil {
				t.Errorf("concurrent SendCommand(%s) returned nil", m)
			}
		}(method)
	}
	wg.Wait()

	// --- Disconnect. ---
	clientConn.Close(websocket.StatusNormalClosure, "test done")
	time.Sleep(200 * time.Millisecond) // wait for hub to process disconnect

	if hub.HasConnections() {
		t.Fatal("hub should have no connections after client disconnects")
	}
}

// TestWebSocket_SendCommand_NoConnection tests that SendCommand
// returns a clear error when no browser is connected.
func TestWebSocket_SendCommand_NoConnection(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	defer hub.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := hub.SendCommand(ctx, "", "browser/navigate", nil, 2*time.Second)
	if err == nil {
		t.Fatal("expected error when no browser is connected")
	}
}

// TestWebSocket_SendCommand_WrongID tests that SendCommand
// returns a clear error for an unknown browserID.
func TestWebSocket_SendCommand_WrongID(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	defer hub.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := hub.SendCommand(ctx, "nonexistent-browser", "browser/click", nil, 2*time.Second)
	if err == nil {
		t.Fatal("expected error for unknown browser ID")
	}
}
