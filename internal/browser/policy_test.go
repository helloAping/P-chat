package browser

import (
	"testing"

	"github.com/p-chat/pchat/internal/tool"
)

func TestMatchHost(t *testing.T) {
	cases := []struct {
		host, pattern string
		want          bool
	}{
		{"example.com", "example.com", true},
		{"EXAMPLE.com", "example.com", true},
		{"sub.example.com", "*.example.com", true},
		{"example.com", "*.example.com", true},
		{"evil-example.com", "*.example.com", false},
		{"example.com", "other.com", false},
		{"", "example.com", false},
		{"example.com", "", false},
	}
	for _, c := range cases {
		if got := matchHost(c.host, c.pattern); got != c.want {
			t.Errorf("matchHost(%q, %q) = %v, want %v", c.host, c.pattern, got, c.want)
		}
	}
}

func TestHostOf(t *testing.T) {
	if got := hostOf("https://Accounts.Google.com/path?q=1"); got != "accounts.google.com" {
		t.Fatalf("hostOf full URL = %q", got)
	}
	if got := hostOf("example.com"); got != "example.com" {
		t.Fatalf("hostOf bare = %q", got)
	}
	if got := hostOf(""); got != "" {
		t.Fatalf("hostOf empty = %q", got)
	}
}

func TestActionRisk(t *testing.T) {
	if actionRisk("browser_snapshot", nil) != "low" {
		t.Fatal("snapshot should be low")
	}
	if actionRisk("browser_navigate", map[string]any{"url": "https://x"}) != "medium" {
		t.Fatal("navigate should be medium")
	}
	if actionRisk("browser_type", map[string]any{"text": "hi"}) != "high" {
		t.Fatal("type should be high")
	}
	if actionRisk("browser_tabs", map[string]any{"action": "list"}) != "low" {
		t.Fatal("tabs list should be low")
	}
	if actionRisk("browser_tabs", map[string]any{"action": "new"}) != "medium" {
		t.Fatal("tabs new should be medium")
	}
}

func TestDecide_Table(t *testing.T) {
	cfg := PolicyConfig{
		RequireConfirm: "dangerous",
		BlockedHosts:   []string{"evil.example"},
		AllowedHosts:   []string{"trusted.local"},
		SensitiveHosts: []string{"accounts.google.com", "*.alipay.com"},
	}

	cases := []struct {
		name     string
		tool     string
		params   map[string]any
		pageURL  string
		wantDec  tool.SandboxDecision
		wantRisk string
	}{
		{
			name:    "blocked host hard-blocks navigate",
			tool:    "browser_navigate",
			params:  map[string]any{"url": "https://evil.example/x"},
			wantDec: tool.SandboxBlock, wantRisk: "medium",
		},
		{
			name:    "allowed host auto-passes type",
			tool:    "browser_type",
			params:  map[string]any{"ref": "i1", "text": "secret"},
			pageURL: "https://trusted.local/login",
			wantDec: tool.SandboxAllow, wantRisk: "high",
		},
		{
			name:    "sensitive host confirms snapshot",
			tool:    "browser_snapshot",
			pageURL: "https://accounts.google.com/",
			wantDec: tool.SandboxConfirm, wantRisk: "low",
		},
		{
			name:    "sensitive wildcard confirms type",
			tool:    "browser_type",
			params:  map[string]any{"ref": "i1", "text": "x"},
			pageURL: "https://www.alipay.com/pay",
			wantDec: tool.SandboxConfirm, wantRisk: "high",
		},
		{
			name:    "ordinary navigate auto-passes",
			tool:    "browser_navigate",
			params:  map[string]any{"url": "https://example.com/docs"},
			wantDec: tool.SandboxAllow, wantRisk: "medium",
		},
		{
			name:    "high-risk type on normal host confirms",
			tool:    "browser_type",
			params:  map[string]any{"ref": "i1", "text": "pwd"},
			pageURL: "https://example.com/login",
			wantDec: tool.SandboxConfirm, wantRisk: "high",
		},
		{
			name:    "low-risk snapshot on normal host allows",
			tool:    "browser_snapshot",
			pageURL: "https://example.com/",
			wantDec: tool.SandboxAllow, wantRisk: "low",
		},
		{
			name:    "click without known URL confirms",
			tool:    "browser_click",
			params:  map[string]any{"ref": "b1"},
			pageURL: "",
			wantDec: tool.SandboxConfirm, wantRisk: "medium",
		},
		{
			name:    "require always confirms low-risk",
			tool:    "browser_snapshot",
			pageURL: "https://example.com/",
			wantDec: tool.SandboxConfirm, wantRisk: "low",
		},
	}

	// Override last case's config.
	for i, c := range cases {
		pc := cfg
		if c.name == "require always confirms low-risk" {
			pc.RequireConfirm = "always"
		}
		got := Decide(pc, c.tool, c.params, c.pageURL)
		if got.Decision != c.wantDec {
			t.Errorf("[%d] %s: Decision=%v want %v (reason=%q)", i, c.name, got.Decision, c.wantDec, got.Reason)
		}
		if got.RiskLevel != c.wantRisk {
			t.Errorf("[%d] %s: Risk=%q want %q", i, c.name, got.RiskLevel, c.wantRisk)
		}
		if got.PathClass != "browser" {
			t.Errorf("[%d] %s: PathClass=%q", i, c.name, got.PathClass)
		}
	}
}

func TestDecide_RequireNever(t *testing.T) {
	cfg := PolicyConfig{
		RequireConfirm: "never",
		BlockedHosts:   []string{"evil.example"},
	}
	// Still blocks blocked hosts.
	got := Decide(cfg, "browser_navigate", map[string]any{"url": "https://evil.example/"}, "")
	if got.Decision != tool.SandboxBlock {
		t.Fatalf("never mode must still honour blocked_hosts, got %v", got.Decision)
	}
	// High-risk otherwise allows.
	got = Decide(cfg, "browser_type", map[string]any{"text": "x"}, "https://example.com")
	if got.Decision != tool.SandboxAllow {
		t.Fatalf("never mode should allow high-risk, got %v", got.Decision)
	}
}

func TestResolveTargetURL(t *testing.T) {
	u := resolveTargetURL("browser_navigate", map[string]any{"url": "https://a.com"}, "https://b.com")
	if u != "https://a.com" {
		t.Fatalf("navigate prefers args.url, got %q", u)
	}
	u = resolveTargetURL("browser_click", map[string]any{"ref": "x"}, "https://b.com/page")
	if u != "https://b.com/page" {
		t.Fatalf("click uses page URL, got %q", u)
	}
}

func TestGateBrowserCall_Block(t *testing.T) {
	policyFn := func() PolicyConfig {
		return PolicyConfig{
			RequireConfirm: "dangerous",
			BlockedHosts:   []string{"evil.example"},
		}
	}
	blocked, result := gateBrowserCall(
		nil,
		policyFn,
		"browser_navigate",
		map[string]any{"url": "https://evil.example/x"},
		"",
		[]byte(`{"url":"https://evil.example/x"}`),
	)
	if !blocked {
		t.Fatal("expected blocked")
	}
	if result == nil || !result.IsError {
		t.Fatal("expected error result")
	}
	if result != nil && result.Content == "" {
		t.Fatal("expected non-empty error content")
	}
}

func TestGateBrowserCall_NilPolicyAllows(t *testing.T) {
	blocked, result := gateBrowserCall(nil, nil, "browser_type", map[string]any{"text": "x"}, "https://x", nil)
	if blocked || result != nil {
		t.Fatalf("nil policy should not gate, blocked=%v result=%v", blocked, result)
	}
}
