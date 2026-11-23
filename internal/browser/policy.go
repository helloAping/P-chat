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
	"regexp"
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

type evalWritePattern struct {
	reason  string
	pattern *regexp.Regexp
}

type evalExpressionClass uint8

const (
	evalExpressionUnknown evalExpressionClass = iota
	evalExpressionReadOnly
	evalExpressionBlocked
)

// evalWritePatterns 覆盖常见的页面写入、导航、网络和跨边界 API。
// evalWritePatterns covers common page writes, navigation, network, and cross-boundary APIs.
// 普通读取（document.title、classList.contains、getComputedStyle、history.state）不会匹配。
// Plain reads (document.title, classList.contains, getComputedStyle, history.state) remain usable.
var evalWritePatterns = []evalWritePattern{
	{reason: "network or cross-context communication", pattern: regexp.MustCompile(`\b(?:fetch|XMLHttpRequest|WebSocket|EventSource|WebTransport|BroadcastChannel|Worker|SharedWorker|RTCPeerConnection)\b|\.(?:sendBeacon|postMessage)\s*\(`)},
	{reason: "dynamic code or deferred execution", pattern: regexp.MustCompile(`\b(?:eval|import|importScripts|setTimeout|setInterval|queueMicrotask|requestAnimationFrame)\s*\(|\bnew\s+(?:Function|Worker|SharedWorker)\b`)},
	{reason: "window or navigation mutation", pattern: regexp.MustCompile(`\bwindow\s*\.\s*(?:open|close)\s*\(|\blocation\s*(?:\.[A-Za-z_$][\w$]*)?\s*(?:\+=|-=|\*=|/=|%=|&&=|\|\|=|\?\?=|<<=|>>>=?|&=|\|=|\^=|=(?:[^=>]|$))|\blocation\s*\.\s*(?:assign|replace|reload)\s*\(`)},
	{reason: "history mutation or navigation", pattern: regexp.MustCompile(`\bhistory\s*\.\s*(?:pushState|replaceState|back|forward|go)\s*\(|\bnavigation\s*\.\s*(?:navigate|reload|traverseTo|back|forward)\s*\(`)},
	{reason: "clipboard or sharing access", pattern: regexp.MustCompile(`\bnavigator\s*\.\s*(?:clipboard|share)\b|\bdocument\s*\.\s*execCommand\s*\(`)},
	{reason: "persistent storage mutation", pattern: regexp.MustCompile(`\b(?:localStorage|sessionStorage)\s*\.\s*(?:setItem|removeItem|clear)\s*\(|\bindexedDB\s*\.\s*(?:open|deleteDatabase)\s*\(|\bcaches\s*\.\s*(?:open|delete)\s*\(|\.(?:add|addAll|put|delete)\s*\(`)},
	{reason: "service worker registration", pattern: regexp.MustCompile(`\bnavigator\s*\.\s*serviceWorker\s*\.\s*(?:register|unregister)\s*\(`)},
	{reason: "document stream mutation", pattern: regexp.MustCompile(`\bdocument\s*\.\s*(?:write|writeln|open|close)\s*\(`)},
	{reason: "DOM tree or form mutation", pattern: regexp.MustCompile(`\.(?:appendChild|removeChild|insertBefore|replaceChild|append|prepend|remove|replaceWith|replaceChildren|before|after|insertAdjacentHTML|insertAdjacentText|insertAdjacentElement|setAttribute|setAttributeNS|removeAttribute|removeAttributeNS|toggleAttribute|submit|requestSubmit|reset|click|focus|blur|dispatchEvent|setRangeText|insertNode|deleteContents)\s*\(`)},
	{reason: "style or class mutation", pattern: regexp.MustCompile(`\.(?:classList)\s*\.\s*(?:add|remove|toggle|replace)\s*\(|\.style\s*\.\s*(?:setProperty|removeProperty)\s*\(|\.(?:insertRule|deleteRule|replaceSync)\s*\(|\bCSSStyleSheet\s*\.\s*prototype\s*\.\s*replace\b`)},
	{reason: "reflective mutation", pattern: regexp.MustCompile(`\bObject\s*\.\s*(?:assign|defineProperty|defineProperties|setPrototypeOf)\s*\(|\bReflect\s*\.\s*(?:set|deleteProperty|defineProperty|setPrototypeOf)\s*\(`)},
	{reason: "property assignment", pattern: regexp.MustCompile(`(?:\.\s*[A-Za-z_$][\w$]*|\])\s*(?:\+=|-=|\*=|/=|%=|&&=|\|\|=|\?\?=|<<=|>>>=?|&=|\|=|\^=|=(?:[^=>]|$))`)},
	{reason: "property update or deletion", pattern: regexp.MustCompile(`(?:\.\s*[A-Za-z_$][\w$]*|\])\s*(?:\+\+|--)|\bdelete\s+`)},
}

func evalWriteReason(expr string) string {
	masked, ok := maskEvalStrings(expr)
	if !ok || strings.TrimSpace(masked) == "" {
		return "expression is empty or missing"
	}
	return evalWriteReasonMasked(masked)
}

// evalWriteReasonMasked evaluates only executable JavaScript tokens. Callers
// must mask strings and comments first, because selector text and comments may
// legitimately contain names such as "fetch" without invoking them.
func evalWriteReasonMasked(masked string) string {
	for _, rule := range evalWritePatterns {
		if rule.pattern.MatchString(masked) {
			return rule.reason
		}
	}
	return ""
}

