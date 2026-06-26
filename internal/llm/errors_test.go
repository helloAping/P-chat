package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

func TestClassifyAPIError_Nil(t *testing.T) {
	if err := ClassifyAPIError("cs", nil); err != nil {
		t.Errorf("nil error should give nil, got %v", err)
	}
}

func TestClassifyAPIError_Auth(t *testing.T) {
	orig := &openai.APIError{
		HTTPStatusCode: 401,
		Message:        "invalid api key",
	}
	apiErr := ClassifyAPIError("cs", orig)
	var got *APIError
	if !errors.As(apiErr, &got) {
		t.Fatalf("expected *APIError, got %T", apiErr)
	}
	if got.Kind != KindAuth {
		t.Errorf("Kind = %v, want KindAuth", got.Kind)
	}
	if got.StatusCode != 401 {
		t.Errorf("StatusCode = %d, want 401", got.StatusCode)
	}
	if got.Suggestion == "" {
		t.Error("expected non-empty suggestion for auth error")
	}
	// Original error should be preserved via Unwrap.
	if !errors.Is(apiErr, orig) {
		t.Error("Unwrap should preserve original error")
	}
}

func TestClassifyAPIError_RateLimit(t *testing.T) {
	apiErr := ClassifyAPIError("cs", &openai.APIError{HTTPStatusCode: 429, Message: "rate"})
	var got *APIError
	if !errors.As(apiErr, &got) {
		t.Fatal("expected *APIError")
	}
	if got.Kind != KindRateLimit {
		t.Errorf("Kind = %v, want KindRateLimit", got.Kind)
	}
}

func TestClassifyAPIError_NotFound(t *testing.T) {
	apiErr := ClassifyAPIError("cs", &openai.APIError{HTTPStatusCode: 404, Message: "model not found"})
	var got *APIError
	if !errors.As(apiErr, &got) {
		t.Fatal("expected *APIError")
	}
	if got.Kind != KindNotFound {
		t.Errorf("Kind = %v, want KindNotFound", got.Kind)
	}
}

func TestClassifyAPIError_ServerError(t *testing.T) {
	apiErr := ClassifyAPIError("cs", &openai.APIError{HTTPStatusCode: 500, Message: "internal"})
	var got *APIError
	if !errors.As(apiErr, &got) {
		t.Fatal("expected *APIError")
	}
	if got.Kind != KindServer {
		t.Errorf("Kind = %v, want KindServer", got.Kind)
	}
}

func TestClassifyAPIError_Timeout(t *testing.T) {
	apiErr := ClassifyAPIError("cs", context.DeadlineExceeded)
	var got *APIError
	if !errors.As(apiErr, &got) {
		t.Fatal("expected *APIError")
	}
	if got.Kind != KindTimeout {
		t.Errorf("Kind = %v, want KindTimeout", got.Kind)
	}
}

func TestClassifyAPIError_Network(t *testing.T) {
	apiErr := ClassifyAPIError("cs", fmt.Errorf("dial tcp: connection refused"))
	var got *APIError
	if !errors.As(apiErr, &got) {
		t.Fatal("expected *APIError")
	}
	if got.Kind != KindNetwork {
		t.Errorf("Kind = %v, want KindNetwork", got.Kind)
	}
}

func TestClassifyAPIError_PassThrough(t *testing.T) {
	// Non-classified errors pass through unchanged.
	orig := errors.New("some weird error")
	got := ClassifyAPIError("cs", orig)
	if got != orig {
		t.Errorf("expected pass-through, got %v", got)
	}
}

func TestClassifyAPIError_Idempotent(t *testing.T) {
	// Classifying an already-classified error is a no-op.
	first := ClassifyAPIError("cs", &openai.APIError{HTTPStatusCode: 401, Message: "x"})
	second := ClassifyAPIError("cs", first)
	if first != second {
		t.Errorf("ClassifyAPIError should be idempotent, got %v then %v", first, second)
	}
}

