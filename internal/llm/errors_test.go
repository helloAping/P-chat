package llm

import (
	"context"
	"errors"
	"fmt"
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
