//go:build live

package browser

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

func TestLiveServerEndpoint(t *testing.T) {
	port := os.Getenv("PCHAT_TEST_PORT")
	if port == "" {
		port = "14712"
	}
	url := fmt.Sprintf("ws://127.0.0.1:%s/api/v1/browser/ws", port)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	t.Logf("Connecting to %s", url)
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("Dial error: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	t.Log("Connected! Sending hello...")

	hello := HelloParams{
		BrowserName: "LiveTestClient",
		TabsCount:   1,
		ID:          "",
	}
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	defer wcancel()
	if err := wsjson.Write(wctx, conn, hello); err != nil {
		t.Fatalf("Write hello error: %v", err)
	}
	t.Log("Hello sent, waiting for hello-ok...")

	var resp HelloResponse
	rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
	defer rcancel()
	if err := wsjson.Read(rctx, conn, &resp); err != nil {
		t.Fatalf("Read hello-ok error: %v", err)
	}
	if resp.Type != "hello-ok" {
		t.Fatalf("Expected hello-ok, got: %+v", resp)
	}
	fmt.Printf("SUCCESS! browser_id=%s\n", resp.BrowserID)

	// Send a JSON-RPC command and see if server routes it back
	// (won't work since we're the only client and the server
	// would try to route to us via a different client)
	t.Logf("Connection established, browser_id=%s", resp.BrowserID)

	// Wait a moment for hub to register
	time.Sleep(500 * time.Millisecond)
}
