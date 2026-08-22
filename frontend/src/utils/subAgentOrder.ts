import type { MessagePart, SubAgentPart } from '../api/client.ts'

export function insertAsyncSubAgentAfterTaskTools(parts: MessagePart[], sub: SubAgentPart): void {
  const insertAt = findAsyncSubAgentInsertIndex(parts, sub)
  parts.splice(insertAt, 0, sub)
}

function findAsyncSubAgentInsertIndex(parts: MessagePart[], sub: SubAgentPart): number {
  const taskOrder: string[] = []
  const seen = new Set<string>()
  let lastTaskIndex = -1

  parts.forEach((part, index) => {
    if (part.kind !== 'tool' || part.name !== 'task') return
    lastTaskIndex = index
    for (const key of taskToolOrderKeys(part)) {
      if (seen.has(key)) continue
      seen.add(key)
      taskOrder.push(key)
    }
  })

  if (lastTaskIndex < 0) return parts.length

  const order = new Map<string, number>()
  taskOrder.forEach((key, index) => {
    if (!order.has(key)) order.set(key, index)
  })
  const newRank = asyncSubAgentRank(sub, order, taskOrder.length)
  let insertAt = lastTaskIndex + 1
  while (insertAt < parts.length) {
    const part = parts[insertAt]
    if (part.kind !== 'sub_agent' || part.runMode !== 'async') break
    const existingRank = asyncSubAgentRank(part, order, taskOrder.length)
    if (existingRank > newRank) break
    insertAt++
  }
  return insertAt
}

function taskToolOrderKeys(part: Extract<MessagePart, { kind: 'tool' }>): string[] {
  return [
    fieldFromJSON(part.args, ['task_id', 'taskId']),
    taskIDFromText(part.result),
    fieldFromJSON(part.args, ['description']),
  ].map(normalizeKey).filter(Boolean)
}

function asyncSubAgentRank(part: SubAgentPart, order: Map<string, number>, fallbackBase: number): number {
  for (const key of [part.taskId, part.task]) {
    const rank = order.get(normalizeKey(key))
    if (rank != null) return rank
  }
  return fallbackBase
}

function fieldFromJSON(raw: string | undefined, fields: string[]): string {
  if (!raw) return ''
  try {
    const payload = JSON.parse(raw) as Record<string, unknown>
    for (const field of fields) {
      const value = payload[field]
      if (typeof value === 'string') return value
    }
  } catch {
    return ''
  }
  return ''
}

function taskIDFromText(text: string | undefined): string {
  if (!text) return ''
  const marker = 'task_id='
  const start = text.indexOf(marker)
  if (start < 0) return ''
  const rest = text.slice(start + marker.length)
  const end = rest.search(/[,\s]/)
  return end >= 0 ? rest.slice(0, end) : rest
}

function normalizeKey(value: string | undefined): string {
  return (value || '').trim()
}
