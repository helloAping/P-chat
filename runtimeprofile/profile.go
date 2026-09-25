// Package runtimeprofile 为每个 P-Chat 数据目录提供稳定运行身份；不同数据目录
// 可以共存，同一数据目录则有意共享同一个身份。
// Package runtimeprofile gives each P-Chat data directory a stable runtime
// identity so different data homes can coexist safely.
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
	// ProfileEnv 是可选的人类可读运行环境名称变量。
	// ProfileEnv names the optional human-readable runtime profile.
	ProfileEnv = "PCHAT_PROFILE"
)

// Profile 描述一个隔离的 P-Chat 运行环境。
// Profile describes one isolated P-Chat runtime profile.
type Profile struct {
	Name     string `json:"name"`
	ID       string `json:"id"`
	DataHome string `json:"data_home"`
}

// Current 根据 dataHome 解析运行环境，并以 PCHAT_PROFILE 作为可选显示名。
// Current resolves the profile for dataHome using PCHAT_PROFILE as its
// optional display name.
func Current(dataHome string) (Profile, error) {
	return Resolve(dataHome, os.Getenv(ProfileEnv))
}

// Resolve 规范化 dataHome 并派生稳定身份。显示名不参与身份计算，避免通过
// 改名绕过同一数据目录的单实例约束。
// Resolve canonicalizes dataHome and derives a stable identity from it; the
// display name is intentionally excluded from the identity.
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

// WindowTitle 返回当前运行环境对用户可见的窗口标题。
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
