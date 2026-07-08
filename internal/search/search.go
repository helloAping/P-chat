// Package search provides a pluggable web-search backend for the
// `web_search` tool. The tool asks the package-global Provider for
// results; the concrete implementation (Tavily, OpenAI-compatible,
// disabled sentinel) is chosen at server startup from the
// `config.SearchConfig` block.
//
// Why pluggable: every search API (Tavily / Brave / SerpAPI /
// jina.ai / bocha / kagi) has a different request/response shape
// and a different pricing model. Hard-coding Tavily would lock
// users out of free-tier alternatives or self-hosted proxies. The
// interface is intentionally minimal so adding a new backend is
// a single file (~150 lines).
package search

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Query is the search request the LLM can issue.
//
// All fields are optional except Query; zero-valued numeric
// fields are treated as "use provider default" by every
// implementation. RecencyDays and Language/Site are passed
// through to providers that support them; the others ignore
// unknown fields.
type Query struct {
	Query       string // required, non-empty
	MaxResults  int    // 0 = provider default; cap 10
	RecencyDays int    // 0 = any time
	Language    string // "zh" | "en" | "" = any
	Site        string // e.g. "github.com"; "" = any
}

// Result is a single search hit. The LLM sees Title + URL + Snippet;
// PublishedAt / Score are informational and may be empty.
type Result struct {
	Title       string
	URL         string
	Snippet     string
	PublishedAt string // RFC3339, may be empty
	Score       float64
}

// Provider is the contract every search backend must satisfy.
//
// Implementations MUST be safe for concurrent use (the agent runs
// multiple tool calls in parallel).
type Provider interface {
	// Name returns a short identifier ("tavily", "openai_compat",
	// "disabled", ...). The "web_search is not configured" error
	// surfaces this string so users can debug their config.
	Name() string

	// Search returns up to q.MaxResults hits. An empty slice with
	// a nil error is a valid "no results" outcome.
	Search(ctx context.Context, q Query) ([]Result, error)
}

// Errors are stable codes the LLM can branch on. The
// handleWebSearch tool maps these to user-facing E_* tags
// (E_DISABLED, E_AUTH, E_QUOTA, E_UPSTREAM, E_TIMEOUT, E_BADCFG).
var (
	// ErrDisabled means the user has not configured web_search
	// (no API key, or `web_search.enabled: false`).
	ErrDisabled = fmt.Errorf("E_DISABLED: web_search is not configured")

	// ErrAuth means the API rejected the key (HTTP 401/403).
	// The tool surfaces this as a permanent error; retrying with
	// the same key will keep failing until the user rotates it.
	ErrAuth = fmt.Errorf("E_AUTH: invalid or expired API key")

	// ErrQuota means the provider returned 429 (rate-limited)
	// or a billing-related code. The tool surfaces this so the
	// LLM falls back to local tools.
	ErrQuota = fmt.Errorf("E_QUOTA: search provider quota exhausted")

	// ErrBadConfig means the user-supplied config is invalid
	// (e.g. base_url missing for openai_compat, base_url using
	// http:// to a non-loopback host). This is a permanent
	// error — the LLM cannot recover by retrying.
	ErrBadConfig = fmt.Errorf("E_BADCFG: invalid search configuration")
)

// DisabledProvider is the sentinel returned when no key is
// configured. Its Search always returns ErrDisabled so the
// `web_search` tool can surface a helpful message to the LLM.
type DisabledProvider struct{}

// Name implements Provider.
func (DisabledProvider) Name() string { return "disabled" }

// Search implements Provider.
func (DisabledProvider) Search(_ context.Context, _ Query) ([]Result, error) {
	return nil, ErrDisabled
}

// ====================================================================
// Global provider (atomic.Pointer so we can hot-swap on UpdateConfig)
// ====================================================================

var globalProvider atomic.Pointer[Provider]

func init() {
	// Default to DisabledProvider so any caller that runs before
	// SetGlobal gets a clean "not configured" error rather than
	// a nil deref.
	d := Provider(DisabledProvider{})
	globalProvider.Store(&d)
}

// SetGlobal installs a new provider. Safe to call from any
// goroutine. The server calls this on startup and again whenever
// the user updates `web_search` via the settings panel.
func SetGlobal(p Provider) {
	if p == nil {
		d := Provider(DisabledProvider{})
		globalProvider.Store(&d)
		return
	}
	globalProvider.Store(&p)
}

// Global returns the current provider. Always non-nil; the worst
// case is DisabledProvider so the tool handler can rely on it.
func Global() Provider {
	p := globalProvider.Load()
	if p == nil {
		return DisabledProvider{}
	}
	return *p
}

// ====================================================================
// Shared tuned *http.Transport (one per process, like httpcli)
// ====================================================================

// sharedTransport is built lazily on first call. MaxIdleConns=100,
// MaxIdleConnsPerHost=20, IdleConnTimeout=90s — mirrors
// internal/httpcli so we don't burn sockets when the LLM fires
// many searches back to back. Safe for concurrent use.
var sharedTransportOnce atomic.Bool
var sharedTransportVal http.RoundTripper

func sharedTransport() http.RoundTripper {
	if !sharedTransportOnce.Load() {
		sharedTransportOnce.Store(true)
		sharedTransportVal = &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   20,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
	}
	return sharedTransportVal
}

// newClient returns a *http.Client that uses the shared transport.
// Per-request timeouts are still applied via context.
func newClient() *http.Client {
	return &http.Client{Transport: sharedTransport()}
}

// ====================================================================
// Shared helpers (used by all providers)
// ====================================================================

// maxResponseBytes caps the response body the search providers
// will buffer into memory. 4 MiB matches the SSE line cap used
// elsewhere in the codebase and is far more than any reasonable
// search-result payload (Tavily tops out around 200 KB for 10
// results with snippets).
const maxResponseBytes = 4 << 20

// truncateSnippet clips a snippet to 500 chars at a word
// boundary when possible, to keep the LLM's context tidy.
func truncateSnippet(s string) string {
	const max = 500
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if sp := strings.LastIndex(cut, " "); sp > max-80 {
		cut = cut[:sp]
	}
	return cut + "…"
}

// firstNonEmpty returns the first non-empty string among args.
// Used to fall back across alternative JSON field names
// (e.g. snippet / content / description).
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
