package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/httpcli"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
)

// newTestLocalContext constructs a minimal localContext backed by a
// freshly-loaded config and an empty LLM client. It mirrors the
// state the real REPL has just after `cmd/pchat/main.go` finishes
// setup. The REPL's other fields (agent, tools, ...) stay nil,
// which is fine for the read-only methods we exercise here.
//
// The store is closed via t.Cleanup so the tempdir teardown can
// remove the underlying db file on Windows (where SQLite holds an
// exclusive handle).
func newTestLocalContext(t *testing.T) (cliContext, *REPL) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".p-chat"), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := `llm:
  default: openai
  providers:
    - name: openai
      protocol: openai
      base_url: http://example.com/v1
      api_key: sk-x
      model: gpt-4o
    - name: cs
      protocol: openai
      base_url: http://example.com/v1
      api_key: sk-y
      model: doubao
      models:
        - name: doubao
          default: true
        - name: doubao-pro
`
	if err := os.WriteFile(filepath.Join(dir, ".p-chat", "config.yaml"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	llmClient, err := buildTestLLMClient()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := configLoadForTest()
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, ".p-chat", "store.db")
	store, err := memory.OpenAt(dbPath, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	r := &REPL{llm: llmClient, cfg: cfg, provider: "openai", store: store, toolCache: newToolResultCache(20)}
	return r.asContext(), r
}

// openTestStore opens a fresh in-memory store for tests. The store
// uses a per-test temp file so the global HOME/.p-chat/store.db
// doesn't leak between tests.
//
// Unused in the current tests (newTestLocalContext inlines the
// open + t.Cleanup), kept here for ad-hoc debugging.
var _ = memory.OpenAt

// TestCliContext_TypePredicates verifies the type-predicate helpers
// used by handlers to fall through to REPL-only paths.
func TestCliContext_TypePredicates(t *testing.T) {
	ctx, _ := newTestLocalContext(t)
	if !isLocalContext(ctx) {
		t.Error("isLocalContext(localContext) = false, want true")
	}
	lc := asLocalContext(ctx)
	if lc.r.cfg == nil {
		t.Error("asLocalContext returned localContext with nil r.cfg")
	}

	// A nil interface should not panic on isLocalContext.
	var nilCtx cliContext
	if isLocalContext(nilCtx) {
		t.Error("isLocalContext(nil) = true, want false")
	}
}

func TestHTTPEventToChunk_PreservesRichStreamFields(t *testing.T) {
	chunk := httpEventToChunk(httpcli.StreamEvent{
		Seq:                   7,
		Thinking:              "reasoning",
		ToolID:                "call_1",
		ToolArgs:              `{"path":"a.go"}`,
		ToolResultFull:        "full result",
		ToolResultTruncated:   true,
		ToolResultFullLen:     1234,
		ToolChangedPaths:      []string{"a.go"},
		SubAgent:              true,
		SubAgentTask:          "inspect code",
		SubAgentType:          "explore",
		SubAgentRunMode:       "async",
		SubAgentFailureReason: "timeout",
		TraceID:               "T-12345678",
		ToolConfirmJSON:       `{"action":"edit"}`,
		ContentRewrite:        "replacement",
		ThinkingRewrite:       "redacted",
		SessionStatus:         "busy",
	})

	if chunk.Seq != 7 || chunk.Thinking != "reasoning" || chunk.ToolID != "call_1" {
		t.Errorf("basic stream fields were lost: %+v", chunk)
	}
	if !chunk.SubAgent || chunk.SubAgentTask != "inspect code" || chunk.SubAgentType != "explore" {
		t.Errorf("sub-agent fields were lost: %+v", chunk)
	}
	if chunk.SubAgentRunMode != "async" || !chunk.ToolResultTruncated || chunk.ToolResultFullLen != 1234 {
		t.Errorf("new rich fields were lost: %+v", chunk)
	}
	if chunk.ToolConfirmJSON == "" || chunk.ContentRewrite != "replacement" || chunk.SessionStatus != "busy" {
		t.Errorf("rich stream fields were lost: %+v", chunk)
	}
}

// TestIsUnsupported_TrueForHTTPStub verifies that errors.As recognizes the
// remaining HTTP-unsupported local-file operations.
func TestIsUnsupported_TrueForHTTPStub(t *testing.T) {
	httpCtx := &httpContext{c: nil, style: "cute", prov: "openai"}

	if _, err := httpCtx.ListSkills(); err == nil {
		t.Fatal("httpContext.ListSkills should error")
	} else if !isUnsupported(err) {
		t.Errorf("httpContext.ListSkills err = %v, want ErrUnsupported", err)
	}

	// err is nil -> isUnsupported returns false.
	if isUnsupported(nil) {
		t.Error("isUnsupported(nil) = true, want false")
	}

	// unrelated errors are not unsupported.
	if isUnsupported(errors.New("boom")) {
		t.Error("isUnsupported(generic error) = true, want false")
	}
}

// TestHTTPContext_SandboxNoOp locks in the deliberate no-op +
// visible-warning behavior of the HTTP context's sandbox methods.
// The previous implementation silently swallowed the call (just
// `func (c *httpContext) SetSandbox(bool) {}`), which was a
// silently-fail-open footgun: a CLI connected to a remote
// pchat-server would print "✓ 沙箱已禁用" and then keep enforcing
// the sandbox unchanged. These tests guard the new behavior:
//   - SetSandbox / BypassSandboxOnce must not panic and must not
//     mutate the context's "c" client (which is nil in the test).
//   - RebuildSandbox must return *ErrUnsupported so callers can
//     branch on isUnsupported() and render a localized message
//     instead of crashing.
func TestHTTPContext_SandboxNoOp(t *testing.T) {
	httpCtx := &httpContext{c: nil, style: "cute", prov: "openai"}

	// These must be safe to call repeatedly without panicking.
	// We don't assert on the warning text (color.Yellow writes
	// to the global stdout and would race with parallel tests);
	// the contract is "don't crash, don't change state".
	httpCtx.SetSandbox(true)
	httpCtx.SetSandbox(false)
	httpCtx.BypassSandboxOnce()
	httpCtx.BypassSandboxOnce()

	// RebuildSandbox returns ErrUnsupported so /unsafe off can
	// render the standard "unsupported in HTTP mode" message.
	if err := httpCtx.RebuildSandbox(); err == nil {
		t.Fatal("httpContext.RebuildSandbox should error")
	} else if !isUnsupported(err) {
		t.Errorf("httpContext.RebuildSandbox err = %v, want ErrUnsupported", err)
	}
}

func TestHTTPContext_InstructionObservationsUseCurrentSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/sessions/session-1/rules":
			_ = json.NewEncoder(w).Encode(map[string]any{"rules": []httpcli.RuleInfo{{Name: "global", Scope: "global", Path: "/global/rules/global.md"}, {Name: "project", Scope: "project", Path: "/project/.p-chat/rules/project.md"}}})
		case "/api/v1/sessions/session-1/agents":
			_ = json.NewEncoder(w).Encode(httpcli.AgentsContext{Candidates: []httpcli.AgentsCandidate{{Slot: "project_root", Label: "项目根", Path: "/project/AGENTS.md", Exists: true, Selected: true}}, Prompt: "## Agent Instructions\n\n### Project (root)\n\nproject"})
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	ctx := NewHTTPContext(httpcli.NewClient(srv.URL), "tech", "coding", "openai", "session-1")
	rules, err := ctx.ListRules()
	if err != nil || len(rules) != 2 || rules[1].Scope != "project" {
		t.Fatalf("ListRules = %+v, %v", rules, err)
	}
	agents, err := ctx.AgentsContext()
	if err != nil || len(agents.Candidates) != 1 || !agents.Candidates[0].Selected || !strings.Contains(agents.Prompt, "Project (root)") {
		t.Fatalf("AgentsContext = %+v, %v", agents, err)
	}
}

