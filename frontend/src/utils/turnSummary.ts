/**
 * Delight-3 turn summary — derive a one-line recap from assistant parts.
 * No backend change; counts are best-effort from tool names / args.
 */

import type { Message, MessagePart } from '../api/client'

export interface TurnSummary {
  filesWritten: number
  filesRead: number
  /** Paths this turn actually wrote. Dry-runs are omitted. */
  files: string[]
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
  const paths = pathsFromToolArgs('', args)
  return paths[0] || ''
}

function pushPath(out: string[], seen: Set<string>, raw: unknown) {
  if (typeof raw !== 'string') return
  const path = raw.trim()
  if (!path || seen.has(path)) return
  seen.add(path)
  out.push(path)
}

/** Paths a write/edit/patch call intended to change. */
export function pathsFromToolArgs(name: string, args?: string): string[] {
  if (!args) return []
  let obj: Record<string, unknown>
  try {
    const parsed = JSON.parse(args)
    if (!parsed || typeof parsed !== 'object') return []
    obj = parsed as Record<string, unknown>
  } catch {
    return []
  }
  if (obj.dry_run === true) return []
  const out: string[] = []
  const seen = new Set<string>()
  pushPath(out, seen, obj.path || obj.file || obj.file_path || obj.target)
  if (Array.isArray(obj.operations)) {
    for (const op of obj.operations) {
      if (op && typeof op === 'object') pushPath(out, seen, (op as { path?: unknown }).path)
    }
  }
  if (name === 'apply_patch' && typeof obj.patch === 'string') {
    const matches = obj.patch.matchAll(/^\*\*\* (?:Update|Add|Delete) File:\s*(.+)$/gm)
    for (const match of matches) pushPath(out, seen, match[1])
  }
  return out
}

/** Join a tool path onto the active project when it is not already absolute. */
export function resolveTurnPath(raw: string, projectRoot: string): string {
  const path = raw.trim()
  if (!path) return ''
  if (/^[a-zA-Z]:[\\/]/.test(path) || path.startsWith('\\\\') || path.startsWith('/')) return path
  const root = projectRoot.trim()
  if (!root) return path
  const sep = root.includes('\\') ? '\\' : '/'
  return `${root.replace(/[\\/]+$/, '')}${sep}${path.replace(/^[\\/]+/, '').replace(/\//g, sep)}`
}

export function turnFileLabel(path: string): string {
  const normalized = path.replace(/\\/g, '/')
  const slash = normalized.lastIndexOf('/')
  return slash >= 0 ? (normalized.slice(slash + 1) || path) : path
}

function formatTokens(n: number): string {
  if (n < 1000) return String(n)
  if (n < 10_000) return `${(n / 1000).toFixed(1)}k`
  return `${Math.round(n / 1000)}k`
}

export function buildTurnSummary(message: Message): TurnSummary {
  const written = new Set<string>()
  const read = new Set<string>()
  let anonymousWrites = 0
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
      const paths = pathsFromToolArgs(name, p.args)
      if (paths.length) paths.forEach(item => written.add(item))
      else if (!p.args || !/"dry_run"\s*:\s*true/.test(p.args)) anonymousWrites += 1
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
  const filesWritten = written.size + anonymousWrites
  if (filesWritten) bits.push(`改 ${filesWritten} 文件`)
  if (read.size) bits.push(`读 ${read.size} 处`)
  if (commands) bits.push(`跑 ${commands} 命令`)
  if (searches) bits.push(`检索 ${searches} 次`)
  if (subAgents) bits.push(`子代理 ${subAgents}`)
  if (!bits.length && tools) bits.push(`工具 ${tools} 次`)
  if (tokensOut || tokensIn) {
    bits.push(`约 ${formatTokens(tokensOut || tokensIn)} tokens`)
  }
  if (message.cache_hit_tokens != null || message.cache_miss_tokens != null) {
    const hit = message.cache_hit_tokens ?? 0
    const miss = message.cache_miss_tokens ?? 0
    const total = hit + miss
    if (total > 0) {
      bits.push(`cache ${Math.round((hit / total) * 100)}%`)
    }
  }
  if (message.elapsed) bits.push(message.elapsed)

  const line = bits.length ? `本回合 · ${bits.join(' · ')}` : ''

  return {
    filesWritten,
    filesRead: read.size,
    files: [...written],
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
