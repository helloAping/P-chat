package tool

import (
	"encoding/json"
	"testing"
)

func TestDecodeWebFetchArgs(t *testing.T) {
	url, method, body, err := DecodeWebFetchArgs(json.RawMessage(`{"url":"https://example.com/doc","method":"post","body":"{}"}`))
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://example.com/doc" || method != "post" || body != "{}" {
		t.Fatalf("got url=%q method=%q body=%q", url, method, body)
	}
	if got := NormalizeWebFetchMethod(""); got != "GET" {
		t.Fatalf("empty method = %q, want GET", got)
	}
	if got := NormalizeWebFetchMethod("post"); got != "POST" {
		t.Fatalf("post method = %q, want POST", got)
	}
}

func TestPublicWebFetchURLError(t *testing.T) {
	if err := PublicWebFetchURLError("https://example.com/docs"); err != nil {
		t.Fatalf("public hostname: %v", err)
	}
	if err := PublicWebFetchURLError("https://8.8.8.8/lookup"); err != nil {
		t.Fatalf("public IP: %v", err)
	}
	blocked := []string{
		"http://127.0.0.1/",
		"https://127.0.0.1/secret",
		"http://localhost/admin",
		"https://[::1]/",
		"http://169.254.169.254/latest/meta-data",
		"http://10.0.0.1/",
		"https://192.168.1.1/",
		"http://172.16.0.1/",
		"ftp://example.com/",
		"https://0.0.0.0/",
	}
	for _, raw := range blocked {
		if err := PublicWebFetchURLError(raw); err == nil {
			t.Errorf("PublicWebFetchURLError(%q) = nil, want error", raw)
		}
	}
}
