package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestModelProtocolAPIAddsOverrideAndClearsToInheritance(t *testing.T) {
	server, _ := newTestServerWithConfig(t, `{"llm":{"default":"mixed","providers":[{"name":"mixed","protocol":"openai_chat","base_url":"https://example.com/v1","api_key":"k"}]}}`)

	add := httptest.NewRecorder()
	addRequest := httptest.NewRequest(http.MethodPost, "/api/v1/providers/mixed/models", bytes.NewBufferString(`{"name":"claude","type":"llm","protocol":"anthropic_messages"}`))
	addRequest.Header.Set("Content-Type", "application/json")
	server.engine.ServeHTTP(add, addRequest)
	if add.Code != http.StatusCreated {
		t.Fatalf("add status = %d, body=%s", add.Code, add.Body.String())
	}

	model := fetchProviderModel(t, server, "mixed", "claude")
	if model["protocol"] != "anthropic_messages" || model["api_endpoint"] != "/messages" {
		t.Fatalf("added model = %#v", model)
	}

	update := httptest.NewRecorder()
	updateRequest := httptest.NewRequest(http.MethodPut, "/api/v1/providers/mixed/models/claude", bytes.NewBufferString(`{"protocol":""}`))
	updateRequest.Header.Set("Content-Type", "application/json")
	server.engine.ServeHTTP(update, updateRequest)
	if update.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", update.Code, update.Body.String())
	}

	model = fetchProviderModel(t, server, "mixed", "claude")
	if protocol, exists := model["protocol"]; exists && protocol != "" {
		t.Fatalf("cleared protocol = %#v", protocol)
	}
	if model["api_endpoint"] != "/chat/completions" {
		t.Fatalf("inherited endpoint = %#v", model["api_endpoint"])
	}
}

func fetchProviderModel(t *testing.T, server *Server, providerName, modelName string) map[string]any {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/providers/"+providerName, nil)
	server.engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("get provider status = %d, body=%s", response.Code, response.Body.String())
	}
	var provider struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.NewDecoder(response.Body).Decode(&provider); err != nil {
		t.Fatal(err)
	}
	for _, model := range provider.Models {
		if model["name"] == modelName {
			return model
		}
	}
	t.Fatalf("model %q not found in %#v", modelName, provider.Models)
	return nil
}
