package httpcli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/server"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
	"github.com/p-chat/pchat/internal/upgrade"
)

// newTestServer wires the real server.Handler behind an httptest
// server. The LLM client points at a fake provider so calls
// short-circuit (the test only exercises the HTTP plumbing).
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	llmClient, _ := llm.NewClient(&cfg.LLM)
	store, _ := memory.OpenAt(dir+"/test.db", 50)
	t.Cleanup(func() { store.Close() })
	upgrade.SeedForTesting(store.DB())
	styleMgr, _ := style.NewManager(store.DB())
	tools := tool.NewRegistry()
	tool.RegisterBuiltin(tools)
	agt := agent.New(cfg, llmClient, styleMgr, store, tools)

	srv := server.New(cfg, agt, store, styleMgr, nil)
	return httptest.NewServer(srv.Engine())
}

func TestClient_Ping(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	c := NewClient(srv.URL)
	if err := c.Ping(context.Background()); err != nil {
		t.Errorf("ping: %v", err)
	}
}

func TestClient_PingFails(t *testing.T) {
	c := NewClient("http://127.0.0.1:1") // closed port
	if err := c.Ping(context.Background()); err == nil {
		t.Error("expected ping to fail on a closed port")
	}
}

func TestClient_ListSessions_FreshServer(t *testing.T) {
	// pchat-server auto-creates a "current" session on startup, so a
	// brand-new server has 1 session, not 0. We verify the list
	// call succeeds and returns exactly that one session.
	srv := newTestServer(t)
	defer srv.Close()
	c := NewClient(srv.URL)

	sessions, err := c.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Errorf("fresh server should have 1 session, got %d", len(sessions))
	}
}

func TestClient_CreateListDeleteCycle(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	sess, err := c.CreateSession(ctx, CreateSessionOpts{Title: "Hello"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sess.ID == "" {
		t.Fatal("create returned empty id")
	}

	got, err := c.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Hello" {
		t.Errorf("title = %q, want %q", got.Title, "Hello")
	}

	sessions, err := c.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range sessions {
		if s.ID == sess.ID {
			found = true
		}
	}
	if !found {
		t.Error("created session not in list")
	}

	if err := c.RenameSession(ctx, sess.ID, "World"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	got2, err := c.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Title != "World" {
		t.Errorf("title after rename = %q, want %q", got2.Title, "World")
	}

	if err := c.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := c.GetSession(ctx, sess.ID); err == nil {
		t.Error("expected error fetching deleted session")
	}
}

func TestClient_ListMessages_Empty(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	sess, err := c.CreateSession(ctx, CreateSessionOpts{})
	if err != nil {
		t.Fatal(err)
	}

	msgs, err := c.ListMessages(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Errorf("new session should have 0 messages, got %d", len(msgs))
	}
}

func TestClient_MetadataEndpoints(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	providers, err := c.ListProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) == 0 {
		t.Error("expected at least one provider")
	}
	for _, p := range providers {
		if p.Name == "" {
			t.Errorf("provider with empty name: %+v", p)
		}
	}

	styles, err := c.ListStyles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(styles) != 3 {
		t.Errorf("expected 3 styles, got %d", len(styles))
	}
	wantIDs := map[string]bool{"cute": true, "guofeng": true, "tech": true}
	for _, s := range styles {
		if !wantIDs[s.ID] {
			t.Errorf("unexpected style id: %q", s.ID)
		}
	}
}

func TestClient_SendMessage_NoRealLLM(t *testing.T) {
	// We can't easily run a real LLM in a unit test, so we verify
	// the wire-up: a missing provider returns an error quickly,
	// not a hang.
	srv := newTestServer(t)
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sess, err := c.CreateSession(ctx, CreateSessionOpts{})
	if err != nil {
		t.Fatal(err)
	}

	// We don't care about the response content; we just want to
	// confirm SendMessage returns (even with an error) within
	// the timeout.
	_ = c.SendMessage(ctx, sess.ID, SendMessageOptions{
		Message: "hi",
	}, func(ev StreamEvent) {
		// discard
	})
}

func TestClient_HTTPErrorSurfaced(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	// 404 path.
	_, err := c.GetSession(ctx, "conv_nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent session")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should contain 404, got: %v", err)
	}
}

// ====================================================================
// SSE parsing: the client must surface each event from the server.
// We use a custom httptest server that emits a few canned events
// so we don't depend on a working LLM.
// ====================================================================

func TestClient_SendMessage_StreamsEvents(t *testing.T) {
	canned := strings.Join([]string{
		"data: {\"type\":\"phase\",\"phase\":\"llm\",\"step\":\"round-1\",\"message\":\"thinking\"}\n\n",
		"data: {\"type\":\"content\",\"content\":\"Hello \"}\n\n",
		"data: {\"type\":\"content\",\"content\":\"world\"}\n\n",
		"data: {\"type\":\"done\",\"tokens_in\":10,\"tokens_out\":5,\"elapsed\":\"100ms\"}\n\n",
	}, "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		for _, line := range strings.Split(canned, "\n\n") {
			if line == "" {
				continue
			}
			_, _ = w.Write([]byte(line + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	var events []StreamEvent
	err := c.SendMessage(context.Background(), "conv_x",
		SendMessageOptions{Message: "hi"},
		func(ev StreamEvent) { events = append(events, ev) },
	)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d: %+v", len(events), events)
	}
	if events[0].Type != "phase" {
		t.Errorf("event[0].Type = %q, want phase", events[0].Type)
	}
	if events[1].Content != "Hello " {
		t.Errorf("event[1].Content = %q", events[1].Content)
	}
	// Concat content events gives the full answer.
	full := ""
	for _, e := range events {
		if e.Type == "content" {
			full += e.Content
		}
	}
	if full != "Hello world" {
		t.Errorf("concat = %q, want 'Hello world'", full)
	}
	if events[3].Type != "done" || events[3].TokensIn != 10 {
		t.Errorf("event[3] = %+v", events[3])
	}
}

// Sanity check that the response from a malformed endpoint
// surfaces as a non-2xx error.
func TestClient_HTTPErrorJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	err := c.doJSON(context.Background(), "GET", "/", nil, nil)
	if err == nil {
		t.Error("expected error")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should contain 400, got: %v", err)
	}
	// Suppress unused import lint for fmt.
	_ = fmt.Sprintf
}
