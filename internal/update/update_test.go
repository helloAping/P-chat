package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestCompareVersionsIgnoresSuffix(t *testing.T) {
	tests := []struct {
		a    string
		b    string
		want int
	}{
		{"1.0.13", "1.0.12", 1},
		{"v1.0.12", "1.0.12-release", 0},
		{"1.2.0", "1.10.0", -1},
		{"dev", "1.0.0", -1},
	}
	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			if got := CompareVersions(tt.a, tt.b); got != tt.want {
				t.Fatalf("CompareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestCheckSelectsInstallableFullZip(t *testing.T) {
	pkg := []byte("fake zip bytes")
	sum := sha256Hex(pkg)
	var sawQuery bool
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/latest.json" {
			t.Fatalf("path = %q, want /latest.json", r.URL.Path)
		}
		if r.URL.Query().Get("platform") != "windows" {
			t.Fatalf("platform query = %q", r.URL.Query().Get("platform"))
		}
		if r.URL.Query().Get("arch") != "amd64" {
			t.Fatalf("arch query = %q", r.URL.Query().Get("arch"))
		}
		if r.URL.Query().Get("current_version") != "1.0.12" {
			t.Fatalf("current_version query = %q", r.URL.Query().Get("current_version"))
		}
		sawQuery = true
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"name":"P-Chat",
			"slug":"p-chat",
			"version":"1.0.13",
			"channel":"stable",
			"release_notes":"bug fixes",
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
		}`, len(pkg), sum, srv.URL+"/pchat-update-windows-amd64-v1.0.13.zip")
	}))
	defer srv.Close()

	svc := NewService(Options{
		LatestURL: srv.URL + "/latest.json",
		Platform:  "windows",
		Arch:      "amd64",
	})
	res, err := svc.Check(t.Context(), "1.0.12")
	if err != nil {
		t.Fatal(err)
	}
	if !sawQuery {
		t.Fatal("server did not receive update query")
	}
	if !res.HasUpdate {
		t.Fatal("HasUpdate = false, want true")
	}
	if !res.Installable {
		t.Fatal("Installable = false, want true for zip artifact")
	}
	if res.Artifact == nil || res.Artifact.Kind != "full" {
		t.Fatalf("artifact = %#v, want full artifact", res.Artifact)
	}
}

func TestCheckPrefersPatchZipWhenAvailable(t *testing.T) {
	patch := []byte("fake patch zip bytes")
	patchSum := sha256Hex(patch)
	full := []byte("fake setup bytes")
	fullSum := sha256Hex(full)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("current_version") != "1.0.12" {
			t.Fatalf("current_version query = %q", r.URL.Query().Get("current_version"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"name":"P-Chat",
			"slug":"p-chat",
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
			},
			"patch":{
				"kind":"patch",
				"platform":"windows",
				"arch":"amd64",
				"from_version":"1.0.12",
				"size":%d,
				"sha256":%q,
				"url":%q
			}
		}`, len(full), fullSum, srv.URL+"/pchat-setup-v1.0.13.exe", len(patch), patchSum, srv.URL+"/pchat-update-windows-amd64-v1.0.13.zip")
	}))
	defer srv.Close()

	svc := NewService(Options{
		LatestURL: srv.URL + "/latest.json",
		Platform:  "windows",
		Arch:      "amd64",
	})
	res, err := svc.Check(t.Context(), "1.0.12")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Installable {
		t.Fatal("Installable = false, want true for selected patch zip")
	}
	if res.Artifact == nil || res.Artifact.Kind != "patch" {
		t.Fatalf("artifact = %#v, want patch artifact", res.Artifact)
	}
	if res.URL != srv.URL+"/pchat-update-windows-amd64-v1.0.13.zip" {
		t.Fatalf("url = %q, want patch url", res.URL)
	}
}

func TestCheck404MeansNoUpdate(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	svc := NewService(Options{LatestURL: srv.URL, Platform: "windows", Arch: "amd64"})
	res, err := svc.Check(t.Context(), "1.0.12")
	if err != nil {
		t.Fatal(err)
	}
	if res.HasUpdate {
		t.Fatal("HasUpdate = true, want false")
	}
}

func TestDownloadVerifiesSHAAndWritesFile(t *testing.T) {
	pkg := []byte("zip payload")
	sum := sha256Hex(pkg)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{
				"version":"1.0.13",
				"platform":"windows",
				"arch":"amd64",
				"full":{
					"kind":"full",
					"size":%d,
					"sha256":%q,
					"url":%q
				}
			}`, len(pkg), sum, srv.URL+"/pchat-update-windows-amd64-v1.0.13.zip")
		case "/pchat-update-windows-amd64-v1.0.13.zip":
			_, _ = w.Write(pkg)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	svc := NewService(Options{LatestURL: srv.URL + "/latest.json", Platform: "windows", Arch: "amd64", DownloadDir: dir})
	got, err := svc.Download(t.Context(), "1.0.12")
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA256 != sum {
		t.Fatalf("sha256 = %q, want %q", got.SHA256, sum)
	}
	if got.Size != int64(len(pkg)) {
		t.Fatalf("size = %d, want %d", got.Size, len(pkg))
	}
	if got.DownloadPath != filepath.Join(dir, "pchat-update-windows-amd64-v1.0.13.zip") {
		t.Fatalf("download path = %q", got.DownloadPath)
	}
}

func TestDownloadRejectsHashMismatch(t *testing.T) {
	pkg := []byte("zip payload")
	bad := sha256Hex([]byte("other"))
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{
				"version":"1.0.13",
				"full":{
					"kind":"full",
					"size":%d,
					"sha256":%q,
					"url":%q
				}
			}`, len(pkg), bad, srv.URL+"/pchat-update-windows-amd64-v1.0.13.zip")
		case "/pchat-update-windows-amd64-v1.0.13.zip":
			_, _ = w.Write(pkg)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	svc := NewService(Options{LatestURL: srv.URL + "/latest.json", Platform: "windows", Arch: "amd64", DownloadDir: t.TempDir()})
	if _, err := svc.Download(t.Context(), "1.0.12"); err == nil {
		t.Fatal("expected hash mismatch error")
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
