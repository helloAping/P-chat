package paths

import (
	"os"
	"path/filepath"
	"sync/atomic"
)

// resolveStrategy explains why a particular home directory was
// chosen. The string values are short and stable — the startup
// log line surfaces them to the user so they can verify at a
// glance which mode is active.
type resolveStrategy string

const (
	// StrategyEnvVar: PCHAT_DATA_HOME env var was set and
	// non-empty. Highest priority — explicit data-dir override
	// from the operator.
	//
	// NB: this used to be PCHAT_HOME, but PCHAT_HOME is now
	// owned by install.ps1 as the *install* dir (used in
	// PATH as %PCHAT_HOME%). Conflating the two caused
	// memory/config to land in the install directory on
	// any machine that had run install.ps1 -AddToPath.
	// PCHAT_DATA_HOME is the data-dir override from now on.
	StrategyEnvVar resolveStrategy = "PCHAT_DATA_HOME env var"

	// StrategyDevBin: the running binary lives in a
	// "bin" or "dev-bin" subdirectory; we use the sibling
	// "<parent>/.p-chat" so a local build doesn't touch the
	// user's real ~/.p-chat.
	StrategyDevBin resolveStrategy = "sibling of binary (bin/dev-bin)"

	// StrategyHome: fallback. Uses $HOME/.p-chat (or
	// %USERPROFILE%/.p-chat on Windows). This is the original
	// behaviour and what installed / release builds should
	// use.
	StrategyHome resolveStrategy = "$HOME/.p-chat (default)"
)

// devBinNames are the directory names that trigger sibling
// resolution. The list is deliberately tiny — a project using
// "build/" or "out/" should still resolve to ~/.p-chat unless
// the user opts in via PCHAT_DATA_HOME.
var devBinNames = map[string]bool{
	"bin":     true,
	"dev-bin": true,
}

// resolved holds the (dir, strategy) pair. The fields
// are exposed as separate return values from resolveHome
// rather than allocated per call so the hot path doesn't
// generate garbage.
type resolved struct {
	dir      string
	strategy resolveStrategy
}

// homeOverride, when set, replaces the value of $PCHAT_DATA_HOME.
// Used by tests so they can verify the env-var path without
// polluting the real environment.
var homeOverride atomic.Pointer[string]

// execOverride, when set, replaces the value of os.Executable().
// Used by tests so we can simulate "binary lives in /x/bin/".
var execOverride atomic.Pointer[string]

// resolveHome returns the home directory + the strategy
// that picked it.
//
// Order of precedence (highest first):
//  1. PCHAT_DATA_HOME env var (or homeOverride in tests).
//     This is the operator's explicit data-dir override.
//  2. Sibling: if os.Executable() lives in a "bin" or
//     "dev-bin" directory, use <parent>/.p-chat/.
//  3. $HOME/.p-chat (the original behaviour).
//
// We don't cache the result: env vars (USERPROFILE, HOME)
// and os.Executable() are all re-read every call so tests
// that flip the env via t.Setenv (and t.Setenv restores
// after the test) get the right answer on each call without
// a manual cache-invalidation hook. The cost is one env
// read + one filepath parse per GlobalDir() call, which is
// nanoseconds — well under the cost of the file I/O that
// every caller (Load, EnsureGlobal, etc.) does on top.
//
// PCHAT_HOME is NOT consulted here. PCHAT_HOME is the
// install root (set by install.ps1 -AddToPath, used in PATH
// as %PCHAT_HOME%). Treating it as a data-dir override
// caused installs to write memory/config under the install
// directory — see internal/upgrade stepV3toV4 for the
// one-time migration that fixes existing installs.
func resolveHome() resolved {
	// Tier 1: env var. Always re-read so t.Setenv in tests
	// takes effect without a manual cache reset.
	if h := readEnvOrOverride("PCHAT_DATA_HOME", &homeOverride); h != "" {
		return resolved{dir: h, strategy: StrategyEnvVar}
	}

	// Tier 2 & 3: no cache — re-resolve on every call.
	return computeSiblingOrHome()
}

func computeSiblingOrHome() resolved {
	// 2. Sibling of "bin" or "dev-bin".
	if execPath := readExecOrOverride(&execOverride); execPath != "" {
		parent := filepath.Dir(execPath)
		base := filepath.Base(parent)
		if devBinNames[base] {
			return resolved{
				dir:      filepath.Join(parent, GlobalDirName),
				strategy: StrategyDevBin,
			}
		}
	}

	// 3. $HOME/.p-chat (the original behaviour).
	home, _ := os.UserHomeDir()
	return resolved{
		dir:      filepath.Join(home, GlobalDirName),
		strategy: StrategyHome,
	}
}

// ResolveStrategy returns the strategy that picked the
// current home directory. Useful for startup log lines so
// the user can verify isolation is working as intended.
//
// Returns one of the Strategy* constants above.
func ResolveStrategy() resolveStrategy {
	return resolveHome().strategy
}

// readEnvOrOverride is a small helper that returns the env
// var if set, else the override pointer's value (used by
// tests). The override is read atomically; a nil pointer
// is treated as "no override".
func readEnvOrOverride(envName string, override *atomic.Pointer[string]) string {
	if override != nil {
		if v := override.Load(); v != nil {
			return *v
		}
	}
	return os.Getenv(envName)
}

// readExecOrOverride returns the path to the running
// binary, or the test override if set.
//
// os.Executable can fail in odd containerised / chroot
// environments; in that case we fall through to the
// $HOME default rather than crashing the process at
// startup over a path-resolution curiosity.
func readExecOrOverride(override *atomic.Pointer[string]) string {
	if override != nil {
		if v := override.Load(); v != nil {
			return *v
		}
	}
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return ""
}

// SetExecutableForTest is the test-only hook that lets
// individual tests fake "this binary lives in /x/bin/"
// without writing a real binary to disk. The new value
// is picked up on the next GlobalDir() call (no cache to
// invalidate).
func SetExecutableForTest(path string) {
	if path == "" {
		execOverride.Store(nil)
		return
	}
	execOverride.Store(&path)
}

// SetHomeForTest is the test-only hook for the PCHAT_DATA_HOME
// path. Same semantics as SetExecutableForTest.
func SetHomeForTest(path string) {
	if path == "" {
		homeOverride.Store(nil)
		return
	}
	homeOverride.Store(&path)
}
