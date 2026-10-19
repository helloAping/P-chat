package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// newTestService disables publisher-signature enforcement unless the test
// explicitly sets RequireSignature. Production NewService still requires it.
func newTestService(opts Options) *Service {
	if opts.RequireSignature == nil {
		v := false
		opts.RequireSignature = &v
	}
	return NewService(opts)
}

func boolPtr(v bool) *bool { return &v }

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a    string
		b    string
		want int
	}{
		{"1.0.13", "1.0.12", 1},
		{"v1.0.12", "1.0.12-release", 0},
		{"1.2.0", "1.10.0", -1},
		{"1.0.13.beta", "1.0.12", 1},
		{"1.0.13", "1.0.13.beta", 1},
		{"1.0.13.beta", "1.0.13", -1},
		{"1.0.13.beta.2", "1.0.13.beta.1", 1},
		{"1.0.13-beta.2", "1.0.13.beta.2", 0},
		{"1.0.13.rc.1", "1.0.13.beta.9", 1},
		{"1.0.13.beta", "1.0.13.alpha", 1},
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

	svc := newTestService(Options{
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

	svc := newTestService(Options{
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
	svc := newTestService(Options{LatestURL: srv.URL, Platform: "windows", Arch: "amd64"})
	res, err := svc.Check(t.Context(), "1.0.12")
	if err != nil {
		t.Fatal(err)
	}
	if res.HasUpdate {
		t.Fatal("HasUpdate = true, want false")
	}
}

func TestCheckRejectsPrereleaseFromStableChannel(t *testing.T) {
	var sawIncludePrerelease bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawIncludePrerelease = r.URL.Query().Get("include_prerelease") == "1"
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version":"1.0.13.beta","channel":"beta","full":{"kind":"full","url":"https://example.test/pchat-update.zip","sha256":"abc"}}`)
	}))
	defer srv.Close()

	stable := newTestService(Options{LatestURL: srv.URL, Platform: "windows", Arch: "amd64"})
	res, err := stable.Check(t.Context(), "1.0.12")
	if err != nil {
		t.Fatal(err)
	}
	if res.HasUpdate || res.Latest != "" {
		t.Fatalf("stable check exposed prerelease: %#v", res)
	}
	if sawIncludePrerelease {
		t.Fatal("stable check unexpectedly requested prereleases")
	}
}

func TestCheckIncludesOnlyBetaPrerelease(t *testing.T) {
	var sawIncludePrerelease bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawIncludePrerelease = r.URL.Query().Get("include_prerelease") == "1"
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version":"1.0.13.beta","channel":"beta","full":{"kind":"full","url":"https://example.test/pchat-update.zip","sha256":"abc"}}`)
	}))
	defer srv.Close()

	svc := newTestService(Options{LatestURL: srv.URL, Platform: "windows", Arch: "amd64", IncludePrerelease: true})
	res, err := svc.Check(t.Context(), "1.0.12")
	if err != nil {
		t.Fatal(err)
	}
	if !sawIncludePrerelease {
		t.Fatal("prerelease check did not request prereleases upstream")
	}
	if !res.HasUpdate || res.Latest != "1.0.13.beta" {
		t.Fatalf("beta check result = %#v", res)
	}
}

func TestAllowsLatestRejectsAlphaAndRC(t *testing.T) {
	for _, latest := range []Latest{
		{Version: "1.0.13.alpha", Channel: "alpha"},
		{Version: "1.0.13.rc.1", Channel: "rc"},
		{Version: "1.0.13.beta", Channel: "rc"},
		{Version: "1.0.13.beta", Channel: "stable"},
		{Version: "1.0.13.beta"},
		{Version: "1.0.13", Channel: "beta"},
		{Version: "1.0.13.preview", Channel: "stable"},
	} {
		if allowsLatest(latest, true) {
			t.Fatalf("allowsLatest(%#v, true) = true, want false", latest)
		}
	}
}

func TestDownloadRejectsChangedExpectedVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version":"1.0.14","channel":"stable","full":{"kind":"full","url":"https://example.test/pchat-update.zip","sha256":"abc"}}`)
	}))
	defer srv.Close()

	svc := newTestService(Options{LatestURL: srv.URL, Platform: "windows", Arch: "amd64"})
	_, err := svc.Download(t.Context(), "1.0.12", "1.0.13")
	if !errors.Is(err, ErrExpectedVersionChanged) {
		t.Fatalf("Download error = %v, want ErrExpectedVersionChanged", err)
	}
}

