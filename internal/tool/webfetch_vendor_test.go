package tool

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/search"
)

type stubFetcher struct {
	name    string
	page    search.FetchPage
	err     error
	calls   int
	lastURL string
}

func (s *stubFetcher) Name() string { return s.name }

func (s *stubFetcher) Fetch(_ context.Context, pageURL string) (search.FetchPage, error) {
	s.calls++
	s.lastURL = pageURL
	return s.page, s.err
}

func TestFetchViaVendorRoutes(t *testing.T) {
	stub := &stubFetcher{name: "deepseek", page: search.FetchPage{Title: "Remote", Content: "remote-page"}}
	search.SetGlobalFetcher(stub)
	search.SetFetchQuotaLimit(0)
	t.Cleanup(func() { search.SetGlobalFetcher(search.LocalFetcher{}) })

	got, handled := fetchViaVendor(context.Background(), "https://example.com/docs")
	if !handled || got.IsError || !strings.Contains(got.Content, "# Remote") || !strings.Contains(got.Content, "remote-page") {
		t.Fatalf("result handled=%v %#v", handled, got)
	}

	stub.err = search.ErrFetchFallback
	if _, handled := fetchViaVendor(context.Background(), "https://example.com/docs"); handled {
		t.Fatal("fallback should return to local fetch")
	}

	stub.err = search.ErrAuth
	got, handled = fetchViaVendor(context.Background(), "https://example.com/secure")
	if !handled || !got.IsError || !strings.Contains(got.Content, "E_AUTH") {
		t.Fatalf("auth result handled=%v %#v", handled, got)
	}
}

func TestWebFetchPOSTSkipsVendor(t *testing.T) {
	stub := &stubFetcher{name: "deepseek", page: search.FetchPage{Title: "Remote", Content: "remote-page"}}
	search.SetGlobalFetcher(stub)
	search.SetFetchQuotaLimit(0)
	t.Cleanup(func() { search.SetGlobalFetcher(search.LocalFetcher{}) })

	reg := NewRegistry()
	RegisterBuiltin(reg)
	_, handler, ok := reg.Lookup("web_fetch")
	if !ok {
		t.Fatal("web_fetch missing")
	}
	got, err := handler(context.Background(), []byte(`{"url":"https://example.com/docs"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.IsError || stub.calls != 1 || !strings.Contains(got.Content, "remote-page") {
		t.Fatalf("get result calls=%d %#v", stub.calls, got)
	}

	got, err = handler(context.Background(), []byte(`{"url":"http://example.com:99999","method":"POST","body":"{}"}`))
	if err != nil {
		t.Fatal(err)
	}
	if stub.calls != 1 {
		t.Fatalf("POST called vendor, calls=%d content=%q", stub.calls, got.Content)
	}
	if got.Content == "" {
		t.Fatal(http.StatusText(http.StatusBadRequest))
	}
}
