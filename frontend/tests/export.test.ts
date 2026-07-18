import assert from 'node:assert/strict'
import test from 'node:test'

import {
  exportMessages,
  suggestFilename,
  toHTML,
  toJSON,
  toMarkdown,
  type ExportFormat,
} from '../src/utils/export.ts'

// Minimal Message shape for unit tests. The real type
// (frontend/src/api/client.ts) is wider; we only need
// what export.ts actually reads.
function msg(over: Partial<{
  role: 'user' | 'assistant' | 'tool' | 'system'
  content: string
  parts: any[]
  thinking: string
  created_at: number
}>): any {
  return over
}

test('suggestFilename strips filesystem-unsafe characters', () => {
  const f = suggestFilename('a/b\\c:d*e?f"g<h>i|j', 'markdown')
  // All of the 9 reserved characters should be replaced.
  // The exact substitute doesn't matter as long as none
  // of the originals survive.
  assert.equal(/[\\/:*?"<>|]/.test(f), false)
  assert.match(f, /\.md$/)
})

test('suggestFilename picks the right extension per format', () => {
  assert.match(suggestFilename('s', 'markdown'), /\.md$/)
  assert.match(suggestFilename('s', 'json'), /\.json$/)
  assert.match(suggestFilename('s', 'html'), /\.html$/)
})

test('suggestFilename falls back to a safe default', () => {
  const f = suggestFilename('', 'markdown')
  assert.match(f, /^pchat-session-/)
  assert.match(f, /\.md$/)
})

test('toMarkdown emits a header + every message', () => {
  const messages = [
    msg({ role: 'user', content: 'hi' }),
    msg({ role: 'assistant', content: 'hello' }),
  ]
  const md = toMarkdown(messages, 'session-1')
  assert.match(md, /^# session-1/m)
  assert.match(md, /\*\*Messages\*\*: 2/)
  assert.match(md, /## 🧑 user/)
  assert.match(md, /## 🤖 assistant/)
  assert.match(md, /hi/)
  assert.match(md, /hello/)
})

test('toMarkdown renders thinking in a details block', () => {
  const messages = [
    msg({ role: 'assistant', content: 'final', thinking: 'let me think' }),
  ]
  const md = toMarkdown(messages, 's')
  assert.match(md, /<details>.*thinking.*<\/details>/s)
  assert.match(md, /let me think/)
  assert.match(md, /final/)
})

test('toMarkdown renders tool parts with status + args + result', () => {
  const messages = [
    msg({
      role: 'assistant',
      content: '',
      parts: [
        {
          kind: 'tool',
          name: 'read_file',
          status: 'ok',
          args: '{"path":"/etc/hosts"}',
          result: '127.0.0.1 localhost',
        },
      ],
    }),
  ]
  const md = toMarkdown(messages, 's')
  assert.match(md, /read_file/)
  assert.match(md, /ok/)
  assert.match(md, /"path"\s*:\s*"\/etc\/hosts"/)
  assert.match(md, /127\.0\.0\.1 localhost/)
})

test('toMarkdown renders sub_agent as a nested H3 + its parts', () => {
  const messages = [
    msg({
      role: 'assistant',
      content: '',
      parts: [
        {
          kind: 'sub_agent',
          task: 'list repo',
          status: 'ok',
          parts: [
            { kind: 'text', text: 'found 12 files' },
          ],
        },
      ],
    }),
  ]
  const md = toMarkdown(messages, 's')
  assert.match(md, /### 🤖 sub-agent: list repo/)
  assert.match(md, /found 12 files/)
})

test('toJSON emits the versioned envelope', () => {
  const messages = [
    msg({ role: 'user', content: 'hi' }),
    msg({ role: 'assistant', content: 'hello' }),
  ]
  const j = JSON.parse(toJSON(messages, 'session-x'))
  assert.equal(j.version, 'pchat-frontend-export/1')
  assert.equal(j.session.title, 'session-x')
  assert.equal(j.messages.length, 2)
  assert.equal(j.messages[0].role, 'user')
  assert.equal(j.messages[0].content, 'hi')
  assert.equal(j.messages[0].index, 1)
  assert.equal(j.messages[1].index, 2)
  // SQLite row id intentionally not exported.
  assert.equal('id' in j.messages[0], false)
})

test('toJSON flattens parts[].text into the denormalized content', () => {
  const messages = [
    msg({
      role: 'assistant',
      content: '',
      parts: [
        { kind: 'text', text: 'a' },
        { kind: 'text', text: 'b' },
        { kind: 'thinking', text: 'c' }, // thinking excluded
      ],
    }),
  ]
  const j = JSON.parse(toJSON(messages, 's'))
  assert.equal(j.messages[0].content, 'ab')
})

test('toHTML is a self-contained single file', () => {
  const messages = [msg({ role: 'user', content: 'hi' })]
  const html = toHTML(messages, 'session-z')
  // Self-contained: doctype + html + body, no <script src=...>
  assert.match(html, /^<!doctype html>/i)
  assert.match(html, /<html lang=/)
  assert.match(html, /<\/html>/)
  assert.equal(/<script src=/.test(html), false)
  // Has the session title in <title> and <h1>
  assert.match(html, /<title>session-z<\/title>/)
  assert.match(html, /<h1>session-z<\/h1>/)
  // CSS is inlined in <style>, not in an external <link>
  assert.match(html, /<style>[\s\S]+<\/style>/)
  assert.equal(/<link rel="stylesheet"/.test(html), false)
  // XSS-safe: the literal <script> string in user content
  // must be escaped, not rendered as a real tag.
  const xss = [msg({ role: 'user', content: '<script>alert(1)</script>' })]
  const xssHtml = toHTML(xss, 'x')
  assert.equal(/<script>alert/.test(xssHtml), false)
  assert.match(xssHtml, /&lt;script&gt;/)
})

test('exportMessages dispatches by format', () => {
  const messages = [msg({ role: 'user', content: 'x' })]
  assert.equal(exportMessages(messages, 'markdown', 's'), toMarkdown(messages, 's'))
  assert.equal(exportMessages(messages, 'json', 's'), toJSON(messages, 's'))
  assert.equal(exportMessages(messages, 'html', 's'), toHTML(messages, 's'))
})

test('exportMessages with empty message list still produces valid output', () => {
  for (const fmt of ['markdown', 'json', 'html'] as ExportFormat[]) {
    const body = exportMessages([], fmt, 'empty')
    assert.ok(body.length > 0, `format ${fmt} produced empty body`)
    if (fmt === 'json') {
      const j = JSON.parse(body)
      assert.equal(j.messages.length, 0)
    } else if (fmt === 'markdown') {
      assert.match(body, /\*\*Messages\*\*: 0/)
    } else if (fmt === 'html') {
      assert.match(body, /0 message\(s\)/)
    }
  }
})
