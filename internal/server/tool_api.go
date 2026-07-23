package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/paths"
	"github.com/p-chat/pchat/internal/tool"
	"github.com/p-chat/pchat/internal/tool/dynamic"
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
	// Dynamic is true for tools loaded from a YAML tools directory.
	Dynamic bool `json:"dynamic"`
	// Scope identifies where this effective tool came from: builtin,
	// global, or project. Project entries override global entries.
	Scope string `json:"scope"`
	// Source is the absolute path to the YAML file the
	// tool was loaded from. Empty for built-ins.
	Source string `json:"source,omitempty"`
	// ProjectRoot is set for project-level tools loaded from
	// <project>/.p-chat/tools/*.yaml.
	ProjectRoot string `json:"project_root,omitempty"`
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
	projectRoot := h.toolsProjectRoot(c)
	h.loadProjectDynamicTools(projectRoot)
	if h.toolReg == nil {
		// Test server without a registry — return an
		// empty list rather than 500, so the UI can
		// still render an empty state cleanly.
		c.JSON(http.StatusOK, gin.H{"tools": []ToolInfo{}, "diagnostics": dynamic.DiagnosticsSnapshotForRoot(projectRoot)})
		return
	}
	entries := h.toolReg.ListEntriesForProject(projectRoot)
	out := make([]ToolInfo, 0, len(entries))
	for _, entry := range entries {
		origin := entry.Origin
		info := ToolInfo{
			Name:        entry.Name,
			Description: entry.Description,
			Parameters:  entry.Parameters,
			Scope:       string(origin.Scope),
			Source:      origin.Source,
			ProjectRoot: origin.ProjectRoot,
		}
		info.Dynamic = origin.Scope == tool.ToolOriginGlobal || origin.Scope == tool.ToolOriginProject
		out = append(out, info)
	}
	// Stable order: built-ins first, then global custom tools, then project
	// custom tools. Registry already applies the same scope ordering; keep a
	// defensive sort here for the HTTP contract.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return toolInfoScopeRank(out[i].Scope) < toolInfoScopeRank(out[j].Scope)
		}
		return out[i].Name < out[j].Name
	})
	c.JSON(http.StatusOK, gin.H{"tools": out, "diagnostics": dynamic.DiagnosticsSnapshotForRoot(projectRoot)})
}

type toolTrialRequest struct {
	Arguments json.RawMessage `json:"arguments"`
	DryRun    bool            `json:"dry_run"`
}

type toolTrialResponse struct {
	Name    string `json:"name"`
	Args    string `json:"args"`
	DryRun  bool   `json:"dry_run"`
	Status  string `json:"status"`
	Result  string `json:"result"`
	Error   string `json:"error,omitempty"`
	Elapsed string `json:"elapsed"`
}

// TrialTool POST /api/v1/tools/:name/trial runs a dynamic tool directly from
// the tools drawer. Built-ins are intentionally excluded: this endpoint is for
// validating user YAML without starting a chat turn.
func (h *Handler) TrialTool(c *gin.Context) {
	name := c.Param("name")
	projectRoot := h.toolsProjectRoot(c)
	h.loadProjectDynamicTools(projectRoot)
	if h.toolReg == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "tool registry unavailable"})
		return
	}
	spec, ok := dynamic.LookupSpecForRoot(name, projectRoot)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "dynamic tool not found"})
		return
	}
	_, handler, ok := h.toolReg.LookupForProject(name, projectRoot)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "tool handler not found"})
		return
	}
	var req toolTrialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
		return
	}
	args := req.Arguments
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}
	if !json.Valid(args) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "arguments must be a JSON object"})
		return
	}
	var obj map[string]any
	if err := json.Unmarshal(args, &obj); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "arguments must be a JSON object"})
		return
	}

	start := time.Now()
	var (
		res *tool.CallResult
		err error
	)
	if req.DryRun {
		res, err = dynamic.Preview(spec, args)
	} else {
		if spec.Template.Type != "echo" && spec.Sandbox.Exec != "allow" {
			msg := "dynamic tool real execution requires sandbox.exec: allow; use dry-run to preview safely"
			c.JSON(http.StatusOK, toolTrialResponse{Name: name, Args: string(args), DryRun: false, Status: "error", Error: msg, Result: msg, Elapsed: time.Since(start).Round(time.Millisecond).String()})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), spec.Template.Timeout.Std())
		defer cancel()
		res, err = handler(ctx, args)
	}
	elapsed := time.Since(start).Round(time.Millisecond).String()
	if err != nil {
		c.JSON(http.StatusOK, toolTrialResponse{Name: name, Args: string(args), DryRun: req.DryRun, Status: "error", Error: err.Error(), Result: err.Error(), Elapsed: elapsed})
		return
	}
	if res == nil {
		c.JSON(http.StatusOK, toolTrialResponse{Name: name, Args: string(args), DryRun: req.DryRun, Status: "error", Error: "tool returned no result", Result: "tool returned no result", Elapsed: elapsed})
		return
	}
	status := "ok"
	if res.IsError {
		status = "error"
	}
	resp := toolTrialResponse{Name: name, Args: string(args), DryRun: req.DryRun, Status: status, Result: res.Content, Elapsed: elapsed}
	if res.IsError {
		resp.Error = res.Content
	}
	c.JSON(http.StatusOK, resp)
}
func (h *Handler) loadProjectDynamicTools(projectRoot string) {
	if projectRoot == "" || h.toolReg == nil {
		return
	}
	entries, specs, diagnostics, err := dynamic.LoadSnapshot(paths.ProjectToolsDirWithRoot(projectRoot), func(name string) map[string]any {
		cfg := h.getCfg()
		if cfg == nil || cfg.Dynamic == nil {
			return nil
		}
		return cfg.Dynamic[name]
	})
	if err != nil {
		return
	}
	origin := tool.ToolOrigin{Scope: tool.ToolOriginProject, ProjectRoot: projectRoot}
	h.toolReg.SetDynamicSnapshot(origin, entries)
	dynamic.SetSpecsForRoot(tool.ToolOriginProject, projectRoot, specs)
	dynamic.SetDiagnosticsForRoot(tool.ToolOriginProject, projectRoot, diagnostics)
}

func (h *Handler) toolsProjectRoot(c *gin.Context) string {
	if sessionID := c.Query("session_id"); sessionID != "" {
		return h.sessionProjectPath(sessionID)
	}
	return c.Query("project_path")
}

func toolInfoScopeRank(scope string) int {
	switch scope {
	case string(tool.ToolOriginBuiltin):
		return 0
	case string(tool.ToolOriginGlobal):
		return 1
	case string(tool.ToolOriginProject):
		return 2
	default:
		return 3
	}
}
