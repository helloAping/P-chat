/**
 * Delight-3 style greetings for empty sessions.
 * Keep copy short; never auto-send. Users can hide via localStorage.
 */

const GREETING_DISMISS_KEY = 'pchat-style-greeting-hidden'

const BY_STYLE: Record<string, string[]> = {
  cute: [
    '嗨～今天想一起捣鼓点什么？',
    '我在呢。丢个想法过来就好。',
    '准备好啦，轻轻点一张卡片，或直接打字。',
  ],
  guofeng: [
    '山风正好，今日想理哪一段代码？',
    '案已备妥，但说无妨。',
    '工欲善其事——先从一张卡片或一句需求起。',
  ],
  tech: [
    '会话就绪。描述目标，或选下方快捷入口。',
    '上下文已加载。直接下指令即可。',
    '待命中。优先路径：卡片引导 → 或自由输入。',
  ],
}

const FALLBACK = [
  '开始一个新对话吧——点卡片填提示，或直接输入。',
  '我在听。说说你想先解决什么。',
  '空会话最适合开干：解释项目、找 bug，或随便聊聊。',
]

export function isStyleGreetingHidden(): boolean {
  try {
    return localStorage.getItem(GREETING_DISMISS_KEY) === '1'
  } catch {
    return false
  }
}

export function setStyleGreetingHidden(hidden: boolean) {
  try {
    if (hidden) localStorage.setItem(GREETING_DISMISS_KEY, '1')
    else localStorage.removeItem(GREETING_DISMISS_KEY)
  } catch { /* ignore */ }
}

/** Pick a stable-enough greeting for the style (rotates daily). */
export function styleGreeting(styleId: string | undefined, projectName?: string): string {
  const id = (styleId || 'off').trim().toLowerCase()
  const pool = id && id !== 'off' ? (BY_STYLE[id] || FALLBACK) : FALLBACK
  const day = Math.floor(Date.now() / 86_400_000)
  const line = pool[Math.abs(day) % pool.length]
  if (projectName && projectName !== '全局') {
    return `${line}（当前项目：${projectName}）`
  }
  return line
}
