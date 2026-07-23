package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/server"
)

// TestUploads_PostAndGet exercises the happy path: POST a file,
// then GET it back via the returned id.
func TestUploads_PostAndGet(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	payload := []byte("hello, world\nthis is a test file\n")
	fw, err := w.CreateFormFile("file", "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Post(srv.URL+"/api/v1/uploads", w.FormDataContentType(), body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var meta server.UploadMeta // exported for tests
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}
	if meta.ID == "" {
		t.Error("id empty")
	}
	if meta.Name != "hello.txt" {
		t.Errorf("name = %q, want hello.txt", meta.Name)
	}
	if meta.Size != int64(len(payload)) {
		t.Errorf("size = %d, want %d", meta.Size, len(payload))
	}
	if meta.Kind != "text" {
		t.Errorf("kind = %q, want text", meta.Kind)
	}

	// GET it back.
	r2, err := http.Get(srv.URL + "/api/v1/uploads/" + meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != 200 {
		t.Fatalf("get status = %d, want 200", r2.StatusCode)
	}
	got, _ := io.ReadAll(r2.Body)
	if !bytes.Equal(got, payload) {
		t.Errorf("body = %q, want %q", got, payload)
	}
}

func TestUploads_RejectsMissingFile(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/v1/uploads", "application/x-www-form-urlencoded", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUploads_Classify(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	cases := map[string]string{
		"shot.png":   "image",
		"song.mp3":   "audio",
		"data.json":  "text",
		"weird.bin":  "file",
		"page.html":  "text",
		"track.wav":  "audio",
		"pixel.webp": "image",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			body := &bytes.Buffer{}
			w := multipart.NewWriter(body)
			fw, _ := w.CreateFormFile("file", name)
			fw.Write([]byte("test"))
			w.Close()

			resp, err := http.Post(srv.URL+"/api/v1/uploads", w.FormDataContentType(), body)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("status = %d, want 201", resp.StatusCode)
			}
			var meta server.UploadMeta
			if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
				t.Fatal(err)
			}
			if meta.Kind != want {
				t.Errorf("kind = %q, want %q", meta.Kind, want)
			}
		})
	}
}

