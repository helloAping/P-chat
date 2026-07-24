// tools.go (P1 stub — will be expanded in P2)
//
// This file defines the browser tool registration points called
// by the Manager. The actual tool implementations live in the
// same package and are wired up during P2.
package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/p-chat/pchat/internal/tool"
)

// browserToolNames is the complete list of tools this package
// can register. Used to clean up on unregister.
var browserToolNames = []string{
	"browser_navigate",
	"browser_click",
	"browser_type",
	"browser_press_key",
	"browser_scroll",
	"browser_hover",
	"browser_select_option",
	"browser_file_upload",
	"browser_drag",
	"browser_evaluate",
	"browser_snapshot",
	"browser_screenshot",
	"browser_find",
	"browser_tabs",
	"browser_extract",
}

// PolicyProvider returns the current BR-04 permission rules.
// Captured at registration so SetPolicy takes effect without
// re-registering handlers.
type PolicyProvider func() PolicyConfig

// RegisterBrowserTools installs all browser tools into the registry.
// Called by the Manager when a connection arrives. policyFn is
// optional — nil disables permission gating (tests / emergency).
func RegisterBrowserTools(r *tool.Registry, hub *BridgeHub, policyFn PolicyProvider) {
	defs := buildToolDefs()
	handlers := buildHandlers(hub, policyFn)
	for i, td := range defs {
		r.Register(td, handlers[i])
	}
}

// UnregisterBrowserTools removes all browser tools from the registry.
func UnregisterBrowserTools(r *tool.Registry) {
	for _, name := range browserToolNames {
		r.Unregister(name)
	}
}

