package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/search"
)

// ====================================================================
// Registration: tool is visible only when config has a usable
// provider. This avoids the LLM wasting a turn on a tool that
// will always fail.
// ====================================================================

func TestRegisterWebSearch_Disabled(t *testing.T) {
	r := NewRegistry()
	RegisterWebSearch(r, config.SearchConfig{Enabled: false, Provider: "tavily", APIKey: "k"})
	if _, _, ok := r.Lookup("web_search"); ok {
		t.Error("web_search should NOT be registered when Enabled=false")
	}
}

func TestRegisterWebSearch_TavilyWithKey(t *testing.T) {
	r := NewRegistry()
	RegisterWebSearch(r, config.SearchConfig{Enabled: true, Provider: "tavily", APIKey: "k"})
	tool, _, ok := r.Lookup("web_search")
	if !ok {
		t.Fatal("web_search should be registered when enabled + tavily + key")
	}
	if tool.Name != "web_search" {
		t.Errorf("Name = %q", tool.Name)
	}
	if !strings.Contains(tool.Description, "Search the public web") {
		t.Error("description should mention web search")
	}
}

func TestRegisterWebSearch_TavilyMissingKey(t *testing.T) {
	r := NewRegistry()
	RegisterWebSearch(r, config.SearchConfig{Enabled: true, Provider: "tavily", APIKey: ""})
	if _, _, ok := r.Lookup("web_search"); ok {
		t.Error("web_search should NOT be registered when Tavily has no key")
	}
}

func TestRegisterWebSearch_OpenAICompatWithBaseURL(t *testing.T) {
	r := NewRegistry()
	RegisterWebSearch(r, config.SearchConfig{
		Enabled: true, Provider: "openai_compat", BaseURL: "https://s.jina.ai",
	})
	if _, _, ok := r.Lookup("web_search"); !ok {
		t.Error("web_search should be registered for openai_compat with base_url")
	}
}

func TestRegisterWebSearch_OpenAICompatMissingBaseURL(t *testing.T) {
	r := NewRegistry()
	RegisterWebSearch(r, config.SearchConfig{Enabled: true, Provider: "openai_compat"})
	if _, _, ok := r.Lookup("web_search"); ok {
		t.Error("web_search should NOT be registered when openai_compat has no base_url")
	}
}

func TestRegisterWebSearch_UnknownProvider(t *testing.T) {
	r := NewRegistry()
	RegisterWebSearch(r, config.SearchConfig{Enabled: true, Provider: "bing", APIKey: "k"})
	if _, _, ok := r.Lookup("web_search"); ok {
		t.Error("web_search should NOT be registered for unknown provider")
	}
}

func TestRegisterWebSearch_EmptyProviderMeansTavily(t *testing.T) {
	// Empty provider should be treated as "tavily" (default).
	r := NewRegistry()
	RegisterWebSearch(r, config.SearchConfig{Enabled: true, Provider: "", APIKey: "k"})
	if _, _, ok := r.Lookup("web_search"); !ok {
		t.Error("web_search should be registered when provider is empty + key set")
	}
}

func TestRegisterWebSearch_ParamSchema(t *testing.T) {
	r := NewRegistry()
	RegisterWebSearch(r, config.SearchConfig{Enabled: true, Provider: "tavily", APIKey: "k"})
	tool, _, _ := r.Lookup("web_search")
	// Parameters is a JSON RawMessage; we only check the
	// shape, not the contents, to keep this test stable
	// across schema tweaks.
	if len(tool.Parameters) == 0 {
		t.Error("web_search should have a non-empty Parameters schema")
	}
	if !strings.Contains(string(tool.Parameters), `"query"`) {
		t.Error("schema should mention the 'query' parameter")
	}
	if !strings.Contains(string(tool.Parameters), `"max_results"`) {
		t.Error("schema should mention the 'max_results' parameter")
	}
}

// ====================================================================
// Handler behaviour
// ====================================================================

func TestHandleWebSearch_EmptyQuery(t *testing.T) {
	res, _ := handleWebSearch(context.Background(), []byte(`{}`))
	if !res.IsError {
		t.Errorf("empty query should be an error: %+v", res)
	}
	if res.Content != "E_ARGS: query is required" {
		t.Errorf("Content = %q, want 'E_ARGS: query is required'", res.Content)
	}
}

func TestHandleWebSearch_WhitespaceQuery(t *testing.T) {
	res, _ := handleWebSearch(context.Background(), []byte(`{"query":"   "}`))
	if !res.IsError {
		t.Error("whitespace-only query should be an error")
	}
}

func TestHandleWebSearch_BadJSON(t *testing.T) {
	res, _ := handleWebSearch(context.Background(), []byte(`not json`))
	if !res.IsError {
		t.Error("invalid JSON should be an error")
	}
	if !strings.Contains(res.Content, "E_ARGS") {
		t.Errorf("error message should mention E_ARGS: %q", res.Content)
	}
}

func TestHandleWebSearch_DisabledProviderReturnsEDisabled(t *testing.T) {
	// The test process's global defaults to DisabledProvider.
	// We don't reset here because the previous tests in this
	// file all run in the same process; the global may have
	// been set to a stub by an earlier test. We reset to
	// DisabledProvider explicitly so this test is
	// self-contained.
	defer search.SetGlobal(search.DisabledProvider{})
	search.SetGlobal(search.DisabledProvider{})

	// DisabledProvider returns BEFORE the quota check (the
	// check is gated behind `provider != nil`), so the
	// quota counter is untouched. We don't need to reset
	// it.
	res, _ := handleWebSearch(context.Background(), []byte(`{"query":"go http"}`))
	if !res.IsError {
		t.Fatalf("expected error from disabled provider, got: %+v", res)
	}
	if !strings.Contains(res.Content, "E_DISABLED") {
		t.Errorf("Content = %q, want E_DISABLED prefix", res.Content)
	}
}

