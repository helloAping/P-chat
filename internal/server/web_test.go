package server_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/server"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
)

// newTestServer is duplicated in httpcli tests; we duplicate to
// avoid a test-only export cycle.
func newWebServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	llmClient, _ := llm.NewClient(&cfg.LLM)
	styleMgr, _ := style.NewManager(config.PromptDir())
	store, _ := memory.OpenAt(dir+"/test.db", 50)
	t.Cleanup(func() { store.Close() })
	tools := tool.NewRegistry()
	tool.RegisterBuiltin(tools)
	agt := agent.New(cfg, llmClient, styleMgr, store, tools)

	// Use an absolute path so the test is cwd-independent.
	absWeb, _ := filepath.Abs("../../web")
	return httptest.NewServer(server.NewWithStaticDir(cfg, agt, store, styleMgr, absWeb).Engine())
}

func TestWebGUI_IndexPage(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	// Request the index directly (gin's StaticFS doesn't auto-serve
	// directory indexes for /app/; users have to type the file).
	resp, err := http.Get(srv.URL + "/app/index.html")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	got := string(body)

	// The Vue 3 SPA shell is tiny — everything else lives in the
	// bundled JS/CSS. We just sanity-check the mount point, title,
	// and that the JS/CSS assets are wired up.
	wants := []string{
		"<title>P-Chat</title>",  // title tag
		`<div id="app">`,        // Vue mount point
		`/app/assets/`,           // asset base path
		`.js"`,                   // at least one JS bundle linked
		`.css"`,                  // at least one CSS bundle linked
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("page missing %q", w)
		}
	}
}

func TestWebGUI_APIStillWorks(t *testing.T) {
	// The web GUI and the CLI both hit the same API. Verify the
	// HTTP server still answers /api/v1/* alongside /app/*.
	srv := newWebServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("health = %d, want 200", resp.StatusCode)
	}

	// And the session API works.
	resp2, err := http.Get(srv.URL + "/api/v1/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("sessions = %d, want 200", resp2.StatusCode)
	}
}
