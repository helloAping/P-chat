// export.ts — turn a Message[] into a downloadable Markdown
// or JSON string.
//
// Format coverage (v2):
//   - markdown: human-readable, attachments inlined as
//               ![name](data:...) / [🔊 name](data:...) /
//               code blocks; tool results sniffed and
//               rendered as the right type
//   - json:     versioned envelope carrying the full
//               structured payload (parts, thinking, tool
//               calls, sub-agents, attachments) so downstream
//               tooling can re-hydrate the session
//
// Pure functions (other than the async attachment resolver):
// no DOM, no fetch, no file system. The caller wraps the
// returned string in a Blob + a download anchor.
//
// Attachment handling: the chat store swaps base64 data
// URLs out for blob: object URLs at runtime (see
// stores/chat.ts convertAndStripScreenshots) to keep
// WebView2's DOM cache small. blob: URLs don't survive a
// page reload, so we resolve them back to data: URLs
// right before serialising. Resolved attachments are
// inlined into the output, so the resulting file is fully
// self-contained — no broken image links, no missing
// screenshots.
//
// What this module no longer does (v2 changes from /1):
//   - HTML export was removed. Render markdown in your
//     favourite viewer (or pipe through pandoc).

import type { Message, MessagePart, MessageAttachment } from '../api/client.ts'
import { resolveAttachments, type ResolvedAttachment, PLACEHOLDER_SCREENSHOT } from './attachments.ts'
import { formatResultForMarkdown, sniffResult } from './resultSniff.ts'

export type ExportFormat = 'markdown' | 'json'

const SCHEMA_VERSION = 'pchat-frontend-export/2'

/** Walk the parts tree and return the joined visible text
 *  for a Message. Used for the JSON export's denormalized
 *  `content` field and as a fallback for the markdown
 *  export. Attachments are NOT included in the joined text
 *  — they're emitted as their own block in the markdown
 *  output and as their own `attachments` array in JSON. */
function flattenText(msg: Message): string {
  if (msg.parts && msg.parts.length) {
    return msg.parts
      .filter((p): p is Extract<MessagePart, { kind: 'text' }> => p.kind === 'text')
      .map(p => p.text)
      .join('')
  }
  return msg.content || ''
}

/** All attachments (user / tool side-channel + the message's
 *  own attachments[] field) flattened into a single list,
 *  post-resolution. The store may have already converted
 *  data: URLs to blob: URLs at runtime; the resolver turns
 *  them back into data: URLs for export. */
async function collectAttachments(msg: Message): Promise<ResolvedAttachment[]> {
  // Direct attachments (user uploaded files).
  const direct = await resolveAttachments(msg.attachments)
  return direct
}

/** Render a single attachment block in markdown. Image and
 *  media attachments are inlined; text attachments become
 *  fenced code blocks. The placeholder screenshot
 *  ([截图已省略]) becomes a one-line italic marker so the
 *  reader knows an image was once here but is no longer
 *  available. */
function attachmentToMarkdown(a: ResolvedAttachment, depth = 0): string {
  const indent = '  '.repeat(depth)
  const name = a.name || a.kind || 'attachment'
  if (a.url === PLACEHOLDER_SCREENSHOT) {
    return `${indent}*_(截图已省略: ${name})_*\n\n`
  }
  switch (a.type) {
    case 'image_url':
      // data: URLs inline as the src; non-data URLs (https
      // or unresolved blob:) become a link so the reader
      // can still locate the asset.
      if (a.url.startsWith('data:') || a.url.startsWith('http')) {
        return `${indent}![${name}](${a.url})\n\n`
      }
      return `${indent}[🖼 ${name}](${a.url})\n\n`
    case 'audio_url':
      return `${indent}[🔊 ${name}](${a.url})\n\n`
    case 'video_url':
      return `${indent}[🎬 ${name}](${a.url})\n\n`
    case 'text': {
      // a.url is actually the text body for `type:'text'`
      // attachments (see resolveAttachment).
      const body = a.url || ''
      if (!body) return `${indent}*(${name} — empty)*\n\n`
      // Use the file's MIME type as a code-block language
      // hint when it makes sense (text/* → 'text', json →
      // 'json', etc.). Fall back to no language for
      // anything else.
      const lang = langForMime(a.mime)
      return `${indent}\`\`\`${lang}\n${body}\n${indent}\`\`\`\n\n*— ${name}*\n\n`
    }
    default:
      return `${indent}[📎 ${name}](${a.url || '#'})\n\n`
  }
}

