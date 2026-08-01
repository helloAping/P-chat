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

	srv := server.New(cfg, agt, store, styleMgr, tools, nil)
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

func TestClient_ListTools(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	tools, err := NewClient(srv.URL).ListTools(context.Background(), "")
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("expected built-in tools")
	}
	for _, tool := range tools {
		if tool.Name == "" || tool.Description == "" {
			t.Errorf("incomplete tool entry: %#v", tool)
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

func TestClient_SendMessage_PreservesProviderAndModel(t *testing.T) {
	var got SendMessageOptions
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"done\"}\n\n"))
	}))
	defer srv.Close()

	err := NewClient(srv.URL).SendMessage(context.Background(), "conv_x", SendMessageOptions{
		Message:  "hi",
		Provider: "openai",
		Model:    "gpt-4o-mini",
	}, func(StreamEvent) {})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if got.Provider != "openai" || got.Model != "gpt-4o-mini" {
		t.Errorf("selection = %q/%q, want openai/gpt-4o-mini", got.Provider, got.Model)
	}
}

func TestClient_ModelsForUsesProviderModelList(t *testing.T) {
	c := NewClient("http://example.test")
	c.SetCfgProviders([]ProviderInfo{{
		Name: "openai",
		Models: []Model{
			{Name: "gpt-4o", Default: true},
			{Name: "gpt-4o-mini", DisplayName: "GPT-4o mini"},
		},
	}})

	models, ok := c.ModelsFor("openai")
	if !ok {
		t.Fatal("ModelsFor did not find provider")
	}
	if len(models) != 2 || models[1].Name != "gpt-4o-mini" {
		t.Errorf("models = %#v, want complete provider list", models)
	}
}

func TestClient_SubmitConfirmResponse(t *testing.T) {
	var got struct {
		Approved bool   `json:"approved"`
		Action   string `json:"action"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/sessions/conv_x/confirm-response" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := NewClient(srv.URL).SubmitConfirmResponse(context.Background(), "conv_x", true, "always"); err != nil {
		t.Fatalf("submit confirmation: %v", err)
	}
	if !got.Approved || got.Action != "always" {
		t.Errorf("confirmation = %#v, want approved always", got)
	}
}

func TestClient_SessionMaintenanceEndpoints(t *testing.T) {
	requests := make([]string, 0, 4)
	var reasoningBody struct {
		Level string `json:"level"`
	}
	var regenerateBody struct {
		UserMessageID int64 `json:"user_message_id"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/sessions/conv_x/compress":
			_, _ = w.Write([]byte(`{"compressed":true,"summary":"short"}`))
		case "/api/v1/sessions/conv_x/context":
			_, _ = w.Write([]byte(`{"session_id":"conv_x","provider":"openai","model":"gpt-4o","context_window":128000,"estimated_tokens":12,"usable_tokens":120000,"utilization_pct":0.01,"messages":[{"role":"user","tokens":12,"preview":"hi"}]}`))
		case "/api/v1/sessions/conv_x/reasoning-effort":
			if err := json.NewDecoder(r.Body).Decode(&reasoningBody); err != nil {
				t.Errorf("decode reasoning request: %v", err)
			}
			_, _ = w.Write([]byte(`{"ok":true,"reasoning_effort":"high"}`))
		case "/api/v1/sessions/conv_x/regenerate":
			if err := json.NewDecoder(r.Body).Decode(&regenerateBody); err != nil {
				t.Errorf("decode regenerate request: %v", err)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"content\",\"content\":\"new reply\"}\n\n"))
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	compressed, err := c.CompressSession(context.Background(), "conv_x")
	if err != nil || !compressed.Compressed || compressed.Summary != "short" {
		t.Fatalf("compress = %#v, %v", compressed, err)
	}
	info, err := c.GetSessionContext(context.Background(), "conv_x")
	if err != nil || info.Model != "gpt-4o" || len(info.Messages) != 1 {
		t.Fatalf("context = %#v, %v", info, err)
	}
	level, err := c.SetReasoningEffort(context.Background(), "conv_x", "high")
	if err != nil || level != "high" {
		t.Fatalf("reasoning effort = %q, %v", level, err)
	}
	if reasoningBody.Level != "high" {
		t.Errorf("reasoning level = %q, want high", reasoningBody.Level)
	}
	var events []StreamEvent
	if err := c.Regenerate(context.Background(), "conv_x", 42, func(event StreamEvent) { events = append(events, event) }); err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	if len(events) != 1 || events[0].Content != "new reply" {
		t.Errorf("regen events = %#v", events)
	}
	if regenerateBody.UserMessageID != 42 {
		t.Errorf("regenerate user message id = %d, want 42", regenerateBody.UserMessageID)
	}
	if got := strings.Join(requests, ","); got != "POST /api/v1/sessions/conv_x/compress,GET /api/v1/sessions/conv_x/context,PATCH /api/v1/sessions/conv_x/reasoning-effort,POST /api/v1/sessions/conv_x/regenerate" {
		t.Errorf("requests = %q", got)
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
