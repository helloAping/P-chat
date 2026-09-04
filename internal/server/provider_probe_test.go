package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDefaultProbeBaseURL(t *testing.T) {
	if got := defaultProbeBaseURL("openai", ""); got != "https://api.openai.com/v1" {
		t.Fatalf("openai default = %q", got)
	}
	if got := defaultProbeBaseURL("anthropic", ""); got != "https://api.anthropic.com/v1" {
		t.Fatalf("anthropic default = %q", got)
	}
	if got := defaultProbeBaseURL("openai", " https://proxy.example/v1 "); got != "https://proxy.example/v1" {
		t.Fatalf("explicit base = %q", got)
	}
}

func TestProbeUpstreamModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "gpt-4o-mini", "created": 1, "owned_by": "openai"},
				{"id": "gpt-4o", "created": 2, "owned_by": "openai"},
			},
		})
	}))
	t.Cleanup(upstream.Close)

	h := &Handler{}
	r := gin.New()
	r.POST("/api/v1/providers/probe-models", h.ProbeUpstreamModels)

	body := `{"base_url":"` + upstream.URL + `/v1","api_key":"sk-test","protocol":"openai"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/providers/probe-models", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Models []UpstreamModelsItem `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Models) != 2 || resp.Models[0].ID != "gpt-4o-mini" {
		t.Fatalf("models = %#v", resp.Models)
	}
}

func TestProbeUpstreamModelsRequiresAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{}
	r := gin.New()
	r.POST("/api/v1/providers/probe-models", h.ProbeUpstreamModels)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/providers/probe-models", strings.NewReader(`{"protocol":"openai"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestProviderConnectionUsesDefaultModelAndSaysHi(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotModel string
	var gotPrompt string
	var gotMaxTokens int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		var body struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			Messages  []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotModel = body.Model
		gotMaxTokens = body.MaxTokens
		if len(body.Messages) == 1 {
			switch content := body.Messages[0].Content.(type) {
			case string:
				gotPrompt = content
			case []any:
				if len(content) == 1 {
					if part, ok := content[0].(map[string]any); ok {
						gotPrompt, _ = part["text"].(string)
					}
				}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": "hi from default"},
			}},
		})
	}))
	t.Cleanup(upstream.Close)

	cfg := fmt.Sprintf(`{
		"llm": {
			"default": "test-provider",
			"providers": [{
				"name": "test-provider",
				"protocol": "openai",
				"base_url": %q,
				"api_key": "sk-test",
				"models": [
					{"name": "model-a"},
					{"name": "model-default", "default": true, "max_tokens_output": 4096}
				]
			}]
		}
	}`, upstream.URL+"/v1")
	srv, _ := newTestServerWithConfig(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/providers/test-provider/test", nil)
	w := httptest.NewRecorder()
	srv.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if gotModel != "model-default" {
		t.Fatalf("upstream model = %q, want model-default", gotModel)
	}
	if gotPrompt != "sayhi" {
		t.Fatalf("upstream prompt = %q, want sayhi", gotPrompt)
	}
	if gotMaxTokens != 64 {
		t.Fatalf("upstream max_tokens = %d, want 64", gotMaxTokens)
	}
	var resp struct {
		OK       bool   `json:"ok"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Response string `json:"response"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || resp.Provider != "test-provider" || resp.Model != "model-default" || resp.Response != "hi from default" {
		t.Fatalf("response = %#v", resp)
	}

	gotModel = ""
	gotPrompt = ""
	req = httptest.NewRequest(http.MethodPost, "/api/v1/providers/test-provider/test", strings.NewReader(`{"model":"model-a"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("explicit model status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if gotModel != "model-a" || gotPrompt != "sayhi" {
		t.Fatalf("explicit upstream request model=%q prompt=%q", gotModel, gotPrompt)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode explicit model response: %v", err)
	}
	if !resp.OK || resp.Model != "model-a" {
		t.Fatalf("explicit model response = %#v", resp)
	}
}
