package search

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	mimoDefaultBaseURL = "https://api.xiaomimimo.com/v1"
	mimoDefaultModel   = "mimo-v2.6-flash"
)

// MiMoProvider searches through the MiMo Chat Completions web_search tool.
// One keyword is requested per call so a single tool invocation is billed once.
type MiMoProvider struct {
	APIKey  string
	BaseURL string
	Model   string
	Timeout time.Duration
}

// Name implements Provider.
func (p *MiMoProvider) Name() string { return "mimo" }

// Search implements Provider.
func (p *MiMoProvider) Search(ctx context.Context, q Query) ([]Result, error) {
	if strings.TrimSpace(p.APIKey) == "" {
		return nil, ErrBadConfig
	}
	query := strings.TrimSpace(withSearchHints(q.Query, q))
	if query == "" {
		return nil, fmt.Errorf("%w: query is empty", ErrBadConfig)
	}
	base := p.BaseURL
	if strings.TrimSpace(base) == "" {
		base = mimoDefaultBaseURL
	}
	body, code, err := postJSON(ctx, endpoint(base, "/chat/completions"), p.APIKey, p.Timeout, map[string]any{
		"model": pickModel(p.Model, mimoDefaultModel),
		"messages": []map[string]string{
			{"role": "user", "content": query},
		},
		"max_completion_tokens": 512,
		"tools": []map[string]any{{
			"type":         "web_search",
			"max_keyword":  1,
			"force_search": true,
			"limit":        clampCount(q.MaxResults),
		}},
		"tool_choice": "auto",
		"thinking":    map[string]string{"type": "disabled"},
	})
	if err != nil {
		return nil, mapProviderNetErr("mimo", err)
	}
	if err := classifyStatus("mimo", code, body); err != nil {
		return nil, err
	}
	var parsed struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Choices []struct {
			Message struct {
				Annotations []struct {
					Type        string `json:"type"`
					URL         string `json:"url"`
					Title       string `json:"title"`
					Summary     string `json:"summary"`
					PublishTime string `json:"publish_time"`
				} `json:"annotations"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if parsed.Error != nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return nil, fmt.Errorf("E_UPSTREAM: mimo: %s", strings.TrimSpace(parsed.Error.Message))
	}
	out := make([]Result, 0)
	seen := map[string]struct{}{}
	for _, choice := range parsed.Choices {
		for _, ann := range choice.Message.Annotations {
			if ann.Type != "" && ann.Type != "url_citation" {
				continue
			}
			pageURL := strings.TrimSpace(ann.URL)
			if pageURL == "" {
				continue
			}
			if _, ok := seen[pageURL]; ok {
				continue
			}
			seen[pageURL] = struct{}{}
			out = append(out, Result{
				Title:       strings.TrimSpace(ann.Title),
				URL:         pageURL,
				Snippet:     truncateRunes(strings.TrimSpace(ann.Summary), deepseekSummaryRuneLimit),
				PublishedAt: strings.TrimSpace(ann.PublishTime),
			})
			if len(out) >= clampCount(q.MaxResults) {
				return out, nil
			}
		}
	}
	return out, nil
}
