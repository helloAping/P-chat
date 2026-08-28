package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/p-chat/pchat/internal/version"
)

func TestCheckUpdateEndpoint(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("current_version") != "1.0.12" {
			t.Fatalf("current_version = %q, want 1.0.12", r.URL.Query().Get("current_version"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"name":"P-Chat",
			"slug":"p-chat",
			"version":"1.0.13",
			"channel":"stable",
			"platform":"windows",
			"arch":"amd64",
			"full":{
				"kind":"full",
				"platform":"windows",
				"arch":"amd64",
				"size":3,
				"sha256":%q,
				"url":%q
			}
		}`, sha256HexString([]byte("zip")), upstream.URL+"/pchat-update-windows-amd64-v1.0.13.zip")
	}))
	defer upstream.Close()
	t.Setenv("PCHAT_UPDATE_URL", upstream.URL)

	s, _ := newTestServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/updates/check?current_version=1.0.12", nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["has_update"] != true {
		t.Fatalf("has_update = %v, want true", body["has_update"])
	}
	if body["installable"] != true {
		t.Fatalf("installable = %v, want true", body["installable"])
	}
}

func TestCheckUpdateEndpointUsesReleaseVersionWhenNoQuery(t *testing.T) {
	oldVersion := version.Version
	version.Version = "1.0.12"
	t.Cleanup(func() { version.Version = oldVersion })

	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("current_version") != "1.0.12" {
			t.Fatalf("current_version = %q, want 1.0.12", r.URL.Query().Get("current_version"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"version":"1.0.13",
			"platform":"windows",
			"arch":"amd64",
			"full":{
				"kind":"full",
				"platform":"windows",
				"arch":"amd64",
				"size":3,
				"sha256":%q,
				"url":%q
			}
		}`, sha256HexString([]byte("zip")), upstream.URL+"/pchat-update-windows-amd64-v1.0.13.zip")
	}))
	defer upstream.Close()
	t.Setenv("PCHAT_UPDATE_URL", upstream.URL)

	s, _ := newTestServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/updates/check", nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestDownloadUpdateEndpoint(t *testing.T) {
	payload := []byte("zip")
	sum := sha256HexString(payload)
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{
				"version":"1.0.13",
				"platform":"windows",
				"arch":"amd64",
				"full":{
					"kind":"full",
					"platform":"windows",
					"arch":"amd64",
					"size":%d,
					"sha256":%q,
					"url":%q
				}
			}`, len(payload), sum, upstream.URL+"/pchat-update-windows-amd64-v1.0.13.zip")
		case "/pchat-update-windows-amd64-v1.0.13.zip":
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	t.Setenv("PCHAT_UPDATE_URL", upstream.URL+"/latest.json")

	s, _ := newTestServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/updates/download?current_version=1.0.12", nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body struct {
		DownloadPath string `json:"download_path"`
		FileName     string `json:"file_name"`
		SHA256       string `json:"sha256"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.FileName != "pchat-update-windows-amd64-v1.0.13.zip" {
		t.Fatalf("file name = %q", body.FileName)
	}
	if body.DownloadPath == "" {
		t.Fatal("download path is empty")
	}
	if body.SHA256 != sum {
		t.Fatalf("sha256 = %q, want %q", body.SHA256, sum)
	}
}

func sha256HexString(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
