package search

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/config"
)

func TestDeepSeekSearchMapsSummaryAndHints(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tools/search" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		got = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"T","url":"https://example.com/a","snippet":"short","summary":"long summary","date":"2026-06-01T00:00:00Z","rerank_score":2.4},
			{"title":"skip","url":"","snippet":"x","summary":"y"}
		]}`))
	}))
	defer srv.Close()

	p := &DeepSeekProvider{APIKey: "secret", BaseURL: srv.URL, Timeout: tavilyDefaultTimeout}
	results, err := p.Search(context.Background(), Query{
		Query:       strings.Repeat("查", 140),
		MaxResults:  3,
		Language:    "zh",
		Site:        "example.com",
		RecencyDays: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"need_content":false`) || !strings.Contains(got, `"count":3`) {
		t.Fatalf("body = %s", got)
	}
	if !strings.Contains(got, "site:example.com") || !strings.Contains(got, "lang:zh") || !strings.Contains(got, "after:") {
		t.Fatalf("hints missing: %s", got)
	}
	if n := len([]rune(extractQuery(t, got))); n > 128 || n == 0 {
		t.Fatalf("query rune len = %d body=%s", n, got)
	}
	if len(results) != 1 || results[0].Snippet != "long summary" || results[0].Score != 2.4 {
		t.Fatalf("results = %#v", results)
	}
}

func TestDeepSeekFetchFallbackAndSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/empty":
			_, _ = w.Write([]byte(`{"title":"T","content":"  "}`))
		case "/down":
			w.WriteHeader(http.StatusBadGateway)
		case "/deny":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			_, _ = w.Write([]byte(`{"title":"Example","content":"# Hello"}`))
		}
	}))
	defer srv.Close()

	okFetcher := &DeepSeekFetcher{APIKey: "k", BaseURL: srv.URL, Path: "/", Timeout: tavilyDefaultTimeout}
	page, err := okFetcher.Fetch(context.Background(), "https://example.com")
	if err != nil || page.Title != "Example" || page.Content != "# Hello" {
		t.Fatalf("page=%#v err=%v", page, err)
	}

	empty := &DeepSeekFetcher{APIKey: "k", BaseURL: srv.URL, Path: "/empty", Timeout: tavilyDefaultTimeout}
	if _, err := empty.Fetch(context.Background(), "https://example.com"); !errors.Is(err, ErrFetchFallback) {
		t.Fatalf("empty err = %v", err)
	}
	down := &DeepSeekFetcher{APIKey: "k", BaseURL: srv.URL, Path: "/down", Timeout: tavilyDefaultTimeout}
	if _, err := down.Fetch(context.Background(), "https://example.com"); !errors.Is(err, ErrFetchFallback) {
		t.Fatalf("down err = %v", err)
	}
	deny := &DeepSeekFetcher{APIKey: "k", BaseURL: srv.URL, Path: "/deny", Timeout: tavilyDefaultTimeout}
	if _, err := deny.Fetch(context.Background(), "https://example.com"); !errors.Is(err, ErrAuth) {
		t.Fatalf("deny err = %v", err)
	}
}

func TestMiMoSearchReadsCitations(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		got = string(body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"annotations":[{"type":"url_citation","url":"https://example.com/a","title":"A","summary":"sum","publish_time":"2026-01-02"},{"type":"url_citation","url":"https://example.com/a","title":"dup"}]}}]}`))
	}))
	defer srv.Close()

	p := &MiMoProvider{APIKey: "k", BaseURL: srv.URL, Timeout: tavilyDefaultTimeout}
	results, err := p.Search(context.Background(), Query{Query: "天气", MaxResults: 5, Site: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"max_keyword":1`) || !strings.Contains(got, `"force_search":true`) {
		t.Fatalf("body = %s", got)
	}
	if !strings.Contains(got, "mimo-v2.6-flash") || !strings.Contains(got, "site:example.com") {
		t.Fatalf("body = %s", got)
	}
	if len(results) != 1 || results[0].URL != "https://example.com/a" || results[0].Snippet != "sum" {
		t.Fatalf("results = %#v", results)
	}
}

func TestQwenSearchAndFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		text := string(body)
		if strings.Contains(text, "web_extractor") {
			if strings.Contains(text, "https://other.example") {
				_, _ = w.Write([]byte(`{"output":[{"type":"web_extractor_call","action":{"url":"https://elsewhere.example","content":"wrong"}}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"output":[{"type":"web_extractor_call","result":{"url":"https://example.com/a","title":"Page","content":"body"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"output":[{"type":"web_search_call","action":{"sources":[{"url":"https://example.com/a","title":"A","snippet":"s"}]}}]}`))
	}))
	defer srv.Close()

	p := &QwenProvider{APIKey: "k", BaseURL: srv.URL, Model: "qwen3.8-flash", Timeout: tavilyDefaultTimeout}
	results, err := p.Search(context.Background(), Query{Query: "杭州天气", MaxResults: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "A" || results[0].Snippet != "s" {
		t.Fatalf("results = %#v", results)
	}

	f := &QwenFetcher{APIKey: "k", BaseURL: srv.URL, Timeout: tavilyDefaultTimeout}
	if _, err := f.Fetch(context.Background(), "https://other.example"); !errors.Is(err, ErrFetchFallback) {
		t.Fatalf("mismatch err = %v", err)
	}
	page, err := f.Fetch(context.Background(), "https://example.com/a")
	if err != nil || page.Title != "Page" || page.Content != "body" {
		t.Fatalf("page=%#v err=%v", page, err)
	}
}

func TestBuildProviderAndFetcher(t *testing.T) {
	if got := BuildProvider(config.SearchConfig{Enabled: true, Provider: "deepseek"}).Name(); got != "disabled" {
		t.Fatalf("deepseek without key = %s", got)
	}
	if got := BuildProvider(config.SearchConfig{Enabled: true, Provider: "deepseek", APIKey: "k"}).Name(); got != "deepseek" {
		t.Fatalf("deepseek = %s", got)
	}
	if got := BuildProvider(config.SearchConfig{Enabled: true, Provider: "mimo", APIKey: "k"}).Name(); got != "mimo" {
		t.Fatalf("mimo = %s", got)
	}
	if got := BuildProvider(config.SearchConfig{Enabled: true, Provider: "qwen", APIKey: "k"}).Name(); got != "disabled" {
		t.Fatalf("qwen without base = %s", got)
	}
	if got := BuildFetcher(config.FetchConfig{}).Name(); got != "local" {
		t.Fatalf("default fetch = %s", got)
	}
	if got := BuildFetcher(config.FetchConfig{Provider: "qwen", APIKey: "k", BaseURL: "https://example.com/v1"}).Name(); got != "qwen" {
		t.Fatalf("qwen fetch = %s", got)
	}
}

func extractQuery(t *testing.T, body string) string {
	t.Helper()
	var payload struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Query
}
