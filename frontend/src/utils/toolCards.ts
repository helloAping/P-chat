import type { MessagePart, ToolPart } from '../api/client'

export interface ToolCardEvent {
  tool_id?: string
  tool_name: string
  tool_args?: string
  tool_status?: string
  tool_result?: string
  tool_error?: string
  tool_elapsed?: string
  tool_result_truncated?: boolean
  tool_result_full_len?: number
  tool_context_refs?: string[]
}

function isOpenTool(part: MessagePart, toolId: string | undefined, name: string): part is ToolPart {
  if (part.kind !== 'tool' || part.status !== 'start') return false
  if (toolId) return part.tool_id === toolId
  return part.name === name
}

// findOpenToolCard returns the live start card for this call.
// A tool_id matches anywhere in the list: several tools stream
// together, so the card is rarely the last part when the final
// start event arrives. Name matching stays last-part-only so two
// parallel calls of the same tool are not merged.
export function findOpenToolCard(parts: MessagePart[], toolId: string | undefined, name: string): ToolPart | undefined {
  if (toolId) {
    for (let i = 0; i < parts.length; i++) {
      const part = parts[i]
      if (isOpenTool(part, toolId, name)) return part
    }
    return undefined
  }
  const last = parts[parts.length - 1]
  if (last && isOpenTool(last, undefined, name)) return last
  return undefined
}

// settleOpenToolCards writes a terminal status onto the open card
// and drops any extra start cards that share the same tool_id.
// Those extras are the duplicate "执行中" rows left when a later
// start event did not find the preview card.
export function settleOpenToolCards(
  parts: MessagePart[],
  event: ToolCardEvent,
  apply: (card: ToolPart) => void,
): boolean {
  const toolId = event.tool_id
  const name = event.tool_name
  const matches: number[] = []
  if (toolId) {
    for (let i = 0; i < parts.length; i++) {
      if (isOpenTool(parts[i], toolId, name)) matches.push(i)
    }
  } else {
    for (let i = 0; i < parts.length; i++) {
      const part = parts[i]
      if (part.kind === 'tool' && part.status === 'start' && part.name === name) {
        matches.push(i)
        break
      }
    }
  }
  if (!matches.length) return false
  const keep = parts[matches[0]]
  if (keep.kind !== 'tool') return false
  apply(keep)
  for (let k = matches.length - 1; k >= 1; k--) {
    parts.splice(matches[k], 1)
  }
  return true
}