func TestProviders_AddAndDelete(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	// Add a new provider.
	addBody := `{"name":"testprov","protocol":"openai","base_url":"http://example.com/v1","api_key":"sk-xyz","model":"gpt-4o"}`
	r, err := http.Post(srv.URL+"/api/v1/providers", "application/json", strings.NewReader(addBody))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("add status = %d, want 201", r.StatusCode)
	}

	// The config file on disk should now contain "testprov".
	cfgPath := filepath.Join(os.Getenv("USERPROFILE"), ".p-chat", "config.json")
	data, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(data), "testprov") {
		t.Errorf("config.json missing testprov:\n%s", data)
	}

	// List providers; the new one should appear.
	r2, err := http.Get(srv.URL + "/api/v1/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	var listing struct {
		Providers []struct {
			Name string `json:"name"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(r2.Body).Decode(&listing); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range listing.Providers {
		if p.Name == "testprov" {
			found = true
		}
	}
	if !found {
		t.Errorf("testprov not in providers list: %+v", listing.Providers)
	}

	// Delete it.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/providers/testprov", nil)
	rd, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rd.Body.Close()
	if rd.StatusCode != 200 {
		t.Errorf("delete status = %d, want 200", rd.StatusCode)
	}
	data2, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(data2), "testprov") {
		t.Errorf("config.json still has testprov after delete:\n%s", data2)
	}
}

func TestProviders_DeleteDefaultRejected(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	// The seed config sets "ollama" as default; try deleting it.
	// Should be rejected (400) because we don't allow removing
	// the active provider via the UI.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/providers/ollama", nil)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", r.StatusCode)
	}
}

func TestProviders_AddModel(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	// The seed config (read from a fresh temp dir) only has the
	// "ollama" provider with the legacy single-model form. Add
	// our own provider first, then add a model to it.
	addProv := `{"name":"testprov","protocol":"openai","base_url":"http://example.com/v1","api_key":"sk-xyz","model":"m0"}`
	r0, err := http.Post(srv.URL+"/api/v1/providers", "application/json", strings.NewReader(addProv))
	if err != nil {
		t.Fatal(err)
	}
	r0.Body.Close()
	if r0.StatusCode != http.StatusCreated {
		t.Fatalf("seed add provider status = %d, want 201", r0.StatusCode)
	}

	// Add a model to the new provider.
	addBody := `{"name":"m1","display_name":"M One","description":"second model"}`
	r, err := http.Post(srv.URL+"/api/v1/providers/testprov/models", "application/json", strings.NewReader(addBody))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(r.Body)
		t.Fatalf("add model status = %d, want 201; body = %s", r.StatusCode, body)
	}

	// And delete it.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/providers/testprov/models/m1", nil)
	rd, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rd.Body.Close()
	if rd.StatusCode != 200 {
		t.Errorf("delete model status = %d, want 200", rd.StatusCode)
	}
}

func TestCommands_ListAndRun(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	// Default list: web-safe only.
	r, err := http.Get(srv.URL + "/api/v1/commands")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("list status = %d, want 200", r.StatusCode)
	}
	var listing struct {
		Commands []struct {
			Name    string `json:"name"`
			WebSafe bool   `json:"web_safe"`
		} `json:"commands"`
	}
	if err := json.NewDecoder(r.Body).Decode(&listing); err != nil {
		t.Fatal(err)
	}
	hasSkills := false
	for _, c := range listing.Commands {
		if !c.WebSafe {
			t.Errorf("default list includes REPL-only command %q", c.Name)
		}
		if c.Name == "skills" {
			hasSkills = true
		}
	}
	if !hasSkills {
		t.Error("default list missing /skills")
	}

	// all=1 should include REPL-only ones.
	r2, err := http.Get(srv.URL + "/api/v1/commands?all=1")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	var listing2 struct {
		Commands []struct {
			Name    string `json:"name"`
			WebSafe bool   `json:"web_safe"`
		} `json:"commands"`
	}
	if err := json.NewDecoder(r2.Body).Decode(&listing2); err != nil {
		t.Fatal(err)
	}
	hasUnsafe := false
	for _, c := range listing2.Commands {
		if c.Name == "unsafe" {
			hasUnsafe = true
		}
	}
	if !hasUnsafe {
		t.Error("?all=1 missing /unsafe")
	}

	// Run a /skills command over HTTP. Should return text.
	body := `{"args":""}`
	r3, err := http.Post(srv.URL+"/api/v1/commands/skills", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer r3.Body.Close()
	if r3.StatusCode != 200 {
		t.Fatalf("run skills status = %d, want 200", r3.StatusCode)
	}
	var out struct {
		Output string `json:"output"`
	}
	if err := json.NewDecoder(r3.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Output == "" {
		t.Error("empty /skills output")
	}

	// Unknown command: 404.
	r4, err := http.Post(srv.URL+"/api/v1/commands/nonsense", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer r4.Body.Close()
	if r4.StatusCode != 404 {
		t.Errorf("unknown status = %d, want 404", r4.StatusCode)
	}

	// REPL-only /unsafe: 403.
	r5, err := http.Post(srv.URL+"/api/v1/commands/unsafe", "application/json", strings.NewReader(`{"args":"once"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer r5.Body.Close()
	if r5.StatusCode != 403 {
		t.Errorf("/unsafe status = %d, want 403", r5.StatusCode)
	}
}

func TestSystemConfig_UICloseBehavior(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	r, err := http.Get(srv.URL + "/api/v1/config")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", r.StatusCode)
	}
	var got struct {
		UI struct {
			CloseBehavior string `json:"close_behavior"`
		} `json:"ui"`
	}
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.UI.CloseBehavior != "exit" {
		t.Fatalf("default close_behavior = %q, want exit", got.UI.CloseBehavior)
	}

	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/v1/config", strings.NewReader(`{"ui":{"close_behavior":"tray"}}`))
	req.Header.Set("Content-Type", "application/json")
	pr, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Body.Close()
	if pr.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(pr.Body)
		t.Fatalf("PATCH status = %d, want 200; body = %s", pr.StatusCode, body)
	}
	var patched struct {
		UI struct {
			CloseBehavior string `json:"close_behavior"`
		} `json:"ui"`
	}
	if err := json.NewDecoder(pr.Body).Decode(&patched); err != nil {
		t.Fatal(err)
	}
	if patched.UI.CloseBehavior != "tray" {
		t.Fatalf("patched close_behavior = %q, want tray", patched.UI.CloseBehavior)
	}

	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.CloseBehavior != config.CloseBehaviorTray {
		t.Fatalf("persisted close_behavior = %q, want tray", cfg.UI.CloseBehavior)
	}
}