// buildToolDefs returns the metadata (name, description, params schema)
// for every browser tool.
func buildToolDefs() []tool.Tool {
	bidPropOpt := map[string]any{
		"type":        "string",
		"description": "Browser connection ID. Omit to use the default browser.",
	}
	// tab_id is optional on all page-level tools. When omitted, the extension
	// uses the preferred tab selected in GUI (or the browser's active tab).
	tabIDPropOpt := map[string]any{
		"type":        "integer",
		"description": "Target tab id. Omit to use the preferred control tab selected in GUI (or the browser's currently active tab).",
	}

	return []tool.Tool{
		{
			Name:        "browser_navigate",
			Description: "Navigate the browser to a URL on the preferred/control target tab (or an explicit tab_id).",
			Parameters: tool.ObjectSchema(map[string]any{
				"url":        tool.StringProp("The URL to navigate to (must be fully-formed)."),
				"browser_id": bidPropOpt,
				"tab_id":     tabIDPropOpt,
			}, []string{"url"}),
		},
		{
			Name:        "browser_click",
			Description: "Click an element on the page. Use browser_snapshot first to get element refs.",
			Parameters: tool.ObjectSchema(map[string]any{
				"ref":        tool.StringProp("Element ref from browser_snapshot (e.g. 'button-3')."),
				"browser_id": bidPropOpt,
				"tab_id":     tabIDPropOpt,
			}, []string{"ref"}),
		},
		{
			Name:        "browser_type",
			Description: "Type text into an input element. Use browser_snapshot first to get element refs.",
			Parameters: tool.ObjectSchema(map[string]any{
				"ref":        tool.StringProp("Element ref from browser_snapshot."),
				"text":       tool.StringProp("Text to type."),
				"clear":      map[string]any{"type": "boolean", "description": "Clear existing content before typing."},
				"browser_id": bidPropOpt,
				"tab_id":     tabIDPropOpt,
			}, []string{"ref", "text"}),
		},
		{
			Name:        "browser_press_key",
			Description: "Press a keyboard key (e.g. 'Enter', 'Tab', 'Escape', 'ArrowDown').",
			Parameters: tool.ObjectSchema(map[string]any{
				"key":        tool.StringProp("Key name (e.g. 'Enter', 'Tab', 'Escape', single character)."),
				"browser_id": bidPropOpt,
				"tab_id":     tabIDPropOpt,
			}, []string{"key"}),
		},
		{
			Name:        "browser_scroll",
			Description: "Scroll the page up or down by page or half-page.",
			Parameters: tool.ObjectSchema(map[string]any{
				"direction":  tool.StringEnumProp("Scroll direction.", "up", "down"),
				"amount":     tool.StringEnumProp("Scroll amount.", "page", "half"),
				"browser_id": bidPropOpt,
				"tab_id":     tabIDPropOpt,
			}, []string{"direction"}),
		},
		{
			Name:        "browser_hover",
			Description: "Hover the mouse over an element (triggers tooltips, dropdowns, etc.).",
			Parameters: tool.ObjectSchema(map[string]any{
				"ref":        tool.StringProp("Element ref from browser_snapshot."),
				"browser_id": bidPropOpt,
				"tab_id":     tabIDPropOpt,
			}, []string{"ref"}),
		},
		{
			Name:        "browser_select_option",
			Description: "Select an option in a <select> dropdown element.",
			Parameters: tool.ObjectSchema(map[string]any{
				"ref":        tool.StringProp("Element ref of the <select>."),
				"values":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Values to select."},
				"browser_id": bidPropOpt,
				"tab_id":     tabIDPropOpt,
			}, []string{"ref", "values"}),
		},
		{
			Name:        "browser_file_upload",
			Description: "Upload files via a file input element. Triggers the browser's file chooser and fills it.",
			Parameters: tool.ObjectSchema(map[string]any{
				"paths":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Absolute file paths to upload."},
				"browser_id": bidPropOpt,
				"tab_id":     tabIDPropOpt,
			}, []string{"paths"}),
		},
		{
			Name:        "browser_drag",
			Description: "Drag one element onto another.",
			Parameters: tool.ObjectSchema(map[string]any{
				"start_ref":  tool.StringProp("Ref of the element to drag from."),
				"end_ref":    tool.StringProp("Ref of the drop target."),
				"browser_id": bidPropOpt,
				"tab_id":     tabIDPropOpt,
			}, []string{"start_ref", "end_ref"}),
		},
		{
			Name:        "browser_evaluate",
			Description: "Execute a JavaScript expression in the page context via an isolated world (bypasses page CSP). Returns the result as a string. Use for DOM queries, reading __INITIAL_STATE__, etc.",
			Parameters: tool.ObjectSchema(map[string]any{
				"expression": tool.StringProp("JavaScript expression to evaluate. Has access to DOM APIs (querySelector, textContent, etc.) and page JS globals."),
				"browser_id": bidPropOpt,
				"tab_id":     tabIDPropOpt,
			}, []string{"expression"}),
		},
		{
			Name:        "browser_snapshot",
			Description: "Return a structured text snapshot of the page's interactive elements with ref IDs. Use this before clicking or typing to find target elements.",
			Parameters: tool.ObjectSchema(map[string]any{
				"browser_id": bidPropOpt,
				"tab_id":     tabIDPropOpt,
			}, nil),
		},
		{
			Name:        "browser_screenshot",
			Description: "Capture a screenshot of the visible viewport (JPEG, quality 80). Returns base64-encoded image data.",
			Parameters: tool.ObjectSchema(map[string]any{
				"full_page":  map[string]any{"type": "boolean", "description": "Capture full page height instead of viewport. Default false."},
				"browser_id": bidPropOpt,
				"tab_id":     tabIDPropOpt,
			}, nil),
		},
		{
			Name:        "browser_find",
			Description: "Search the page for text matching a string or regex. Returns matching nodes with refs.",
			Parameters: tool.ObjectSchema(map[string]any{
				"text":       map[string]any{"type": "string", "description": "Plain text to search for (case-insensitive)."},
				"regex":      map[string]any{"type": "string", "description": "Regular expression to search for. Provide either text or regex, not both."},
				"browser_id": bidPropOpt,
				"tab_id":     tabIDPropOpt,
			}, nil),
		},
		{
			Name:        "browser_tabs",
			Description: "Manage browser tabs: list open tabs, open a new one, close one, or switch the preferred control target. Prefer tab_id over index when available. action=select also sets the preferred tab used by other browser_* tools.",
			Parameters: tool.ObjectSchema(map[string]any{
				"action":     tool.StringEnumProp("Tab operation.", "list", "new", "close", "select"),
				"tab_id":     map[string]any{"type": "integer", "description": "Chrome tab id (preferred for close/select)."},
				"index":      map[string]any{"type": "integer", "description": "Tab index fallback (for close and select when tab_id is unknown)."},
				"url":        map[string]any{"type": "string", "description": "URL to open in new tab (action=new)."},
				"browser_id": bidPropOpt,
			}, []string{"action"}),
		},
		{
			Name:        "browser_extract",
			Description: "Extract all visible text content from the current page (rendered by JavaScript). Ideal for SPA pages where browser_snapshot only returns interactive elements. Returns url, title, and visible_text.",
			Parameters: tool.ObjectSchema(map[string]any{
				"browser_id": bidPropOpt,
				"tab_id":     tabIDPropOpt,
			}, nil),
		},
	}
}

