//go:build windows

package main

import (
	"errors"
	"log"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const defaultSingleInstanceMutex = `Local\PChatGuiSingleInstance`

var (
	procFindWindowW = windows.NewLazySystemDLL("user32.dll").NewProc("FindWindowW")
)

type singleInstanceLock struct {
	handle windows.Handle
}

// acquireSingleInstance 获取单实例锁；第二实例应通知已有实例并自行退出。
// acquireSingleInstance ensures only one GUI instance owns the tray/server pair.
func acquireSingleInstance() (*singleInstanceLock, bool, error) {
	name, err := windows.UTF16PtrFromString(singleInstanceMutexName())
	if err != nil {
		return nil, false, err
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if isAlreadyExistsError(err) {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
		return nil, true, nil
	}
	if err != nil {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
		return nil, false, err
	}
	return &singleInstanceLock{handle: handle}, false, nil
}

func (l *singleInstanceLock) release() {
	if l == nil || l.handle == 0 {
		return
	}
	_ = windows.CloseHandle(l.handle)
	l.handle = 0
}

// signalExistingInstance 通知已有实例恢复窗口；失败时仅记录日志，第二实例仍退出。
// signalExistingInstance asks the already-running tray window to restore itself.
func signalExistingInstance() bool {
	className, _ := windows.UTF16PtrFromString(trayWindowClass)
	hwnd, _, err := procFindWindowW.Call(uintptr(unsafe.Pointer(className)), 0)
	if hwnd == 0 {
		log.Printf("single instance: existing mutex found, but tray window was not found: %v", err)
		return false
	}
	if ok, _, err := procPostMessageW.Call(hwnd, trayShowMainWindowMsg, 0, 0); ok == 0 {
		log.Printf("single instance: PostMessage(show) failed: %v", err)
		return false
	}
	return true
}

func singleInstanceMutexName() string {
	if v := os.Getenv("PCHAT_SINGLE_INSTANCE_MUTEX"); v != "" {
		return v
	}
	return defaultSingleInstanceMutex
}

func isAlreadyExistsError(err error) bool {
	return errors.Is(err, windows.ERROR_ALREADY_EXISTS)
}
