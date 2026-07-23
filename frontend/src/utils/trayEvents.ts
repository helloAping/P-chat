import { createSession, switchSession } from '../stores/chat'

type TraySwitchPayload = {
  session_id?: string
}

// setupTrayEventListeners wires native tray actions into the Vue store.
// setupTrayEventListeners 在 Wails 桌面端监听托盘事件；浏览器预览中安全空转。
export async function setupTrayEventListeners(): Promise<() => void> {
  let runtime: typeof import('../../wailsjs/runtime/runtime') | null = null
  try {
    runtime = await import('../../wailsjs/runtime/runtime')
    runtime.EventsOn('__pchat_tray_probe__', () => {})()
  } catch {
    return () => {}
  }

  const offFns: Array<() => void> = []
  const on = (name: string, cb: (...args: any[]) => void) => {
    if (!runtime) return
    try {
      offFns.push(runtime.EventsOn(name, cb))
    } catch {
      // Ignore missing runtime hooks in browser preview.
    }
  }

  on('tray:new-session', () => {
    createSession().catch((e) => console.warn('tray:new-session failed', e))
  })

  on('tray:switch-session', (payload?: TraySwitchPayload) => {
    const id = payload?.session_id
    if (!id) return
    switchSession(id).catch((e) => console.warn('tray:switch-session failed', e))
  })

  on('tray:show-session-picker', () => {
    // The Go side already restores the main window. The first version
    // only needs a stable event hook for future picker UI.
    // Go 侧已恢复窗口；这里先保留未来会话选择器入口。
  })

  return () => {
    for (const off of offFns.splice(0)) {
      try { off() } catch { /* ignore */ }
    }
  }
}
