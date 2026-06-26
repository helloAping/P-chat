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
	cfgPath := filepath.Join(os.Getenv("USERPROFILE"), ".p-chat", "config.yaml")
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
		t.Errorf("config.yaml still has testprov after delete:\n%s", data2)
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

// Helper: not actually used by the tests but kept for parity.
var _ = fmt.Sprintf
