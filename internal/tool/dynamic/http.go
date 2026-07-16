package dynamic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/p-chat/pchat/internal/tool"
)

// MakeHTTPHandler builds a tool.ToolHandler for a spec whose
// template.type == "http". Renders the URL / headers / body
// with the same text/template substitutions as the exec
// handler, then issues the request. The response body is
// returned as the tool's content (truncated to 32 KiB so a
// JSON-blob response doesn't OOM the chat).
//
// The handler does NOT do any auth / header sanitation. The
// user is in charge of the YAML they wrote; if they put an
// Authorization header with their API key in plaintext, they
// meant to. The plan explicitly defers any "trust level"
// model to a future iteration.
func MakeHTTPHandler(spec Spec) tool.ToolHandler {
	client := &http.Client{}
	return func(ctx context.Context, args json.RawMessage) (*tool.CallResult, error) {
		var argMap map[string]any
		if len(args) > 0 {
			if err := json.Unmarshal(args, &argMap); err != nil {
				return &tool.CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
			}
		}
		rc := RenderCtx{Args: argMap, Config: spec.Config}

		urlStr, err := render(spec.Template.URL, rc)
		if err != nil {
			return &tool.CallResult{Content: "url render: " + err.Error(), IsError: true}, nil
		}
		if urlStr == "" {
			return &tool.CallResult{Content: "rendered url is empty", IsError: true}, nil
		}

		bodyStr, err := render(spec.Template.Body, rc)
		if err != nil {
			return &tool.CallResult{Content: "body render: " + err.Error(), IsError: true}, nil
		}

		method := strings.ToUpper(strings.TrimSpace(spec.Template.Method))
		if method == "" {
			method = http.MethodGet
		}

		timeout := spec.Template.Timeout.Std()
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		var body io.Reader
		if bodyStr != "" {
			body = strings.NewReader(bodyStr)
		}
		req, err := http.NewRequestWithContext(runCtx, method, urlStr, body)
		if err != nil {
			return &tool.CallResult{Content: "build request: " + err.Error(), IsError: true}, nil
		}
		// Render and apply each header. Empty values are
		// skipped (a literal "" header is almost always a
		// mistake from a missing template substitution).
		for k, v := range spec.Template.Headers {
			rendered, rerr := render(v, rc)
			if rerr != nil {
				return &tool.CallResult{Content: fmt.Sprintf("header %q render: %v", k, rerr), IsError: true}, nil
			}
			if rendered == "" {
				continue
			}
			req.Header.Set(k, rendered)
		}
		// Default Content-Type for body-bearing requests if
		// the user didn't set one. Common API gateways reject
		// POST/PUT without it.
		if body != nil && req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := client.Do(req)
		if err != nil {
			return &tool.CallResult{Content: "http: " + err.Error(), IsError: true}, nil
		}
		defer resp.Body.Close()

		// Cap response read at 32 KiB. The LLM only needs
		// the first few KB of a JSON response to make sense
		// of it; larger bodies are almost always a paging
		// boundary that the user should re-shape with a
		// query parameter.
		const maxRead = 32 * 1024
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxRead+1))
		truncated := false
		if len(respBody) > maxRead {
			respBody = respBody[:maxRead]
			truncated = true
		}

		// Non-2xx is reported as a tool error but the body
		// is still surfaced so the LLM can debug (e.g. a
		// 422 with a structured error message).
		status := resp.StatusCode
		content := fmt.Sprintf("HTTP %d %s\n%s", status, http.StatusText(status), string(respBody))
		if truncated {
			content += "\n... (truncated)"
		}
		if status >= 400 {
			return &tool.CallResult{Content: content, IsError: true}, nil
		}
		return &tool.CallResult{Content: content}, nil
	}
}

// _ = bytes.NewReader — silence the unused import warning on
// builds where this file's only contribution is the http
// template. Keeps the import block stable if a future
// contributor adds a body-formatting helper.
var _ = bytes.NewReader
