package search

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"

	"github.com/p-chat/pchat/internal/config"
)

// FetchPage is the text a web_fetch backend returns to the model.
type FetchPage struct {
	Title   string
	Content string
}

// PageFetcher retrieves one public page.
//
// Fetch returns ErrFetchFallback when the caller should use the local
// HTTP client instead: empty body, timeout, or an upstream 5xx.
type PageFetcher interface {
	Name() string
	Fetch(ctx context.Context, pageURL string) (FetchPage, error)
}

// ErrFetchFallback tells web_fetch to read the URL from this machine.
var ErrFetchFallback = errors.New("E_FALLBACK: vendor fetch unavailable")

// LocalFetcher is the sentinel for the built-in HTTP fetch.
type LocalFetcher struct{}

// Name implements PageFetcher.
func (LocalFetcher) Name() string { return "local" }

// Fetch implements PageFetcher by asking the tool handler to use its
// own HTTP client.
func (LocalFetcher) Fetch(context.Context, string) (FetchPage, error) {
	return FetchPage{}, ErrFetchFallback
}

var globalFetcher atomic.Pointer[PageFetcher]

func init() {
	f := PageFetcher(LocalFetcher{})
	globalFetcher.Store(&f)
}

// SetGlobalFetcher installs the process-wide web_fetch backend.
func SetGlobalFetcher(f PageFetcher) {
	if f == nil {
		f = LocalFetcher{}
	}
	globalFetcher.Store(&f)
}

// GlobalFetcher returns the current web_fetch backend. Never nil.
func GlobalFetcher() PageFetcher {
	f := globalFetcher.Load()
	if f == nil {
		return LocalFetcher{}
	}
	return *f
}

// BuildFetcher selects the web_fetch backend. An unusable vendor
// config falls back to local so the tool keeps working.
func BuildFetcher(cfg config.FetchConfig) PageFetcher {
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "deepseek":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return LocalFetcher{}
		}
		return &DeepSeekFetcher{
			APIKey:  cfg.APIKey,
			BaseURL: cfg.BaseURL,
			Path:    cfg.Path,
			Timeout: pickTimeout(cfg.RequestTimeout),
		}
	case "qwen":
		if strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.BaseURL) == "" {
			return LocalFetcher{}
		}
		return &QwenFetcher{
			APIKey:  cfg.APIKey,
			BaseURL: cfg.BaseURL,
			Model:   cfg.Model,
			Timeout: pickTimeout(cfg.RequestTimeout),
		}
	default:
		return LocalFetcher{}
	}
}

func vendorFetchFallback(provider string, err error, code int) error {
	if err != nil && errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		return ErrFetchFallback
	}
	if code >= 500 {
		return ErrFetchFallback
	}
	if err != nil {
		return mapProviderNetErr(provider, err)
	}
	return nil
}

func capFetchText(s string) string {
	const max = 1 << 20
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && cut[len(cut)-1]&0xC0 == 0x80 {
		cut = cut[:len(cut)-1]
	}
	return cut
}

func pickModel(configured, fallback string) string {
	if model := strings.TrimSpace(configured); model != "" {
		return model
	}
	return fallback
}
