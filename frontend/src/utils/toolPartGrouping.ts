import type { MessagePart, ToolPart } from '../api/client'

export type IndexedPart = {
  part: MessagePart
  index: number
}

export type PartRenderEntry =
  | {
      kind: 'part'
      part: MessagePart
      index: number
    }
  | {
      kind: 'tool_group'
      parts: ToolPart[]
      indexes: number[]
      startIndex: number
      endIndex: number
    }

export function isRenderablePart(part: MessagePart): boolean {
  if (part.kind === 'text' || part.kind === 'thinking') {
    return Boolean((part.text || '').trim())
  }
  return true
}

// 仅展示层分组：连续工具调用合并显示，原始 parts 顺序和数据不变。
// Display-only grouping: consecutive tool calls render together without
// changing the underlying parts array or stream order.
export function groupConsecutiveToolParts(entries: IndexedPart[], minGroupSize = 2): PartRenderEntry[] {
  const result: PartRenderEntry[] = []
  let pending: Array<{ part: ToolPart; index: number }> = []

  const flushPending = () => {
    if (pending.length >= minGroupSize) {
      result.push({
        kind: 'tool_group',
        parts: pending.map(entry => entry.part),
        indexes: pending.map(entry => entry.index),
        startIndex: pending[0].index,
        endIndex: pending[pending.length - 1].index,
      })
    } else {
      for (const entry of pending) {
        result.push({ kind: 'part', part: entry.part, index: entry.index })
      }
    }
    pending = []
  }

  for (const entry of entries) {
    if (!isRenderablePart(entry.part)) {
      continue
    }
    if (entry.part.kind === 'tool') {
      // question 工具由 QuestionTable 展示；无错误时跳过，避免与「LLM 提问」卡片重复。
      // Question tools render via QuestionTable; skip the tool card unless it has an error.
      if (entry.part.name === 'question') {
        flushPending()
        if (entry.part.error) {
          result.push({ kind: 'part', part: entry.part, index: entry.index })
        }
        continue
      }
      pending.push({ part: entry.part, index: entry.index })
      continue
    }
    flushPending()
    result.push({ kind: 'part', part: entry.part, index: entry.index })
  }

  flushPending()
  return result
}
