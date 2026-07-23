// policy.go implements BR-04 browser tool permission gating.
//
// Before a browser_* tool runs, Decide() extracts the target page URL
// (from navigate args or the preferred tab cache), classifies the
// action risk, matches domain rules, and returns Allow / Block /
// Confirm. Handlers then either proceed, reject, or WaitForConfirm.
package browser

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/tool"
)

// PolicyConfig is the runtime view of browser permission settings.
// Copied from config.BrowserConfig so handlers don't depend on the
// full config package beyond construction.
type PolicyConfig struct {
	RequireConfirm string
	AllowedHosts   []string
	BlockedHosts   []string
	SensitiveHosts []string
	AllowEvalWrite bool
}

// PolicyFromConfig converts config.BrowserConfig into a PolicyConfig
// with defaults applied.
func PolicyFromConfig(cfg config.BrowserConfig) PolicyConfig {
	rc := strings.ToLower(strings.TrimSpace(cfg.RequireConfirm))
	if rc == "" {
		rc = "dangerous"
	}
	return PolicyConfig{
		RequireConfirm: rc,
		AllowedHosts:   append([]string(nil), cfg.AllowedHosts...),
		BlockedHosts:   append([]string(nil), cfg.BlockedHosts...),
		SensitiveHosts: append([]string(nil), cfg.SensitiveHosts...),
		AllowEvalWrite: cfg.AllowEvalWrite,
	}
}

// PolicyVerdict is the outcome of Decide for one tool invocation.
type PolicyVerdict struct {
	Decision  tool.SandboxDecision
	Reason    string
	TargetURL string
	Host      string
	RiskLevel string // "low" | "medium" | "high"
	// PathClass reuses the confirm-modal chip channel. Always
	// "browser" so the UI can render a browser-specific label.
	PathClass string
}

// actionRisk classifies browser tools by side-effect severity.
//
//	low    — observation only (snapshot / extract / find / screenshot / scroll / hover / tabs list|select)
//	medium — navigation or mild UI interaction (navigate, tabs new/close, click, press_key, select, drag)
//	high   — form input, file upload, arbitrary JS (type, file_upload, evaluate)
func actionRisk(toolName string, params map[string]any) string {
	switch toolName {
	case "browser_snapshot", "browser_extract", "browser_find",
		"browser_screenshot", "browser_scroll", "browser_hover":
		return "low"
	case "browser_tabs":
		action, _ := params["action"].(string)
		switch strings.ToLower(action) {
		case "list", "select", "":
			return "low"
		case "new", "close":
			return "medium"
		default:
			return "medium"
		}
	case "browser_navigate", "browser_click", "browser_press_key",
		"browser_select_option", "browser_drag":
		return "medium"
	case "browser_type", "browser_file_upload", "browser_evaluate":
		return "high"
	default:
		// Unknown browser_* tool → fail-safe high.
		if strings.HasPrefix(toolName, "browser_") {
			return "high"
		}
		return "low"
	}
}

// resolveTargetURL picks the URL the tool will act on:
//   - browser_navigate → args.url
//   - browser_tabs action=new → args.url (may be empty)
//   - everything else → preferred tab URL from the hub cache
func resolveTargetURL(toolName string, params map[string]any, pageURL string) string {
	switch toolName {
	case "browser_navigate":
		if u, _ := params["url"].(string); strings.TrimSpace(u) != "" {
			return strings.TrimSpace(u)
		}
	case "browser_tabs":
		action, _ := params["action"].(string)
		if strings.EqualFold(action, "new") {
			if u, _ := params["url"].(string); strings.TrimSpace(u) != "" {
				return strings.TrimSpace(u)
			}
		}
	}
	return strings.TrimSpace(pageURL)
}

