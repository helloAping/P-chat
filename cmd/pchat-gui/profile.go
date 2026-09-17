package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/p-chat/pchat/runtimeprofile"
)

const trayWindowClassPrefix = "PChatTrayWindow-"

// currentRuntimeProfile 返回当前数据目录对应的稳定运行身份。
// currentRuntimeProfile returns the stable identity for the active data home.
func currentRuntimeProfile() runtimeprofile.Profile {
	profile, err := runtimeprofile.Current(resolveHomeDir())
	if err == nil {
		return profile
	}
	log.Printf("runtime profile: %v", err)
	return runtimeprofile.Profile{Name: "prod", ID: "default", DataHome: resolveHomeDir()}
}

func applicationTitle() string {
	return currentRuntimeProfile().WindowTitle()
}

func trayWindowClassName() string {
	return trayWindowClassPrefix + currentRuntimeProfile().ID
}

func webviewUserDataPath() string {
	profile := currentRuntimeProfile()
	// Preserve Wails' historical default for the normal production data home,
	// so an upgrade does not discard the user's existing WebView2 local state.
	home, _ := os.UserHomeDir()
	if home != "" {
		if defaultProfile, err := runtimeprofile.Resolve(filepath.Join(home, ".p-chat"), "prod"); err == nil && profile.ID == defaultProfile.ID {
			return ""
		}
	}
	root, err := os.UserConfigDir()
	if err != nil || root == "" {
		root = os.TempDir()
	}
	return filepath.Join(root, "P-Chat", "webview2", profile.ID)
}
