// Package version 提供统一的版本号。三个二进制（pchat / pchat-server /
// pchat-gui）共享同一版本，通过 ldflags 在构建时注入，或回退到
// 读取项目根目录的 VERSION 文件。
//
// 构建命令：
//
//	go build -ldflags "-X 'internal/version.Version=1.2.3' \
//	                   -X 'internal/version.GitCommit=$(git rev-parse --short HEAD)'" \
//	       ./cmd/pchat-server
//
// 以上命令将 VERSION 文件和 git hash 编译进二进制。
// 运行时版本字符串格式：
//
//	生产版本: 1.2.3
//	开发版本: dev-abc1234   (未注入 Version 时自动使用 git hash 回退)
package version

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	// Version 由构建脚本通过 -ldflags 注入。空字符串表示未注入
	//（开发环境 — go run / go build 不带 ldflags）。
	Version string

	// GitCommit 由构建脚本通过 -ldflags 注入。未注入时自动读取
	// git rev-parse。
	GitCommit string
)

// String 返回用于显示和 /api/v1/version 的版本字符串。
func String() string {
	if Version != "" {
		return Version
	}
	// 开发版本 — 尝试 git hash 回退
	hash := gitCommit()
	if hash != "" {
		return "dev-" + hash
	}
	// 最后的回退 — 读取 VERSION 文件
	v := fileVersion()
	if v != "" {
		return v
	}
	return "dev"
}

// FullString 返回含 git hash 的完整版本字符串，用于日志和诊断。
func FullString() string {
	v := String()
	hash := gitCommit()
	if hash == "" {
		return v
	}
	// 如果已包含 hash（dev-xxx），不重复添加
	if strings.HasPrefix(v, "dev-") {
		return v
	}
	return fmt.Sprintf("%s (%s)", v, hash)
}

func gitCommit() string {
	if GitCommit != "" {
		return GitCommit
	}
	// 运行时回退 — 读 .git/HEAD（仅开发环境有效）
	return resolveGitHash("")
}

// resolveGitHash 从 root 目录解析 git hash。
func resolveGitHash(root string) string {
	if root == "" {
		root = projectRoot()
	}
	head, err := os.ReadFile(filepath.Join(root, ".git", "HEAD"))
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(head))
	if !strings.HasPrefix(ref, "ref: ") {
		// detached HEAD — HEAD 文件本身包含 hash
		if len(ref) >= 7 {
			return ref[:7]
		}
		return ref
	}
	refPath := filepath.Join(root, ".git", strings.TrimPrefix(ref, "ref: "))
	data, err := os.ReadFile(refPath)
	if err != nil {
		return ""
	}
	h := strings.TrimSpace(string(data))
	if len(h) >= 7 {
		h = h[:7]
	}
	return h
}

// fileVersion 从 VERSION 文件读取版本号。搜索项目根目录。
func fileVersion() string {
	root := projectRoot()
	if root == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// projectRoot 向上查找包含 VERSION 或 go.mod 的目录。
func projectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "VERSION")); err == nil {
			return dir
		}
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
