// 提示音 + 系统通知管理器
//
// 音效：Web Audio API 合成（零外部文件）
//   完成   → C5→E5 上行双音   (愉悦完成)
//   确认   → G4 短音          (请求注意)
//   提问   → E5 双短促音       (有新问题)
//   错误   → D5→B4 下行       (警告提示)
//
// 系统通知：Notification API + 点击自动聚焦窗口
//   Wails 桌面端 → WindowUnminimise → WindowShow → WindowSetAlwaysOnTop

let audioCtx: AudioContext | null = null

function getCtx(): AudioContext {
  if (!audioCtx) audioCtx = new AudioContext()
  if (audioCtx.state === 'suspended') audioCtx.resume()
  return audioCtx
}

function playTone(freq: number, duration: number, rampDown = 0.04) {
  try {
    const ctx = getCtx()
    const osc = ctx.createOscillator()
    const gain = ctx.createGain()
    osc.type = 'sine'
    osc.frequency.value = freq
    const t = ctx.currentTime
    gain.gain.setValueAtTime(0.25, t)
    gain.gain.exponentialRampToValueAtTime(0.001, t + duration - rampDown)
    osc.connect(gain)
    gain.connect(ctx.destination)
    osc.start(t)
    osc.stop(t + duration)
  } catch { /* autoplay blocked — silently ignore */ }
}

function playDoneSound()     { playTone(523, 0.18, 0.03); setTimeout(() => playTone(659, 0.22, 0.03), 160) }
function playConfirmSound()  { playTone(392, 0.28, 0.05) }
function playQuestionSound() { playTone(659, 0.12, 0.03); setTimeout(() => playTone(659, 0.12, 0.03), 180) }
function playErrorSound()    { playTone(587, 0.14, 0.03); setTimeout(() => playTone(494, 0.18, 0.04), 150) }

// ---- 窗口聚焦 ----

let _wailsCalled = false

function focusWindow() {
  const w = window as any
  // Wails runtime — 三层 API 确保桌面窗口弹出到最前
  if (w.runtime) {
    try { w.runtime.WindowUnminimise() }      catch {}
    try { w.runtime.WindowShow() }            catch {}
    try { w.runtime.WindowSetAlwaysOnTop(true) } catch {}
    setTimeout(() => {
      try { w.runtime.WindowSetAlwaysOnTop(false) } catch {}
    }, 300)
    _wailsCalled = true
  }
  if (!_wailsCalled) {
    try { window.focus() } catch {}
  }
}

// ---- 系统通知 ----

function sendNotification(title: string, body: string) {
  if (!('Notification' in window)) return
  const show = () => {
    try {
      const n = new Notification(title, { body, icon: '/app/favicon.ico' })
      n.onclick = () => { n.close(); focusWindow() }
    } catch { /* e.g. permission revoked after grant */ }
  }
  if (Notification.permission === 'granted') {
    show()
  } else if (Notification.permission === 'default') {
    Notification.requestPermission().then(p => { if (p === 'granted') show() })
  }
}

// ---- 静音控制 ----

let _mute = false
try { _mute = localStorage.getItem('pchat-mute') === '1' } catch {}

export const notifyManager = {
  get mute() { return _mute },
  set mute(v: boolean) {
    _mute = v
    try { localStorage.setItem('pchat-mute', v ? '1' : '0') } catch {}
  },

  play(type: 'done' | 'confirm' | 'question' | 'error') {
    if (_mute) return
    ;({ done: playDoneSound, confirm: playConfirmSound, question: playQuestionSound, error: playErrorSound })[type]()
  },

  notify(title: string, body: string) {
    if (_mute) return
    sendNotification(title, body)
  },

  /** 首次用户交互后触发，解锁 Web Audio（必须在点击/按键事件里调用） */
  unlock() {
    try { getCtx().resume() } catch {}
  },
}
