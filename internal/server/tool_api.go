package server

import (
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
)

// ToolInfo is the P3-2 response shape for GET /api/v1/tools.
// The frontend's ToolListDrawer renders one row per entry;
// the `dynamic` + `source` flags let it badge the user's
// own tools differently from the built-ins.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Parameters is the JSON Schema object the LLM sees
	// when deciding whether to call the tool. For
	// built-ins this comes from tool.Tool.Parameters; for
	// dynamic tools it's the user's YAML `parameters:`
	// block, compiled to a json.RawMessage in
	// dynamic.ParseSpec.
	Parameters any `json:"parameters,omitempty"`
	// Dynamic is true for tools the P3-2 watcher loaded
	// from ~/.p-chat/tools/*.yaml. The frontend uses it
	// to add the "自定义" badge and surface the source
	// YAML in an expandable section.
	Dynamic bool `json:"dynamic"`
	// Source is the absolute path to the YAML file the
	// tool was loaded from. Empty for built-ins.
	Source string `json:"source,omitempty"`
}

// ListTools GET /api/v1/tools
//
// Returns the full tool registry: built-ins (registered at
// startup) plus any dynamic tools the user has dropped in
// ~/.p-chat/tools/. The dynamic flag tells the frontend
// which entries to badge as "user-defined" without a second
// round-trip to read ~/.p-chat/tools/.
//
// Always returns 200 with at least an empty array. The
// registry is in-memory and shared with the agent loop, so
// the answer is what the LLM will see on the very next
// turn — no caching concerns on the client side.
func (h *Handler) ListTools(c *gin.Context) {
	if h.toolReg == nil {
		// Test server without a registry — return an
		// empty list rather than 500, so the UI can
		// still render an empty state cleanly.
		c.JSON(http.StatusOK, gin.H{"tools": []ToolInfo{}})
		return
	}
	tools := h.toolReg.List()
	out := make([]ToolInfo, 0, len(tools))
	for _, t := range tools {
		info := ToolInfo{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		}
		// Distinguish dynamic tools from built-ins. We
		// rely on the SourceFile helper in the registry;
		// if the tool was registered through the dynamic
		// watcher it has a non-empty source path. Built-in
		// tools never set Source, so this is a clean
		// discriminator.
		if src, ok := h.toolReg.SourceFile(t.Name); ok && src != "" {
			info.Dynamic = true
			info.Source = src
		}
		out = append(out, info)
	}
	// Stable order: built-ins first (alphabetical), then
	// dynamic tools (alphabetical). Keeps the UI table
	// sorted even after the user adds a new file.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Dynamic != out[j].Dynamic {
			return !out[i].Dynamic // built-ins first
		}
		return out[i].Name < out[j].Name
	})
	c.JSON(http.StatusOK, gin.H{"tools": out})
}
