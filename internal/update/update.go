// Package update implements P-Chat's client-side update checks and
// verified update-package downloads.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/p-chat/pchat/internal/paths"
)

const (
	// DefaultLatestURL is the 08ms update metadata endpoint documented for
	// P-Chat clients.
	DefaultLatestURL = "http://www.08ms.cn/software/p-chat/latest.json"
	defaultTimeout   = 10 * time.Minute
)

var errNoArtifact = errors.New("no update artifact available")

// Artifact describes one downloadable update artifact from latest.json.
type Artifact struct {
	ArtifactID  int    `json:"artifact_id,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Platform    string `json:"platform,omitempty"`
	Arch        string `json:"arch,omitempty"`
	FromVersion string `json:"from_version,omitempty"`
	Size        int64  `json:"size,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	URL         string `json:"url,omitempty"`
}

// Latest describes the 08ms latest.json response.
type Latest struct {
	Name         string    `json:"name,omitempty"`
	Slug         string    `json:"slug,omitempty"`
	Version      string    `json:"version,omitempty"`
	Channel      string    `json:"channel,omitempty"`
	ReleaseNotes string    `json:"release_notes,omitempty"`
	PublishedAt  string    `json:"published_at,omitempty"`
	Platform     string    `json:"platform,omitempty"`
	Arch         string    `json:"arch,omitempty"`
	Size         int64     `json:"size,omitempty"`
	SHA256       string    `json:"sha256,omitempty"`
	URL          string    `json:"url,omitempty"`
	Full         *Artifact `json:"full,omitempty"`
	Patch        *Artifact `json:"patch,omitempty"`
}

// CheckResult is returned to the GUI after an update check.
type CheckResult struct {
	Name         string    `json:"name,omitempty"`
	Slug         string    `json:"slug,omitempty"`
	Current      string    `json:"current"`
	Latest       string    `json:"latest,omitempty"`
	Channel      string    `json:"channel,omitempty"`
	ReleaseNotes string    `json:"release_notes,omitempty"`
	PublishedAt  string    `json:"published_at,omitempty"`
	Platform     string    `json:"platform"`
	Arch         string    `json:"arch"`
	HasUpdate    bool      `json:"has_update"`
	Installable  bool      `json:"installable"`
	URL          string    `json:"url,omitempty"`
	Artifact     *Artifact `json:"artifact,omitempty"`
	Full         *Artifact `json:"full,omitempty"`
	Patch        *Artifact `json:"patch,omitempty"`
}

