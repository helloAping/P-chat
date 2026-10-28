package search

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const qwenDefaultModel = "qwen3.8-flash"

// QwenProvider searches through the Bailian Responses API web_search tool.
type QwenProvider struct {
	APIKey  string
	BaseURL string
	Model   string
	Timeout time.Duration
}

// Name implements Provider.
func (p *QwenProvider) Name() string { return "qwen" }

// Search implements Provider.
func (p *QwenProvider) Search(ctx context.Context, q Query) ([]Result, error) {
	if strings.TrimSpace(p.APIKey) == "" || strings.TrimSpace(p.BaseURL) == "" {
		return nil, ErrBadConfig
	}
	query := strings.TrimSpace(withSearchHints(q.Query, q))
	if query == "" {
		return nil, fmt.Errorf("%w: query is empty", ErrBadConfig)
	}
	body, code, err := postJSON(ctx, endpoint(p.BaseURL, "/responses"), p.APIKey, p.Timeout, map[string]any{
		"model":           pickModel(p.Model, qwenDefaultModel),
		"input":           query,
		"tools":           []map[string]string{{"type": "web_search"}},
		"enable_thinking": true,
	})
	if err != nil {
		return nil, mapProviderNetErr("qwen", err)
	}
	if err := classifyStatus("qwen", code, body); err != nil {
		return nil, err
	}
	hits, err := qwenSources(body)
	if err != nil {
		return nil, err
	}
	if len(hits) > clampCount(q.MaxResults) {
		hits = hits[:clampCount(q.MaxResults)]
	}
	return hits, nil
}

// QwenFetcher asks a Bailian model to open one URL with web_extractor.
// The request also carries web_search because Bailian rejects extractor
// calls that omit it. This process still treats fetch as its own channel.
type QwenFetcher struct {
	APIKey  string
	BaseURL string
	Model   string
	Timeout time.Duration
}

// Name implements PageFetcher.
func (p *QwenFetcher) Name() string { return "qwen" }

// Fetch implements PageFetcher.
func (p *QwenFetcher) Fetch(ctx context.Context, pageURL string) (FetchPage, error) {
	if strings.TrimSpace(p.APIKey) == "" || strings.TrimSpace(p.BaseURL) == "" {
		return FetchPage{}, ErrBadConfig
	}
	pageURL = strings.TrimSpace(pageURL)
	body, code, err := postJSON(ctx, endpoint(p.BaseURL, "/responses"), p.APIKey, p.Timeout, map[string]any{
		"model": pickModel(p.Model, qwenDefaultModel),
		"input": "只打开下面这个 URL，提取该页 Markdown 正文。不要搜索其他网页，不要改写。\n" + pageURL,
		"tools": []map[string]string{
			{"type": "web_search"},
			{"type": "web_extractor"},
		},
		"enable_thinking": true,
	})
	if fb := vendorFetchFallback("qwen", err, code); err != nil || code >= 500 {
		if fb != nil {
			return FetchPage{}, fb
		}
	}
	if err != nil {
		return FetchPage{}, mapProviderNetErr("qwen", err)
	}
	if code >= 500 {
		return FetchPage{}, ErrFetchFallback
	}
	if err := classifyStatus("qwen", code, body); err != nil {
		return FetchPage{}, err
	}
	page, ok := qwenExtract(body, pageURL)
	if !ok {
		return FetchPage{}, ErrFetchFallback
	}
	page.Content = capFetchText(page.Content)
	return page, nil
}

type qwenItem struct {
	Type   string `json:"type"`
	Action struct {
		URL     string   `json:"url"`
		Content string   `json:"content"`
		Sources []rawHit `json:"sources"`
	} `json:"action"`
	Result struct {
		URL     string `json:"url"`
		Title   string `json:"title"`
		Content string `json:"content"`
		Text    string `json:"text"`
	} `json:"result"`
}

func qwenSources(body []byte) ([]Result, error) {
	var parsed struct {
		Output []qwenItem `json:"output"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if parsed.Error != nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return nil, fmt.Errorf("E_UPSTREAM: qwen: %s", strings.TrimSpace(parsed.Error.Message))
	}
	out := make([]Result, 0)
	seen := map[string]struct{}{}
	for _, item := range parsed.Output {
		if item.Type != "" && item.Type != "web_search_call" {
			continue
		}
		for _, src := range item.Action.Sources {
			pageURL := strings.TrimSpace(firstNonEmpty(src.URL))
			if pageURL == "" {
				continue
			}
			if _, ok := seen[pageURL]; ok {
				continue
			}
			seen[pageURL] = struct{}{}
			out = append(out, Result{
				Title:       strings.TrimSpace(src.Title),
				URL:         pageURL,
				Snippet:     truncateRunes(strings.TrimSpace(firstNonEmpty(src.Snippet, src.Content, src.Description)), deepseekSummaryRuneLimit),
				PublishedAt: strings.TrimSpace(firstNonEmpty(src.PublishedAt, src.Date)),
				Score:       src.Score,
			})
		}
	}
	return out, nil
}

func qwenExtract(body []byte, pageURL string) (FetchPage, bool) {
	var parsed struct {
		Output []qwenItem `json:"output"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return FetchPage{}, false
	}
	var best FetchPage
	for _, item := range parsed.Output {
		kind := strings.ToLower(item.Type)
		if !strings.Contains(kind, "extract") && !strings.Contains(kind, "fetch") {
			continue
		}
		content := strings.TrimSpace(firstNonEmpty(item.Result.Content, item.Result.Text, item.Action.Content))
		if content == "" {
			continue
		}
		found := strings.TrimSpace(firstNonEmpty(item.Result.URL, item.Action.URL))
		if found != "" && !sameFetchURL(found, pageURL) {
			continue
		}
		if len(content) > len(best.Content) {
			best = FetchPage{Title: strings.TrimSpace(item.Result.Title), Content: content}
		}
	}
	return best, best.Content != ""
}

func sameFetchURL(a, b string) bool {
	trim := func(s string) string {
		return strings.TrimRight(strings.ToLower(strings.TrimSpace(s)), "/")
	}
	return trim(a) == trim(b)
}
