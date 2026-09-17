package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/provider"
)

func TestProviderPresetsEndpoint(t *testing.T) {
	srv, _ := newTestServerWithConfig(t, richTestConfigJSON)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/provider-presets", nil)
	w := httptest.NewRecorder()
	srv.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp ProviderPresetsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Presets) == 0 {
		t.Fatal("presets empty")
	}
	var custom *provider.ProviderPreset
	for i := range resp.Presets {
		if resp.Presets[i].ID == "custom" {
			custom = &resp.Presets[i]
			break
		}
	}
	if custom == nil {
		t.Fatal("custom preset missing")
	}
	var hasResponses bool
	for _, protocol := range custom.Protocols {
		if protocol.ID == provider.ProtocolOpenAIResponses && protocol.EndpointDefaults.OpenAIResponses == "/responses" {
			hasResponses = true
			break
		}
	}
	if !hasResponses {
		t.Fatalf("custom protocols missing OpenAI Responses defaults: %#v", custom.Protocols)
	}
}

func TestProviderStrategyFieldsRoundTrip(t *testing.T) {
	srv, _ := newTestServerWithConfig(t, richTestConfigJSON)

	addBody := `{"name":"direct","provider_id":"openai","strategy_variant":"global","protocol":"openai_responses","base_url":"https://api.openai.com/v1","api_key":"sk","model":"gpt-4.1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/providers", strings.NewReader(addBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.engine.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("add status = %d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/providers/direct", nil)
	w = httptest.NewRecorder()
	srv.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", w.Code, w.Body.String())
	}
	var got ProviderFull
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ProviderID != "openai" || got.StrategyVariant != "global" || got.Protocol != "openai_responses" {
		t.Fatalf("provider strategy fields = %#v", got)
	}

	patchBody := `{"provider_id":"custom","strategy_variant":""}`
	req = httptest.NewRequest(http.MethodPatch, "/api/v1/providers/direct", strings.NewReader(patchBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("patch status = %d body=%s", w.Code, w.Body.String())
	}
	var patched ProviderFull
	if err := json.Unmarshal(w.Body.Bytes(), &patched); err != nil {
		t.Fatal(err)
	}
	if patched.ProviderID != "custom" || patched.StrategyVariant != "" {
		t.Fatalf("patched strategy fields = %#v", patched)
	}
}
