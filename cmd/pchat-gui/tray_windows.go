//go:build windows

package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

const (
	trayWindowClass = "PChatTrayWindow"
	trayUID         = 1
	trayCallbackMsg = 0x0400 + 121

	cmdTrayOpen          = 1001
	cmdTrayNewSession    = 1002
	cmdTraySessionPicker = 1003
	cmdTrayQuit          = 1004
	cmdTrayRecentBase    = 2000

	wmCommand     = 0x0111
	wmClose       = 0x0010
	wmDestroy     = 0x0002
	wmLButtonUp   = 0x0202
	wmLButtonDbl  = 0x0203
	wmRButtonUp   = 0x0205
	wmContextMenu = 0x007B

	nimAdd        = 0x00000000
	nimDelete     = 0x00000002
	nimSetVersion = 0x00000004
	nifMessage    = 0x00000001
	nifIcon       = 0x00000002
	nifTip        = 0x00000004

	notifyIconVersion4 = 4

	imageIcon      = 1
	lrLoadFromFile = 0x00000010
	lrDefaultSize  = 0x00000040
	idiApplication = 32512
	mfString       = 0x00000000
	mfGrayed       = 0x00000001
	mfSeparator    = 0x00000800
	mfPopup        = 0x00000010
	tpmRightButton = 0x00000002
	tpmReturnCmd   = 0x00000100
	tpmNonotify    = 0x00000080
)

var (
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	user32                = windows.NewLazySystemDLL("user32.dll")
	shell32               = windows.NewLazySystemDLL("shell32.dll")
	procGetModuleHandleW  = kernel32.NewProc("GetModuleHandleW")
	procRegisterClassExW  = user32.NewProc("RegisterClassExW")
	procCreateWindowExW   = user32.NewProc("CreateWindowExW")
	procDefWindowProcW    = user32.NewProc("DefWindowProcW")
	procDestroyWindow     = user32.NewProc("DestroyWindow")
	procPostQuitMessage   = user32.NewProc("PostQuitMessage")
	procGetMessageW       = user32.NewProc("GetMessageW")
	procTranslateMessage  = user32.NewProc("TranslateMessage")
	procDispatchMessageW  = user32.NewProc("DispatchMessageW")
	procPostMessageW      = user32.NewProc("PostMessageW")
	procLoadImageW        = user32.NewProc("LoadImageW")
	procLoadIconW         = user32.NewProc("LoadIconW")
	procDestroyIcon       = user32.NewProc("DestroyIcon")
	procCreatePopupMenu   = user32.NewProc("CreatePopupMenu")
	procDestroyMenu       = user32.NewProc("DestroyMenu")
	procAppendMenuW       = user32.NewProc("AppendMenuW")
	procTrackPopupMenu    = user32.NewProc("TrackPopupMenu")
	procGetCursorPos      = user32.NewProc("GetCursorPos")
	procSetForegroundWnd  = user32.NewProc("SetForegroundWindow")
	procShellNotifyIconW  = shell32.NewProc("Shell_NotifyIconW")
	trayWndProc           = syscall.NewCallback(trayWindowProc)
	trayWindowsMu         sync.Mutex
	trayWindows           = map[windows.Handle]*trayHandle{}
	trayClassRegisterOnce sync.Once
)

type trayHandle struct {
	app            *App
	hwnd           windows.Handle
	hicon          windows.Handle
	mu             sync.Mutex
	recentCommands map[int]traySession
	ok             atomic.Bool
	once           sync.Once
}

type point struct {
	X int32
	Y int32
}

