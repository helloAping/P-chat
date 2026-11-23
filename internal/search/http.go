package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func endpoint(base, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	path = "/" + strings.Trim(strings.TrimSpace(path), "/")
	if strings.HasSuffix(base, path) {
		return base
	}
	return base + path
}

func postJSON(ctx context.Context, rawURL, apiKey string, timeout time.Duration, payload any) ([]byte, int, error) {
	if err := validateBaseURL(rawURL); err != nil {
		return nil, 0, err
	}
	if timeout <= 0 {
		timeout = tavilyDefaultTimeout
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal: %w", err)
	}
	httpCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(httpCtx, http.MethodPost, rawURL, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "P-Chat/1.0")
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}
	resp, err := newClient().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read: %w", err)
	}
	return body, resp.StatusCode, nil
}

func classifyStatus(provider string, code int, body []byte) error {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrAuth
	case http.StatusTooManyRequests:
		return ErrQuota
	}
	if code >= 500 {
		return fmt.Errorf("E_UPSTREAM: %s returned %d", provider, code)
	}
	if code >= 400 {
		preview := strings.TrimSpace(string(body))
		if len(preview) > 256 {
			preview = preview[:256]
		}
		return fmt.Errorf("E_UPSTREAM: %s HTTP %d: %s", provider, code, preview)
	}
	return nil
}

func mapProviderNetErr(provider string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("E_TIMEOUT: %s request timed out", provider)
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return fmt.Errorf("E_UPSTREAM: %s network error: %w", provider, err)
}
