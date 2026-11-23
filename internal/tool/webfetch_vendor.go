package tool

import (
	"context"
	"errors"
	"strings"

	"github.com/p-chat/pchat/internal/search"
)

// fetchViaVendor handles a public GET when a vendor fetch backend is
// configured. handled is false when the caller should use local HTTP.
func fetchViaVendor(ctx context.Context, pageURL string) (*CallResult, bool) {
	fetcher := search.GlobalFetcher()
	if fetcher == nil || fetcher.Name() == "local" {
		return nil, false
	}
	if ok, _ := search.FetchQuota().CheckAndIncrement(); !ok {
		return nil, false
	}
	page, err := fetcher.Fetch(ctx, pageURL)
	if err != nil {
		if errors.Is(err, search.ErrFetchFallback) {
			return nil, false
		}
		if errors.Is(err, context.Canceled) {
			return &CallResult{Content: "E_TIMEOUT: fetch canceled", IsError: true}, true
		}
		msg := err.Error()
		if !strings.HasPrefix(msg, "E_") {
			msg = "E_UPSTREAM: " + msg
		}
		return &CallResult{Content: msg, IsError: true}, true
	}
	var b strings.Builder
	if strings.TrimSpace(page.Title) != "" {
		b.WriteString("# ")
		b.WriteString(strings.TrimSpace(page.Title))
		b.WriteString("\n\n")
	}
	b.WriteString(page.Content)
	return &CallResult{Content: b.String()}, true
}