type msg struct {
	HWnd    windows.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type notifyIconData struct {
	CbSize           uint32
	HWnd             windows.Handle
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            windows.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	TimeoutOrVersion uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     windows.Handle
}

// startTray installs the Windows notification-area icon.
// startTray 启动 Windows 通知区域图标和右键菜单。
func startTray(app *App) *trayHandle {
	t := &trayHandle{app: app}
	go t.run()
	return t
}

func (t *trayHandle) stop() {
	t.once.Do(func() {
		if t.hwnd != 0 {
			t.deleteIcon()
			procPostMessageW.Call(uintptr(t.hwnd), wmClose, 0, 0)
		}
		t.ok.Store(false)
	})
}

func (t *trayHandle) run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	instance := getModuleHandle()
	className, _ := windows.UTF16PtrFromString(trayWindowClass)
	trayClassRegisterOnce.Do(func() {
		wc := wndClassEx{
			Size:      uint32(unsafe.Sizeof(wndClassEx{})),
			WndProc:   trayWndProc,
			Instance:  instance,
			ClassName: className,
		}
		procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	})

	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(className)),
		0,
		0, 0, 0, 0,
		0, 0,
		uintptr(instance),
		0,
	)
	if hwnd == 0 {
		log.Printf("tray: CreateWindowExW failed: %v", err)
		return
	}
	t.hwnd = windows.Handle(hwnd)
	trayWindowsMu.Lock()
	trayWindows[t.hwnd] = t
	trayWindowsMu.Unlock()

	t.hicon = loadTrayIcon(instance)
	if !t.addIcon() {
		procDestroyWindow.Call(uintptr(t.hwnd))
		return
	}
	t.ok.Store(true)

	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}

	trayWindowsMu.Lock()
	delete(trayWindows, t.hwnd)
	trayWindowsMu.Unlock()
	if t.hicon != 0 {
		procDestroyIcon.Call(uintptr(t.hicon))
	}
}

func (t *trayHandle) ready() bool {
	return t != nil && t.ok.Load()
}

func (t *trayHandle) addIcon() bool {
	nid := t.notifyData()
	if ok, _, err := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid))); ok == 0 {
		log.Printf("tray: Shell_NotifyIconW(NIM_ADD) failed: %v", err)
		return false
	}
	nid.TimeoutOrVersion = notifyIconVersion4
	procShellNotifyIconW.Call(nimSetVersion, uintptr(unsafe.Pointer(&nid)))
	return true
}

func (t *trayHandle) deleteIcon() {
	nid := notifyIconData{
		CbSize: uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd:   t.hwnd,
		UID:    trayUID,
	}
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
}

func (t *trayHandle) notifyData() notifyIconData {
	nid := notifyIconData{
		CbSize:           uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd:             t.hwnd,
		UID:              trayUID,
		UFlags:           nifMessage | nifIcon | nifTip,
		UCallbackMessage: trayCallbackMsg,
		HIcon:            t.hicon,
	}
	copyUTF16(nid.SzTip[:], "P-Chat")
	return nid
}

