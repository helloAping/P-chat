package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListModelsRemoteOpenAIShape(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Provider-Test"); got != "model-list" {
			t.Fatalf("X-Provider-Test = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "gpt-4o-mini", "created": 1, "owned_by": "openai"},
				{"id": "gpt-4o", "created": 2, "owned_by": "openai"},
			},
		})
	}))
	t.Cleanup(upstream.Close)

	result, status, err := ListModels(context.Background(), ModelListRequest{
		ProviderID:    "openai",
		Protocol:      string(ProtocolOpenAIChat),
		BaseURL:       upstream.URL + "/v1",
		APIKey:        "sk-test",
		CustomHeaders: map[string]string{"X-Provider-Test": "model-list"},
		Existing:      map[string]bool{"gpt-4o": true},
	})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if result.Source != "remote" || result.Endpoint != "/models" {
		t.Fatalf("result metadata = %#v", result)
	}
	if len(result.Models) != 2 || result.Models[0].ID != "gpt-4o-mini" || !result.Models[1].Added {
		t.Fatalf("models = %#v", result.Models)
	}
	if result.Models[0].DefaultEndpoint != "/chat/completions" {
		t.Fatalf("default endpoint = %q", result.Models[0].DefaultEndpoint)
	}
}

func TestListModelsCustomRemoteFailureAllowsManual(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no models here", http.StatusNotFound)
	}))
	t.Cleanup(upstream.Close)

	result, status, err := ListModels(context.Background(), ModelListRequest{
		ProviderID: "custom",
		Protocol:   string(ProtocolOpenAIResponses),
		BaseURL:    upstream.URL + "/v1",
		APIKey:     "sk-test",
	})
	if err != nil {
		t.Fatalf("manual-capable provider should not fail hard: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if result.Source != "manual" || result.RemoteError == "" || !result.ManualAllowed {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Models) != 0 {
		t.Fatalf("models = %#v", result.Models)
	}
}

func TestListModelsRemoteFailureReturnsStaticFallback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "models unavailable", http.StatusNotFound)
	}))
	t.Cleanup(upstream.Close)

	result, status, err := ListModels(context.Background(), ModelListRequest{
		ProviderID: "deepseek",
		Protocol:   string(ProtocolOpenAIChat),
		BaseURL:    upstream.URL,
		APIKey:     "sk-test",
		Existing:   map[string]bool{"deepseek-v4-pro": true},
	})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if result.Source != "static" || result.RemoteError == "" {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Models) < 2 || result.Models[0].Source != "static" {
		t.Fatalf("models = %#v", result.Models)
	}
	var foundAdded bool
	for _, model := range result.Models {
		if model.ID == "deepseek-v4-pro" {
			foundAdded = model.Added
		}
	}
	if !foundAdded {
		t.Fatalf("fallback models did not mark existing model: %#v", result.Models)
	}
}

func TestListModelsUsesVariantDefaultBaseURL(t *testing.T) {
	result, status, err := ListModels(context.Background(), ModelListRequest{
		ProviderID:      "volcengine",
		StrategyVariant: "coding_plan",
		Protocol:        string(ProtocolOpenAIChat),
		APIKey:          "",
	})
	if err == nil {
		t.Fatal("expected missing api_key error")
	}
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d", status)
	}
	if result.BaseURL != "https://ark.cn-beijing.volces.com/api/coding/v3" {
		t.Fatalf("base_url = %q", result.BaseURL)
	}
}

func TestListModelsParsesAnthropicShape(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "claude-sonnet-4-5", "type": "model", "display_name": "Claude Sonnet 4.5", "created_at": "2026-09-01T00:00:00Z"},
			},
		})
	}))
	t.Cleanup(upstream.Close)

	result, _, err := ListModels(context.Background(), ModelListRequest{
		ProviderID: "anthropic",
		Protocol:   string(ProtocolAnthropicMessages),
		BaseURL:    upstream.URL + "/v1",
		APIKey:     "sk-ant",
	})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(result.Models) != 1 || result.Models[0].ID != "claude-sonnet-4-5" {
		t.Fatalf("models = %#v", result.Models)
	}
	if result.Models[0].Created == 0 || result.Models[0].DisplayName != "Claude Sonnet 4.5" {
		t.Fatalf("anthropic metadata = %#v", result.Models[0])
	}
}
