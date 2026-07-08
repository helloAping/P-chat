package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/search"
)

// webSearchArgs mirrors the JSON schema registered for the
// `web_search` tool. LLM-supplied args are decoded into this
// struct; fields are zero-defaulted by the JSON decoder.
type webSearchArgs struct {
	Query       string `json:"query"`
	MaxResults  int    `json:"max_results,omitempty"`
	RecencyDays int    `json:"recency_days,omitempty"`
	Language    string `json:"language,omitempty"`
	Site        string `json:"site,omitempty"`
}

// RegisterWebSearch registers the `web_search` tool. It is
// called separately from RegisterBuiltin so the tool only
// appears in the LLM's tool list when:
//
//  1. The user has enabled web_search in config
//  2. A usable API key is configured
//
// This avoids the "tool visible but always fails" antipattern
// where the LLM wastes a turn calling a feature that isn't
// set up. The same pattern is used by wiki_lookup and
// grep_knowledge_bases.
func RegisterWebSearch(r *Registry, cfg config.SearchConfig) {
	// Quick sanity check: if there's no provider that can
	// answer, don't even register the tool. The check is
	// duplicated in search.BuildProvider; we keep it here
	// so the tool is *truly* invisible (not just erroring on
	// every call).
	if !cfg.Enabled {
		return
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "", "tavily":
		if cfg.APIKey == "" {
			return
		}
	case "openai_compat":
		if cfg.BaseURL == "" {
			return
		}
	default:
		// Unknown provider name — don't register. The
		// settings UI surfaces this as a validation error
		// at save time, so the LLM never sees the tool
		// in a broken state.
		return
	}

	r.Register(Tool{
		Name: "web_search",
		Description: "Search the public web and return a list of {title, url, snippet} results. " +
			"Use when you need up-to-date information, news, documentation, or to verify " +
			"facts beyond your training data. The response is a concise list — call " +
			"`web_fetch` on a specific URL if you need the full page content. " +
			"NOT for: private/local data (use read_file / list_files), or anything that " +
			"requires login.",
		Parameters: ObjectSchema(map[string]any{
			"query": StringProp("The search query. Be specific: include years, versions, exact phrases in quotes."),
			"max_results": map[string]any{
				"type":        "integer",
				"description": "Max results to return (1-10, default 5). Lower = faster + cheaper.",
				"minimum":     1,
				"maximum":     10,
			},
			"recency_days": map[string]any{
				"type":        "integer",
				"description": "Optional: only return results from the last N days. 0 = any time.",
				"minimum":     0,
			},
			"language": StringEnumProp("Optional: prefer results in this language", "zh", "en"),
			"site":     StringProp("Optional: restrict to a domain (e.g. 'github.com' or 'stackoverflow.com')"),
		}, []string{"query"}),
	}, handleWebSearch)
}

// handleWebSearch is the registered handler. It is a thin
// wrapper around search.Global() that maps the package-level
// sentinel errors to human-readable messages the LLM can act on.
func handleWebSearch(ctx context.Context, args json.RawMessage) (*CallResult, error) {
	var a webSearchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return &CallResult{
			Content: "E_ARGS: invalid arguments: " + err.Error(),
			IsError: true,
		}, nil
	}
	if strings.TrimSpace(a.Query) == "" {
		return &CallResult{
			Content: "E_ARGS: query is required",
			IsError: true,
		}, nil
	}

	provider := search.Global()
	// Defensive: the global should never be nil (init pins
	// it to DisabledProvider), but if a future refactor
	// breaks that invariant we want a clean error rather
	// than a nil deref mid-tool-call.
	if provider == nil {
		return &CallResult{
			Content: search.ErrDisabled.Error(),
			IsError: true,
		}, nil
	}

	// Enforce the daily cap BEFORE the network call so a
	// flooded quota doesn't cost us any provider-side
	// billing. The test-connection endpoint calls
	// search.Global() directly and bypasses this — tests
	// are not real searches.
	ok, _ := search.Quota().CheckAndIncrement()
	if !ok {
		return &CallResult{
			Content: search.ErrQuota.Error(),
			IsError: true,
		}, nil
	}

	results, err := provider.Search(ctx, search.Query{
		Query:       a.Query,
		MaxResults:  a.MaxResults,
		RecencyDays: a.RecencyDays,
		Language:    a.Language,
		Site:        a.Site,
	})
	if err != nil {
		// Sentinel errors carry the LLM-friendly code prefix
		// (E_DISABLED, E_AUTH, ...). Plain errors get a
		// generic E_UPSTREAM wrapper so the LLM still has a
		// code to branch on.
		msg := err.Error()
		if !strings.HasPrefix(msg, "E_") {
			msg = "E_UPSTREAM: " + msg
		}
		return &CallResult{Content: msg, IsError: true}, nil
	}

	if len(results) == 0 {
		return &CallResult{
			Content: fmt.Sprintf("No results found for %q.", a.Query),
		}, nil
	}

	// Format the results. The LLM will read this text and
	// decide whether to call web_fetch on any URL.
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d result(s) for %q (provider: %s):\n\n",
		len(results), a.Query, provider.Name()))
	for i, r := range results {
		sb.WriteString(fmt.Sprintf("[%d] %s\n", i+1, r.Title))
		sb.WriteString(fmt.Sprintf("    URL: %s\n", r.URL))
		if r.Snippet != "" {
			sb.WriteString(fmt.Sprintf("    %s\n", r.Snippet))
		}
		if r.PublishedAt != "" {
			sb.WriteString(fmt.Sprintf("    Published: %s\n", r.PublishedAt))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("To fetch the full content of any result, call `web_fetch` with the URL.\n")
	return &CallResult{Content: sb.String()}, nil
}
