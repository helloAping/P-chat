/**
 * Right-edge ticks for user turns in the loaded list.
 * Earlier turns get a longer bar. Positions follow the
 * loaded message order, not the full server history.
 */

import type { Message, MessagePart } from '../api/client'

export interface MinimapMark {
  messageId: number
  /** 0..1 along the loaded message list. */
  position: number
  /** 1 = earliest (longest bar), 0 = latest. */
  span: number
  tip: string
}

const TIP_LIMIT = 80

function userText(message: Message): string {
  const fromParts = (message.parts || [])
    .filter((part): part is Extract<MessagePart, { kind: 'text' }> => part.kind === 'text')
    .map(part => part.text || '')
    .join(' ')
  return (message.content || fromParts).replace(/\s+/g, ' ').trim()
}

function tipFor(message: Message): string {
  const text = userText(message)
  if (!text) return '用户消息'
  return text.length > TIP_LIMIT ? `${text.slice(0, TIP_LIMIT)}…` : text
}

export function buildMinimapMarks(messages: Message[]): MinimapMark[] {
  const users = messages
    .map((message, index) => ({ message, index }))
    .filter(item => item.message.role === 'user' && (item.message.id ?? 0) > 0)
  if (users.length < 1) return []
  const listSpan = Math.max(1, messages.length - 1)
  const ageSpan = Math.max(1, users.length - 1)
  return users.map((item, order) => ({
    messageId: item.message.id as number,
    position: item.index / listSpan,
    span: 1 - order / ageSpan,
    tip: tipFor(item.message),
  }))
}