func TestHTTPContext_ToolsSettingIsSentWithMessages(t *testing.T) {
	var got httpcli.SendMessageOptions
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"done\"}\n\n"))
	}))
	defer srv.Close()

	ctx := NewHTTPContext(httpcli.NewClient(srv.URL), "tech", "coding", "openai", "conv-1")
	ctx.SetToolsEnabled(false)
	stream, err := ctx.ChatWithTools(context.Background(), agent.ChatRequest{
		Style:    style.Tech,
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Type: llm.TypeText, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	for range stream {
	}
	if ctx.ToolsEnabled() {
		t.Fatal("ToolsEnabled stayed true after disabling tools")
	}
	if got.ToolsEnabled == nil || *got.ToolsEnabled {
		t.Fatalf("tools_enabled = %v, want explicit false", got.ToolsEnabled)
	}
}

// TestLocalContext_ListProviders confirms ListProviders returns the
// same providers the on-disk config has.
func TestLocalContext_ListProviders(t *testing.T) {
	ctx, _ := newTestLocalContext(t)
	ps, err := ctx.ListProviders(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 {
		t.Fatalf("got %d providers, want 2", len(ps))
	}
	names := map[string]bool{}
	for _, p := range ps {
		names[p.Name] = true
	}
	for _, want := range []string{"openai", "cs"} {
		if !names[want] {
			t.Errorf("missing provider %q in %+v", want, ps)
		}
	}
}

// TestLocalContext_ProviderConfig returns the rich view used by
// /provider, including whether an API key is configured without exposing it.
func TestLocalContext_ProviderConfig(t *testing.T) {
	ctx, _ := newTestLocalContext(t)
	v, err := ctx.ProviderConfig("cs")
	if err != nil {
		t.Fatal(err)
	}
	if v.Protocol != "openai" {
		t.Errorf("protocol = %q, want openai", v.Protocol)
	}
	if !v.APIKeySet {
		t.Error("APIKeySet = false, want true")
	}
	if v.Model != "doubao" {
		t.Errorf("Model = %q, want doubao", v.Model)
	}
	if len(v.Models) != 2 {
		t.Errorf("Models len = %d, want 2", len(v.Models))
	}
}

// TestLocalContext_ListAllModels verifies the multi-model form is
// reported correctly (including the Default flag).
func TestLocalContext_ListAllModels(t *testing.T) {
	ctx, _ := newTestLocalContext(t)
	all := ctx.ListAllModels("")
	// openai: legacy single-model (1 entry). cs: 2 entries.
	if len(all) != 3 {
		t.Fatalf("got %d models, want 3 (1 from openai + 2 from cs): %+v", len(all), all)
	}
	var csDefault string
	for _, m := range all {
		if m.Provider == "cs" && m.Default {
			csDefault = m.Name
		}
	}
	if csDefault != "doubao" {
		t.Errorf("cs default = %q, want doubao", csDefault)
	}
}

// TestLocalContext_SetStyleAndGetStyle verifies the style round-trips
// through SetStyle + StyleName.
func TestLocalContext_SetStyleAndGetStyle(t *testing.T) {
	ctx, r := newTestLocalContext(t)
	if ctx.StyleName() != "cute" {
		// The seed test config didn't set a style; default to cute.
		r.style = style.Cute
	}
	if err := ctx.SetStyle("tech"); err != nil {
		t.Fatal(err)
	}
	if got := ctx.StyleName(); got != "tech" {
		t.Errorf("StyleName = %q, want tech", got)
	}
	if err := ctx.SetStyle("bogus"); err == nil {
		t.Error("SetStyle(bogus) should error")
	}
}

// TestLocalContext_ToolsEnabledSetGet exercises the tool toggle.
func TestLocalContext_ToolsEnabledSetGet(t *testing.T) {
	ctx, _ := newTestLocalContext(t)
	if !ctx.ToolsEnabled() {
		// Default is true in our seed; flip and flip back.
		ctx.SetToolsEnabled(true)
	}
	ctx.SetToolsEnabled(false)
	if ctx.ToolsEnabled() {
		t.Error("SetToolsEnabled(false) did not stick")
	}
	ctx.SetToolsEnabled(true)
	if !ctx.ToolsEnabled() {
		t.Error("SetToolsEnabled(true) did not stick")
	}
}

func TestHTTPContext_ProjectsScopeListsAndCreatesSessions(t *testing.T) {
	projectPath := "D:/work/my project"
	var listSeen, createSeen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/sessions":
			listSeen = true
			if got := r.URL.Query().Get("project_path"); got != projectPath {
				t.Fatalf("list project_path = %q, want %q", got, projectPath)
			}
			_, _ = w.Write([]byte(`{"sessions":[]}`))
		case "POST /api/v1/sessions":
			createSeen = true
			var opts httpcli.CreateSessionOpts
			if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
				t.Fatal(err)
			}
			if opts.ProjectPath != projectPath {
				t.Fatalf("create project_path = %q, want %q", opts.ProjectPath, projectPath)
			}
			_, _ = w.Write([]byte(`{"id":"project-session"}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	ctx := NewHTTPContext(httpcli.NewClient(srv.URL), "tech", "coding", "openai", "project-session", projectPath)
	if _, err := ctx.ListSessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.NewSession(context.Background(), httpcli.CreateSessionOpts{}); err != nil {
		t.Fatal(err)
	}
	if !listSeen || !createSeen {
		t.Fatalf("listSeen=%v createSeen=%v", listSeen, createSeen)
	}
}

func TestHTTPContext_RejectsCrossProjectSessionSwitch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/sessions/other-project" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write([]byte(`{"id":"other-project","project_path":"D:/other"}`))
	}))
	defer srv.Close()

	ctx := NewHTTPContext(httpcli.NewClient(srv.URL), "tech", "coding", "openai", "current", "D:/current")
	if err := ctx.SetCurrentSession("other-project"); err == nil {
		t.Fatal("SetCurrentSession accepted a session outside the active project")
	}
}

// TestLocalContext_SessionLifecycle walks NewSession �?ListSessions
// �?RenameSession �?SetCurrentSession �?DeleteSession.
func TestLocalContext_SessionLifecycle(t *testing.T) {
	ctx, _ := newTestLocalContext(t)
	sess, err := ctx.NewSession(nil, httpcli.CreateSessionOpts{Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID == "" {
		t.Error("NewSession returned empty ID")
	}
	all, err := ctx.ListSessions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 1 {
		t.Fatalf("ListSessions returned %d, want >= 1", len(all))
	}

	if err := ctx.RenameSession(nil, sess.ID, "renamed"); err != nil {
		t.Fatal(err)
	}
	if err := ctx.SetCurrentSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	if got := ctx.GetCurrentSessionID(); got != sess.ID {
		t.Errorf("GetCurrentSessionID = %q, want %q", got, sess.ID)
	}
	if err := ctx.DeleteSession(nil, sess.ID); err != nil {
		t.Fatal(err)
	}
}

// TestLocalContext_ExpandCache verifies the tool-result cache passes
// through ExpandList / ExpandByIndex / ExpandLast on an empty cache.
func TestLocalContext_ExpandCache_Empty(t *testing.T) {
	ctx, _ := newTestLocalContext(t)
	if got := ctx.ExpandList(); got != nil {
		t.Errorf("ExpandList on empty cache = %+v, want nil", got)
	}
	if _, ok, err := ctx.ExpandLast(); err != nil || ok {
		t.Error("ExpandLast on empty cache returned ok=true")
	}
	if _, ok, err := ctx.ExpandByIndex(1); err != nil || ok {
		t.Error("ExpandByIndex on empty cache returned ok=true")
	}
}

func TestLocalContext_ExpandCache_RecordsTerminalToolResult(t *testing.T) {
	ctx, repl := newTestLocalContext(t)
	lc := asLocalContext(ctx)
	lc.recordToolChunk("session-a", agent.ChatStreamChunk{ToolID: "call-a", ToolName: "read_file", ToolArgs: `{"path":"a.txt"}`, ToolResult: "file content", ToolElapsed: "12ms"})

	result, ok, err := lc.ExpandLast()
	if err != nil || !ok {
		t.Fatalf("ExpandLast = (%+v, %v, %v)", result, ok, err)
	}
	if result.Result != "file content" || result.Tool != "read_file" || result.Duration != 12*time.Millisecond {
		t.Fatalf("result = %+v", result)
	}
	if repl.toolCache.len() != 1 {
		t.Fatalf("cache len = %d", repl.toolCache.len())
	}
}

func TestHTTPContext_ExpandLoadsTruncatedResultOnceAndKeepsSession(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v1/sessions/session-a/messages/0/tool-result/call-a" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"tool_id":"call-a","content":"complete output","bytes":15}`))
	}))
	defer srv.Close()

	ctx := NewHTTPContext(httpcli.NewClient(srv.URL), "tech", "coding", "openai", "session-a").(*httpContext)
	ctx.recordToolChunk("session-a", agent.ChatStreamChunk{ToolID: "call-a", ToolName: "exec_command", ToolResult: "preview", ToolResultTruncated: true, ToolResultFullLen: 15})

	first, ok, err := ctx.ExpandLast()
	if err != nil || !ok || first.Result != "complete output" {
		t.Fatalf("first expand = (%+v, %v, %v)", first, ok, err)
	}
	second, ok, err := ctx.ExpandLast()
	if err != nil || !ok || second.Result != "complete output" {
		t.Fatalf("second expand = (%+v, %v, %v)", second, ok, err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1 after cache backfill", requests)
	}
}