func trayWindowProc(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	trayWindowsMu.Lock()
	t := trayWindows[windows.Handle(hwnd)]
	trayWindowsMu.Unlock()

	switch msg {
	case trayCallbackMsg:
		if t != nil {
			switch lparam {
			case wmLButtonUp, wmLButtonDbl:
				go t.app.showMainWindow()
				return 0
			case wmRButtonUp, wmContextMenu:
				t.showMenu()
				return 0
			}
		}
	case wmCommand:
		if t != nil {
			t.handleCommand(int(wparam & 0xffff))
			return 0
		}
	case wmClose:
		if t != nil {
			t.deleteIcon()
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wparam, lparam)
	return ret
}

func (t *trayHandle) showMenu() {
	menu, _, err := procCreatePopupMenu.Call()
	if menu == 0 {
		log.Printf("tray: CreatePopupMenu failed: %v", err)
		return
	}
	defer procDestroyMenu.Call(menu)

	appendTrayMenuItem(menu, cmdTrayOpen, "打开 P-Chat")
	appendTrayMenuItem(menu, cmdTrayNewSession, "新增对话")
	appendTrayMenuItem(menu, cmdTraySessionPicker, "打开对话...")
	t.appendRecentSessionsMenu(menu)
	procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	appendTrayMenuItem(menu, cmdTrayQuit, "退出 P-Chat")

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWnd.Call(uintptr(t.hwnd))
	cmd, _, _ := procTrackPopupMenu.Call(
		menu,
		tpmRightButton|tpmReturnCmd|tpmNonotify,
		uintptr(pt.X),
		uintptr(pt.Y),
		0,
		uintptr(t.hwnd),
		0,
	)
	if cmd != 0 {
		t.handleCommand(int(cmd))
	}
}

func (t *trayHandle) handleCommand(command int) {
	switch command {
	case cmdTrayOpen:
		go t.app.showMainWindow()
	case cmdTrayNewSession:
		go func() {
			t.app.showMainWindow()
			if t.app.ctx != nil {
				wailsruntime.EventsEmit(t.app.ctx, "tray:new-session")
			}
		}()
	case cmdTraySessionPicker:
		go func() {
			t.app.showMainWindow()
			if t.app.ctx != nil {
				wailsruntime.EventsEmit(t.app.ctx, "tray:show-session-picker")
			}
		}()
	case cmdTrayQuit:
		go t.app.quitApp()
	default:
		if session, ok := t.recentSessionForCommand(command); ok {
			go func() {
				t.app.showMainWindow()
				if t.app.ctx != nil {
					wailsruntime.EventsEmit(t.app.ctx, "tray:switch-session", map[string]string{
						"session_id":   session.ID,
						"project_path": session.ProjectPath,
					})
				}
			}()
		}
	}
}

func (t *trayHandle) appendRecentSessionsMenu(menu uintptr) {
	sessions := t.app.recentTraySessions()
	t.setRecentCommands(sessions)

	submenu, _, err := procCreatePopupMenu.Call()
	if submenu == 0 {
		log.Printf("tray: CreatePopupMenu(recent) failed: %v", err)
		return
	}
	if len(sessions) == 0 {
		appendTrayMenuItemWithFlags(submenu, 0, "暂无最近对话", mfString|mfGrayed)
	} else {
		for i, session := range sessions {
			appendTrayMenuItem(submenu, uintptr(cmdTrayRecentBase+i), traySessionMenuLabel(i, session))
		}
	}
	procAppendMenuW.Call(menu, mfPopup, submenu, uintptr(unsafe.Pointer(mustUTF16Ptr("最近对话"))))
}

func (t *trayHandle) setRecentCommands(sessions []traySession) {
	commands := make(map[int]traySession, len(sessions))
	for i, session := range sessions {
		commands[cmdTrayRecentBase+i] = session
	}
	t.mu.Lock()
	t.recentCommands = commands
	t.mu.Unlock()
}

func (t *trayHandle) recentSessionForCommand(command int) (traySession, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	session, ok := t.recentCommands[command]
	return session, ok
}

func appendTrayMenuItem(menu uintptr, id uintptr, label string) {
	appendTrayMenuItemWithFlags(menu, id, label, mfString)
}

func appendTrayMenuItemWithFlags(menu uintptr, id uintptr, label string, flags uintptr) {
	ptr, _ := windows.UTF16PtrFromString(label)
	procAppendMenuW.Call(menu, flags, id, uintptr(unsafe.Pointer(ptr)))
}

func mustUTF16Ptr(s string) *uint16 {
	ptr, _ := windows.UTF16PtrFromString(s)
	return ptr
}

func loadTrayIcon(instance windows.Handle) windows.Handle {
	if h := loadIconFromResource(instance); h != 0 {
		return h
	}
	for _, path := range trayIconCandidates() {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		ptr, _ := windows.UTF16PtrFromString(path)
		h, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(ptr)), imageIcon, 0, 0, lrLoadFromFile|lrDefaultSize)
		if h != 0 {
			return windows.Handle(h)
		}
	}
	h, _, _ := procLoadIconW.Call(0, uintptr(idiApplication))
	return windows.Handle(h)
}

func getModuleHandle() windows.Handle {
	h, _, _ := procGetModuleHandleW.Call(0)
	return windows.Handle(h)
}

func loadIconFromResource(instance windows.Handle) windows.Handle {
	h, _, _ := procLoadImageW.Call(uintptr(instance), 1, imageIcon, 0, 0, lrDefaultSize)
	return windows.Handle(h)
}

func trayIconCandidates() []string {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "icon.ico"),
			filepath.Join(dir, "build", "windows", "icon.ico"),
			filepath.Join(dir, "..", "build", "windows", "icon.ico"),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(cwd, "build", "windows", "icon.ico"),
			filepath.Join(cwd, "cmd", "pchat-gui", "build", "windows", "icon.ico"),
		)
	}
	return candidates
}

func copyUTF16(dst []uint16, s string) {
	u := syscall.StringToUTF16(s)
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}