// buildHandlers returns the handler functions matching buildToolDefs.
func buildHandlers(hub *BridgeHub, policyFn PolicyProvider) []tool.ToolHandler {
	return []tool.ToolHandler{
		makeHandler(hub, policyFn, "browser_navigate", "browser/navigate"),
		makeHandler(hub, policyFn, "browser_click", "browser/click"),
		makeHandler(hub, policyFn, "browser_type", "browser/type"),
		makeHandler(hub, policyFn, "browser_press_key", "browser/press_key"),
		makeHandler(hub, policyFn, "browser_scroll", "browser/scroll"),
		makeHandler(hub, policyFn, "browser_hover", "browser/hover"),
		makeHandler(hub, policyFn, "browser_select_option", "browser/select_option"),
		makeHandler(hub, policyFn, "browser_file_upload", "browser/file_upload"),
		makeHandler(hub, policyFn, "browser_drag", "browser/drag"),
		makeHandler(hub, policyFn, "browser_evaluate", "browser/evaluate"),
		makeHandler(hub, policyFn, "browser_snapshot", "browser/snapshot"),
		makeHandler(hub, policyFn, "browser_screenshot", "browser/screenshot"),
		makeHandler(hub, policyFn, "browser_find", "browser/find"),
		makeHandler(hub, policyFn, "browser_tabs", "browser/tabs"),
		makeHandler(hub, policyFn, "browser_extract", "browser/extract"),
	}
}

// makeHandler builds a generic tool handler that forwards args to the
// extension via the Hub. The method name follows JSON-RPC naming
// convention (e.g. "browser/navigate").
//
// BR-04: before forwarding, the handler runs Decide() against the
// current policy and page URL. Block → error tool result; Confirm →
// shared ToolConfirmModal via tool.RequireConfirm; Allow → proceed.
//
// Args are unmarshalled, injected with browser_id if absent, then
// forwarded to the extension. The extension's response is returned
// verbatim to the LLM.
func makeHandler(hub *BridgeHub, policyFn PolicyProvider, toolName, method string) tool.ToolHandler {
	return func(ctx context.Context, args json.RawMessage) (*tool.CallResult, error) {
		var params map[string]any
		if len(args) > 0 {
			if err := json.Unmarshal(args, &params); err != nil {
				return &tool.CallResult{
					Content: fmt.Sprintf("invalid args: %v", err),
					IsError: true,
				}, nil
			}
		} else {
			params = make(map[string]any)
		}

		browserID, _ := params["browser_id"].(string)
		// Inject preferred tab when the model did not specify one.
		// browser/tabs manages tabs itself; do not force tab_id there.
		pageURL := ""
		if c, err := hub.getClient(browserID); err == nil {
			pageURL = c.ActiveTabURL()
			if method != "browser/tabs" {
				if _, has := params["tab_id"]; !has {
					if aid := c.ActiveTabID(); aid != 0 {
						params["tab_id"] = aid
					}
				}
			}
		}

		// BR-04 permission gate (before any extension round-trip).
		if blocked, result := gateBrowserCall(ctx, policyFn, toolName, params, pageURL, args); blocked {
			return result, nil
		}

		timeout := defaultCommandTimeout

		resp, err := hub.SendCommand(ctx, browserID, method, params, timeout)
		if err != nil {
			return &tool.CallResult{
				Content: fmt.Sprintf("browser command %s failed: %v", method, err),
				IsError: true,
			}, nil
		}

		if resp.Error != nil {
			return &tool.CallResult{
				Content: fmt.Sprintf("browser %s error: %s", strings.TrimPrefix(method, "browser/"), resp.Error.Message),
				IsError: true,
			}, nil
		}

		// browser_screenshot: Content carries a short metadata
		// description (for the LLM — keeps base64 out of the tool
		// result); RawFull holds the full data URL for the
		// frontend's ToolCallCard rendering; Image carries the
		// decoded base64 + MIME so the agent can inject a
		// separate role=user, type=image ChatMessage for the LLM.
		// Other methods return the extension result verbatim.
		if method == "browser/screenshot" {
			dataURL := extractScreenshotURL(resp.Result)
			if dataURL == "" {
				// No data URL found — fall back to verbatim.
				return &tool.CallResult{Content: string(resp.Result)}, nil
			}
			rawB64, mime := splitDataURL(dataURL)
			return &tool.CallResult{
				Content: "[浏览器截图已截取，下方附有图片，请分析图片内容]",
				RawFull: dataURL,
				Image: &tool.CallResultImage{
					Data:     rawB64,
					MIMEType: mime,
					Name:     "browser-screenshot.jpg",
				},
			}, nil
		}

		// Keep preferred-tab cache in sync when tools manage tabs
		// or navigate (so subsequent Confirm modals show the new URL).
		if method == "browser/tabs" {
			refreshPreferredFromTabsResult(hub, browserID, params, resp.Result)
		}
		if method == "browser/navigate" {
			refreshPreferredFromNavigate(hub, browserID, params)
		}

		return &tool.CallResult{
			Content: string(resp.Result),
		}, nil
	}
}

