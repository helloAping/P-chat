package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/generation"
)

const generationTestConfigJSON = `{
  "server": {"host":"127.0.0.1","port":8960},
  "llm": {
    "default": "shared",
    "providers": [{
      "name": "shared",
      "vendor": "minimax",
      "protocol": "openai",
      "base_url": "https://api.minimax.io",
      "api_key": "test",
      "models": [
        {"name":"chat","type":"llm","default":true},
        {"name":"MiniMax-H3","type":"media_generation","generation":{"operations":{"text_to_video":{},"image_to_video":{}}}},
        {"name":"image-01","type":"media_generation","generation":{"operations":{"text_to_image":{},"image_to_image":{}}}}
      ]
    }]
  },
  "generation": {
    "defaults": {
      "text_to_video":{"provider":"shared","model":"MiniMax-H3"},
      "text_to_image":{"provider":"shared","model":"image-01"}
    }
  }
}`

func TestSessionGenerationOperationsDefaultOffAndCanBeEnabled(t *testing.T) {
	server, _ := newTestServerWithConfig(t, generationTestConfigJSON)

	create := httptest.NewRecorder()
	server.engine.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/v1/sessions", bytes.NewBufferString(`{}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	var session SessionResponse
	if err := json.Unmarshal(create.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if len(session.EnabledGenerationOperations) != 0 {
		t.Fatalf("new session operations = %#v, want off", session.EnabledGenerationOperations)
	}

	patch := httptest.NewRecorder()
	body := `{"enabled_generation_operations":["text_to_video"],"generation_prompt_assist":false}`
	server.engine.ServeHTTP(patch, httptest.NewRequest(http.MethodPatch, "/api/v1/sessions/"+session.ID, bytes.NewBufferString(body)))
	if patch.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", patch.Code, patch.Body.String())
	}
	var updated SessionResponse
	if err := json.Unmarshal(patch.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if len(updated.EnabledGenerationOperations) != 1 || updated.EnabledGenerationOperations[0] != "text_to_video" {
		t.Fatalf("enabled operations = %#v", updated.EnabledGenerationOperations)
	}
	if updated.GenerationPromptAssist {
		t.Fatal("prompt assist should reflect the explicit false setting")
	}

	options := httptest.NewRecorder()
	server.engine.ServeHTTP(options, httptest.NewRequest(http.MethodGet, "/api/v1/generation/options?session_id="+session.ID, nil))
	if options.Code != http.StatusOK {
		t.Fatalf("options status=%d body=%s", options.Code, options.Body.String())
	}
	var payload struct {
		PromptAssist bool `json:"prompt_assist"`
		Operations   []struct {
			Operation       string            `json:"operation"`
			Enabled         bool              `json:"enabled"`
			Available       bool              `json:"available"`
			EffectiveTarget map[string]string `json:"effective_target"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(options.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.PromptAssist {
		t.Fatal("options prompt_assist should be false")
	}
	found := false
	for _, operation := range payload.Operations {
		if operation.Operation == "text_to_video" {
			found = true
			if !operation.Enabled || !operation.Available || operation.EffectiveTarget["model"] != "MiniMax-H3" {
				t.Fatalf("text_to_video option = %#v", operation)
			}
		}
	}
	if !found {
		t.Fatal("text_to_video option missing")
	}
}

func TestSessionGenerationRejectsModelWithoutCapability(t *testing.T) {
	server, _ := newTestServerWithConfig(t, generationTestConfigJSON)
	create := httptest.NewRecorder()
	server.engine.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/v1/sessions", bytes.NewBufferString(`{}`)))
	var session SessionResponse
	if err := json.Unmarshal(create.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}

	patch := httptest.NewRecorder()
	body := `{"enabled_generation_operations":["text_to_video"],"generation_model_overrides":{"text_to_video":{"provider":"shared","model":"image-01"}}}`
	server.engine.ServeHTTP(patch, httptest.NewRequest(http.MethodPatch, "/api/v1/sessions/"+session.ID, bytes.NewBufferString(body)))
	if patch.Code != http.StatusBadRequest {
		t.Fatalf("patch status=%d want 400; body=%s", patch.Code, patch.Body.String())
	}
}

func TestGeneratedAssetEndpointServesLocalizedMedia(t *testing.T) {
	server, _ := newTestServerWithConfig(t, generationTestConfigJSON)
	store := generation.NewLocalAssetStore(t.TempDir(), "/api/v1/generated")
	server.handler.generatedStore = store
	asset, err := store.Save("session-1", config.MediaImage, "image/png", "generated.png", []byte("png-bytes"))
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	server.engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, asset.URL, nil))
	if response.Code != http.StatusOK || response.Body.String() != "png-bytes" {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("content-type=%q", got)
	}
}
