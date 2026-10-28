/**
 * Delight-3 turn summary — derive a one-line recap from assistant parts.
 * No backend change; counts are best-effort from tool names / args.
 */

import type { Message, MessagePart } from '../api/client'

export interface TurnSummary {
  filesWritten: number
  filesRead: number
  commands: number
  searches: number
  subAgents: number
  tools: number
  tokensIn: number
  tokensOut: number
  elapsed?: string
  /** Human one-liner; empty if nothing worth showing. */
  line: string
}

const WRITE_TOOLS = new Set(['write_file', 'edit_file', 'apply_patch'])
const READ_TOOLS = new Set(['read_file', 'list_files', 'grep', 'read_attachment'])
const EXEC_TOOLS = new Set(['exec_command', 'start_process'])
const SEARCH_TOOLS = new Set(['web_search', 'web_fetch', 'wiki_lookup', 'wiki_list'])

function walk(parts: MessagePart[] | undefined, visit: (p: MessagePart) => void) {
  if (!parts) return
  for (const p of parts) {
    visit(p)
    if (p.kind === 'sub_agent' && Array.isArray(p.parts)) walk(p.parts, visit)
  }
}

function pathFromArgs(args?: string): string {
  if (!args) return ''
  try {
    const obj = JSON.parse(args)
    const raw = obj.path || obj.file || obj.file_path || obj.target || ''
    return typeof raw === 'string' ? raw : ''
  } catch {
    return ''
  }
}

function formatTokens(n: number): string {
  if (n < 1000) return String(n)
  if (n < 10_000) return `${(n / 1000).toFixed(1)}k`
  return `${Math.round(n / 1000)}k`
}

export function buildTurnSummary(message: Message): TurnSummary {
  const written = new Set<string>()
  const read = new Set<string>()
  let commands = 0
  let searches = 0
  let subAgents = 0
  let tools = 0

  walk(message.parts, (p) => {
    if (p.kind === 'sub_agent') {
      subAgents += 1
      return
    }
    if (p.kind !== 'tool') return
    tools += 1
    const name = p.name || ''
    const path = pathFromArgs(p.args)
    if (WRITE_TOOLS.has(name)) {
      written.add(path || `${name}:${tools}`)
    } else if (READ_TOOLS.has(name)) {
      read.add(path || `${name}:${tools}`)
    } else if (EXEC_TOOLS.has(name)) {
      commands += 1
    } else if (SEARCH_TOOLS.has(name)) {
      searches += 1
    }
  })

  const tokensIn = message.tokens_in || 0
  const tokensOut = message.tokens_out || 0
  const bits: string[] = []
  if (written.size) bits.push(`改 ${written.size} 文件`)
  if (read.size) bits.push(`读 ${read.size} 处`)
  if (commands) bits.push(`跑 ${commands} 命令`)
  if (searches) bits.push(`检索 ${searches} 次`)
  if (subAgents) bits.push(`子代理 ${subAgents}`)
  if (!bits.length && tools) bits.push(`工具 ${tools} 次`)
  if (tokensOut || tokensIn) {
    bits.push(`约 ${formatTokens(tokensOut || tokensIn)} tokens`)
  }
  if (message.elapsed) bits.push(message.elapsed)

  const line = bits.length ? `本回合 · ${bits.join(' · ')}` : ''

  return {
    filesWritten: written.size,
    filesRead: read.size,
    commands,
    searches,
    subAgents,
    tools,
    tokensIn,
    tokensOut,
    elapsed: message.elapsed,
    line,
  }
}

/** Show summary when the turn did tool work or spent meaningful tokens. */
export function shouldShowTurnSummary(summary: TurnSummary): boolean {
  return summary.tools > 0 || summary.subAgents > 0 || (summary.tokensOut + summary.tokensIn) >= 200
}