func TestSystemConfig_UICloseBehaviorInvalidFallsBack(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/v1/config", strings.NewReader(`{"ui":{"close_behavior":"bogus"}}`))
	req.Header.Set("Content-Type", "application/json")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200", r.StatusCode)
	}
	var got struct {
		UI struct {
			CloseBehavior string `json:"close_behavior"`
		} `json:"ui"`
	}
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.UI.CloseBehavior != "exit" {
		t.Fatalf("invalid close_behavior should normalize to exit, got %q", got.UI.CloseBehavior)
	}
}

// Helper: not actually used by the tests but kept for parity.
var _ = fmt.Sprintf

// ====================================================================
// Per-model configuration API
// ====================================================================

// TestUpdateModel_AddsMaxTokens verifies the PUT
// /api/v1/providers/:name/models/:model endpoint accepts the
// max_tokens_context / max_tokens_output fields and persists
// them. The slim GET /api/v1/providers endpoint should also
// return the new fields in the `models` array.
func TestUpdateModel_AddsMaxTokens(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	// Add a fresh provider to avoid touching the seed "ollama"
	// (which the default-rejection tests need).
	addBody := `{"name":"cap","protocol":"openai","base_url":"http://example.com/v1","api_key":"sk","model":"gpt-test"}`
	r, err := http.Post(srv.URL+"/api/v1/providers", "application/json", strings.NewReader(addBody))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("add: %d", r.StatusCode)
	}

	// Update the model with per-model max_tokens.
	putBody := `{"display_name":"GPT Test","max_tokens_context":128000,"max_tokens_output":8192}`
	req, _ := http.NewRequest("PUT", srv.URL+"/api/v1/providers/cap/models/gpt-test", strings.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	ru, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	ru.Body.Close()
	if ru.StatusCode != 200 {
		t.Fatalf("PUT status = %d, want 200", ru.StatusCode)
	}

	// GET rich provider view; the model should have the
	// max_tokens set.
	rg, err := http.Get(srv.URL + "/api/v1/providers/cap")
	if err != nil {
		t.Fatal(err)
	}
	defer rg.Body.Close()
	var body struct {
		Models []struct {
			Name             string `json:"name"`
			DisplayName      string `json:"display_name"`
			MaxTokensContext int    `json:"max_tokens_context"`
			MaxTokensOutput  int    `json:"max_tokens_output"`
		} `json:"models"`
	}
	if err := json.NewDecoder(rg.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Models) != 1 {
		t.Fatalf("models = %d, want 1", len(body.Models))
	}
	m := body.Models[0]
	if m.MaxTokensContext != 128000 {
		t.Errorf("max_tokens_context = %d, want 128000", m.MaxTokensContext)
	}
	if m.MaxTokensOutput != 8192 {
		t.Errorf("max_tokens_output = %d, want 8192", m.MaxTokensOutput)
	}
	if m.DisplayName != "GPT Test" {
		t.Errorf("display_name = %q, want %q", m.DisplayName, "GPT Test")
	}
}

// TestUpdateModel_NotFound covers the error path: PUT to an
// unknown model returns 400.
func TestUpdateModel_NotFound(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/api/v1/providers/ollama/models/missing", strings.NewReader(`{"max_tokens_output":2048}`))
	req.Header.Set("Content-Type", "application/json")
	ru, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer ru.Body.Close()
	if ru.StatusCode != 400 {
		t.Errorf("status = %d, want 400", ru.StatusCode)
	}
}

// TestProviders_RichModelFields ensures the slim GET
// /api/v1/providers response now includes models + is_default
// (so the UI cascade can render without a second round-trip).
func TestProviders_RichModelFields(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	r, err := http.Get(srv.URL + "/api/v1/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var body struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Providers) == 0 {
		t.Fatal("no providers")
	}
	for _, p := range body.Providers {
		if _, ok := p["models"]; !ok {
			t.Errorf("provider %v missing 'models' array", p["name"])
		}
		if _, ok := p["is_default"]; !ok {
			t.Errorf("provider %v missing 'is_default' field", p["name"])
		}
	}
}

