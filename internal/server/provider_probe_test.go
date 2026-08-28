package server

import (
	"encoding/json"
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
