package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
)

func TestDisabledProvider(t *testing.T) {
	d := DisabledProvider{}
	if d.Name() != "disabled" {
		t.Errorf("Name() = %q, want %q", d.Name(), "disabled")
	}
	_, err := d.Search(context.Background(), Query{Query: "x"})
	if err != ErrDisabled {
		t.Errorf("err = %v, want ErrDisabled", err)
	}
}

func TestGlobal_DefaultIsDisabled(t *testing.T) {
	// Reset to a known state. Other tests may have swapped
	// the global; pin it back to Disabled for this assertion.
	SetGlobal(DisabledProvider{})
	if Global().Name() != "disabled" {
		t.Errorf("Global().Name() = %q, want %q", Global().Name(), "disabled")
	}
}

func TestSetGlobal_NilBecomesDisabled(t *testing.T) {
	// Pin a non-nil sentinel first so we can prove the
	// nil-reset path actually swaps.
	SetGlobal(&TavilyProvider{APIKey: "test"})
	SetGlobal(nil)
	if Global().Name() != "disabled" {
		t.Errorf("SetGlobal(nil) should reset to disabled, got %q", Global().Name())
	}
}

func TestBuildProvider_DisabledWhenFlagOff(t *testing.T) {
	p := BuildProvider(config.SearchConfig{Enabled: false, Provider: "tavily", APIKey: "x"})
	if p.Name() != "disabled" {
		t.Errorf("Enabled=false should yield disabled, got %q", p.Name())
	}
}

func TestBuildProvider_Tavily(t *testing.T) {
	p := BuildProvider(config.SearchConfig{Enabled: true, Provider: "tavily", APIKey: "k"})
	if _, ok := p.(*TavilyProvider); !ok {
		t.Errorf("provider = %T, want *TavilyProvider", p)
	}
}

func TestBuildProvider_TavilyEmptyKey(t *testing.T) {
	p := BuildProvider(config.SearchConfig{Enabled: true, Provider: "tavily", APIKey: ""})
	if p.Name() != "disabled" {
		t.Errorf("empty key should yield disabled, got %q", p.Name())
	}
}

func TestBuildProvider_OpenAICompat(t *testing.T) {
	p := BuildProvider(config.SearchConfig{
		Enabled: true, Provider: "openai_compat", BaseURL: "https://s.jina.ai",
	})
	if _, ok := p.(*OpenAICompatProvider); !ok {
		t.Errorf("provider = %T, want *OpenAICompatProvider", p)
	}
}

func TestBuildProvider_OpenAICompatEmptyBaseURL(t *testing.T) {
	p := BuildProvider(config.SearchConfig{Enabled: true, Provider: "openai_compat"})
	if p.Name() != "disabled" {
		t.Errorf("empty base_url should yield disabled, got %q", p.Name())
	}
}

func TestBuildProvider_UnknownProvider(t *testing.T) {
	p := BuildProvider(config.SearchConfig{Enabled: true, Provider: "bing", APIKey: "x"})
	if p.Name() != "disabled" {
		t.Errorf("unknown provider should yield disabled, got %q", p.Name())
	}
}

func TestBuildProvider_EmptyProviderMeansTavily(t *testing.T) {
	p := BuildProvider(config.SearchConfig{Enabled: true, Provider: "", APIKey: "k"})
	if _, ok := p.(*TavilyProvider); !ok {
		t.Errorf("empty provider should default to Tavily, got %T", p)
	}
}

func TestValidateBaseURL(t *testing.T) {
	tests := []struct {
		url   string
		valid bool
	}{
		{"https://api.tavily.com", true},
		{"https://s.jina.ai", true},
		{"http://127.0.0.1:8080", true},      // loopback http OK
		{"http://localhost:9000/search", true}, // loopback http OK
		{"http://api.example.com", false},      // non-loopback http
		{"ftp://example.com", false},           // wrong scheme
		{"://no-scheme", false},
		{"javascript:alert(1)", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			err := validateBaseURL(tc.url)
			gotValid := err == nil
			if gotValid != tc.valid {
				t.Errorf("validateBaseURL(%q) valid=%v, want %v (err=%v)", tc.url, gotValid, tc.valid, err)
			}
		})
	}
}

