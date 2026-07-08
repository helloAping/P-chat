package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// TavilyProvider calls https://api.tavily.com/search. This is the
// default provider for P-Chat — 1000 free searches/month, results
// already cleaned (no HTML), optional `answer` synthesis.
//
// Docs: https://docs.tavily.com/docs/rest-api/api-reference#endpoint-search
type TavilyProvider struct {
	APIKey  string
	Timeout time.Duration // per-request; 0 = 20s default
	BaseURL string        // override for self-hosted Tavily; "" = https://api.tavily.com
	// Topic restricts the search corpus. "" (default) maps to
	// "general"; other valid values are "news" and "finance".
	Topic string
}

const tavilyDefaultBaseURL = "https://api.tavily.com"
const tavilyDefaultTimeout = 20 * time.Second

// Name implements Provider.
func (p *TavilyProvider) Name() string { return "tavily" }

// Search implements Provider.
func (p *TavilyProvider) Search(ctx context.Context, q Query) ([]Result, error) {
	if p.APIKey == "" {
		return nil, ErrBadConfig
	}

	base := p.BaseURL
	if base == "" {
		base = tavilyDefaultBaseURL
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
		"api_key":             p.APIKey,
		"query":               q.Query,
		"max_results":         max,
		"include_raw_content": false, // we only want clean snippets
		"include_answer":      false, // keep the response small + let the LLM synthesize
		"search_depth":        "basic",
		"topic":               pickTavilyTopic(p.Topic),
	}
	if q.RecencyDays > 0 {
		body["days"] = q.RecencyDays
	}
	if q.Site != "" {
		// Tavily doesn't have a native `site:` filter, but it
		// accepts `include_domains` as a list. We add it to the
		// query to give the LLM a hint, AND set the structured
		// field for the engine to honour.
		body["include_domains"] = []string{q.Site}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, "POST", base+"/search", bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "P-Chat/1.0")

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
		return nil, fmt.Errorf("E_UPSTREAM: tavily returned %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		// Read a small prefix to give the LLM a hint; the rest
		// of the body is opaque provider error markup.
		preview := readPrefix(resp.Body, 256)
		return nil, fmt.Errorf("E_UPSTREAM: tavily HTTP %d: %s", resp.StatusCode, preview)
	}

	dec := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes))
	dec.DisallowUnknownFields() // Tavily occasionally adds fields; we ignore them

	var parsed tavilyResponse
	if err := dec.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	out := make([]Result, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		// Tavily sometimes returns results with empty URL;
		// filter them so the LLM doesn't try to web_fetch ""
		if r.URL == "" {
			continue
		}
		out = append(out, Result{
			Title:       strings.TrimSpace(r.Title),
			URL:         r.URL,
			Snippet:     truncateSnippet(firstNonEmpty(r.Content, r.RawContent)),
			PublishedAt: r.PublishedDate,
			Score:       r.Score,
		})
	}
	return out, nil
}

// firstNonEmpty returns a if a is non-empty, else b. Used to
// fall back from `content` to `raw_content` when Tavily's
// include_raw_content=false path returned an empty snippet.

// pickTavilyTopic maps the configured topic to one of Tavily's
// three allowed values; any unknown value is silently treated as
// "general" so a typo in config doesn't hard-fail the tool.
func pickTavilyTopic(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "news":
		return "news"
	case "finance":
		return "finance"
	default:
		return "general"
	}
}

// readPrefix returns up to n bytes of body as a string, replacing
// non-printable bytes with '·' so error messages stay readable.
// Best-effort: if reading fails, returns "".
func readPrefix(body io.Reader, n int) string {
	const max = 256
	if n > max {
		n = max
	}
	buf := make([]byte, n)
	r := io.LimitReader(body, int64(n))
	m, _ := io.ReadFull(r, buf)
	if m == 0 {
		return ""
	}
	for i, b := range buf[:m] {
		if b < 0x20 || b == 0x7f {
			buf[i] = '.'
		}
	}
	return strings.TrimSpace(string(buf[:m]))
}

// ====================================================================
// Tavily wire types
// ====================================================================

// tavilyResponse mirrors the relevant subset of the Tavily /search
// response. We intentionally do not decode `answer`, `images`, or
// `follow_up_questions` — none of them help the LLM make a
// better tool call next.
type tavilyResponse struct {
	Results []struct {
		Title         string  `json:"title"`
		URL           string  `json:"url"`
		Content       string  `json:"content"`
		RawContent    string  `json:"raw_content,omitempty"`
		Score         float64 `json:"score"`
		PublishedDate string  `json:"published_date,omitempty"`
	} `json:"results"`
}

// mapNetErr maps low-level network errors to user-friendly codes.
// Context deadlines bubble up as E_TIMEOUT so the LLM knows the
// call was cancelled, not a hard failure.
func mapNetErr(err error) error {
	if err == nil {
		return nil
	}
	if err == context.DeadlineExceeded {
		return fmt.Errorf("E_TIMEOUT: tavily request timed out")
	}
	if err == context.Canceled {
		return err // propagate cancellation
	}
	return fmt.Errorf("E_UPSTREAM: tavily network error: %w", err)
}