// gateBrowserCall applies BR-04 policy. Returns blocked=true when the
// call must not proceed (hard block or user rejected / confirm error).
func gateBrowserCall(
	ctx context.Context,
	policyFn PolicyProvider,
	toolName string,
	params map[string]any,
	pageURL string,
	rawArgs json.RawMessage,
) (blocked bool, result *tool.CallResult) {
	if policyFn == nil {
		return false, nil
	}
	// permission=full skips confirm AND allowlist-only paths, but
	// still honour hard BlockedHosts so a misconfigured "full"
	// session cannot drive the browser onto blocked domains.
	cfg := policyFn()
	verdict := Decide(cfg, toolName, params, pageURL)

	// evaluate write guard (independent of domain policy).
	if toolName == "browser_evaluate" && !cfg.AllowEvalWrite {
		// Soft note only — we still allow the call; the extension
		// side may further restrict. Leave as policy signal for now.
		_ = cfg.AllowEvalWrite
	}

	switch verdict.Decision {
	case tool.SandboxBlock:
		return true, &tool.CallResult{
			Content: fmt.Sprintf(
				"E_BROWSER_POLICY: blocked\n  tool: %s\n  url: %s\n  host: %s\n  reason: %s",
				toolName, verdict.TargetURL, verdict.Host, verdict.Reason,
			),
			IsError: true,
		}
	case tool.SandboxConfirm:
		// Build a confirm payload the existing ToolConfirmModal
		// already understands. ResolvedPath carries the page URL
		// so the modal's "目标路径" row becomes "目标页面".
		argsStr := string(rawArgs)
		if argsStr == "" {
			if b, err := json.Marshal(params); err == nil {
				argsStr = string(b)
			}
		}
		req := tool.ConfirmRequest{
			ToolName:     toolName,
			Args:         argsStr,
			Reason:       verdict.Reason,
			ResolvedPath: verdict.TargetURL,
			PathClass:    verdict.PathClass,
			RiskLevel:    verdict.RiskLevel,
		}
		approved, err := tool.RequireConfirm(ctx, req)
		if err != nil {
			return true, &tool.CallResult{
				Content: fmt.Sprintf("E_BROWSER_POLICY: confirm failed: %v", err),
				IsError: true,
			}
		}
		if !approved {
			return true, &tool.CallResult{
				Content: "工具调用被用户拒绝",
				IsError: true,
			}
		}
		return false, nil
	default:
		return false, nil
	}
}

