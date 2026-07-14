package main

import (
	"os"
	"path/filepath"
)

// resolveHomeDir decides which directory the GUI (and the
// pchat-server it spawns) should use for ~/.p-chat equivalent
// state. The logic intentionally mirrors
// internal/paths.GlobalDir() so the GUI and the server end
// up looking at the same place without a shared Go module.
//
// Why a local copy instead of importing internal/paths:
// cmd/pchat-gui is a SEPARATE Go module (its own go.mod,
// driven by the Wails CLI). Go's `internal/` rule restricts
// imports of github.com/p-chat/pchat/internal/* to packages
// within the same module, so the GUI cannot import from the
// root module's internal tree. Duplicating ~30 lines is the
// lightest weight fix.
//
// Resolution order (highest priority first):
//  1. PCHAT_DATA_HOME env var — explicit operator override
//     of the data dir (memory / config / …).
//  2. Sibling: if this binary lives in bin/ or dev-bin/,
//     use <parent>/.p-chat/ so a dev build doesn't touch
//     the user's real ~/.p-chat.
//  3. $HOME/.p-chat fallback (the original behaviour).
//
// PCHAT_HOME is NOT consulted. PCHAT_HOME is the install
// root (set by install.ps1 -AddToPath, used in PATH as
// %PCHAT_HOME%). Reading it for the data dir used to cause
// memory / config to land in the install directory; that
// bug was fixed in lock-step with internal/paths and the
// install / uninstall scripts. See internal/upgrade
// stepV3toV4 for the data migration that rescues existing
// installs.
func resolveHomeDir() string {
	if h := os.Getenv("PCHAT_DATA_HOME"); h != "" {
		return h
	}
	if exe, err := os.Executable(); err == nil {
		parent := filepath.Dir(exe)
		base := filepath.Base(parent)
		if base == "bin" || base == "dev-bin" {
			return filepath.Join(parent, ".p-chat")
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".p-chat")
}

// resolveConfigPath returns the active p-chat config path.
// Prefers the newer json file, falls back to yaml. Returns
// "" when neither exists (fresh install — pchat-server uses
// built-in defaults). Also mirrors the home resolution
// rules above.
func resolveConfigPath() string {
	home := resolveHomeDir()
	jsonPath := filepath.Join(home, "config.json")
	if _, err := os.Stat(jsonPath); err == nil {
		return jsonPath
	}
	yamlPath := filepath.Join(home, "config.yaml")
	if _, err := os.Stat(yamlPath); err == nil {
		return yamlPath
	}
	return ""
}
