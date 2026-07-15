package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/p-chat/pchat/internal/httpcli"
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
	r := &REPL{llm: llmClient, cfg: cfg, provider: "openai", store: store}
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

// TestIsUnsupported_TrueForHTTPStub verifies that the HTTP context
// returns *ErrUnsupported for the operations the server doesn't
// implement, and that errors.As picks them up correctly.
func TestIsUnsupported_TrueForHTTPStub(t *testing.T) {
	httpCtx := &httpContext{c: nil, style: "cute", prov: "openai"}
	if err := httpCtx.AddProvider(ProviderConfigInput{}); err == nil {
		t.Fatal("httpContext.AddProvider should error")
	} else if !isUnsupported(err) {
		t.Errorf("httpContext.AddProvider err = %v, want ErrUnsupported", err)
	}

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
// /provider, including the APIKey (caller responsibility to mask
// it before display).
func TestLocalContext_ProviderConfig(t *testing.T) {
	ctx, _ := newTestLocalContext(t)
	v, err := ctx.ProviderConfig("cs")
	if err != nil {
		t.Fatal(err)
	}
	if v.Protocol != "openai" {
		t.Errorf("protocol = %q, want openai", v.Protocol)
	}
	if v.APIKey != "sk-y" {
		t.Errorf("APIKey = %q, want sk-y", v.APIKey)
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
	if _, ok := ctx.ExpandLast(); ok {
		t.Error("ExpandLast on empty cache returned ok=true")
	}
	if _, ok := ctx.ExpandByIndex(1); ok {
		t.Error("ExpandByIndex on empty cache returned ok=true")
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
