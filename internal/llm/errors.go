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
//
// Note: SSE-stream errors (where the HTTP response is 200 OK and
// the error is delivered as a `data: {"error": ...}` line in the
// stream) are also represented as *APIError. In that case
// StatusCode is 0 and the Kind/Message/Suggestion are derived
// from the Type/Code fields by applySSECodeClassification.
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
	// KindVisionUnsupported: the LLM API rejected an image input
	// with a clear "this model does not support image input" style
	// error. The image was not processed; the user can either drop
	// the image or switch to a vision-capable model.
	KindVisionUnsupported
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
	case KindVisionUnsupported:
		return "vision_unsupported"
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

	// "Model does not support image input" cuts across
	// every classification path (HTTP 400, SSE stream
	// error, plain error). Check it first so the more
	// specific vision-unsupported message wins over the
	// generic bad_request / rate_limit / server_error
	// buckets. Without this, an SSE-delivered error like
	//   type: invalid_request_error
	//   message: Cannot read "image.png" (this model does not support image input)
	// would land in KindBadRequest with a generic
	// "请求格式或参数被拒绝" message and a tool-schema
	// suggestion that's actively misleading.
	if IsVisionUnsupportedError(err) {
		return &APIError{
			Kind:       KindVisionUnsupported,
			Message:    "当前模型不支持图片输入",
			Suggestion: "移除附件中的图片，或在 /model 切换到支持视觉的模型（如 gpt-4o / claude-3.5+）",
			Cause:      err,
		}
	}

	// OpenAI SDK API errors carry HTTP status.
	var openaiErr *openai.APIError
	if errors.As(err, &openaiErr) {
		out := &APIError{
			StatusCode: openaiErr.HTTPStatusCode,
			Cause:      err,
		}
		// SSE-stream errors arrive with HTTPStatusCode == 0 (the
		// HTTP response itself is 200 OK; the error is delivered
		// as a `data: {"error": ...}` line and the SDK wraps it in
		// an *APIError). Classify those by Type/Code instead.
		if openaiErr.HTTPStatusCode == 0 {
			applySSECodeClassification(out, openaiErr, providerName)
			return out
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

// IsVisionUnsupportedError reports whether the upstream LLM
// rejected an image input. The various providers all use
// slightly different phrasings for the same underlying problem,
// so we match on a few stable substrings rather than a single
// exact string.
//
// Examples seen in the wild:
//   - "Cannot read \"image.png\" (this model does not support image input). Inform the user."
//   - "This model does not support image inputs."
//   - "Image input is not supported by this model."
//   - "The model does not accept image content."
//
// Anything matching one of these (case-insensitive) is treated
// as a vision-unsupported error.
func IsVisionUnsupportedError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "does not support image input"),
		strings.Contains(msg, "does not support image inputs"),
		strings.Contains(msg, "does not accept image"),
		strings.Contains(msg, "image input is not supported"),
		strings.Contains(msg, "no support for image"):
		return true
	}
	// "Cannot read \"image.png\" ... Inform the user." — the
	// proxy's standard error when a non-vision model is sent
	// an image. We anchor on both halves so we don't false-match
	// a generic "cannot read" error.
	if strings.Contains(msg, "cannot read") && strings.Contains(msg, "inform the user") {
		return true
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// codeString normalises the *openai.APIError.Code field (which is
// `any` because the JSON spec lets it be a string OR a number) into
// a string we can match on.
func codeString(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%d", int(v))
	case int:
		return fmt.Sprintf("%d", v)
	case int64:
		return fmt.Sprintf("%d", v)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

// applySSECodeClassification maps the Type/Code fields of an SSE
// stream error (HTTPStatusCode == 0) into the appropriate
// *APIError.Kind, with a Chinese message and a useful suggestion.
//
// The OpenAI-style taxonomy is loosely:
//
//	type=invalid_request_error  -> KindBadRequest
//	type=authentication_error   -> KindAuth
//	type=permission_denied      -> KindAuth
//	type=not_found_error        -> KindNotFound
//	type=rate_limit_error       -> KindRateLimit
//	type=insufficient_quota     -> KindRateLimit (quota == rate limit in spirit)
//	type=server_error           -> KindServer
//	code=route_not_found        -> KindServer (proxy has no upstream route for this model)
//
// The proxy the user was using (api-convert.08ms.cn) returns
// `type: "server_error", code: "route_not_found"` for models that
// have no working upstream — which is the case we most want to
// classify cleanly. See errors_test.go for the canonical examples.
func applySSECodeClassification(out *APIError, openaiErr *openai.APIError, providerName string) {
	t := strings.ToLower(strings.TrimSpace(openaiErr.Type))
	code := strings.ToLower(strings.TrimSpace(codeString(openaiErr.Code)))
	msg := openaiErr.Message

	// route_not_found is special: the proxy has no upstream route
	// for this model. We always want the user-facing suggestion to
	// point them at a model switch, regardless of type.
	isRouteNotFound := code == "route_not_found" ||
		strings.Contains(strings.ToLower(msg), "no available route")

	switch {
	case t == "authentication_error" || code == "invalid_api_key" || code == "invalid_api_key.":
		out.Kind = KindAuth
		out.Message = "API key 无效或未授权"
		out.Suggestion = fmt.Sprintf("用 /config key %s <新key> 更新", providerName)
	case t == "permission_denied":
		out.Kind = KindAuth
		out.Message = "权限不足"
		out.Suggestion = "确认该 API key 对当前模型有访问权限"
	case t == "not_found_error" || code == "model_not_found" || code == "model_not_exists":
		out.Kind = KindNotFound
		out.Message = fmt.Sprintf("模型不存在 (%s)", truncate(msg, 80))
		out.Suggestion = "/model 切换到该 provider 已配置的模型"
	case t == "rate_limit_error" || code == "rate_limit_exceeded":
		out.Kind = KindRateLimit
		out.Message = "请求频率超限 (rate limit)"
		out.Suggestion = "稍后重试，或考虑切换到更便宜的模型"
	case t == "insufficient_quota" || code == "insufficient_quota":
		out.Kind = KindRateLimit
		out.Message = "账户配额已用尽"
		out.Suggestion = "到 provider 控制台充值或更换 API key"
	case t == "invalid_request_error":
		out.Kind = KindBadRequest
		out.Message = "请求格式或参数被拒绝"
		out.Suggestion = "检查提示词长度 / 工具 schema 是否合法"
	case t == "server_error" || isRouteNotFound:
		out.Kind = KindServer
		if isRouteNotFound {
			out.Message = fmt.Sprintf("provider 端无路由 (%s)", truncate(msg, 80))
			out.Suggestion = fmt.Sprintf("在 /model 切换到 %s 下其他模型，或换 provider", providerName)
		} else {
			out.Message = fmt.Sprintf("上游服务异常 (%s)", truncate(msg, 80))
			out.Suggestion = "稍后重试，或切换到其他 provider"
		}
	default:
		out.Kind = KindUnknown
		out.Message = truncate(msg, 120)
	}
}
