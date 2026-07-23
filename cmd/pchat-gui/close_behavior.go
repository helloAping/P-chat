package main

import (
	"encoding/json"
	"os"
	"strings"
)

const (
	closeBehaviorExit = "exit"
	closeBehaviorTray = "tray"
)

// normalizeCloseBehavior returns the safe desktop close behaviour.
// normalizeCloseBehavior 将未知值回落到退出，保持旧行为。
func normalizeCloseBehavior(v string) string {
	switch strings.TrimSpace(v) {
	case closeBehaviorTray:
		return closeBehaviorTray
	default:
		return closeBehaviorExit
	}
}

// shouldPreventClose tells Wails whether to cancel the close event.
// shouldPreventClose 返回 true 表示隐藏窗口并阻止真正关闭。
func shouldPreventClose(quitting bool, closeBehavior string) bool {
	if quitting {
		return false
	}
	return normalizeCloseBehavior(closeBehavior) == closeBehaviorTray
}

// readCloseBehavior reads ui.close_behavior from the active config file.
// readCloseBehavior 只读取 GUI 关闭行为；失败时安全回落到退出。
func readCloseBehavior() string {
	path := pickConfigPath()
	if path == "" {
		return closeBehaviorExit
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return closeBehaviorExit
	}
	if strings.HasSuffix(strings.ToLower(path), ".yaml") || strings.HasSuffix(strings.ToLower(path), ".yml") {
		return normalizeCloseBehavior(readYAMLCloseBehavior(string(data)))
	}
	var cfg struct {
		UI struct {
			CloseBehavior string `json:"close_behavior"`
		} `json:"ui"`
	}
	if err := json.Unmarshal(stripUTF8BOM(data), &cfg); err != nil {
		return closeBehaviorExit
	}
	return normalizeCloseBehavior(cfg.UI.CloseBehavior)
}

func stripUTF8BOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}

func readYAMLCloseBehavior(text string) string {
	inUI := false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent == 0 {
			inUI = strings.TrimSuffix(trimmed, ":") == "ui"
			continue
		}
		if !inUI || !strings.HasPrefix(trimmed, "close_behavior:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "close_behavior:"))
		value = strings.SplitN(value, "#", 2)[0]
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return closeBehaviorExit
}