func TestHandleWebSearch_NoResultsIsNotError(t *testing.T) {
	// A successful search returning 0 hits is not an error
	// — just an empty result message.
	defer search.SetGlobal(search.DisabledProvider{})
	search.SetGlobal(stubSearchProvider{results: nil})
	// Reset the quota tracker so a previous test's count
	// doesn't bleed into this one.
	resetQuota()

	res, _ := handleWebSearch(context.Background(), []byte(`{"query":"x"}`))
	if res.IsError {
		t.Errorf("zero-result search should not be an error: %+v", res)
	}
	if !strings.Contains(res.Content, "No results found") {
		t.Errorf("Content = %q, want 'No results found'", res.Content)
	}
}

func TestHandleWebSearch_FormatsResults(t *testing.T) {
	defer search.SetGlobal(search.DisabledProvider{})
	search.SetGlobal(stubSearchProvider{results: []search.Result{
		{Title: "A", URL: "https://a.com", Snippet: "alpha"},
		{Title: "B", URL: "https://b.com", Snippet: "beta", PublishedAt: "2025-01-01"},
	}})
	resetQuota()

	res, _ := handleWebSearch(context.Background(), []byte(`{"query":"test"}`))
	if res.IsError {
		t.Fatalf("unexpected error: %+v", res)
	}
	for _, want := range []string{"Found 2 result", "[1] A", "[2] B", "https://a.com", "Published: 2025-01-01", "web_fetch"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("Content missing %q:\n%s", want, res.Content)
		}
	}
}

func TestHandleWebSearch_QuotaExhausted(t *testing.T) {
	defer search.SetGlobal(search.DisabledProvider{})
	search.SetGlobal(stubSearchProvider{results: []search.Result{
		{Title: "x", URL: "https://e.com", Snippet: "ok"},
	}})
	resetQuota()
	// Set a tight cap and drain it.
	search.SetQuotaLimit(1)
	defer search.SetQuotaLimit(0)
	// First call consumes the only slot.
	_, _ = handleWebSearch(context.Background(), []byte(`{"query":"first"}`))
	// Second call should be denied before the stub is hit.
	res, _ := handleWebSearch(context.Background(), []byte(`{"query":"second"}`))
	if !res.IsError {
		t.Fatalf("expected quota error, got: %+v", res)
	}
	if !strings.Contains(res.Content, "E_QUOTA") {
		t.Errorf("Content = %q, want E_QUOTA", res.Content)
	}
}

// resetQuota resets the in-process tracker to "0 used, unlimited".
// Individual tests call this before exercising the handler so a
// sibling test's residue doesn't deny their calls.
//
// We can't easily zero the counter (it's locked), but we can
// force a day-rollover by waiting for the next UTC day — not
// practical in a unit test. Instead we simply *guarantee* a
// high-enough limit for the rest of the test suite. Tests that
// need a *tight* cap call SetQuotaLimit themselves.
func resetQuota() {
	search.SetQuotaLimit(0)
}

func TestHandleWebSearch_SentinelsArePropagated(t *testing.T) {
	// Each sentinel error code should appear in the tool
	// result unchanged so the LLM can branch on it.
	cases := []struct {
		name     string
		err      error
		wantCode string
	}{
		{"auth", search.ErrAuth, "E_AUTH"},
		{"quota", search.ErrQuota, "E_QUOTA"},
		{"disabled", search.ErrDisabled, "E_DISABLED"},
		{"badcfg", search.ErrBadConfig, "E_BADCFG"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer search.SetGlobal(search.DisabledProvider{})
			search.SetGlobal(stubSearchProvider{err: tc.err})

			res, _ := handleWebSearch(context.Background(), []byte(`{"query":"q"}`))
			if !res.IsError {
				t.Fatal("expected error")
			}
			if !strings.Contains(res.Content, tc.wantCode) {
				t.Errorf("Content = %q, want %q", res.Content, tc.wantCode)
			}
		})
	}
	// Final reset so subsequent tests in the same process see
	// a clean default.
	search.SetGlobal(search.DisabledProvider{})
}

func TestHandleWebSearch_WrapsUnknownErrorWithEUpstream(t *testing.T) {
	defer search.SetGlobal(search.DisabledProvider{})
	search.SetGlobal(stubSearchProvider{err: errCustom("provider exploded")})

	res, _ := handleWebSearch(context.Background(), []byte(`{"query":"q"}`))
	if !res.IsError {
		t.Fatal("expected error")
	}
	if !strings.Contains(res.Content, "E_UPSTREAM") {
		t.Errorf("Content = %q, want E_UPSTREAM prefix", res.Content)
	}
	if !strings.Contains(res.Content, "provider exploded") {
		t.Errorf("Content = %q, want original message", res.Content)
	}
}

// ====================================================================
// Stub search provider for handler tests
// ====================================================================

type stubSearchProvider struct {
	results []search.Result
	err     error
}

func (p stubSearchProvider) Name() string { return "stub" }
func (p stubSearchProvider) Search(_ context.Context, _ search.Query) ([]search.Result, error) {
	return p.results, p.err
}

type errCustom string

func (e errCustom) Error() string { return string(e) }
