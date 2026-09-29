package llm

import (
	"strings"
	"testing"
)

func TestRedactDiagnosticText(t *testing.T) {
	input := `{"authorization":"Bearer secret-token","api_key":"sk-secret","choices":[{"delta":{"content":"hello"}}]}`
	got := redactDiagnosticText(input)
	for _, secret := range []string{"secret-token", "sk-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted text leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "hello") {
		t.Fatalf("debug redaction should retain non-secret payload shape: %s", got)
	}
}

func TestRedactDiagnosticTextMalformed(t *testing.T) {
	input := `upstream failed Authorization: Bearer top-secret X-Goog-Api-Key: goog-secret api_key=sk-other content=hello`
	got := redactDiagnosticText(input)
	for _, secret := range []string{"top-secret", "goog-secret", "sk-other"} {
		if strings.Contains(got, secret) {
			t.Fatalf("malformed diagnostic leaked %q: %s", secret, got)
		}
	}
}

func TestRedactDiagnosticTextVendorJSONKey(t *testing.T) {
	got := redactDiagnosticText(`{"x-goog-api-key":"goog-secret","session_token":"token-secret"}`)
	for _, secret := range []string{"goog-secret", "token-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted JSON leaked %q: %s", secret, got)
		}
	}
}

func TestUpstreamHTTPErrorOmitsBody(t *testing.T) {
	t.Setenv("PC_LLM_DEBUG", "0")
	err := upstreamHTTPError("openai", 400, []byte(`{"error":{"message":"echoed user prompt","api_key":"sk-secret"}}`))
	got := err.Error()
	if strings.Contains(got, "echoed user prompt") || strings.Contains(got, "sk-secret") {
		t.Fatalf("default HTTP error leaked upstream body: %s", got)
	}
	if !strings.Contains(got, "openai http 400") || !strings.Contains(got, "sha256=") {
		t.Fatalf("HTTP error lost classification metadata: %s", got)
	}
}