// DownloadResult is returned after the selected update package has been
// downloaded and verified.
type DownloadResult struct {
	CheckResult
	DownloadPath string `json:"download_path"`
	FileName     string `json:"file_name"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
}

// Options customizes update checks. Zero values use production defaults.
type Options struct {
	LatestURL   string
	Platform    string
	Arch        string
	DownloadDir string
	HTTPClient  *http.Client
}

// Service performs update checks and downloads.
type Service struct {
	latestURL   string
	platform    string
	arch        string
	downloadDir string
	httpClient  *http.Client
}

// NewService creates an update service.
func NewService(opts Options) *Service {
	latestURL := strings.TrimSpace(opts.LatestURL)
	if latestURL == "" {
		latestURL = os.Getenv("PCHAT_UPDATE_URL")
	}
	if latestURL == "" {
		latestURL = DefaultLatestURL
	}
	platform := strings.TrimSpace(opts.Platform)
	if platform == "" {
		platform = Platform()
	}
	arch := strings.TrimSpace(opts.Arch)
	if arch == "" {
		arch = Arch()
	}
	downloadDir := strings.TrimSpace(opts.DownloadDir)
	if downloadDir == "" {
		downloadDir = filepath.Join(paths.GlobalDir(), "updates")
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	return &Service{
		latestURL:   latestURL,
		platform:    platform,
		arch:        arch,
		downloadDir: downloadDir,
		httpClient:  client,
	}
}

// Platform returns the update-service platform token.
func Platform() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	default:
		return runtime.GOOS
	}
}

// Arch returns the update-service architecture token.
func Arch() string {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return runtime.GOARCH
	case "386":
		return "386"
	default:
		return runtime.GOARCH
	}
}

// Check requests latest.json and decides whether the current binary needs an
// update.
func (s *Service) Check(ctx context.Context, currentVersion string) (*CheckResult, error) {
	currentVersion = strings.TrimSpace(currentVersion)
	latest, status, err := s.fetchLatest(ctx, currentVersion)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return &CheckResult{
			Current:     currentVersion,
			Platform:    s.platform,
			Arch:        s.arch,
			HasUpdate:   false,
			Installable: false,
		}, nil
	}

	res := &CheckResult{
		Name:         latest.Name,
		Slug:         latest.Slug,
		Current:      currentVersion,
		Latest:       latest.Version,
		Channel:      latest.Channel,
		ReleaseNotes: latest.ReleaseNotes,
		PublishedAt:  latest.PublishedAt,
		Platform:     nonEmpty(latest.Platform, s.platform),
		Arch:         nonEmpty(latest.Arch, s.arch),
		Full:         normalizeArtifact(latest.Full, "full", latest),
		Patch:        normalizeArtifact(latest.Patch, "patch", latest),
	}
	res.HasUpdate = CompareVersions(latest.Version, currentVersion) > 0
	if res.HasUpdate {
		artifact := selectArtifact(res.Full, res.Patch, currentVersion)
		res.Artifact = artifact
		if artifact != nil {
			res.URL = artifact.URL
			res.Installable = isZipURL(artifact.URL)
		}
	}
	if res.URL == "" {
		res.URL = latest.URL
	}
	return res, nil
}

// Download checks for an update, downloads the selected zip update package,
// verifies SHA-256, and returns the local file path.
func (s *Service) Download(ctx context.Context, currentVersion string) (*DownloadResult, error) {
	check, err := s.Check(ctx, currentVersion)
	if err != nil {
		return nil, err
	}
	if !check.HasUpdate {
		return nil, fmt.Errorf("no update available")
	}
	artifact := check.Artifact
	if artifact == nil || artifact.URL == "" {
		return nil, errNoArtifact
	}
	if !isZipURL(artifact.URL) {
		return nil, fmt.Errorf("selected artifact is not an update zip: %s", artifact.URL)
	}
	if strings.TrimSpace(artifact.SHA256) == "" {
		return nil, fmt.Errorf("selected artifact has no sha256")
	}
	downloadPath, size, sum, err := s.downloadArtifact(ctx, artifact, check.Latest)
	if err != nil {
		return nil, err
	}
	return &DownloadResult{
		CheckResult:  *check,
		DownloadPath: downloadPath,
		FileName:     filepath.Base(downloadPath),
		Size:         size,
		SHA256:       sum,
	}, nil
}

// fetchLatest queries the update metadata endpoint with a bounded retry
// for transient failures. A single connection reset / 5xx used to surface
// directly as "检查更新失败" (2026-09-25 report); the check now retries up
// to maxFetchAttempts times with a short backoff. Non-retryable outcomes
// (404 = no update metadata, other 4xx, undecodable bodies) return
// immediately — retrying those cannot help.
func (s *Service) fetchLatest(ctx context.Context, currentVersion string) (Latest, int, error) {
	var lastErr error
	for attempt := 0; attempt < maxFetchAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Latest{}, 0, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		latest, status, err := s.fetchLatestOnce(ctx, currentVersion)
		if err == nil {
			return latest, status, nil
		}
		lastErr = err
		// status == 0 means the request never got a response (connection
		// reset, timeout, DNS); 5xx means the server may recover. Both
		// are worth another attempt.
		if status != 0 && status < 500 {
			break
		}
	}
	return Latest{}, 0, lastErr
}

// maxFetchAttempts bounds the update-metadata fetch retries (initial
// attempt + 2 retries with 1s / 2s backoff).
const maxFetchAttempts = 3

func (s *Service) fetchLatestOnce(ctx context.Context, currentVersion string) (Latest, int, error) {
	u, err := url.Parse(s.latestURL)
	if err != nil {
		return Latest{}, 0, fmt.Errorf("parse update url: %w", err)
	}
	q := u.Query()
	q.Set("platform", s.platform)
	q.Set("arch", s.arch)
	if currentVersion != "" {
		q.Set("current_version", currentVersion)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Latest{}, 0, fmt.Errorf("build update request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return Latest{}, 0, fmt.Errorf("check update: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Latest{}, resp.StatusCode, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Latest{}, resp.StatusCode, fmt.Errorf("check update: http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var latest Latest
	if err := json.NewDecoder(resp.Body).Decode(&latest); err != nil {
		return Latest{}, resp.StatusCode, fmt.Errorf("decode update response: %w", err)
	}
	return latest, resp.StatusCode, nil
}

func (s *Service) downloadArtifact(ctx context.Context, artifact *Artifact, latestVersion string) (string, int64, string, error) {
	if err := os.MkdirAll(s.downloadDir, 0o755); err != nil {
		return "", 0, "", fmt.Errorf("create updates dir: %w", err)
	}
	name, err := artifactFileName(artifact, latestVersion)
	if err != nil {
		return "", 0, "", err
	}
	finalPath := filepath.Join(s.downloadDir, name)
	wantHash := strings.ToLower(strings.TrimSpace(artifact.SHA256))
	if ok, size, sum := verifyExisting(finalPath, wantHash); ok {
		return finalPath, size, sum, nil
	}

	u, err := url.Parse(artifact.URL)
	if err != nil {
		return "", 0, "", fmt.Errorf("parse artifact url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", 0, "", fmt.Errorf("unsupported artifact url scheme: %s", u.Scheme)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return "", 0, "", fmt.Errorf("build artifact request: %w", err)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", 0, "", fmt.Errorf("download update package: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", 0, "", fmt.Errorf("download update package: http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	tmpPath := finalPath + ".download"
	_ = os.Remove(tmpPath)
	out, err := os.Create(tmpPath)
	if err != nil {
		return "", 0, "", fmt.Errorf("create download file: %w", err)
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(out, hash), resp.Body)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return "", 0, "", fmt.Errorf("write download file: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", 0, "", fmt.Errorf("close download file: %w", closeErr)
	}
	if artifact.Size > 0 && size != artifact.Size {
		_ = os.Remove(tmpPath)
		return "", 0, "", fmt.Errorf("download size mismatch: got %d, want %d", size, artifact.Size)
	}
	gotHash := hex.EncodeToString(hash.Sum(nil))
	if gotHash != wantHash {
		_ = os.Remove(tmpPath)
		return "", 0, "", fmt.Errorf("download sha256 mismatch: got %s, want %s", gotHash, wantHash)
	}
	_ = os.Remove(finalPath)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, "", fmt.Errorf("store update package: %w", err)
	}
	return finalPath, size, gotHash, nil
}

func verifyExisting(filePath string, wantHash string) (bool, int64, string) {
	f, err := os.Open(filePath)
	if err != nil {
		return false, 0, ""
	}
	defer f.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, f)
	if err != nil {
		return false, 0, ""
	}
	got := hex.EncodeToString(hash.Sum(nil))
	return got == wantHash, size, got
}

func normalizeArtifact(a *Artifact, defaultKind string, latest Latest) *Artifact {
	if a == nil {
		if defaultKind != "full" || latest.URL == "" {
			return nil
		}
		a = &Artifact{
			Kind:     "full",
			Platform: latest.Platform,
			Arch:     latest.Arch,
			Size:     latest.Size,
			SHA256:   latest.SHA256,
			URL:      latest.URL,
		}
	} else {
		cp := *a
		a = &cp
	}
	if a.Kind == "" {
		a.Kind = defaultKind
	}
	if a.Platform == "" {
		a.Platform = latest.Platform
	}
	if a.Arch == "" {
		a.Arch = latest.Arch
	}
	if a.Size == 0 && defaultKind == "full" {
		a.Size = latest.Size
	}
	if a.SHA256 == "" && defaultKind == "full" {
		a.SHA256 = latest.SHA256
	}
	if a.URL == "" && defaultKind == "full" {
		a.URL = latest.URL
	}
	return a
}

// selectArtifact prefers the smaller patch package, but only when the
// patch's declared from_version actually covers the running version. A
// patch built "from 1.0.0" applied to anything older would produce a
// corrupt install, so an uncovered current version falls back to the
// full package. Pre-release versions compare normally (1.0.13-beta.1
// satisfies from_version=1.0.0); an empty from_version keeps the legacy
// behaviour of always preferring the patch, and an empty current version
// (unknown build) refuses the patch because its file set is unknowable.
func selectArtifact(full *Artifact, patch *Artifact, currentVersion string) *Artifact {
	if patch != nil && patch.URL != "" && patchAppliesTo(patch, currentVersion) {
		return patch
	}
	if full != nil && full.URL != "" {
		return full
	}
	return nil
}

// patchAppliesTo reports whether the patch's from_version floor covers
// currentVersion. The floor is inclusive: from_version=1.0.0 means any
// build >= 1.0.0 may apply the patch.
func patchAppliesTo(patch *Artifact, currentVersion string) bool {
	from := strings.TrimSpace(patch.FromVersion)
	if from == "" {
		return true
	}
	if strings.TrimSpace(currentVersion) == "" {
		return false
	}
	return CompareVersions(currentVersion, from) >= 0
}

func artifactFileName(artifact *Artifact, latestVersion string) (string, error) {
	u, err := url.Parse(artifact.URL)
	if err != nil {
		return "", fmt.Errorf("parse artifact url: %w", err)
	}
	name := path.Base(u.Path)
	if name == "." || name == "/" || strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("pchat-update-%s-%s-v%s.zip", nonEmpty(artifact.Platform, Platform()), nonEmpty(artifact.Arch, Arch()), latestVersion)
	}
	name = filepath.Base(name)
	if !strings.EqualFold(filepath.Ext(name), ".zip") {
		return "", fmt.Errorf("selected artifact is not a zip file: %s", name)
	}
	return name, nil
}

func isZipURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.EqualFold(path.Ext(u.Path), ".zip")
}

func nonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// CompareVersions compares P-Chat release versions. Both the legacy dot form
// (1.0.13.beta.2) and the SemVer-style hyphen form (1.0.13-beta.2) are
// supported. Pre-release precedence is alpha < beta < rc < stable.
func CompareVersions(a string, b string) int {
	ap := parseVersion(a)
	bp := parseVersion(b)
	for i := 0; i < 3; i++ {
		if ap.parts[i] > bp.parts[i] {
			return 1
		}
		if ap.parts[i] < bp.parts[i] {
			return -1
		}
	}
	if ap.preReleaseRank > bp.preReleaseRank {
		return 1
	}
	if ap.preReleaseRank < bp.preReleaseRank {
		return -1
	}
	if ap.preReleaseSequence > bp.preReleaseSequence {
		return 1
	}
	if ap.preReleaseSequence < bp.preReleaseSequence {
		return -1
	}
	return 0
}

type parsedVersion struct {
	parts              [3]int
	preReleaseRank     int
	preReleaseSequence int
}

func parseVersion(v string) parsedVersion {
	v = strings.TrimSpace(strings.TrimPrefix(v, "v"))
	parts := strings.Split(v, ".")
	var out parsedVersion
	out.preReleaseRank = 4 // Stable releases sort after all pre-releases.
	for i := 0; i < len(parts) && i < 3; i++ {
		p := parts[i]
		end := 0
		for end < len(p) && p[end] >= '0' && p[end] <= '9' {
			end++
		}
		if end == 0 {
			continue
		}
		n, err := strconv.Atoi(p[:end])
		if err == nil {
			out.parts[i] = n
		}
	}

	if len(parts) < 3 {
		return out
	}
	patch := parts[2]
	patchDigits := 0
	for patchDigits < len(patch) && patch[patchDigits] >= '0' && patch[patchDigits] <= '9' {
		patchDigits++
	}
	suffixParts := make([]string, 0, 2)
	if patchDigits < len(patch) {
		suffixParts = append(suffixParts, strings.TrimLeft(patch[patchDigits:], "-"))
	}
	if len(parts) > 3 {
		suffixParts = append(suffixParts, parts[3:]...)
	}
	if len(suffixParts) == 0 {
		return out
	}

	label := strings.ToLower(strings.TrimSpace(suffixParts[0]))
	switch label {
	case "alpha":
		out.preReleaseRank = 1
	case "beta":
		out.preReleaseRank = 2
	case "rc":
		out.preReleaseRank = 3
	default:
		// Unknown legacy suffixes (for example "release") remain stable so
		// older P-Chat builds keep their previous comparison behaviour.
		return out
	}
	if len(suffixParts) > 1 {
		sequence, err := strconv.Atoi(strings.TrimSpace(suffixParts[1]))
		if err == nil {
			out.preReleaseSequence = sequence
		}
	}
	return out
}
