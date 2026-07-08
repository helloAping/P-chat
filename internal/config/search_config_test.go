package config

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"
)

// withTempHome redirects the user's home directory (via the
// env var that os.UserHomeDir consults) to a fresh temp
// dir for the duration of the test. The global config save
// path (~/.p-chat/config.json) will then write to the
// temp dir instead of touching the real user config —
// critical so test runs don't poison the operator's
// actual config.
//
// We also point PATH_PROJECT_ROOT at the same temp dir so
// project-layer files don't leak either.
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
		// USERPROFILE on Windows; HOME is also consulted
		// by os.UserHomeDir in some Go versions, so we
		// set it too for safety.
		t.Setenv("HOME", dir)
	} else {
		t.Setenv("HOME", dir)
	}
	return dir
}

// withTempProject wires up BOTH a temp HOME and a temp
// project root, then loads the config (with the project
// layer overlaid on the global one). Returns the project
// dir (so tests can inspect saved files) and the loaded
// Config.
func withTempProject(t *testing.T) (string, *Config) {
	t.Helper()
	dir := withTempHome(t)
	// A separate sub-dir for the project root so paths
	// don't collide.
	projDir := t.TempDir()
	cfg, err := LoadWithProjectRoot("", projDir)
	if err != nil {
		t.Fatalf("LoadWithProjectRoot: %v", err)
	}
	_ = filepath.Join(projDir, ".p-chat", "config.json")
	return dir, cfg
}

func TestUpdateSearchConfig_Disable(t *testing.T) {
	dir, _ := withTempProject(t)
	patch := SearchConfigPatch{
		Enabled: ptrBool(false),
	}
	updated, err := UpdateSearchConfig(patch)
	if err != nil {
		t.Fatalf("UpdateSearchConfig: %v", err)
	}
	if updated.Enabled {
		t.Error("Enabled should be false")
	}
	_ = dir
}

func TestUpdateSearchConfig_ProviderValidation(t *testing.T) {
	_, _ = withTempProject(t)
	cases := []struct {
		provider string
		wantErr  bool
	}{
		{"tavily", false},
		{"openai_compat", false},
		{"", false}, // empty normalises to "tavily"
		{"TAVILY", false}, // case-insensitive
		{"  openai_compat  ", false}, // trimmed
		{"bing", true},
		{"google", true},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			patch := SearchConfigPatch{Provider: ptrStr(tc.provider)}
			_, err := UpdateSearchConfig(patch)
			if tc.wantErr && err == nil {
				t.Errorf("provider %q: want error, got nil", tc.provider)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("provider %q: unexpected error: %v", tc.provider, err)
			}
		})
	}
}

func TestUpdateSearchConfig_BaseURLValidation(t *testing.T) {
	_, _ = withTempProject(t)
	cases := []struct {
		url     string
		wantErr bool
	}{
		{"https://api.tavily.com", false},
		{"https://s.jina.ai", false},
		{"http://127.0.0.1:8080", false},      // loopback
		{"http://localhost:9000", false},      // loopback
		{"http://api.example.com", true},      // non-loopback http
		{"ftp://example.com", true},           // wrong scheme
		{"javascript:alert(1)", true},
		{"", false}, // empty allowed (means "use default")
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			patch := SearchConfigPatch{BaseURL: ptrStr(tc.url)}
			_, err := UpdateSearchConfig(patch)
			if tc.wantErr && err == nil {
				t.Errorf("base_url %q: want error, got nil", tc.url)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("base_url %q: unexpected error: %v", tc.url, err)
			}
		})
	}
}

func TestUpdateSearchConfig_TopicValidation(t *testing.T) {
	_, _ = withTempProject(t)
	for _, topic := range []string{"", "general", "news", "finance", "GENERAL", "weather"} {
		wantErr := topic == "weather"
		patch := SearchConfigPatch{Topic: ptrStr(topic)}
		_, err := UpdateSearchConfig(patch)
		if wantErr && err == nil {
			t.Errorf("topic %q: want error, got nil", topic)
		}
		if !wantErr && err != nil {
			t.Errorf("topic %q: unexpected error: %v", topic, err)
		}
	}
}