// refreshPreferredFromNavigate updates the cached active-tab URL
// after a successful navigate so the next policy check sees the
// destination host.
func refreshPreferredFromNavigate(hub *BridgeHub, browserID string, params map[string]any) {
	c, err := hub.getClient(browserID)
	if err != nil {
		return
	}
	url, _ := params["url"].(string)
	if strings.TrimSpace(url) == "" {
		return
	}
	id := c.ActiveTabID()
	if tid, ok := params["tab_id"].(float64); ok && int(tid) != 0 {
		id = int(tid)
	} else if tid, ok := params["tab_id"].(int); ok && tid != 0 {
		id = tid
	}
	c.SetActiveTabMeta(id, "", url, 0)
}

// refreshPreferredFromTabsResult updates hub cache after browser_tabs tool calls.
func refreshPreferredFromTabsResult(hub *BridgeHub, browserID string, params map[string]any, raw json.RawMessage) {
	c, err := hub.getClient(browserID)
	if err != nil {
		return
	}
	action, _ := params["action"].(string)
	switch action {
	case "list":
		var result TabsListResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return
		}
		activeID := 0
		title, url := "", ""
		if result.PreferredTabID != nil {
			activeID = *result.PreferredTabID
		}
		for _, t := range result.Tabs {
			if activeID != 0 && t.ID == activeID {
				title, url = t.Title, t.URL
				break
			}
			if activeID == 0 && t.Active {
				activeID = t.ID
				title, url = t.Title, t.URL
			}
		}
		c.SetActiveTabMeta(activeID, title, url, len(result.Tabs))
	case "select", "new":
		var result struct {
			ID             int    `json:"id"`
			Title          string `json:"title"`
			URL            string `json:"url"`
			PreferredTabID int    `json:"preferred_tab_id"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return
		}
		id := result.PreferredTabID
		if id == 0 {
			id = result.ID
		}
		if id != 0 {
			c.SetActiveTabMeta(id, result.Title, result.URL, 0)
		}
	case "close":
		var result struct {
			PreferredTabID *int `json:"preferred_tab_id"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return
		}
		if result.PreferredTabID == nil || *result.PreferredTabID == 0 {
			c.SetActiveTabMeta(0, "", "", 0)
		} else {
			c.SetActiveTabMeta(*result.PreferredTabID, "", "", 0)
		}
	}
}

// defaultCommandTimeout is the per-request deadline for browser commands.
// Most operations (click, navigate) complete quickly; screenshot may take
// longer so the extension should set its own tighter deadline.
const defaultCommandTimeout = 30 * time.Second

// extractScreenshotURL finds the data:image/... URL inside the extension's
// screenshot response. Accepts both wrapper forms:
//
//	{"image": "data:image/jpeg;base64,..."}
//	"data:image/jpeg;base64,..."
//
// Returns the full data URL or "" if not found.
func extractScreenshotURL(raw []byte) string {
	s := string(raw)
	if strings.HasPrefix(s, "data:image/") {
		return s
	}
	// Extract from JSON wrapper {"image":"data:image/..."}.
	var wrapper struct {
		Image string `json:"image"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil && strings.HasPrefix(wrapper.Image, "data:image/") {
		return wrapper.Image
	}
	return ""
}

// splitDataURL splits a "data:<mime>;base64,<payload>" URL into its raw
// base64 data and the MIME type. If the URL doesn't match the expected
// shape, the entire string is returned as data with an "image/jpeg"
// default MIME type (most browser screenshots are JPEG).
func splitDataURL(url string) (rawB64, mime string) {
	if !strings.HasPrefix(url, "data:") {
		return url, "image/jpeg"
	}
	rest := url[len("data:"):]
	i := strings.Index(rest, ";base64,")
	if i < 0 {
		// Might be "data:image/jpeg,<raw>" or other form — just
		// return the whole thing and hope upstream handles it.
		return rest, "image/jpeg"
	}
	mime = rest[:i]
	rawB64 = rest[i+len(";base64,"):]
	return rawB64, mime
}
