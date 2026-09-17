// Package runtimeprofile gives each P-Chat data directory a stable runtime
// identity. Processes that use different data directories may coexist, while
// processes that use the same directory deliberately share one identity.
package runtimeprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// ProfileEnv names the optional human-readable runtime profile.
	ProfileEnv = "PCHAT_PROFILE"
)

// Profile describes one isolated P-Chat runtime profile.
type Profile struct {
	Name     string `json:"name"`
	ID       string `json:"id"`
	DataHome string `json:"data_home"`
}

// Current resolves the profile for dataHome using PCHAT_PROFILE as its
// optional display name.
func Current(dataHome string) (Profile, error) {
	return Resolve(dataHome, os.Getenv(ProfileEnv))
}

// Resolve canonicalizes dataHome and derives a stable identity from it. The
// display name is intentionally excluded from the identity: renaming a profile
// must not allow a second process to open the same data directory.
func Resolve(dataHome, name string) (Profile, error) {
	canonical, err := canonicalDataHome(dataHome)
	if err != nil {
		return Profile{}, err
	}
	if strings.TrimSpace(name) == "" {
		name = inferName(canonical)
	} else {
		name = strings.TrimSpace(name)
	}

	hashInput := canonical
	if runtime.GOOS == "windows" {
		hashInput = strings.ToLower(hashInput)
	}
	sum := sha256.Sum256([]byte(hashInput))
	return Profile{
		Name:     name,
		ID:       hex.EncodeToString(sum[:8]),
		DataHome: canonical,
	}, nil
}

// WindowTitle returns the user-visible title for this profile.
func (p Profile) WindowTitle() string {
	if strings.EqualFold(strings.TrimSpace(p.Name), "prod") || strings.TrimSpace(p.Name) == "" {
		return "P-Chat"
	}
	return fmt.Sprintf("P-Chat [%s]", p.Name)
}

func canonicalDataHome(dataHome string) (string, error) {
	if strings.TrimSpace(dataHome) == "" {
		return "", fmt.Errorf("runtime profile: data home is empty")
	}
	abs, err := filepath.Abs(dataHome)
	if err != nil {
		return "", fmt.Errorf("runtime profile: resolve data home: %w", err)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = filepath.Clean(resolved)
	}
	return abs, nil
}

func inferName(dataHome string) string {
	parent := strings.ToLower(filepath.Base(filepath.Dir(dataHome)))
	if parent == "bin" || parent == "dev-bin" {
		return "dev"
	}
	return "prod"
}
