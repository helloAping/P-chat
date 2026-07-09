package sandbox

import (
	"path/filepath"
	"strings"
)

// PathClass categorises a file path for the sandbox decision table.
// The class is computed once (per tool call) and then fed to the
// Allow/Block/Confirm decision matrix; the class itself does not
// change between read and write operations — the OPERATION is what
// the matrix looks at, not the class.
//
// Precedence (highest first):
//
//	protected  — path is in the hard-block list (e.g. ~/.ssh, /etc).
//	             Always Block, no override.
//	project    — path is inside the session's project root.
//	             Reads: Allow. Writes: Confirm (per 2026-07 spec).
//	allowed    — path is in the user's extra-allow list (Phase 2).
//	             Reads: Allow. Writes: Allow (whitelist is a deliberate
//	             "open this directory up" gesture).
//	global     — path is outside all of the above AND projectRoot is
//	             set. Reads: Confirm. Writes: Confirm.
//	             This is the "项目路径外" bucket the 2026-07 spec
//	             asked for: every operation requires authorisation.
//	external   — no projectRoot configured for this session (the user
//	             selected the "全局模式" project option, or never picked
//	             a project). Reads: Confirm. Writes: Confirm.
//	             Per spec: "全局菜单下访问所有目录都需要授权, 除非
//	             用户自行选择放开权限" — so external = confirm for
//	             everything, and the only escape is the extra-allow
//	             list.
//
// `project` and `external` are different classes even when both
// produce Confirm for writes; the distinction is shown to the user
// in the confirm modal so they understand why they're being asked.
type PathClass string

const (
	PathClassProtected PathClass = "protected"
	PathClassProject   PathClass = "project"
	PathClassAllowed   PathClass = "allowed"
	PathClassGlobal    PathClass = "global"
	PathClassExternal  PathClass = "external"
)

// String implements fmt.Stringer so the class is printable in logs
// and SSE payloads without leaking internal identifiers.
func (c PathClass) String() string { return string(c) }

// classifyPath returns the PathClass of a single path. The function
// is the single source of truth for "is this path inside the
// project?" — every Check* decision funnels through here, which
// keeps the project-vs-external-vs-protected ordering consistent
// across read / write / list tools.
//
// `path` is the user-supplied path (may be relative, may contain
// `..`); the function cleans it before comparison so the
// "read_file('../../../etc/passwd')" trick can't slip through the
// project-root check by textual accident.
//
// `projectRoot` is the session's working directory (from
// ChatRequest.ProjectRoot). Empty string = external mode (the
// user has not pinned a project for this session).
//
// `extraAllowed` and `protectedDirs` are pre-resolved absolute
// paths, as built by Sandbox.New — the caller is responsible for
// home expansion and `filepath.Abs`. classifyPath itself only
// does the containment check.
func classifyPath(path, projectRoot string, extraAllowed []string, protectedDirs []string) PathClass {
	if path == "" {
		// Treat empty path as external (caller decides whether
		// that's block or confirm; classifyPath only classifies).
		return PathClassExternal
	}

	cleaned := cleanForClassify(path)
	if cleaned == "" {
		return PathClassExternal
	}

	// 1. Protected paths win over everything. Even the project
	//    root can't open up ~/.ssh or /etc.
	for _, dir := range protectedDirs {
		if dir == "" {
			continue
		}
		if isPathUnder(cleaned, dir) {
			return PathClassProtected
		}
	}

	// 2. Project root. The session's own working directory
	//    deserves the relaxed read / confirm-write policy.
	if projectRoot != "" {
		if isPathUnder(cleaned, projectRoot) {
			return PathClassProject
		}
	}

	// 3. User's extra-allow list (Phase 2 whitelist). The user
	//    has explicitly said "this directory is OK to read AND
	//    write without a confirm modal", so it sits in its own
	//    bucket — distinct from project because the policy is
	//    different (Allow for both ops, no Confirm at all).
	for _, dir := range extraAllowed {
		if dir == "" {
			continue
		}
		if isPathUnder(cleaned, dir) {
			return PathClassAllowed
		}
	}

	// 4. Global vs External. The only difference is whether the
	//    session has pinned a project at all:
	//      - projectRoot == ""  → external (the "global mode" UX)
	//      - projectRoot != ""  → global (the path is outside the
	//        project, but a project is set)
	// Both end up in the same decision bucket (Confirm for both
	// reads and writes), but the modal shows different labels
	// ("项目外" vs "全局模式") so the user understands the context.
	if projectRoot == "" {
		return PathClassExternal
	}
	return PathClassGlobal
}

// cleanForClassify normalises a path to an absolute, symlink-free
// form for the containment check. The key guarantee is that
// "../foo" and "foo/../bar" and "foo/" all reduce to the same
// final string before isPathUnder compares them — without this,
// isPathUnder("foo/../etc/passwd", "/etc") would return false
// because the textual form starts with "foo", not "/etc".
//
// Symlink resolution is best-effort: the target may not exist
// yet (e.g. a write_file call creating a new directory). When
// EvalSymlinks fails we fall back to the cleaned textual form,
// which is the right answer for the write path (we don't want
// to over-permit: missing file = textual, which then has to pass
// the project-root check on the textual form too).
func cleanForClassify(p string) string {
	if p == "" {
		return ""
	}
	expanded := expandHome(p)
	resolved, err := filepath.EvalSymlinks(expanded)
	if err != nil {
		// Path may not exist (write target) or may be on a
		// non-existent drive. Fall back to textual Abs+Clean.
		if abs, absErr := filepath.Abs(expanded); absErr == nil {
			return filepath.Clean(abs)
		}
		return filepath.Clean(expanded)
	}
	return filepath.Clean(resolved)
}

// isPathUnderClass is a string-form isPathUnder used by callers
// that already have an absolute path. It exists so tests can
// call classifyPath with pre-built absolute paths and skip the
// symlink resolution step.
func isPathUnderClass(path, dir string) bool {
	return isPathUnder(filepath.Clean(path), filepath.Clean(dir))
}

// isExternalOnly returns true when the session has no project
// pinned. Convenience helper for log messages.
func isExternalOnly(projectRoot string) bool {
	return strings.TrimSpace(projectRoot) == ""
}
