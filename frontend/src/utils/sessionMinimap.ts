/**
 * Loaded-message scrubber marks. Only user turns, tool turns,
 * and error turns get a dot. Positions are relative to the
 * loaded list, not the full server history.
 */

import type { Message, MessagePart } from '../api/client'

export type MinimapKind = 'user' | 'tool' | 'error'

export interface MinimapMark {
  messageId: number
  kind: MinimapKind
  /** 0..1 along the loaded message list. */
  position: number
}

export const MINIMAP_MIN_MESSAGES = 8

function walk(parts: MessagePart[] | undefined, visit: (part: MessagePart) => void) {
  if (!parts) return
  for (const part of parts) {
    visit(part)
    if (part.kind === 'sub_agent') walk(part.parts, visit)
  }
}

function classify(message: Message): MinimapKind | null {
  if (message.role === 'user') return 'user'
  if (message.role !== 'assistant') return null
  let hasTool = false
  let hasError = false
  walk(message.parts, (part) => {
    if (part.kind === 'tool') {
      hasTool = true
      if (part.status === 'error' || part.status === 'blocked') hasError = true
    } else if (part.kind === 'sub_agent' && part.status === 'err') {
      hasError = true
    }
  })
  if (hasError) return 'error'
  if (hasTool) return 'tool'
  return null
}

export function buildMinimapMarks(messages: Message[]): MinimapMark[] {
  if (messages.length < MINIMAP_MIN_MESSAGES) return []
  const marks: MinimapMark[] = []
  const span = Math.max(1, messages.length - 1)
  messages.forEach((message, index) => {
    const id = message.id
    if (id == null || id <= 0) return
    const kind = classify(message)
    if (!kind) return
    marks.push({
      messageId: id,
      kind,
      position: index / span,
    })
  })
  return marks
}
