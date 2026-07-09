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
//
// The 2026-07 spec added `projectRoot` and `extraAllowed` as
// function-level parameters on CheckRead/CheckWrite (not struct
// fields) because sessions are dynamic — a single Sandbox is
// shared across goroutines, and a session's projectRoot can be
// updated when the user switches the active project mid-conversation.
// Keeping the projectRoot per-call also means the Sandbox itself
// is stateless and the classification logic is testable without
// mutating the struct.
type Sandbox struct {
	enabled       bool
	requireMode   string // "always" | "dangerous" | "confirm" | "never"
	maxCmdLen     int
	protectedDirs []string // resolved absolute paths (no ~ inside)
	protectedGlobs []string // raw globs that we still do a substring match for
	execPatterns  []*regexp.Regexp
	execNames     []string
	// extraAllowedDirs is the per-user whitelist (Phase 2
	// writes to ~/.p-chat/sandbox_whitelist.json). Phase 1
	// leaves it empty; the field is here so the CheckRead /
	// CheckWrite decision tables already route through the
	// PathClassAllowed bucket once the whitelist API lands.
	extraAllowedDirs []string
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
	// requireMode is the policy applied when a pattern matches:
	//   "never"    — block the call (default for unconfigured systems
	//                that should fail-closed; the name "never" reflects
	//                "never let the dangerous command run")
	//   "always"   — always ask the user to confirm
	//   "confirm"  — same as "always" (alias for clarity in config)
	//   "dangerous" — alias for "never" (kept for backwards-compat with
	//                pre-1.0 configs that used this name)
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

	// Phase 1: the extra-allow list is configured via
	// SandboxConfig (still no user UI for editing — that
	// lands in Phase 2). Resolve home + Abs so the list is
	// directly comparable to the protected-dirs list and
	// the per-call resolved path.
	for _, p := range cfg.ExtraAllowedPaths {
		if p == "" {
			continue
		}
		expanded := expandHome(p)
		if abs, err := filepath.Abs(expanded); err == nil {
			s.extraAllowedDirs = append(s.extraAllowedDirs, abs)
		}
	}

	return s, nil
}

// AddAllowedPaths injects additional allowed directories at
// runtime. Used by the Phase 2 whitelist API (the user's
// in-UI "add this dir to my whitelist" button writes here).
// Phase 1 leaves the API unused — config-based paths are
// the only source of the whitelist today.
func (s *Sandbox) AddAllowedPaths(paths ...string) {
	for _, p := range paths {
		if p == "" {
			continue
		}
		expanded := expandHome(p)
		if abs, err := filepath.Abs(expanded); err == nil {
			s.extraAllowedDirs = append(s.extraAllowedDirs, abs)
		}
	}
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
	//   "never" / "dangerous" — block the call (fail-closed)
	//   "always" / "confirm" — ask the user to confirm
	switch s.requireMode {
	case "never", "dangerous":
		return Block
	case "always", "confirm":
		return Confirm
	default:
		// Unknown mode: fail-closed.
		return Block
	}
}

// CheckWrite decides whether a file path may be written. As of
// 2026-07 the policy is path-class based (project-internal /
// whitelist / global / external), with `projectRoot` carrying
// the session's working directory:
//
//	protected   → Block  (hard, no override)
//	project     → Confirm (user's spec: "项目内写需要授权")
//	allowed     → Allow   (whitelist = deliberate open gesture)
//	global      → Confirm (project set, path outside)
//	external    → Confirm (no project set, "global mode")
//	unknown cls → Block   (fail-closed, should not happen)
//
// The 2026-07 spec also changed `require_confirm: dangerous`
// from "block immediately" to "ask the user" for any
// path-class confirmation — previously the user had to flip
// the mode to `always` to see confirm modals. See CHANGELOG
// for the breaking change note.
func (s *Sandbox) CheckWrite(path, projectRoot string) Decision {
	if !s.enabled {
		return Allow
	}
	if path == "" {
		return Block
	}
	class := classifyPath(path, projectRoot, s.extraAllowedDirs, s.protectedDirs)
	return writeClassDecision(class)
}

// CheckRead decides whether a file path may be read. The class
// table mirrors CheckWrite except reads of project-internal
// files are Allow (the user explicitly asked for "项目内读 =
// 自动通过"). Reads of global / external paths still confirm
// so the LLM can't exfiltrate arbitrary files after a write
// confirm was approved.
func (s *Sandbox) CheckRead(path, projectRoot string) Decision {
	if !s.enabled {
		return Allow
	}
	if path == "" {
		return Block
	}
	class := classifyPath(path, projectRoot, s.extraAllowedDirs, s.protectedDirs)
	return readClassDecision(class)
}

// writeClassDecision maps a path class to the decision for
// write operations. Centralised so the read and write tables
// stay in lock-step (any new path class automatically gets
// both behaviours, which prevents "I added a class but forgot
// the write side" bugs).
func writeClassDecision(c PathClass) Decision {
	switch c {
	case PathClassProtected:
		return Block
	case PathClassAllowed:
		return Allow
	case PathClassProject, PathClassGlobal, PathClassExternal:
		return Confirm
	default:
		// Unknown class → fail-closed. Should not happen
		// unless someone adds a new PathClass constant
		// without updating this table.
		return Block
	}
}

// readClassDecision mirrors writeClassDecision with the
// 2026-07 spec's exception: project-internal reads are Allow.
// Reads of global / external paths still confirm because the
// LLM must not silently read /etc/passwd, ~/.aws/credentials,
// etc. just because the user approved a write.
func readClassDecision(c PathClass) Decision {
	switch c {
	case PathClassProtected:
		return Block
	case PathClassAllowed, PathClassProject:
		return Allow
	case PathClassGlobal, PathClassExternal:
		return Confirm
	default:
		return Block
	}
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
// Project root is passed through for the path-class check.
func (s *Sandbox) CheckWriteBool(path, projectRoot string) bool {
	return s.CheckWrite(path, projectRoot).Allowed()
}

// CheckExecDecision satisfies the expanded tool.SandboxChecker.
func (s *Sandbox) CheckExecDecision(command string) tool.SandboxDecision {
	return tool.SandboxDecision(s.CheckExec(command))
}

// CheckWriteDecision satisfies the expanded tool.SandboxChecker.
func (s *Sandbox) CheckWriteDecision(path, projectRoot string) tool.SandboxDecision {
	return tool.SandboxDecision(s.CheckWrite(path, projectRoot))
}

// CheckReadDecision satisfies the expanded tool.SandboxChecker.
// Added 2026-07 to cover read_file / read_docx / read_pdf /
// list_files (previously these tools bypassed the sandbox).
func (s *Sandbox) CheckReadDecision(path, projectRoot string) tool.SandboxDecision {
	return tool.SandboxDecision(s.CheckRead(path, projectRoot))
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
// Both paths are resolved through any symlinks before comparison
// so an attacker cannot bypass the sandbox by writing through a
// symlink that points outside the allowed tree. A user-created
// ~/.p-chat/secret -> /etc would otherwise let writes via
// ~/.p-chat/secret/passwd slip through the textual-path check
// even though /etc is in the protected-dirs list.
func isPathUnder(path, dir string) bool {
	// Resolve symlinks where possible. If resolution fails
	// (e.g. the target doesn't exist yet for a write), fall
	// back to the textual path so the check still produces
	// a result.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
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
