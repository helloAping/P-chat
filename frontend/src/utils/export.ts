// export.ts — turn a Message[] into a downloadable Markdown / JSON
// / HTML string.
//
// The browser-side export complements the CLI /export command
// (internal/cli/export.go). The CLI operates on the raw
// llm.Message list, this module operates on the structured
// Message[] shape the chat store keeps in memory (which includes
// thinking blocks, tool calls, sub-agent runs, and question
// prompts that the CLI export flattens to a string).
//
// Pure functions: no DOM, no fetch, no file system. The caller
// wraps the returned string in a Blob + a download anchor.

import type { Message, MessagePart } from '../api/client'

export type ExportFormat = 'markdown' | 'json' | 'html'

const ESCAPE_HTML = (s: string) =>
  s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')

/** Extract the visible text from a Message, walking the parts
 *  array. Used for the JSON export's "content" denormalization
 *  and as a fallback when no parts exist. */
function flattenText(msg: Message): string {
  if (msg.parts && msg.parts.length) {
    return msg.parts
      .filter((p): p is Extract<MessagePart, { kind: 'text' }> => p.kind === 'text')
      .map(p => p.text)
      .join('')
  }
  return msg.content || ''
}

/** Render a single part to Markdown. */
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
      const outBlock = part.result
        ? `\n\n${indent}\`\`\`\n${indent}${part.result}\n${indent}\`\`\``
        : part.error
          ? `\n\n${indent}> ❌ ${part.error}`
          : ''
      return `${indent}${head}${argBlock}${outBlock}\n\n`
    }
    case 'sub_agent': {
      const head = `${indent}### 🤖 sub-agent: ${part.task} (${part.status})\n\n`
      const body = part.parts
        .map(p => partToMarkdown(p, depth + 1))
        .join('')
      return `${head}${body}`
    }
    case 'question': {
      // Don't dump raw question JSON into the export — render a
      // short notice so readers know the LLM asked something.
      return `${indent}> ❓ question (${part.question_status ?? 'open'})\n\n`
    }
    default:
      return ''
  }
}

/** Render one Message to a Markdown block. */
function messageToMarkdown(msg: Message, idx: number): string {
  const roleEmoji: Record<string, string> = {
    user: '🧑',
    assistant: '🤖',
    tool: '🔧',
    system: '⚙️',
  }
  const icon = roleEmoji[msg.role] ?? '•'
  const created = msg.created_at ? new Date(msg.created_at * 1000).toISOString() : ''
  const head = `## ${icon} ${msg.role} · msg #${idx + 1}${created ? ` · ${created}` : ''}\n\n`
  if (msg.parts && msg.parts.length) {
    return head + msg.parts.map(p => partToMarkdown(p)).join('') + '\n'
  }
  if (msg.thinking) {
    return (
      head +
      `<details><summary>💭 thinking</summary>\n\n${msg.thinking}\n\n</details>\n\n` +
      (msg.content ? `${msg.content}\n\n` : '')
    )
  }
  return head + (msg.content ? `${msg.content}\n\n` : '')
}