func TestTruncateSnippet_Short(t *testing.T) {
	in := "hello world"
	if got := truncateSnippet(in); got != in {
		t.Errorf("short input should be unchanged, got %q", got)
	}
}

func TestTruncateSnippet_Long(t *testing.T) {
	in := strings.Repeat("a ", 400) // ~800 chars
	out := truncateSnippet(in)
	if len(out) > 510 { // max + 1 char suffix
		t.Errorf("truncated length %d too long", len(out))
	}
	if !strings.HasSuffix(out, "…") {
		t.Error("truncated snippet should end with ellipsis")
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if firstNonEmpty("", "b", "c") != "b" {
		t.Error("firstNonEmpty should pick first non-empty")
	}
	if firstNonEmpty("a", "b") != "a" {
		t.Error("firstNonEmpty should keep 'a' when present")
	}
	if firstNonEmpty("", "") != "" {
		t.Error("firstNonEmpty of all-empty should be empty")
	}
	if firstNonEmpty() != "" {
		t.Error("firstNonEmpty() with no args should be empty")
	}
}

// ====================================================================
// Tavily provider tests (httptest mock of the Tavily HTTP API)
// ====================================================================

func TestTavilyProvider_SearchOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request body is what Tavily expects.
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if body["api_key"] != "test-key" {
			t.Errorf("api_key = %v, want test-key", body["api_key"])
		}
		if body["query"] != "go http client" {
			t.Errorf("query = %v, want 'go http client'", body["query"])
		}
		if body["include_raw_content"] != false {
			t.Errorf("include_raw_content should be false, got %v", body["include_raw_content"])
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"title": "net/http - GoDoc", "url": "https://pkg.go.dev/net/http",
					"content": "Package http provides HTTP client and server implementations.",
					"score": 0.95, "published_date": "2024-01-15"},
				{"title": "Go by Example: HTTP Client", "url": "https://gobyexample.com/http-client",
					"content": "The Go standard library provides excellent HTTP support.",
					"score": 0.87},
			},
		})
	}))
	defer srv.Close()

	p := &TavilyProvider{APIKey: "test-key", BaseURL: srv.URL, Timeout: 5 * time.Second}
	results, err := p.Search(context.Background(), Query{Query: "go http client", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Title != "net/http - GoDoc" {
		t.Errorf("results[0].Title = %q", results[0].Title)
	}
	if results[0].Score != 0.95 {
		t.Errorf("results[0].Score = %f, want 0.95", results[0].Score)
	}
	if results[0].PublishedAt != "2024-01-15" {
		t.Errorf("results[0].PublishedAt = %q", results[0].PublishedAt)
	}
}

func TestTavilyProvider_401ReturnsErrAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"detail":"unauthorized"}`))
	}))
	defer srv.Close()

	p := &TavilyProvider{APIKey: "bad", BaseURL: srv.URL, Timeout: 2 * time.Second}
	_, err := p.Search(context.Background(), Query{Query: "x"})
	if err != ErrAuth {
		t.Errorf("err = %v, want ErrAuth", err)
	}
}

func TestTavilyProvider_429ReturnsErrQuota(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"detail":"rate limited"}`))
	}))
	defer srv.Close()

	p := &TavilyProvider{APIKey: "k", BaseURL: srv.URL, Timeout: 2 * time.Second}
	_, err := p.Search(context.Background(), Query{Query: "x"})
	if err != ErrQuota {
		t.Errorf("err = %v, want ErrQuota", err)
	}
}