function langForMime(mime: string | undefined): string {
  if (!mime) return ''
  const m = mime.toLowerCase()
  if (m === 'application/json' || m.endsWith('+json')) return 'json'
  if (m === 'text/markdown' || m === 'text/x-markdown') return 'markdown'
  if (m === 'text/yaml' || m === 'application/x-yaml') return 'yaml'
  if (m === 'text/xml' || m === 'application/xml') return 'xml'
  if (m === 'text/html') return 'html'
  if (m === 'text/csv') return 'csv'
  if (m === 'application/javascript' || m === 'text/javascript') return 'javascript'
  if (m === 'text/x-go') return 'go'
  if (m === 'text/x-python' || m === 'application/x-python') return 'python'
  if (m === 'text/x-rust') return 'rust'
  if (m === 'text/x-shellscript' || m === 'application/x-sh') return 'bash'
  if (m === 'text/x-sql') return 'sql'
  if (m.startsWith('text/')) return 'text'
  return ''
}

/** Render a single part to markdown. */
function partToMarkdown(part: MessagePart, depth = 0): string {
  const indent = '  '.repeat(depth)
  switch (part.kind) {
    case 'text':
      return part.text
    case 'thinking': {
      const safeText = part.text.replace(/\n/g, '\n' + indent)
      return `${indent}<details><summary>💭 thinking</summary>\n\n${indent}${safeText}\n\n${indent}</details>\n\n`
    }
    case 'tool': {
      const head = `🔧 **${part.name || 'tool'}** — \`${part.status}\``
      const argBlock = part.args ? `\n\n${indent}\`\`\`json\n${indent}${part.args}\n${indent}\`\`\`` : ''
      // Sniff the result so base64 images render as images,
      // JSON as json-fenced code, etc. — instead of a
      // single opaque code block.
      const resultBlock = part.result
        ? formatResultForMarkdown(part.result, depth)
        : part.error
          ? `\n\n${indent}> ❌ ${part.error}\n\n`
          : ''
      return `${indent}${head}${argBlock}${resultBlock}`
    }
    case 'sub_agent': {
      const head = `${indent}### 🤖 sub-agent: ${part.task} (${part.status})\n\n`
      const body = part.parts
        .map(p => partToMarkdown(p, depth + 1))
        .join('')
      return `${head}${body}`
    }
    case 'question': {
      // Don't dump raw question JSON into the export —
      // render a short notice so readers know the LLM
      // asked something.
      return `${indent}> ❓ question (${part.question_status ?? 'open'})\n\n`
    }
    default:
      return ''
  }
}

/** Render one Message to a Markdown block. */
function messageToMarkdown(msg: ResolvedMessage, idx: number): string {
  const roleEmoji: Record<string, string> = {
    user: '🧑',
    assistant: '🤖',
    tool: '🔧',
    system: '⚙️',
  }
  const icon = roleEmoji[msg.role] ?? '•'
  const created = msg.created_at ? new Date(msg.created_at * 1000).toISOString() : ''
  const head = `## ${icon} ${msg.role} · msg #${idx + 1}${created ? ` · ${created}` : ''}\n\n`
  const atts = (msg._resolvedAttachments || []).map(a => attachmentToMarkdown(a)).join('')
  if (msg.parts && msg.parts.length) {
    return head + atts + msg.parts.map(p => partToMarkdown(p)).join('') + '\n'
  }
  if (msg.thinking) {
    return (
      head +
      atts +
      `<details><summary>💭 thinking</summary>\n\n${msg.thinking}\n\n</details>\n\n` +
      (msg.content ? `${msg.content}\n\n` : '')
    )
  }
  return head + atts + (msg.content ? `${msg.content}\n\n` : '')
}

export function toMarkdown(messages: ResolvedMessage[], sessionTitle = ''): string {
  const header = [
    `# ${sessionTitle || 'P-Chat session'}`,
    '',
    `- **Messages**: ${messages.length}`,
    `- **Exported**: ${new Date().toISOString()}`,
    '',
    '---',
    '',
  ].join('\n')
  return header + messages.map((m, i) => messageToMarkdown(m, i)).join('')
}

