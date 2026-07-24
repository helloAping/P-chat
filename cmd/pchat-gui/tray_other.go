//go:build !windows

package main

type trayHandle struct{}

// startTray is a no-op on platforms without this tray implementation.
// startTray 在非 Windows 平台暂不创建托盘图标。
func startTray(app *App) *trayHandle {
	return &trayHandle{}
}

func (t *trayHandle) ready() bool { return false }

func (t *trayHandle) stop() {}