// hostOf extracts the hostname from a URL string. Returns "" when
// the input is empty or unparseable.
func hostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Tolerate bare hosts like "example.com" without a scheme.
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// matchHost reports whether host matches a single pattern.
// Patterns:
//   - exact: "example.com"
//   - subdomain wildcard: "*.example.com" (also matches example.com)
//   - empty pattern never matches
func matchHost(host, pattern string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if host == "" || pattern == "" {
		return false
	}
	if strings.HasPrefix(pattern, "*.") {
		base := pattern[2:]
		if host == base {
			return true
		}
		return strings.HasSuffix(host, "."+base)
	}
	return host == pattern
}

// hostInList reports whether host matches any pattern in list.
func hostInList(host string, list []string) bool {
	for _, p := range list {
		if matchHost(host, p) {
			return true
		}
	}
	return false
}

// Decide applies domain rules + action risk + require_confirm mode.
//
// Decision table (require_confirm = "dangerous", the default):
//
//	BlockedHosts hit          → Block
//	AllowedHosts hit          → Allow  (except require_confirm=always)
//	SensitiveHosts hit        → Confirm
//	risk=high (type/upload/js)→ Confirm
//	risk=medium (nav/click)   → Allow  (nav auto-pass per BR-04)
//	risk=low (read-only)      → Allow
//
// "always" confirms every call that is not blocked.
// "never" allows every call that is not blocked.
func Decide(cfg PolicyConfig, toolName string, params map[string]any, pageURL string) PolicyVerdict {
	if params == nil {
		params = map[string]any{}
	}
	target := resolveTargetURL(toolName, params, pageURL)
	host := hostOf(target)
	risk := actionRisk(toolName, params)

	v := PolicyVerdict{
		Decision:  tool.SandboxAllow,
		TargetURL: target,
		Host:      host,
		RiskLevel: risk,
		PathClass: "browser",
	}

	// Hard block list first — no confirm override.
	if host != "" && hostInList(host, cfg.BlockedHosts) {
		v.Decision = tool.SandboxBlock
		v.Reason = fmt.Sprintf("host %q is in browser.blocked_hosts", host)
		return v
	}

	mode := cfg.RequireConfirm
	if mode == "" {
		mode = "dangerous"
	}

	switch mode {
	case "never":
		v.Decision = tool.SandboxAllow
		v.Reason = "browser.require_confirm=never"
		return v
	case "always":
		v.Decision = tool.SandboxConfirm
		v.Reason = fmt.Sprintf("browser.require_confirm=always (%s risk)", risk)
		return v
	}

	// "dangerous" (and unknown → fail-safe same as dangerous).
	if host != "" && hostInList(host, cfg.AllowedHosts) {
		// Allowlist auto-passes even high-risk actions — the user
		// explicitly opened this host. evaluate still respects
		// AllowEvalWrite separately in the handler.
		v.Decision = tool.SandboxAllow
		v.Reason = fmt.Sprintf("host %q is in browser.allowed_hosts", host)
		return v
	}
	if host != "" && hostInList(host, cfg.SensitiveHosts) {
		v.Decision = tool.SandboxConfirm
		v.Reason = fmt.Sprintf("sensitive host %q requires confirmation", host)
		return v
	}
	switch risk {
	case "high":
		v.Decision = tool.SandboxConfirm
		v.Reason = fmt.Sprintf("high-risk browser action %s (form input / upload / evaluate)", toolName)
	case "medium":
		// BR-04: ordinary navigation / click auto-pass unless the
		// host is sensitive/blocked. Medium still confirms when
		// we have no URL at all (unknown page) and the action
		// mutates (click/press_key/select/drag/tabs close|new).
		if target == "" && toolName != "browser_navigate" && toolName != "browser_tabs" {
			v.Decision = tool.SandboxConfirm
			v.Reason = fmt.Sprintf("%s on unknown page URL — confirm before mutating", toolName)
		} else {
			v.Decision = tool.SandboxAllow
			v.Reason = fmt.Sprintf("%s auto-pass under dangerous mode", toolName)
		}
	default: // low
		v.Decision = tool.SandboxAllow
		v.Reason = fmt.Sprintf("%s is read-only / low risk", toolName)
	}
	return v
}