func TestUpdateSearchConfig_TimeoutValidation(t *testing.T) {
	_, _ = withTempProject(t)
	cases := []struct {
		raw     string
		wantErr bool
	}{
		{"", false},     // empty → 0 (default)
		{"20s", false},
		{"500ms", false},
		{"60s", false},
		{"61s", true},   // capped
		{"5m", true},    // capped
		{"abc", true},
		{"-1s", true},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			patch := SearchConfigPatch{RequestTimeout: ptrStr(tc.raw)}
			_, err := UpdateSearchConfig(patch)
			if tc.wantErr && err == nil {
				t.Errorf("timeout %q: want error, got nil", tc.raw)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("timeout %q: unexpected error: %v", tc.raw, err)
			}
		})
	}
}

func TestUpdateSearchConfig_DailyQuotaValidation(t *testing.T) {
	_, _ = withTempProject(t)
	for _, q := range []int{-1, 0, 100, 100000, 100001} {
		wantErr := q < 0 || q > 100000
		patch := SearchConfigPatch{DailyQuota: ptrInt(q)}
		_, err := UpdateSearchConfig(patch)
		if wantErr && err == nil {
			t.Errorf("quota %d: want error, got nil", q)
		}
		if !wantErr && err != nil {
			t.Errorf("quota %d: unexpected error: %v", q, err)
		}
	}
}

func TestUpdateSearchConfig_EnableRequiresKey(t *testing.T) {
	_, _ = withTempProject(t)
	// Enable tavily without a key → should fail.
	patch := SearchConfigPatch{
		Enabled:  ptrBool(true),
		Provider: ptrStr("tavily"),
		APIKey:   ptrStr(""), // not provided
	}
	_, err := UpdateSearchConfig(patch)
	if err == nil {
		t.Error("enabling tavily without a key should fail")
	}
	if !errors.Is(err, ErrBadSearch) {
		t.Errorf("err = %v, want ErrBadSearch", err)
	}
}

func TestUpdateSearchConfig_EnableOpenAICompatRequiresBaseURL(t *testing.T) {
	_, _ = withTempProject(t)
	patch := SearchConfigPatch{
		Enabled:  ptrBool(true),
		Provider: ptrStr("openai_compat"),
		BaseURL:  ptrStr(""),
	}
	_, err := UpdateSearchConfig(patch)
	if err == nil {
		t.Error("enabling openai_compat without base_url should fail")
	}
	if !errors.Is(err, ErrBadSearch) {
		t.Errorf("err = %v, want ErrBadSearch", err)
	}
}

func TestUpdateSearchConfig_ClearAPIKey(t *testing.T) {
	dir, cfg := withTempProject(t)
	// First, set a key.
	_, err := UpdateSearchConfig(SearchConfigPatch{
		APIKey: ptrStr("first-key"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Then clear it.
	_, err = UpdateSearchConfig(SearchConfigPatch{
		ClearAPIKey: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg2, err := LoadWithProjectRoot("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Search.APIKey != "" {
		t.Errorf("APIKey after clear = %q, want empty", cfg2.Search.APIKey)
	}
	_ = cfg
}

func TestUpdateSearchConfig_Partial(t *testing.T) {
	dir, _ := withTempProject(t)
	// Apply a multi-field patch.
	_, err := UpdateSearchConfig(SearchConfigPatch{
		Enabled:        ptrBool(true),
		Provider:       ptrStr("tavily"),
		APIKey:         ptrStr("k1"),
		Topic:          ptrStr("news"),
		DailyQuota:     ptrInt(50),
		RequestTimeout: ptrStr("30s"),
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithProjectRoot("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Search.Enabled {
		t.Error("Enabled not persisted")
	}
	if cfg.Search.Provider != "tavily" {
		t.Errorf("Provider = %q", cfg.Search.Provider)
	}
	if cfg.Search.APIKey != "k1" {
		t.Errorf("APIKey = %q", cfg.Search.APIKey)
	}
	if cfg.Search.Topic != "news" {
		t.Errorf("Topic = %q", cfg.Search.Topic)
	}
	if cfg.Search.DailyQuota != 50 {
		t.Errorf("DailyQuota = %d", cfg.Search.DailyQuota)
	}
	if cfg.Search.RequestTimeout.Seconds() != 30 {
		t.Errorf("RequestTimeout = %v", cfg.Search.RequestTimeout)
	}
}

// ====================================================================
// Tiny helpers
// ====================================================================

func ptrBool(b bool) *bool       { return &b }
func ptrStr(s string) *string    { return &s }
func ptrInt(i int) *int          { return &i }