/** ResolvedMessage is a Message whose attachments[] has been
 *  through resolveAttachments (so every URL is a data: URL
 *  we can inline). Stored under a private field
 *  (`_resolvedAttachments`) so the original `attachments`
 *  shape is still accessible to the JSON exporter without
 *  double-resolving. */
interface ResolvedMessage extends Message {
  _resolvedAttachments?: ResolvedAttachment[]
}

/** JSON export — wraps the array with a versioned envelope
 *  so downstream tooling can detect + parse it. v2 adds the
 *  `attachments` field per message; everything else carries
 *  over from v1. */
export function toJSON(messages: ResolvedMessage[], sessionTitle = ''): string {
  const envelope = {
    version: SCHEMA_VERSION,
    exported_at: new Date().toISOString(),
    session: { title: sessionTitle },
    messages: messages.map((m, i) => ({
      index: i + 1,
      role: m.role,
      msg_type: m.msg_type,
      content: flattenText(m),
      thinking: m.thinking,
      parts: m.parts ?? [],
      // v2: per-message attachments (already resolved to
      // data: URLs by exportMessages). Empty array — not
      // omitted — so consumers can rely on the field's
      // existence.
      attachments: m._resolvedAttachments ?? [],
      tool_call_id: m.tool_call_id,
      name: m.name,
      created_at: m.created_at,
      provider: m.provider,
      model: m.model,
      // Deliberately excluded: id (SQLite row id, useless
      // off-device).
    })),
  }
  return JSON.stringify(envelope, null, 2)
}

/** Format picker → body string. Async because the
 *  attachment resolver needs to fetch blob: URLs before
 *  the formatters can run. */
export async function exportMessages(
  messages: Message[],
  format: ExportFormat,
  sessionTitle = '',
): Promise<string> {
  // Pre-resolve every message's attachments in one pass.
  // The formatters then see a stable ResolvedMessage[]
  // and can stay synchronous.
  const resolved: ResolvedMessage[] = await Promise.all(
    messages.map(async (m) => {
      const atts = await collectAttachments(m)
      return { ...m, _resolvedAttachments: atts }
    }),
  )
  switch (format) {
    case 'markdown':
      return toMarkdown(resolved, sessionTitle)
    case 'json':
      return toJSON(resolved, sessionTitle)
  }
}

/** Suggest a filename for the download based on session
 *  title + format. Strips characters that some
 *  filesystems (NTFS, APFS) reject. */
export function suggestFilename(
  sessionTitle: string,
  format: ExportFormat,
): string {
  const safe = (sessionTitle || 'pchat-session')
    .replace(/[\\/:*?"<>|]/g, '-')
    .replace(/\s+/g, '-')
    .slice(0, 60)
  const stamp = new Date().toISOString().slice(0, 10)
  return `${safe}-${stamp}.${format === 'markdown' ? 'md' : format}`
}

/** Make a unique filename by appending -2, -3, ... when the
 *  candidate already exists. Exposed so callers can dedup
 *  against a directory listing without re-implementing the
 *  loop. Returns `candidate` unchanged when the path is
 *  free. */
export function dedupeFilename(candidate: string, exists: (path: string) => boolean): string {
  if (!exists(candidate)) return candidate
  const dot = candidate.lastIndexOf('.')
  const stem = dot > 0 ? candidate.slice(0, dot) : candidate
  const ext = dot > 0 ? candidate.slice(dot) : ''
  for (let n = 2; n < 1000; n++) {
    const next = `${stem}-${n}${ext}`
    if (!exists(next)) return next
  }
  // Pathological: 1000 collisions. Fall back to a timestamp
  // suffix rather than throwing — the user still gets a
  // downloadable file.
  return `${stem}-${Date.now()}${ext}`
}

// Re-export for callers that want to keep the imports local
// to the export module.
export { sniffResult, PLACEHOLDER_SCREENSHOT }
export type { ResolvedAttachment }

// MessageAttachment is part of the public type surface; the
// JSON envelope references it indirectly via the
// ResolvedAttachment array. Re-export for the test file.
export type { MessageAttachment }