func TestTavilyProvider_5xxReturnsUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`maintenance`))
	}))
	defer srv.Close()

	p := &TavilyProvider{APIKey: "k", BaseURL: srv.URL, Timeout: 2 * time.Second}
	_, err := p.Search(context.Background(), Query{Query: "x"})
	if err == nil {
		t.Fatal("expected error for 503")
	}
	if !strings.HasPrefix(err.Error(), "E_UPSTREAM:") {
		t.Errorf("err = %v, want E_UPSTREAM prefix", err)
	}
}

func TestTavilyProvider_EmptyKey(t *testing.T) {
	p := &TavilyProvider{APIKey: ""}
	_, err := p.Search(context.Background(), Query{Query: "x"})
	if err != ErrBadConfig {
		t.Errorf("err = %v, want ErrBadConfig", err)
	}
}

func TestTavilyProvider_DefaultTimeout(t *testing.T) {
	p := &TavilyProvider{APIKey: "k", Timeout: 0}
	if p.Timeout != 0 {
		t.Fatal("Timeout field should be 0; default is applied at call time")
	}
}

func TestTavilyProvider_PicksDefaultBaseURL(t *testing.T) {
	p := &TavilyProvider{APIKey: "k"}
	// Can't actually call the real Tavily in a unit test, but
	// we can verify the field default is what we expect and
	// that the constant matches the documented endpoint.
	if tavilyDefaultBaseURL != "https://api.tavily.com" {
		t.Errorf("tavilyDefaultBaseURL = %q, want https://api.tavily.com", tavilyDefaultBaseURL)
	}
	if p.BaseURL != "" {
		t.Errorf("BaseURL field should default to empty (caller uses constant)")
	}
}

func TestTavilyProvider_FiltersEmptyURLs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"results": [
				{"title": "valid", "url": "https://example.com", "content": "ok"},
				{"title": "no-url", "url": "", "content": "should be filtered"},
				{"title": "real", "url": "https://example.org", "content": "ok too"}
			]
		}`))
	}))
	defer srv.Close()

	p := &TavilyProvider{APIKey: "k", BaseURL: srv.URL, Timeout: 2 * time.Second}
	res, err := p.Search(context.Background(), Query{Query: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Errorf("got %d results, want 2 (empty URL filtered)", len(res))
	}
}

func TestTavilyProvider_PicksTopic(t *testing.T) {
	tests := map[string]string{
		"":        "general",
		"news":    "news",
		"finance": "finance",
		"NEWS":    "news",  // case-insensitive
		"  news ": "news",  // trimmed
		"bogus":   "general", // unknown → general
	}
	for in, want := range tests {
		if got := pickTavilyTopic(in); got != want {
			t.Errorf("pickTavilyTopic(%q) = %q, want %q", in, got, want)
		}
	}
}

// ====================================================================
// OpenAI-compat provider tests
// ====================================================================

func TestOpenAICompat_ResultsShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Bearer header is sent when API key is set.
		if got := r.Header.Get("Authorization"); got != "Bearer k1" {
			t.Errorf("Authorization = %q, want 'Bearer k1'", got)
		}
		// Verify request body shape.
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["query"] != "rust async" {
			t.Errorf("query = %v", body["query"])
		}
		if body["max_results"] != float64(3) {
			t.Errorf("max_results = %v, want 3", body["max_results"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"results": [
				{"title": "Tokio", "url": "https://tokio.rs", "snippet": "An async runtime for Rust", "score": 0.9}
			]
		}`))
	}))
	defer srv.Close()

	p := &OpenAICompatProvider{APIKey: "k1", BaseURL: srv.URL, Timeout: 2 * time.Second}
	res, err := p.Search(context.Background(), Query{Query: "rust async", MaxResults: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].URL != "https://tokio.rs" {
		t.Errorf("res = %+v", res)
	}
}

