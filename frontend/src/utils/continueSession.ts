/**
 * Empty-chat "continue last" pick.
 * Chooses the newest real conversation in the current project list,
 * skipping the open draft and other blank sessions.
 */

import { displaySessionTitle } from '../im/sessionSource.ts'

export interface ContinueSessionInput {
  id: string
  title: string
  updated_at: number
  conversation_state?: string
  has_user_messages?: boolean
  user_message_count?: number
  pending_turn_count?: number
}

export interface ContinueSessionPick {
  id: string
  title: string
  /** Short relative time, empty when updated_at is missing. */
  when: string
}

const PLACEHOLDER_TITLES = new Set(['(新会话)', '新会话', '(无标题)', '无标题'])

function isBlankSession(session: ContinueSessionInput): boolean {
  return session.conversation_state === 'blank'
    && !session.id.startsWith('im:')
    && !session.has_user_messages
    && (session.user_message_count || 0) === 0
    && (session.pending_turn_count || 0) === 0
}

function cardTitle(session: ContinueSessionInput): string {
  const shown = displaySessionTitle(session.title || '', session.id).trim()
  if (!shown || PLACEHOLDER_TITLES.has(shown)) return '未命名对话'
  return shown
}

function whenLabel(updatedAt: number, now: number): string {
  if (!updatedAt || !Number.isFinite(updatedAt)) return ''
  const ms = updatedAt > 1e12 ? updatedAt : updatedAt * 1000
  const d = new Date(ms)
  if (Number.isNaN(d.getTime())) return ''
  const nowDate = new Date(now)
  const startOfToday = new Date(nowDate.getFullYear(), nowDate.getMonth(), nowDate.getDate()).getTime()
  const startOfThat = new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()
  const dayDiff = Math.round((startOfToday - startOfThat) / 86_400_000)
  const hm = d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false })
  if (dayDiff <= 0) return `今天 ${hm}`
  if (dayDiff === 1) return '昨天'
  if (dayDiff < 7) {
    const days = ['日', '一', '二', '三', '四', '五', '六']
    return `周${days[d.getDay()]}`
  }
  const month = String(d.getMonth() + 1).padStart(2, '0')
  const date = String(d.getDate()).padStart(2, '0')
  return `${month}/${date}`
}

/** Newest non-blank session other than the one currently open. */
export function pickContinueSession(
  sessions: ContinueSessionInput[],
  currentId: string,
  now = Date.now(),
): ContinueSessionPick | null {
  const ranked = [...sessions].sort((a, b) => (b.updated_at || 0) - (a.updated_at || 0))
  for (const session of ranked) {
    if (!session.id || session.id === currentId) continue
    if (isBlankSession(session)) continue
    return {
      id: session.id,
      title: cardTitle(session),
      when: whenLabel(session.updated_at, now),
    }
  }
  return null
}