func TestAPIError_ErrorString(t *testing.T) {
	tests := []struct {
		name string
		err  APIError
		want string
	}{
		{
			"with suggestion",
			APIError{Kind: KindAuth, Message: "x", Suggestion: "y"},
			"[auth_error] x (y)",
		},
		{
			"without suggestion",
			APIError{Kind: KindServer, Message: "x"},
			"[server_error] x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestErrorKindString(t *testing.T) {
	cases := map[ErrorKind]string{
		KindUnknown:    "unknown",
		KindAuth:       "auth_error",
		KindRateLimit:  "rate_limit",
		KindNotFound:    "not_found",
		KindBadRequest:  "bad_request",
		KindServer:     "server_error",
		KindNetwork:    "network_error",
		KindTimeout:    "timeout",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("ErrorKind(%d).String() = %q, want %q", k, got, want)
		}
	}
}

// --- SSE stream error classification (HTTPStatusCode == 0) ---

// makeSSEError builds an *openai.APIError that looks like one
// unwrapped from a `data: {"error": ...}` line in an SSE stream.
func makeSSEError(t, code any, msg string) error {
	return fmt.Errorf("error, %w", &openai.APIError{
		Type:           fmt.Sprint(t),
		Code:           code,
		Message:        msg,
		HTTPStatusCode: 0,
	})
}

func TestClassifyAPIError_SSE_RouteNotFound(t *testing.T) {
	// The actual case the user hit: api-convert.08ms.cn streaming
	// response for doubao-seed-2.0-lite.
	err := makeSSEError("server_error", "route_not_found",
		"No available route for model because all candidates are temporarily blocked: doubao-seed-2.0-lite")
	got := ClassifyAPIError("cs", err)
	var apiErr *APIError
	if !errors.As(got, &apiErr) {
		t.Fatalf("expected *APIError, got %T", got)
	}
	if apiErr.Kind != KindServer {
		t.Errorf("Kind = %v, want KindServer", apiErr.Kind)
	}
	if apiErr.StatusCode != 0 {
		t.Errorf("StatusCode = %d, want 0 (SSE)", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Message, "无路由") {
		t.Errorf("Message should mention 无路由, got %q", apiErr.Message)
	}
	if !strings.Contains(apiErr.Suggestion, "切换") {
		t.Errorf("Suggestion should suggest switching models, got %q", apiErr.Suggestion)
	}
	if !strings.Contains(apiErr.Suggestion, "cs") {
		t.Errorf("Suggestion should mention provider name, got %q", apiErr.Suggestion)
	}
}

func TestClassifyAPIError_SSE_RouteNotFound_MessageHeuristic(t *testing.T) {
	// Some proxies omit `code` but still say "no available route"
	// in the message. We should still classify this as KindServer
	// with the model-switch suggestion.
	err := makeSSEError("server_error", "",
		"No available route for model foo")
	got := ClassifyAPIError("openai", err)
	var apiErr *APIError
	if !errors.As(got, &apiErr) {
		t.Fatalf("expected *APIError, got %T", got)
	}
	if apiErr.Kind != KindServer {
		t.Errorf("Kind = %v, want KindServer", apiErr.Kind)
	}
	if !strings.Contains(apiErr.Message, "无路由") {
		t.Errorf("Message should mention 无路由, got %q", apiErr.Message)
	}
}

func TestClassifyAPIError_SSE_ServerErrorGeneric(t *testing.T) {
	err := makeSSEError("server_error", "internal_error", "upstream crashed")
	got := ClassifyAPIError("openai", err)
	var apiErr *APIError
	if !errors.As(got, &apiErr) {
		t.Fatalf("expected *APIError, got %T", got)
	}
	if apiErr.Kind != KindServer {
		t.Errorf("Kind = %v, want KindServer", apiErr.Kind)
	}
	if strings.Contains(apiErr.Message, "无路由") {
		t.Errorf("non-route server errors should not say 无路由, got %q", apiErr.Message)
	}
}

func TestClassifyAPIError_SSE_Auth(t *testing.T) {
	err := makeSSEError("authentication_error", "invalid_api_key", "wrong key")
	got := ClassifyAPIError("openai", err)
	var apiErr *APIError
	if !errors.As(got, &apiErr) {
		t.Fatalf("expected *APIError, got %T", got)
	}
	if apiErr.Kind != KindAuth {
		t.Errorf("Kind = %v, want KindAuth", apiErr.Kind)
	}
}

func TestClassifyAPIError_SSE_RateLimit(t *testing.T) {
	err := makeSSEError("rate_limit_error", "rate_limit_exceeded", "slow down")
	got := ClassifyAPIError("openai", err)
	var apiErr *APIError
	if !errors.As(got, &apiErr) {
		t.Fatalf("expected *APIError, got %T", got)
	}
	if apiErr.Kind != KindRateLimit {
		t.Errorf("Kind = %v, want KindRateLimit", apiErr.Kind)
	}
}

func TestClassifyAPIError_SSE_NotFound(t *testing.T) {
	err := makeSSEError("not_found_error", "model_not_found", "no such model")
	got := ClassifyAPIError("openai", err)
	var apiErr *APIError
	if !errors.As(got, &apiErr) {
		t.Fatalf("expected *APIError, got %T", got)
	}
	if apiErr.Kind != KindNotFound {
		t.Errorf("Kind = %v, want KindNotFound", apiErr.Kind)
	}
}

func TestClassifyAPIError_SSE_InsufficientQuota(t *testing.T) {
	err := makeSSEError("insufficient_quota", "insufficient_quota", "out of credits")
	got := ClassifyAPIError("openai", err)
	var apiErr *APIError
	if !errors.As(got, &apiErr) {
		t.Fatalf("expected *APIError, got %T", got)
	}
	if apiErr.Kind != KindRateLimit {
		t.Errorf("Kind = %v, want KindRateLimit (quota = rate-limit in spirit)", apiErr.Kind)
	}
}

func TestClassifyAPIError_SSE_InvalidRequest(t *testing.T) {
	err := makeSSEError("invalid_request_error", "context_length_exceeded", "too long")
	got := ClassifyAPIError("openai", err)
	var apiErr *APIError
	if !errors.As(got, &apiErr) {
		t.Fatalf("expected *APIError, got %T", got)
	}
	if apiErr.Kind != KindBadRequest {
		t.Errorf("Kind = %v, want KindBadRequest", apiErr.Kind)
	}
}

func TestClassifyAPIError_SSE_UnknownType(t *testing.T) {
	err := makeSSEError("weird_custom_type", "weird_code", "weird thing")
	got := ClassifyAPIError("openai", err)
	var apiErr *APIError
	if !errors.As(got, &apiErr) {
		t.Fatalf("expected *APIError, got %T", got)
	}
	if apiErr.Kind != KindUnknown {
		t.Errorf("Kind = %v, want KindUnknown (fallback)", apiErr.Kind)
	}
	if !strings.Contains(apiErr.Message, "weird thing") {
		t.Errorf("Message should contain raw message, got %q", apiErr.Message)
	}
}

func TestCodeString(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"route_not_found", "route_not_found"},
		{"", ""},
		{nil, ""},
		{float64(404), "404"},
		{int(429), "429"},
		{int64(500), "500"},
		{true, "true"},
	}
	for _, c := range cases {
		if got := codeString(c.in); got != c.want {
			t.Errorf("codeString(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