var (
	evalReadOnlyRoot = regexp.MustCompile(`^\s*(?:document|window|globalThis|location|history|navigator|localStorage|sessionStorage|JSON|Math|Number|String|Boolean|Array|getComputedStyle)\b`)
	evalCallPattern  = regexp.MustCompile(`\b([A-Za-z_$][\w$]*(?:\s*\.\s*[A-Za-z_$][\w$]*)*)\s*\(`)
	evalReadUnknown  = regexp.MustCompile(`(?:=>|[;{}]|\b(?:new|function|class|async|await|yield|throw|try|catch|finally|for|while|do|switch|with)\b)`)
	evalAssignment   = regexp.MustCompile(`(?:^|[^=!<>])=(?:[^=>]|$)`)
)

var evalReadOnlyCalls = map[string]struct{}{
	"querySelector": {}, "querySelectorAll": {}, "getElementById": {},
	"getElementsByClassName": {}, "getElementsByTagName": {}, "getElementsByName": {},
	"getAttribute": {}, "getAttributeNS": {}, "hasAttribute": {}, "hasAttributeNS": {},
	"matches": {}, "closest": {}, "contains": {}, "item": {}, "namedItem": {},
	"getItem": {}, "key": {}, "stringify": {}, "parse": {}, "from": {}, "isArray": {},
	"keys": {}, "values": {}, "entries": {}, "getOwnPropertyDescriptor": {},
	"getOwnPropertyDescriptors": {}, "getPrototypeOf": {}, "getComputedStyle": {},
	"getPropertyValue": {}, "trim": {}, "trimStart": {}, "trimEnd": {}, "includes": {},
	"startsWith": {}, "endsWith": {}, "slice": {}, "substring": {}, "substr": {},
	"toLowerCase": {}, "toUpperCase": {}, "normalize": {}, "toString": {},
}

func classifyEvalExpression(expr string) (evalExpressionClass, string) {
	masked, ok := maskEvalStrings(expr)
	if !ok {
		return evalExpressionUnknown, "expression contains an unterminated string, comment, or template literal"
	}
	if reason := evalWriteReasonMasked(masked); reason != "" {
		return evalExpressionBlocked, reason
	}
	if !evalReadOnlyRoot.MatchString(masked) || evalReadUnknown.MatchString(masked) || evalAssignment.MatchString(masked) {
		return evalExpressionUnknown, "expression is not in the conservative read-only subset"
	}
	calls := evalCallPattern.FindAllStringSubmatch(masked, -1)
	if strings.Count(masked, "(") != len(calls) || strings.Count(masked, ")") != len(calls) {
		return evalExpressionUnknown, "expression contains dynamic or grouped invocation"
	}
	for _, call := range calls {
		callee := strings.ReplaceAll(call[1], " ", "")
		parts := strings.Split(callee, ".")
		if _, allowed := evalReadOnlyCalls[parts[len(parts)-1]]; !allowed {
			return evalExpressionUnknown, "expression calls an API outside the read-only allowlist"
		}
	}
	return evalExpressionReadOnly, ""
}

// maskEvalStrings 用空格遮蔽普通字符串和注释，使只读分类只检查可执行 token。
// maskEvalStrings blanks ordinary strings and comments so read-only classification inspects executable tokens only.
// 模板字符串可能执行 ${...}，因此直接归为未知。Template literals may execute ${...}, so they stay unknown.
func maskEvalStrings(expr string) (string, bool) {
	out := []byte(expr)
	for i := 0; i < len(out); {
		switch out[i] {
		case '`':
			return string(out), false
		case '\'', '"':
			quote := out[i]
			out[i] = ' '
			i++
			closed := false
			for i < len(out) {
				if out[i] == '\\' {
					out[i] = ' '
					i++
					if i < len(out) {
						out[i] = ' '
						i++
					}
					continue
				}
				ch := out[i]
				out[i] = ' '
				i++
				if ch == quote {
					closed = true
					break
				}
			}
			if !closed {
				return string(out), false
			}
		case '/':
			if i+1 < len(out) && out[i+1] == '/' {
				for i < len(out) && out[i] != '\n' {
					out[i] = ' '
					i++
				}
				continue
			}
			if i+1 < len(out) && out[i+1] == '*' {
				out[i], out[i+1] = ' ', ' '
				i += 2
				closed := false
				for i+1 < len(out) {
					if out[i] == '*' && out[i+1] == '/' {
						out[i], out[i+1] = ' ', ' '
						i += 2
						closed = true
						break
					}
					out[i] = ' '
					i++
				}
				if !closed {
					return string(out), false
				}
				continue
			}
			i++
		default:
			i++
		}
	}
	return string(out), true
}

// LooksLikeEvalWrite 报告表达式是否命中默认拒绝的副作用规则。
// LooksLikeEvalWrite reports whether an expression matches a default-deny side-effect rule.
// 这是保守的词法闸门而非 JavaScript 沙箱；真正需要写页面时必须显式开启 allow_eval_write。
// This is a conservative lexical gate, not a JavaScript sandbox; page writes require explicit allow_eval_write.
func LooksLikeEvalWrite(expr string) bool {
	return evalWriteReason(expr) != ""
}
