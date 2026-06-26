package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// APIError is a normalized, user-friendly representation of any
// non-2xx response from an upstream LLM API. The original
// (possibly typed) error is preserved via Unwrap.
type APIError struct {
	// StatusCode is the HTTP status from the upstream API, or 0
	// when the error is not an HTTP error (e.g. network/timeout).
	StatusCode int

	// Kind is a coarse category that the UI can branch on without
	// parsing messages.
	Kind ErrorKind

	// Message is a one-line, end-user-friendly explanation.
	Message string

	// Suggestion is an optional actionable hint (e.g. "run /config
	// key <name> to update"). Empty when there's nothing useful.
	Suggestion string

	// Cause is the underlying error (e.g. *openai.APIError).
	Cause error
}

type ErrorKind int

const (
	// KindUnknown: couldn't classify. Show the raw message.
	KindUnknown ErrorKind = iota
	// KindAuth: 401/403, API key invalid or unauthorized.
	KindAuth
	// KindRateLimit: 429, hit the upstream rate limit.
	KindRateLimit
	// KindNotFound: 404, model not found.
	KindNotFound
	// KindBadRequest: 400, malformed request.
	KindBadRequest
	// KindServer: 5xx, upstream is broken.
	KindServer
	// KindNetwork: connection refused, DNS, etc.
	KindNetwork
	// KindTimeout: ctx deadline exceeded.
	KindTimeout
)

func (k ErrorKind) String() string {
	switch k {
	case KindAuth:
		return "auth_error"
	case KindRateLimit:
		return "rate_limit"
	case KindNotFound:
		return "not_found"
	case KindBadRequest:
		return "bad_request"
	case KindServer:
		return "server_error"
	case KindNetwork:
		return "network_error"
	case KindTimeout:
		return "timeout"
	default:
		return "unknown"
	}
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Suggestion != "" {
		return fmt.Sprintf("[%s] %s (%s)", e.Kind, e.Message, e.Suggestion)
	}
	return fmt.Sprintf("[%s] %s", e.Kind, e.Message)
}

func (e *APIError) Unwrap() error { return e.Cause }

// ClassifyAPIError inspects err and returns a user-friendly
// *APIError when possible. Non-API errors (e.g. context canceled)
// pass through unchanged.
func ClassifyAPIError(providerName string, err error) error {
	if err == nil {
		return nil
	}
	// Already classified?
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return err
	}

	// OpenAI SDK API errors carry HTTP status.
	var openaiErr *openai.APIError
	if errors.As(err, &openaiErr) {
		out := &APIError{
			StatusCode: openaiErr.HTTPStatusCode,
			Cause:      err,
		}
		switch {
		case openaiErr.HTTPStatusCode == 401, openaiErr.HTTPStatusCode == 403:
			out.Kind = KindAuth
			out.Message = "API key 无效或未授权"
			out.Suggestion = fmt.Sprintf("用 /config key %s <新key> 更新", providerName)
		case openaiErr.HTTPStatusCode == 404:
			out.Kind = KindNotFound
			out.Message = fmt.Sprintf("模型不存在 (%s)", truncate(openaiErr.Message, 80))
			out.Suggestion = "/model 切换到该 provider 已配置的模型"
		case openaiErr.HTTPStatusCode == 429:
			out.Kind = KindRateLimit
			out.Message = "请求频率超限 (rate limit)"
			out.Suggestion = "稍后重试，或考虑切换到更便宜的模型"
		case openaiErr.HTTPStatusCode >= 400 && openaiErr.HTTPStatusCode < 500:
			out.Kind = KindBadRequest
			out.Message = fmt.Sprintf("请求被拒绝 (%d)", openaiErr.HTTPStatusCode)
		case openaiErr.HTTPStatusCode >= 500:
			out.Kind = KindServer
			out.Message = fmt.Sprintf("上游服务异常 (%d)", openaiErr.HTTPStatusCode)
			out.Suggestion = "稍后重试"
		default:
			out.Kind = KindUnknown
			out.Message = truncate(openaiErr.Message, 120)
		}
		return out
	}

	// Context errors.
	if errors.Is(err, context.DeadlineExceeded) {
		return &APIError{Kind: KindTimeout, Message: "请求超时", Cause: err}
	}
	if errors.Is(err, context.Canceled) {
		return &APIError{Kind: KindUnknown, Message: "已取消", Cause: err}
	}

	// Network errors (rough heuristic on the error string).
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "no such host"),
		strings.Contains(msg, "dial tcp"):
		return &APIError{Kind: KindNetwork, Message: "网络连接失败", Cause: err}
	}

	// Fall through: unknown error, pass through.
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
