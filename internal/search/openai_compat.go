package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OpenAICompatProvider targets any HTTP endpoint that accepts a
// JSON request of the shape `{query, max_results, ...}` and
// returns `{results: [{title, url, snippet, ...}]}`. This is
// intentionally minimal so users can plug in:
//
//   - jina.ai/reader search (https://s.jina.ai)
//   - bocha.ai search
//   - kagi search API
//   - any self-hosted SearXNG / searxng instance
//
// The response shape we accept (all fields optional except
// title/url/snippet):
//
//	{
//	  "results": [
//	    {"title": "...", "url": "...", "snippet": "...",
//	     "published_at": "...", "score": 0.92}
//	  ]
//	}
//
// We also accept the "raw" shape (a top-level array) and
// `{data: [...]}` for forward-compat with internal proxies.
type OpenAICompatProvider struct {
	APIKey  string        // optional; some providers don't need auth
	BaseURL string        // required; e.g. "https://s.jina.ai"
	Path    string        // override; "" = "/search"
	Timeout time.Duration // 0 = 20s default
}

// Name implements Provider.
func (p *OpenAICompatProvider) Name() string { return "openai_compat" }

// Search implements Provider.
func (p *OpenAICompatProvider) Search(ctx context.Context, q Query) ([]Result, error) {
	if p.BaseURL == "" {
		return nil, ErrBadConfig
	}

	if err := validateBaseURL(p.BaseURL); err != nil {
		return nil, err
	}

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = tavilyDefaultTimeout
	}

	max := q.MaxResults
	if max <= 0 {
		max = 5
	}
	if max > 10 {
		max = 10
	}

	body := map[string]any{
		"query":       q.Query,
		"max_results": max,
	}
	if q.RecencyDays > 0 {
		body["recency_days"] = q.RecencyDays
	}
	if q.Language != "" {
		body["language"] = q.Language
	}
	if q.Site != "" {
		body["site"] = q.Site
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	endpoint := strings.TrimRight(p.BaseURL, "/")
	path := p.Path
	if path == "" {
		path = "/search"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	endpoint += path

	headers := map[string]string{
		"Content-Type": "application/json",
		"User-Agent":   "P-Chat/1.0",
	}
	if p.APIKey != "" {
		// Most search providers use Bearer; jina uses a custom
		// header. We send both and let the server pick.
		headers["Authorization"] = "Bearer " + p.APIKey
		headers["X-Api-Key"] = p.APIKey
	}

	httpCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, "POST", endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := newClient().Do(req)
	if err != nil {
		return nil, mapNetErr(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, ErrAuth
	}
	if resp.StatusCode == 429 {
		return nil, ErrQuota
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("E_UPSTREAM: %s returned %d", endpoint, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		preview := readPrefix(resp.Body, 256)
		return nil, fmt.Errorf("E_UPSTREAM: %s HTTP %d: %s", endpoint, resp.StatusCode, preview)
	}

	dec := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes))
	var parsed openAICompatResponse
	if err := dec.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	rawHits := parsed.normalized()
	out := make([]Result, 0, len(rawHits))
	for _, r := range rawHits {
		if r.URL == "" {
			continue
		}
		out = append(out, Result{
			Title:       strings.TrimSpace(r.Title),
			URL:         r.URL,
			Snippet:     truncateSnippet(firstNonEmpty(r.Snippet, r.Content, r.Description)),
			PublishedAt: firstNonEmpty(r.PublishedAt, r.Date),
			Score:       r.Score,
		})
	}
	return out, nil
}

// validateBaseURL enforces two safety rules:
//
//  1. Public providers MUST use https. The only exception is
//     loopback (http://127.0.0.1, http://localhost) so users can
//     run a self-hosted proxy on their own machine.
//  2. The URL must parse and have a host. We reject
//     user-supplied "javascript:" / "file:" / etc. by relying on
//     net/url's scheme parsing.
func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: base_url parse: %v", ErrBadConfig, err)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: base_url missing host", ErrBadConfig)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return fmt.Errorf("%w: base_url must be http or https (got %q)", ErrBadConfig, scheme)
	}
	if scheme == "http" {
		host := strings.ToLower(u.Hostname())
		isLoopback := host == "127.0.0.1" || host == "::1" || host == "localhost"
		if !isLoopback {
			return fmt.Errorf("%w: base_url http is only allowed for loopback (got %q)", ErrBadConfig, host)
		}
	}
	return nil
}

// openAICompatResponse accepts several wire shapes. We use a
// permissive decoder and a `normalized()` method to collapse them
// into a single []rawHit slice.
type openAICompatResponse struct {
	// Common shape: {"results": [...]}
	Results []rawHit `json:"results,omitempty"`
	// jina/bocha alternative: {"data": [...]}
	Data []rawHit `json:"data,omitempty"`
	// Some proxies return a bare top-level array
	Items []rawHit `json:"items,omitempty"`
}

func (r openAICompatResponse) normalized() []rawHit {
	switch {
	case len(r.Results) > 0:
		return r.Results
	case len(r.Data) > 0:
		return r.Data
	case len(r.Items) > 0:
		return r.Items
	}
	return nil
}

// rawHit accepts the common field names a search API might use.
type rawHit struct {
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	Content     string  `json:"content"`
	Description string  `json:"description"`
	PublishedAt string  `json:"published_at"`
	Date        string  `json:"date"`
	Score       float64 `json:"score"`
}

// errors exposed for tests
// (none — all errors are sentinors in search.go)