func TestOpenAICompat_DataShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{"title": "via data field", "url": "https://example.com", "snippet": "ok"}
			]
		}`))
	}))
	defer srv.Close()
	p := &OpenAICompatProvider{BaseURL: srv.URL, Timeout: 2 * time.Second}
	res, err := p.Search(context.Background(), Query{Query: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Title != "via data field" {
		t.Errorf("res = %+v", res)
	}
}

func TestOpenAICompat_FallbackFields(t *testing.T) {
	// Server returns content/description instead of snippet, and
	// date instead of published_at. We should fall back to
	// whichever is present.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"results": [
				{"title": "x", "url": "https://e.com", "content": "fallback content", "date": "2025-01-01"}
			]
		}`))
	}))
	defer srv.Close()
	p := &OpenAICompatProvider{BaseURL: srv.URL, Timeout: 2 * time.Second}
	res, err := p.Search(context.Background(), Query{Query: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Snippet != "fallback content" {
		t.Errorf("Snippet = %q, want fallback content", res[0].Snippet)
	}
	if res[0].PublishedAt != "2025-01-01" {
		t.Errorf("PublishedAt = %q, want 2025-01-01", res[0].PublishedAt)
	}
}

func TestOpenAICompat_EmptyBaseURL(t *testing.T) {
	p := &OpenAICompatProvider{}
	_, err := p.Search(context.Background(), Query{Query: "x"})
	if err != ErrBadConfig {
		t.Errorf("err = %v, want ErrBadConfig", err)
	}
}

func TestOpenAICompat_InvalidBaseURL(t *testing.T) {
	p := &OpenAICompatProvider{BaseURL: "ftp://example.com"}
	_, err := p.Search(context.Background(), Query{Query: "x"})
	if !errors.Is(err, ErrBadConfig) {
		t.Errorf("err = %v, want ErrBadConfig", err)
	}
}

func TestOpenAICompat_NonLoopbackHTTP(t *testing.T) {
	// Even if the server is unreachable, validation should
	// happen before the network call.
	p := &OpenAICompatProvider{BaseURL: "http://api.example.com"}
	_, err := p.Search(context.Background(), Query{Query: "x"})
	if !errors.Is(err, ErrBadConfig) {
		t.Errorf("err = %v, want ErrBadConfig", err)
	}
}

func TestOpenAICompat_CustomPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// p.Path overrides "/search"
		if !strings.HasSuffix(r.URL.Path, "/v1/web") {
			t.Errorf("path = %q, want suffix /v1/web", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results": [{"title": "x", "url": "https://e.com", "snippet": "s"}]}`))
	}))
	defer srv.Close()
	p := &OpenAICompatProvider{BaseURL: srv.URL + "/v1", Path: "/web", Timeout: 2 * time.Second}
	res, err := p.Search(context.Background(), Query{Query: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Errorf("got %d, want 1", len(res))
	}
}

func TestOpenAICompat_AuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	p := &OpenAICompatProvider{BaseURL: srv.URL, Timeout: 2 * time.Second}
	_, err := p.Search(context.Background(), Query{Query: "x"})
	if err != ErrAuth {
		t.Errorf("err = %v, want ErrAuth", err)
	}
}

func TestOpenAICompat_QuotaError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
	}))
	defer srv.Close()
	p := &OpenAICompatProvider{BaseURL: srv.URL, Timeout: 2 * time.Second}
	_, err := p.Search(context.Background(), Query{Query: "x"})
	if err != ErrQuota {
		t.Errorf("err = %v, want ErrQuota", err)
	}
}

// ====================================================================
// PickTimeout helper
// ====================================================================

func TestPickTimeout(t *testing.T) {
	if got := pickTimeout(0); got != tavilyDefaultTimeout {
		t.Errorf("pickTimeout(0) = %v, want %v", got, tavilyDefaultTimeout)
	}
	if got := pickTimeout(-1 * time.Second); got != tavilyDefaultTimeout {
		t.Errorf("pickTimeout(neg) = %v, want default", got)
	}
	if got := pickTimeout(120 * time.Second); got != 60*time.Second {
		t.Errorf("pickTimeout(120s) = %v, want 60s cap", got)
	}
	if got := pickTimeout(5 * time.Second); got != 5*time.Second {
		t.Errorf("pickTimeout(5s) = %v, want 5s", got)
	}
}