export function toMarkdown(messages: Message[], sessionTitle = ''): string {
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

/** JSON export — wraps the array with a versioned envelope so
 *  downstream tooling can detect + parse it. */
export function toJSON(messages: Message[], sessionTitle = ''): string {
  const envelope = {
    version: 'pchat-frontend-export/1',
    exported_at: new Date().toISOString(),
    session: { title: sessionTitle },
    messages: messages.map((m, i) => ({
      index: i + 1,
      role: m.role,
      msg_type: m.msg_type,
      content: flattenText(m),
      thinking: m.thinking,
      parts: m.parts ?? [],
      tool_call_id: m.tool_call_id,
      name: m.name,
      created_at: m.created_at,
      provider: m.provider,
      model: m.model,
      // Deliberately excluded: id (SQLite row id, useless off-device),
      // attachments (would need the original file blob to round-trip).
    })),
  }
  return JSON.stringify(envelope, null, 2)
}

/** Inline CSS for the HTML export. Kept tiny and self-contained
 *  so the file works as a single .html download. */
const HTML_STYLE = `
body { font-family: -apple-system, "Segoe UI", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif; max-width: 760px; margin: 24px auto; padding: 0 16px; line-height: 1.6; color: #1a1a1a; background: #fafafa; }
h1 { font-size: 1.6em; border-bottom: 1px solid #e5e5e5; padding-bottom: 8px; }
h2 { font-size: 1.2em; margin-top: 32px; }
.user { background: #eef6ff; padding: 8px 12px; border-radius: 8px; border-left: 3px solid #4a90e2; }
.assistant { background: #fff; padding: 8px 12px; border-radius: 8px; border-left: 3px solid #9b59b6; }
.tool { background: #fdf6e3; padding: 6px 10px; border-radius: 6px; border-left: 3px solid #b58900; margin: 8px 0; }
.sub_agent { background: #f0f7f0; padding: 8px 12px; border-radius: 8px; border-left: 3px solid #27ae60; margin: 8px 0; }
.thinking { background: #f4f4f4; padding: 6px 10px; border-radius: 6px; color: #555; font-style: italic; }
details { margin: 4px 0; }
pre { background: #f6f8fa; padding: 8px; border-radius: 4px; overflow-x: auto; }
code { background: #f6f8fa; padding: 1px 4px; border-radius: 3px; font-size: 0.9em; }
.meta { color: #888; font-size: 0.85em; }
hr { border: 0; border-top: 1px solid #e5e5e5; margin: 24px 0; }
`

function partToHTML(part: MessagePart, depth = 0): string {
  switch (part.kind) {
    case 'text':
      return ESCAPE_HTML(part.text).replace(/\n/g, '<br>')
    case 'thinking':
      return `<details class="thinking"><summary>💭 thinking</summary><div style="white-space:pre-wrap">${ESCAPE_HTML(part.text)}</div></details>`
    case 'tool': {
      const argHTML = part.args
        ? `<pre>${ESCAPE_HTML(part.args)}</pre>`
        : ''
      const outHTML = part.result
        ? `<pre>${ESCAPE_HTML(part.result)}</pre>`
        : part.error
          ? `<div style="color:#c0392b">❌ ${ESCAPE_HTML(part.error)}</div>`
          : ''
      return `<div class="tool" style="margin-left:${depth * 16}px"><strong>🔧 ${ESCAPE_HTML(part.name || 'tool')}</strong> <span class="meta">${ESCAPE_HTML(part.status)}</span>${argHTML}${outHTML}</div>`
    }
    case 'sub_agent': {
      const body = part.parts.map(p => partToHTML(p, depth + 1)).join('')
      return `<div class="sub_agent" style="margin-left:${depth * 16}px"><strong>🤖 ${ESCAPE_HTML(part.task)}</strong> <span class="meta">${ESCAPE_HTML(part.status)}</span><div>${body}</div></div>`
    }
    case 'question':
      return `<div class="thinking">❓ question (${ESCAPE_HTML(part.question_status ?? 'open')})</div>`
    default:
      return ''
  }
}

function messageToHTML(msg: Message, idx: number): string {
  const cls = msg.role
  const created = msg.created_at ? new Date(msg.created_at * 1000).toISOString() : ''
  const head = `<h2>${ESCAPE_HTML(msg.role)} · #${idx + 1}</h2><div class="meta">${ESCAPE_HTML(created)}</div>`
  let body: string
  if (msg.parts && msg.parts.length) {
    body = msg.parts.map(p => partToHTML(p)).join('')
  } else if (msg.thinking) {
    body = `<details class="thinking"><summary>💭 thinking</summary><div style="white-space:pre-wrap">${ESCAPE_HTML(msg.thinking)}</div></details>` + (msg.content ? `<div>${ESCAPE_HTML(msg.content).replace(/\n/g, '<br>')}</div>` : '')
  } else {
    body = `<div>${ESCAPE_HTML(msg.content || '').replace(/\n/g, '<br>')}</div>`
  }
  return `<section class="${cls}">${head}${body}</section>`
}

export function toHTML(messages: Message[], sessionTitle = ''): string {
  const head = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<title>${ESCAPE_HTML(sessionTitle || 'P-Chat session')}</title>
<style>${HTML_STYLE}</style>
</head>
<body>
<h1>${ESCAPE_HTML(sessionTitle || 'P-Chat session')}</h1>
<p class="meta">${messages.length} message(s) · exported ${new Date().toISOString()}</p>
<hr>
`
  return head + messages.map((m, i) => messageToHTML(m, i)).join('\n') + '\n</body>\n</html>\n'
}

/** Format picker → body string. */
export function exportMessages(
  messages: Message[],
  format: ExportFormat,
  sessionTitle = '',
): string {
  switch (format) {
    case 'markdown':
      return toMarkdown(messages, sessionTitle)
    case 'json':
      return toJSON(messages, sessionTitle)
    case 'html':
      return toHTML(messages, sessionTitle)
  }
}

/** Suggest a filename for the download based on session title +
 *  format. Strips characters that some filesystems (NTFS, APFS)
 *  reject. */
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