func TestDownloadBetaWithExpectedVersion(t *testing.T) {
	pkg := []byte("beta zip payload")
	sum := sha256Hex(pkg)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.json":
			if r.URL.Query().Get("include_prerelease") != "1" {
				t.Error("beta download did not request prerelease metadata")
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"version":"1.0.13.beta","channel":"beta","full":{"kind":"full","size":%d,"sha256":%q,"url":%q}}`, len(pkg), sum, srv.URL+"/pchat-update-windows-amd64-v1.0.13.beta.zip")
		case "/pchat-update-windows-amd64-v1.0.13.beta.zip":
			_, _ = w.Write(pkg)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	svc := newTestService(Options{LatestURL: srv.URL + "/latest.json", Platform: "windows", Arch: "amd64", DownloadDir: t.TempDir(), IncludePrerelease: true})
	got, err := svc.Download(t.Context(), "1.0.12", "1.0.13.beta")
	if err != nil {
		t.Fatal(err)
	}
	if got.Latest != "1.0.13.beta" || got.Channel != "beta" || got.SHA256 != sum || got.DownloadPath == "" {
		t.Fatalf("beta download = %#v", got)
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
	svc := newTestService(Options{LatestURL: srv.URL + "/latest.json", Platform: "windows", Arch: "amd64", DownloadDir: dir})
	got, err := svc.Download(t.Context(), "1.0.12", "")
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

	svc := newTestService(Options{LatestURL: srv.URL + "/latest.json", Platform: "windows", Arch: "amd64", DownloadDir: t.TempDir()})
	if _, err := svc.Download(t.Context(), "1.0.12", ""); err == nil {
		t.Fatal("expected hash mismatch error")
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// latestJSONFixture renders a latest.json body with a full and a patch
// artifact; patchFrom is the patch's from_version floor (empty = none).
func latestJSONFixture(srv *httptest.Server, patchFrom string) string {
	return fmt.Sprintf(`{
		"name":"P-Chat",
		"version":"1.0.13",
		"platform":"windows",
		"arch":"amd64",
		"full":{"kind":"full","platform":"windows","arch":"amd64","size":10,"sha256":"%s","url":"%s"},
		"patch":{"kind":"patch","platform":"windows","arch":"amd64","from_version":%q,"size":10,"sha256":"%s","url":"%s"}
	}`,
		sha256Hex([]byte("full")), srv.URL+"/pchat-setup-v1.0.13.exe",
		patchFrom, sha256Hex([]byte("patch")), srv.URL+"/pchat-update-windows-amd64-v1.0.13.zip")
}

func checkWithPatchFloor(t *testing.T, currentVersion, patchFrom string) *CheckResult {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, latestJSONFixture(srv, patchFrom))
	}))
	t.Cleanup(srv.Close)
	svc := newTestService(Options{LatestURL: srv.URL + "/latest.json", Platform: "windows", Arch: "amd64"})
	res, err := svc.Check(t.Context(), currentVersion)
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasUpdate {
		t.Fatalf("HasUpdate = false for current=%q, want true", currentVersion)
	}
	return res
}

// TestCheckPatchFromVersionCoversBeta is the regression test for the
// 2026-09-25 report: a beta build (1.0.13-beta.1) must be allowed to use
// a patch whose floor is from_version=1.0.0 鈥?pre-release ordering still
// sorts 1.0.13-beta.1 above 1.0.0.
func TestCheckPatchFromVersionCoversBeta(t *testing.T) {
	res := checkWithPatchFloor(t, "1.0.13-beta.1", "1.0.0")
	if res.Artifact == nil || res.Artifact.Kind != "patch" {
		t.Fatalf("artifact = %#v, want patch for beta above the floor", res.Artifact)
	}
	if !res.Installable {
		t.Fatal("Installable = false, want true for patch zip")
	}
}

// TestCheckPatchFromVersionFallbackToFull pins the safety behaviour: when
// the running version is older than the patch floor, the client must fall
// back to the full package instead of applying an inapplicable patch.
func TestCheckPatchFromVersionFallbackToFull(t *testing.T) {
	res := checkWithPatchFloor(t, "1.0.0", "1.0.12")
	if res.Artifact == nil || res.Artifact.Kind != "full" {
		t.Fatalf("artifact = %#v, want full fallback for version below the floor", res.Artifact)
	}
}

// TestCheckPatchWithoutFromVersionKeepsLegacyBehaviour ensures a patch
// with no declared floor is still always preferred (legacy latest.json).
func TestCheckPatchWithoutFromVersionKeepsLegacyBehaviour(t *testing.T) {
	res := checkWithPatchFloor(t, "1.0.12", "")
	if res.Artifact == nil || res.Artifact.Kind != "patch" {
		t.Fatalf("artifact = %#v, want patch when no floor declared", res.Artifact)
	}
}

// TestCheckPatchFromVersionRejectsUnknownCurrent ensures a build with an
// empty current version never receives a patch (its file set is
// unknowable), falling back to the full package instead.
func TestCheckPatchFromVersionRejectsUnknownCurrent(t *testing.T) {
	res := checkWithPatchFloor(t, "", "1.0.0")
	if res.Artifact == nil || res.Artifact.Kind != "full" {
		t.Fatalf("artifact = %#v, want full fallback for unknown current version", res.Artifact)
	}
}

// TestCheckRetriesTransientServerError drives the fetch retry: the first
// two attempts fail with 500, the third succeeds.
func TestCheckRetriesTransientServerError(t *testing.T) {
	var calls int
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, latestJSONFixture(srv, "1.0.0"))
	}))
	defer srv.Close()
	svc := newTestService(Options{LatestURL: srv.URL + "/latest.json", Platform: "windows", Arch: "amd64"})
	res, err := svc.Check(t.Context(), "1.0.12")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 (two 500s then success)", calls)
	}
	if !res.HasUpdate {
		t.Fatal("HasUpdate = false, want true after retry")
	}
}

// TestCheckDoesNotRetryClientError ensures 4xx responses (other than the
// 404 no-metadata case) fail immediately without burning retries.
func TestCheckDoesNotRetryClientError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()
	svc := newTestService(Options{LatestURL: srv.URL + "/latest.json", Platform: "windows", Arch: "amd64"})
	if _, err := svc.Check(t.Context(), "1.0.12"); err == nil {
		t.Fatal("expected error for 400 response")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no retry on 4xx)", calls)
	}
}

// TestCheck404DoesNotRetry ensures the no-metadata 404 path stays a
// single request.
func TestCheck404DoesNotRetry(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.NotFound(w, r)
	}))
	defer srv.Close()
	svc := newTestService(Options{LatestURL: srv.URL + "/latest.json", Platform: "windows", Arch: "amd64"})
	res, err := svc.Check(t.Context(), "1.0.12")
	if err != nil {
		t.Fatal(err)
	}
	if res.HasUpdate {
		t.Fatal("HasUpdate = true, want false for 404")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no retry on 404)", calls)
	}
}

func TestRequireSecureURL(t *testing.T) {
	if err := requireSecureURL("https://www.08ms.cn/software/p-chat/latest.json"); err != nil {
		t.Fatal(err)
	}
	if err := requireSecureURL("http://127.0.0.1:9/latest.json"); err != nil {
		t.Fatal(err)
	}
	if err := requireSecureURL("http://www.08ms.cn/software/p-chat/latest.json"); err == nil {
		t.Fatal("expected non-loopback http to fail")
	}
}

func TestDownloadRequiresPublisherSignature(t *testing.T) {
	pkg := []byte("signed zip")
	sum := sha256Hex(pkg)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := SignArtifact(priv, sum, "1.0.13", "full")
	if err != nil {
		t.Fatal(err)
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"version":"1.0.13","channel":"stable","full":{"kind":"full","size":%d,"sha256":%q,"url":%q,"signature":%q}}`,
				len(pkg), sum, srv.URL+"/pchat-update.zip", sig)
		case "/pchat-update.zip":
			_, _ = w.Write(pkg)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	unsigned := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.json":
			fmt.Fprintf(w, `{"version":"1.0.13","channel":"stable","full":{"kind":"full","size":%d,"sha256":%q,"url":%q}}`,
				len(pkg), sum, srv.URL+"/pchat-update.zip")
		default:
			http.NotFound(w, r)
		}
	}))
	defer unsigned.Close()
	bad := NewService(Options{
		LatestURL:        unsigned.URL + "/latest.json",
		Platform:         "windows",
		Arch:             "amd64",
		DownloadDir:      t.TempDir(),
		PublicKey:        pub,
		RequireSignature: boolPtr(true),
	})
	if _, err := bad.Download(t.Context(), "1.0.12", ""); !errors.Is(err, ErrMissingSignature) {
		t.Fatalf("unsigned download err = %v, want ErrMissingSignature", err)
	}

	ok := NewService(Options{
		LatestURL:        srv.URL + "/latest.json",
		Platform:         "windows",
		Arch:             "amd64",
		DownloadDir:      t.TempDir(),
		PublicKey:        pub,
		RequireSignature: boolPtr(true),
	})
	got, err := ok.Download(t.Context(), "1.0.12", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA256 != sum {
		t.Fatalf("sha256 = %q", got.SHA256)
	}
}

func TestCheckRejectsNonHTTPSLatestURL(t *testing.T) {
	svc := NewService(Options{
		LatestURL:        "http://example.com/latest.json",
		Platform:         "windows",
		Arch:             "amd64",
		RequireSignature: boolPtr(false),
	})
	if _, err := svc.Check(t.Context(), "1.0.12"); err == nil {
		t.Fatal("expected https enforcement error")
	}
}
