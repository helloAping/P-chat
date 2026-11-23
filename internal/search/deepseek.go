package search

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	deepseekDefaultBaseURL   = "https://api.deepseek.com"
	deepseekSearchPath       = "/v1/tools/search"
	deepseekFetchPath        = "/v1/tools/fetch"
	deepseekQueryRuneLimit   = 128
	deepseekSummaryRuneLimit = 800
)

// DeepSeekProvider calls POST /v1/tools/search.
type DeepSeekProvider struct {
	APIKey  string
	BaseURL string
	Path    string
	Timeout time.Duration
}

// Name implements Provider.
func (p *DeepSeekProvider) Name() string { return "deepseek" }

// Search implements Provider.
func (p *DeepSeekProvider) Search(ctx context.Context, q Query) ([]Result, error) {
	if strings.TrimSpace(p.APIKey) == "" {
		return nil, ErrBadConfig
	}
	query := limitRunes(q.Query, q, deepseekQueryRuneLimit)
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("%w: query is empty", ErrBadConfig)
	}
	path := p.Path
	if strings.TrimSpace(path) == "" {
		path = deepseekSearchPath
	}
	base := p.BaseURL
	if strings.TrimSpace(base) == "" {
		base = deepseekDefaultBaseURL
	}
	body, code, err := postJSON(ctx, endpoint(base, path), p.APIKey, p.Timeout, map[string]any{
		"query":        query,
		"count":        clampCount(q.MaxResults),
		"need_content": false,
	})
	if err != nil {
		return nil, mapProviderNetErr("deepseek", err)
	}
	if err := classifyStatus("deepseek", code, body); err != nil {
		return nil, err
	}
	var parsed struct {
		Results []struct {
			RerankScore float64 `json:"rerank_score"`
			Title       string  `json:"title"`
			URL         string  `json:"url"`
			Snippet     string  `json:"snippet"`
			Summary     string  `json:"summary"`
			Date        *string `json:"date"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	out := make([]Result, 0, len(parsed.Results))
	for _, hit := range parsed.Results {
		if strings.TrimSpace(hit.URL) == "" {
			continue
		}
		published := ""
		if hit.Date != nil {
			published = strings.TrimSpace(*hit.Date)
		}
		out = append(out, Result{
			Title:       strings.TrimSpace(hit.Title),
			URL:         strings.TrimSpace(hit.URL),
			Snippet:     truncateRunes(strings.TrimSpace(firstNonEmpty(hit.Summary, hit.Snippet)), deepseekSummaryRuneLimit),
			PublishedAt: published,
			Score:       hit.RerankScore,
		})
	}
	return out, nil
}

// DeepSeekFetcher calls POST /v1/tools/fetch.
type DeepSeekFetcher struct {
	APIKey  string
	BaseURL string
	Path    string
	Timeout time.Duration
}

// Name implements PageFetcher.
func (p *DeepSeekFetcher) Name() string { return "deepseek" }

// Fetch implements PageFetcher.
func (p *DeepSeekFetcher) Fetch(ctx context.Context, pageURL string) (FetchPage, error) {
	if strings.TrimSpace(p.APIKey) == "" {
		return FetchPage{}, ErrBadConfig
	}
	path := p.Path
	if strings.TrimSpace(path) == "" {
		path = deepseekFetchPath
	}
	base := p.BaseURL
	if strings.TrimSpace(base) == "" {
		base = deepseekDefaultBaseURL
	}
	body, code, err := postJSON(ctx, endpoint(base, path), p.APIKey, p.Timeout, map[string]any{
		"url": pageURL,
	})
	if fb := vendorFetchFallback("deepseek", err, code); fb != nil {
		if err != nil {
			return FetchPage{}, fb
		}
	}
	if err != nil {
		return FetchPage{}, mapProviderNetErr("deepseek", err)
	}
	if code >= 500 {
		return FetchPage{}, ErrFetchFallback
	}
	if err := classifyStatus("deepseek", code, body); err != nil {
		return FetchPage{}, err
	}
	var parsed struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return FetchPage{}, fmt.Errorf("decode: %w", err)
	}
	content := strings.TrimSpace(parsed.Content)
	if content == "" {
		return FetchPage{}, ErrFetchFallback
	}
	return FetchPage{Title: strings.TrimSpace(parsed.Title), Content: capFetchText(content)}, nil
}
