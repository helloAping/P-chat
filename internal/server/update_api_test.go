package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// 这条用例覆盖下载路径，签名校验由 internal/update 的专用测试负责。
	// This case covers the download route; signature checks live in internal/update.
	t.Setenv("PCHAT_UPDATE_REQUIRE_SIGNATURE", "0")

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

func TestCheckUpdateEndpointRelaysBetaRequestAndHidesBetaByDefault(t *testing.T) {
	var sawIncludePrerelease bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawIncludePrerelease = r.URL.Query().Get("include_prerelease") == "1"
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version":"1.0.13.beta","channel":"beta","full":{"kind":"full","url":"https://example.test/pchat-update.zip","sha256":"abc"}}`)
	}))
	defer upstream.Close()
	t.Setenv("PCHAT_UPDATE_URL", upstream.URL)

	s, _ := newTestServer(t)
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/updates/check?current_version=1.0.12", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("default status = %d, body=%s", w.Code, w.Body.String())
	}
	var stable map[string]any
	if err := json.NewDecoder(w.Body).Decode(&stable); err != nil {
		t.Fatal(err)
	}
	if stable["has_update"] != false || stable["latest"] != nil {
		t.Fatalf("default check exposed beta: %#v", stable)
	}
	if sawIncludePrerelease {
		t.Fatal("default request included prerelease flag")
	}

	w = httptest.NewRecorder()
	s.engine.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/updates/check?current_version=1.0.12&include_prerelease=1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("beta status = %d, body=%s", w.Code, w.Body.String())
	}
	if !sawIncludePrerelease {
		t.Fatal("beta request did not relay prerelease flag")
	}
	var beta map[string]any
	if err := json.NewDecoder(w.Body).Decode(&beta); err != nil {
		t.Fatal(err)
	}
	if beta["has_update"] != true || beta["latest"] != "1.0.13.beta" {
		t.Fatalf("beta check response = %#v", beta)
	}
}

func TestDownloadUpdateEndpointRejectsChangedExpectedVersion(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version":"1.0.14","channel":"stable","full":{"kind":"full","url":"https://example.test/pchat-update.zip","sha256":"abc"}}`)
	}))
	defer upstream.Close()
	t.Setenv("PCHAT_UPDATE_URL", upstream.URL)

	s, _ := newTestServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/updates/download?current_version=1.0.12", strings.NewReader(`{"expected_version":"1.0.13"}`))
	req.Header.Set("Content-Type", "application/json")
	s.engine.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestDownloadUpdateEndpointRelaysBetaSelectionFromBody(t *testing.T) {
	var sawIncludePrerelease bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawIncludePrerelease = r.URL.Query().Get("include_prerelease") == "1"
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version":"1.0.13.beta","channel":"beta","full":{"kind":"full","url":"https://example.test/pchat-update.zip","sha256":"abc"}}`)
	}))
	defer upstream.Close()
	t.Setenv("PCHAT_UPDATE_URL", upstream.URL)

	s, _ := newTestServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/updates/download?current_version=1.0.12", strings.NewReader(`{"include_prerelease":true,"expected_version":"1.0.12"}`))
	req.Header.Set("Content-Type", "application/json")
	s.engine.ServeHTTP(w, req)
	if !sawIncludePrerelease {
		t.Fatal("download did not relay beta selection to upstream")
	}
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "latest update version changed") {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
}

func sha256HexString(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