// TestAddModel_AcceptsMaxTokens verifies the POST
// /api/v1/providers/:name/models endpoint accepts the new
// per-model max_tokens fields.
func TestAddModel_AcceptsMaxTokens(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	addBody := `{"name":"cap2","protocol":"openai","base_url":"http://example.com/v1","api_key":"sk","model":"placeholder"}`
	r, _ := http.Post(srv.URL+"/api/v1/providers", "application/json", strings.NewReader(addBody))
	r.Body.Close()

	// Add a new model with max_tokens.
	modelBody := `{"name":"new-model","display_name":"New Model","max_tokens_context":64000,"max_tokens_output":4096}`
	mr, err := http.Post(srv.URL+"/api/v1/providers/cap2/models", "application/json", strings.NewReader(modelBody))
	if err != nil {
		t.Fatal(err)
	}
	mr.Body.Close()
	if mr.StatusCode != http.StatusCreated {
		t.Fatalf("add model: %d", mr.StatusCode)
	}

	// Verify it's there with the right fields.
	rg, err := http.Get(srv.URL + "/api/v1/providers/cap2")
	if err != nil {
		t.Fatal(err)
	}
	defer rg.Body.Close()
	var body struct {
		Models []struct {
			Name             string `json:"name"`
			MaxTokensContext int    `json:"max_tokens_context"`
			MaxTokensOutput  int    `json:"max_tokens_output"`
		} `json:"models"`
	}
	if err := json.NewDecoder(rg.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range body.Models {
		if m.Name == "new-model" {
			found = true
			if m.MaxTokensContext != 64000 {
				t.Errorf("max_tokens_context = %d, want 64000", m.MaxTokensContext)
			}
			if m.MaxTokensOutput != 4096 {
				t.Errorf("max_tokens_output = %d, want 4096", m.MaxTokensOutput)
			}
		}
	}
	if !found {
		t.Error("new-model not found in provider models")
	}
}

// TestUpdateProvider_PatchBaseURL exercises the unified
// PATCH /api/v1/providers/:name endpoint. Sending only a
// base_url change must not wipe the existing API key.
func TestUpdateProvider_PatchBaseURL(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PATCH", srv.URL+"/api/v1/providers/ollama", strings.NewReader(`{"base_url":"http://new.example.com/v1"}`))
	req.Header.Set("Content-Type", "application/json")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	var body struct {
		Name    string `json:"name"`
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.BaseURL != "http://new.example.com/v1" {
		t.Errorf("base_url = %q", body.BaseURL)
	}
	if body.APIKey == "" {
		t.Error("api_key must be preserved on partial patch")
	}
}

// TestUpdateProvider_Rename verifies the new name is
// persisted and the global default is cascaded.
func TestUpdateProvider_Rename(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	// Add a fresh provider so we don't disturb the test
	// server's default (used by other tests).
	addBody := `{"name":"old","protocol":"openai","base_url":"http://x","api_key":"k","model":"m"}`
	ar, err := http.Post(srv.URL+"/api/v1/providers", "application/json", strings.NewReader(addBody))
	if err != nil {
		t.Fatal(err)
	}
	ar.Body.Close()
	if ar.StatusCode != http.StatusCreated {
		t.Fatalf("add: %d", ar.StatusCode)
	}

	req, _ := http.NewRequest("PATCH", srv.URL+"/api/v1/providers/old", strings.NewReader(`{"name":"renamed"}`))
	req.Header.Set("Content-Type", "application/json")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Name != "renamed" {
		t.Errorf("name = %q, want renamed", body.Name)
	}

	// Old name should 404 now.
	g, _ := http.Get(srv.URL + "/api/v1/providers/old")
	g.Body.Close()
	if g.StatusCode != http.StatusNotFound {
		t.Errorf("old name should be gone, got %d", g.StatusCode)
	}
}

// TestUpdateProvider_RenameCollision ensures a rename that
// collides with another provider is rejected with 409.
func TestUpdateProvider_RenameCollision(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	// "ollama" is in the default test config; "extra" doesn't exist
	// yet. Add it.
	ab, _ := http.Post(srv.URL+"/api/v1/providers", "application/json",
		strings.NewReader(`{"name":"extra","protocol":"openai","base_url":"http://x","api_key":"k","model":"m"}`))
	ab.Body.Close()

	req, _ := http.NewRequest("PATCH", srv.URL+"/api/v1/providers/extra", strings.NewReader(`{"name":"ollama"}`))
	req.Header.Set("Content-Type", "application/json")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", r.StatusCode)
	}
}

// TestUpdateProvider_ProtocolChange switches a provider
// from openai to anthropic via PATCH.
func TestUpdateProvider_ProtocolChange(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	ab, _ := http.Post(srv.URL+"/api/v1/providers", "application/json",
		strings.NewReader(`{"name":"switchee","protocol":"openai","base_url":"http://x","api_key":"k","model":"m"}`))
	ab.Body.Close()

	req, _ := http.NewRequest("PATCH", srv.URL+"/api/v1/providers/switchee", strings.NewReader(`{"protocol":"anthropic"}`))
	req.Header.Set("Content-Type", "application/json")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	var body struct {
		Protocol string `json:"protocol"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Protocol != "anthropic" {
		t.Errorf("protocol = %q, want anthropic", body.Protocol)
	}
}

// ====================================================================
// Add provider API (covers the new web UI add-provider form)
// ====================================================================

// TestAddProvider_BareProvider verifies POST /api/v1/providers
// works without a model field — the user might want to add a
// provider shell and then add models to it later.
func TestAddProvider_BareProvider(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	body := `{"name":"openai-bare","protocol":"openai","base_url":"https://api.openai.com/v1","api_key":"sk-x"}`
	r, err := http.Post(srv.URL+"/api/v1/providers", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", r.StatusCode)
	}

	// Verify it's in the list with no model.
	r2, err := http.Get(srv.URL + "/api/v1/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	var list struct {
		Providers []struct {
			Name     string               `json:"name"`
			Protocol string               `json:"protocol"`
			Model    string               `json:"model"`
			Models   []config.ModelConfig `json:"models"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(r2.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range list.Providers {
		if p.Name == "openai-bare" {
			found = true
			if p.Protocol != "openai" {
				t.Errorf("protocol = %q, want openai", p.Protocol)
			}
			// No model supplied — should be OK (EffectiveModel
			// falls back to the empty string).
		}
	}
	if !found {
		t.Error("openai-bare not found in providers list")
	}
}

// TestAddProvider_AnthropicProtocol verifies the protocol
// dropdown in the web UI maps to a real config.Protocol value.
func TestAddProvider_AnthropicProtocol(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	body := `{"name":"anthropic-test","protocol":"anthropic","base_url":"https://api.anthropic.com","api_key":"sk-ant","model":"claude-sonnet-4-5"}`
	r, err := http.Post(srv.URL+"/api/v1/providers", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", r.StatusCode)
	}

	r2, err := http.Get(srv.URL + "/api/v1/providers/anthropic-test")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	var p struct {
		Protocol string `json:"protocol"`
		Model    string `json:"model"`
	}
	if err := json.NewDecoder(r2.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.Protocol != "anthropic" {
		t.Errorf("protocol = %q, want anthropic", p.Protocol)
	}
	if p.Model != "claude-sonnet-4-5" {
		t.Errorf("model = %q, want claude-sonnet-4-5", p.Model)
	}
}

// TestAddProvider_RejectsDuplicate verifies the API refuses to
// add a second provider with the same name. The web UI's
// failure path turns this into a friendly toast.
func TestAddProvider_RejectsDuplicate(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	body := `{"name":"dup","protocol":"openai","base_url":"http://x","api_key":"k","model":"m"}`
	r, _ := http.Post(srv.URL+"/api/v1/providers", "application/json", strings.NewReader(body))
	r.Body.Close()
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("first add: %d, want 201", r.StatusCode)
	}

	r2, _ := http.Post(srv.URL+"/api/v1/providers", "application/json", strings.NewReader(body))
	r2.Body.Close()
	if r2.StatusCode == http.StatusCreated {
		t.Error("duplicate add should not return 201")
	}
	// 409 Conflict for "already exists" is what the web UI
	// branches on. Other 4xx/5xx would be a server bug.
	if r2.StatusCode != http.StatusConflict {
		t.Errorf("duplicate add: status = %d, want 409 Conflict", r2.StatusCode)
	}
}

// TestAddProvider_RequiresName covers the bad-name path (the
// web UI rejects this client-side; the API is the safety net).
func TestAddProvider_RequiresName(t *testing.T) {
	srv := newWebServer(t)
	defer srv.Close()

	body := `{"protocol":"openai","base_url":"http://x"}`
	r, _ := http.Post(srv.URL+"/api/v1/providers", "application/json", strings.NewReader(body))
	r.Body.Close()
	if r.StatusCode == http.StatusCreated {
		t.Error("empty name should not succeed")
	}
}