func TestHTTPContext_ExpandUsesOwningSessionAndReportsExpiredResult(t *testing.T) {
	t.Run("owning session", func(t *testing.T) {
		var paths []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			_, _ = w.Write([]byte(`{"content":"full"}`))
		}))
		defer srv.Close()

		ctx := NewHTTPContext(httpcli.NewClient(srv.URL), "tech", "coding", "openai", "active").(*httpContext)
		ctx.recordToolChunk("session-a", agent.ChatStreamChunk{ToolID: "call-a", ToolName: "a", ToolResult: "a", ToolResultTruncated: true})
		ctx.recordToolChunk("session-b", agent.ChatStreamChunk{ToolID: "call-b", ToolName: "b", ToolResult: "b", ToolResultTruncated: true})
		if _, ok, err := ctx.ExpandByIndex(2); err != nil || !ok {
			t.Fatalf("ExpandByIndex = ok=%v err=%v", ok, err)
		}
		if len(paths) != 1 || paths[0] != "/api/v1/sessions/session-b/messages/0/tool-result/call-b" {
			t.Fatalf("paths = %v", paths)
		}
	})

	t.Run("expired", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"tool result not found or expired"}`, http.StatusNotFound)
		}))
		defer srv.Close()

		ctx := NewHTTPContext(httpcli.NewClient(srv.URL), "tech", "coding", "openai", "session-a").(*httpContext)
		ctx.recordToolChunk("session-a", agent.ChatStreamChunk{ToolID: "call-a", ToolName: "exec", ToolResult: "preview", ToolResultTruncated: true})
		_, ok, err := ctx.ExpandLast()
		if ok || err == nil || !strings.Contains(err.Error(), "可能已过期") {
			t.Fatalf("ExpandLast = ok=%v err=%v", ok, err)
		}
	})
}

func TestREPLHTTPKnowledgeCommandsUseServerWithoutWritingLocalConfig(t *testing.T) {
	var added httpcli.KnowledgeBase
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge/bases":
			if err := json.NewDecoder(r.Body).Decode(&added); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/knowledge/config":
			var patch struct {
				Enabled *bool `json:"enabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				t.Fatal(err)
			}
			if patch.Enabled == nil || !*patch.Enabled {
				t.Fatalf("knowledge patch = %+v, want enabled=true", patch)
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge/bases":
			_ = json.NewEncoder(w).Encode([]httpcli.KnowledgeBase{{Name: added.Name, Path: added.Path, Enabled: true, DocCount: 3}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/scan"):
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/scan/status"):
			_ = json.NewEncoder(w).Encode(httpcli.KnowledgeScanStatus{Message: "扫描中...", Current: 1, Total: 3, Chunks: 2})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge/search":
			_ = json.NewEncoder(w).Encode(httpcli.KnowledgeSearchResponse{Results: []httpcli.KnowledgeSearchResult{{Title: "Guide", Source: "guide.md", Content: "install"}}})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	configPath := filepath.Join(t.TempDir(), "server-overlay.json")
	httpCtx := NewHTTPContext(httpcli.NewClient(srv.URL), "tech", "coding", "openai", "session-a").(*httpContext)
	r := NewREPL(httpCtx, &config.Config{}, style.Tech, "openai")
	r.SetKBManager(NewKBManager(&config.Config{}, configPath))
	if got := r.asContext(); got != httpCtx {
		t.Fatalf("asContext() = %T, want original HTTP context", got)
	}

	dir := t.TempDir()
	if err := r.asContext().AddKB(dir); err != nil {
		t.Fatalf("AddKB: %v", err)
	}
	if added.Path != dir || added.Name != filepath.Base(dir) || !added.Enabled {
		t.Fatalf("server base = %+v", added)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("CLI wrote local overlay %q: %v", configPath, err)
	}

	if bases, err := r.asContext().ListKBs(); err != nil || len(bases) != 1 || bases[0].Files != 3 {
		t.Fatalf("ListKBs = %+v, %v", bases, err)
	}
	if started, _, err := r.asContext().ScanKBs(); err != nil || started != 1 {
		t.Fatalf("ScanKBs = (%d, %v)", started, err)
	}
	status, err := r.asContext().GetKBScanStatus(context.Background(), added.Name)
	if err != nil || status.Message != "扫描中..." || status.Current != 1 || status.Total != 3 {
		t.Fatalf("GetKBScanStatus = %+v, %v", status, err)
	}
	if err := r.asContext().Recall(context.Background(), "install", 5); err != nil {
		t.Fatalf("Recall: %v", err)
	}
}

// TestErrUnsupported_ErrorString pins down the error message format
// so handler error formatting can rely on it.
func TestErrUnsupported_ErrorString(t *testing.T) {
	e := &ErrUnsupported{Op: "AddKB"}
	want := "operation AddKB is not supported in HTTP mode"
	if e.Error() != want {
		t.Errorf("Error() = %q, want %q", e.Error(), want)
	}
}
