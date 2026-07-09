package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/p-chat/pchat/internal/tool"
)

// confirmTargetFor is the 2026-07 refactor of the tool-confirm
// dispatch logic that used to live inline in ChatWithTools'
// goroutine (the per-tool outcome loop in agent.go:1761).
//
// Each I/O-bearing tool maps to a (decision, reason, resolved
// path) triple:
//
//   - exec_command  → CheckExecDecision(command)        (regex-based)
//   - write_file    → CheckWriteDecision(path, project) (path-class)
//   - read_file     → CheckReadDecision(path, project)  (path-class)
//   - read_docx     → CheckReadDecision(path, project)
//   - read_pdf      → CheckReadDecision(path, project)
//   - list_files    → CheckReadDecision(path, project)
//
// Returns ok=false for tools the sandbox doesn't cover
// (question, todo, etc.) — the caller just falls through to
// the normal handler invocation.
//
// The resolved path is the same one the tool handler will see
// AFTER resolveToProjectRoot has run, so the user sees the
// path the LLM will actually touch (not the LLM's raw input,
// which may be a relative form or contain `..` segments).
type confirmTarget struct {
	Decision     tool.SandboxDecision
	Reason       string
	ResolvedPath string
	PathClass    string
	RiskLevel    string
}

// sandboxForConfirm is the minimal interface this helper needs
// from the sandbox. Defined as a local interface so we can pass
// a stub in tests without spinning up a real *sandbox.Sandbox.
type sandboxForConfirm interface {
	CheckExecDecision(command string) tool.SandboxDecision
	CheckWriteDecision(path, projectRoot string) tool.SandboxDecision
	CheckReadDecision(path, projectRoot string) tool.SandboxDecision
	MatchedPattern(command string) string
}

// confirmTargetFor returns the (decision, reason, resolved path)
// triple for the given tool call, plus an ok flag. The path
// resolution mirrors what handleXxx does (so the user sees the
// path the LLM will actually touch, with the project root
// prepended for relative inputs and `..` segments resolved).
func confirmTargetFor(toolName, argsJSON, projectRoot string, sb sandboxForConfirm) (confirmTarget, bool) {
	switch toolName {
	case "exec_command":
		var ea struct {
			Command string `json:"command"`
			WorkDir string `json:"work_dir,omitempty"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &ea)
		command := ea.Command
		if command == "" {
			command = argsJSON
		}
		decision := sb.CheckExecDecision(command)
		reason := sb.MatchedPattern(command)
		// If work_dir is set and points outside the project,
		// bump to Confirm so the user sees the out-of-tree
		// intent. P0 2026-07: without this, the LLM could
		// pass work_dir="/etc" and run commands there even
		// though the project root was /home/u/proj.
		if ea.WorkDir != "" && projectRoot != "" && filepath.IsAbs(ea.WorkDir) {
			if !isPathInsideClean(ea.WorkDir, projectRoot) {
				decision = tool.SandboxConfirm
				if reason == "" {
					reason = fmt.Sprintf("exec_command.work_dir=%q is outside the project root", ea.WorkDir)
				} else {
					reason = reason + "; work_dir outside project"
				}
			}
		}
		return confirmTarget{
			Decision:  decision,
			Reason:    reason,
			PathClass: classForWorkDir(ea.WorkDir, projectRoot),
			RiskLevel: "high", // exec_command is always "high" — it's arbitrary code
		}, true
	case "write_file":
		var wa struct{ Path string `json:"path"` }
		_ = json.Unmarshal([]byte(argsJSON), &wa)
		resolved := resolveForConfirm(wa.Path, projectRoot)
		decision := sb.CheckWriteDecision(resolved, projectRoot)
		return confirmTarget{
			Decision:     decision,
			Reason:       "",
			ResolvedPath: resolved,
			PathClass:    classForPath(resolved, projectRoot),
			RiskLevel:    "high", // writes mutate user data
		}, true
	case "read_file", "read_docx", "read_pdf":
		var ra struct{ Path string `json:"path"` }
		_ = json.Unmarshal([]byte(argsJSON), &ra)
		resolved := resolveForConfirm(ra.Path, projectRoot)
		decision := sb.CheckReadDecision(resolved, projectRoot)
		return confirmTarget{
			Decision:     decision,
			Reason:       "",
			ResolvedPath: resolved,
			PathClass:    classForPath(resolved, projectRoot),
			RiskLevel:    "low", // reads don't mutate
		}, true
	case "list_files":
		var la struct{ Path string `json:"path"` }
		_ = json.Unmarshal([]byte(argsJSON), &la)
		if la.Path == "" {
			la.Path = "."
		}
		resolved := resolveForConfirm(la.Path, projectRoot)
		decision := sb.CheckReadDecision(resolved, projectRoot)
		return confirmTarget{
			Decision:     decision,
			Reason:       "",
			ResolvedPath: resolved,
			PathClass:    classForPath(resolved, projectRoot),
			RiskLevel:    "low",
		}, true
	}
	// Tools the sandbox doesn't cover (question, todo, recall,
	// task, web_search, web_fetch in this Phase 1 — web_fetch
	// gets a sandbox check in Phase 2). Returning ok=false lets
	// the caller fall through to the normal handler invocation.
	return confirmTarget{}, false
}

// resolveForConfirm is the same logic handleXxx uses for path
// resolution: relative paths are joined to the project root,
// absolute paths are taken as-is, and the result is
// filepath.Clean'd so `..` segments are resolved before the
// path-class check (the P0 2026-07 hardening).
func resolveForConfirm(p, projectRoot string) string {
	if p == "" {
		return p
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	if projectRoot == "" {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(projectRoot, p))
}

// isPathInsideClean is the textual containment check used to
// detect "work_dir outside the project" escapes. The check is
// deliberately strict (no symlink resolution) because the LLM
// shouldn't be allowed to slip through a `..` segment in the
// work_dir argument even if a symlink would otherwise let it
// reach into the project.
func isPathInsideClean(path, root string) bool {
	if path == "" || root == "" {
		return false
	}
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(root)
	if cleanPath == cleanRoot {
		return true
	}
	rel, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return false
	}
	return true
}

// classForPath returns a short string for the confirm modal's
// "项目内 / 项目外 / 全局" label. The label is shown to the
// user so they understand the authorisation context — without
// it the modal looks identical for a /home/u/proj/foo.go read
// and a /etc/passwd read, which would confuse the user into
// approving the latter just because the former is benign.
func classForPath(resolved, projectRoot string) string {
	switch {
	case resolved == "":
		return "global"
	case projectRoot == "":
		return "external"
	case isPathInsideClean(resolved, projectRoot):
		return "project"
	default:
		return "global"
	}
}

// classForWorkDir is the work_dir analogue of classForPath —
// exec_command.work_dir is always treated as a "global" or
// "project" depending on whether it's under the project root.
// Empty work_dir means the command uses the project root (or
// the server CWD if no project) and is therefore classified
// as "project" / "external" depending on projectRoot.
func classForWorkDir(workDir, projectRoot string) string {
	if workDir == "" {
		return classForPath(projectRoot, projectRoot)
	}
	return classForPath(workDir, projectRoot)
}
