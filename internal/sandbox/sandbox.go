// Package sandbox enforces safety rules on tool invocations. It
// supports two layers:
//
//   1. Path protection — write_file / edit_file must not touch a
//      list of protected paths (e.g. ~/.ssh, /etc, ~/.bashrc).
//   2. Pattern-based exec checks — exec_command is matched against a
//      list of regexes. Matches are categorized as "dangerous" and
//      either blocked outright or held for user confirmation.
//
// The sandbox is process-local: it stores no state and exposes pure
// functions suitable for concurrent use. The runtime configuration
// is loaded from config.SandboxConfig and converted once at startup.
package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/tool"
)

// Decision is the outcome of a sandbox check.
type Decision int

const (
	// Allow: the tool call may proceed.
	Allow Decision = iota
	// Block: the tool call is rejected (returned to the LLM as a
	// tool error so it can adjust its plan).
	Block
	// Confirm: the tool call needs explicit user approval. The tool
	// dispatcher should not run the handler until the user types "yes".
	Confirm
)

func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Block:
		return "block"
	case Confirm:
		return "confirm"
	}
	return "unknown"
}

// Sandbox holds compiled patterns and the resolved write-protected
// paths. Construct one at startup via New and reuse for the life of
// the process.
type Sandbox struct {
	enabled       bool
	requireMode   string // "always" | "dangerous" | "confirm" | "never"
	maxCmdLen     int
	protectedDirs []string // resolved absolute paths (no ~ inside)
	protectedGlobs []string // raw globs that we still do a substring match for
	execPatterns  []*regexp.Regexp
	execNames     []string
}

// New compiles a sandbox from the user's config. If enabled is false
// the returned Sandbox permits everything.
func New(cfg config.SandboxConfig) (*Sandbox, error) {
	s := &Sandbox{
		enabled:     cfg.Enabled,
		requireMode: cfg.RequireConfirm,
		maxCmdLen:   cfg.MaxCommandLength,
	}
	if s.requireMode == "" {
		s.requireMode = "dangerous"
	}
	if s.maxCmdLen <= 0 {
		s.maxCmdLen = 4096
	}

	// Resolve protected paths to absolute form when possible.
	for _, p := range cfg.WriteProtectedPaths {
		if p == "" {
			continue
		}
		expanded := expandHome(p)
		if abs, err := filepath.Abs(expanded); err == nil {
			s.protectedDirs = append(s.protectedDirs, abs)
		} else {
			// Fall back to the literal pattern (substring match).
			s.protectedGlobs = append(s.protectedGlobs, p)
		}
	}

	// Compile exec patterns.
	for _, p := range cfg.ExecDangerousPatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("sandbox: compile exec pattern %q: %w", p, err)
		}
		s.execPatterns = append(s.execPatterns, re)
		s.execNames = append(s.execNames, p)
	}

	return s, nil
}

// Enabled reports whether sandboxing is active.
func (s *Sandbox) Enabled() bool { return s.enabled }

// CheckExec decides whether the given shell command may run.
//
// The decision is one of:
//   - Allow   — execute normally
//   - Block   — never run, return an error to the LLM
//   - Confirm — depends on the configured mode and pattern matches
func (s *Sandbox) CheckExec(command string) Decision {
	if !s.enabled {
		return Allow
	}
	if s.maxCmdLen > 0 && len(command) > s.maxCmdLen {
		return Block
	}
	if !s.anyMatch(command) {
		return Allow
	}
	// A pattern matched. Decide based on RequireConfirm.
	switch s.requireMode {
	case "never":
		return Block
	case "always":
		return Confirm
	case "confirm":
		return Confirm
	default: // "dangerous"
		return Block
	}
}

// CheckWrite decides whether a file path may be written.
func (s *Sandbox) CheckWrite(path string) Decision {
	if !s.enabled {
		return Allow
	}
	if path == "" {
		return Block
	}
	expanded := expandHome(path)
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return Block
	}

	// Match against absolute protected dirs.
	for _, dir := range s.protectedDirs {
		if isPathUnder(abs, dir) {
			return Block
		}
	}
	// Substring match against glob patterns.
	for _, g := range s.protectedGlobs {
		if strings.Contains(strings.ToLower(abs), strings.ToLower(g)) {
			return Block
		}
	}
	return Allow
}

// Allowed returns true when the decision permits the call to proceed
// (i.e. Allow or Confirm). Tools that don't support interactive
// confirmation should treat Confirm the same as Allow; tools that
// do should use the more granular Decision directly.
func (d Decision) Allowed() bool { return d != Block }

// IsConfirm returns true if the user should be asked first.
func (d Decision) IsConfirm() bool { return d == Confirm }

// CheckExecBool satisfies the tool.SandboxChecker interface (returns
// true when the call is allowed). Treats Confirm the same as Allow;
// use CheckExec directly for tools that support user prompts.
func (s *Sandbox) CheckExecBool(command string) bool {
	return s.CheckExec(command).Allowed()
}

// CheckWriteBool satisfies the tool.SandboxChecker interface.
func (s *Sandbox) CheckWriteBool(path string) bool {
	return s.CheckWrite(path).Allowed()
}

// CheckExecDecision satisfies the expanded tool.SandboxChecker.
func (s *Sandbox) CheckExecDecision(command string) tool.SandboxDecision {
	return tool.SandboxDecision(s.CheckExec(command))
}

// CheckWriteDecision satisfies the expanded tool.SandboxChecker.
func (s *Sandbox) CheckWriteDecision(path string) tool.SandboxDecision {
	return tool.SandboxDecision(s.CheckWrite(path))
}

// anyMatch returns true if command matches any dangerous pattern.
func (s *Sandbox) anyMatch(command string) bool {
	for _, re := range s.execPatterns {
		if re.MatchString(command) {
			return true
		}
	}
	return false
}

// MatchedPattern returns the name of the first dangerous pattern that
// matched command, or "" if none matched.
func (s *Sandbox) MatchedPattern(command string) string {
	for i, re := range s.execPatterns {
		if re.MatchString(command) {
			return s.execNames[i]
		}
	}
	return ""
}

// --- helpers ---

func expandHome(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// isPathUnder returns true if path equals or is contained within dir.
func isPathUnder(path, dir string) bool {
	path = filepath.Clean(path)
	dir = filepath.Clean(dir)
	if path == dir {
		return true
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	// If rel starts with ".." the path is outside dir.
	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return false
	}
	return true
}
