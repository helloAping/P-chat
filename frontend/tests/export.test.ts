import assert from 'node:assert/strict'
import test from 'node:test'

import {
  exportMessages,
  suggestFilename,
  toJSON,
  toMarkdown,
  dedupeFilename,
  type ExportFormat,
} from '../src/utils/export.ts'
import {
  isDataURL,
  isBlobURL,
  isPlaceholderScreenshot,
  PLACEHOLDER_SCREENSHOT,
  resolveAttachment,
} from '../src/utils/attachments.ts'
import { sniffResult, formatResultForMarkdown } from '../src/utils/resultSniff.ts'

// Minimal Message shape for unit tests. The real type
// (frontend/src/api/client.ts) is wider; we only need
// what export.ts actually reads.
function msg(over: Partial<{
  role: 'user' | 'assistant' | 'tool' | 'system'
  content: string
  parts: any[]
  thinking: string
  attachments: any[]
  created_at: number
}>): any {
  return over
}

// === Filename helpers ============================================

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
  // No html export format anymore — the type system
  // blocks it, but the runtime fallback still returns
  // the extension verbatim, so we just assert the
  // supported set works.
})

test('suggestFilename falls back to a safe default', () => {
  const f = suggestFilename('', 'markdown')
  assert.match(f, /^pchat-session-/)
  assert.match(f, /\.md$/)
})

test('dedupeFilename appends -2, -3, ... on collision', () => {
  const taken = new Set<string>(['foo-2026-07-19.md'])
  const next = dedupeFilename('foo-2026-07-19.md', (p) => taken.has(p))
  assert.equal(next, 'foo-2026-07-19-2.md')
  taken.add(next)
  const next2 = dedupeFilename('foo-2026-07-19.md', (p) => taken.has(p))
  assert.equal(next2, 'foo-2026-07-19-3.md')
})

test('dedupeFilename passes through when the path is free', () => {
  const taken = new Set<string>()
  assert.equal(dedupeFilename('foo.md', (p) => taken.has(p)), 'foo.md')
})

// === Markdown — basic shape ======================================

test('toMarkdown emits a header + every message', () => {
  const messages = [
    msg({ role: 'user', content: 'hi' }),
    msg({ role: 'assistant', content: 'hello' }),
  ]
  const md = toMarkdown(messages as any, 'session-1')
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
  const md = toMarkdown(messages as any, 's')
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
  const md = toMarkdown(messages as any, 's')
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
  const md = toMarkdown(messages as any, 's')
  assert.match(md, /### 🤖 sub-agent: list repo/)
  assert.match(md, /found 12 files/)
})

// === Markdown — attachments =====================================

test('toMarkdown inlines image attachments as data: URLs', () => {
  const dataURL =
    'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=='
  const messages = [
    msg({
      role: 'user',
      content: '看这张图',
      attachments: [
        { type: 'image_url', url: dataURL, name: 'shot.png', mime: 'image/png' },
      ],
    }),
  ]
  // Pre-resolve (the same path exportMessages takes) so
  // the markdown formatter sees a data: URL.
  return resolveAttachment(messages[0].attachments[0]).then((resolved) => {
    const md = toMarkdown(
      [{ ...messages[0], _resolvedAttachments: [resolved] }] as any,
      's',
    )
    assert.match(md, /!\[shot\.png\]\(data:image\/png;base64,/)
    assert.match(md, /看这张图/)
  })
})

test('toMarkdown renders audio attachments as a link', () => {
  const messages = [
    msg({
      role: 'user',
      content: '听这段',
      attachments: [
        { type: 'audio_url', url: 'data:audio/mp3;base64,SUQz', name: 'song.mp3', mime: 'audio/mp3' },
      ],
    }),
  ]
  return resolveAttachment(messages[0].attachments[0]).then((resolved) => {
    const md = toMarkdown(
      [{ ...messages[0], _resolvedAttachments: [resolved] }] as any,
      's',
    )
    assert.match(md, /\[🔊 song\.mp3\]\(data:audio\/mp3/)
  })
})

test('toMarkdown renders video attachments as a link', () => {
  const messages = [
    msg({
      role: 'user',
      content: '',
      attachments: [
        { type: 'video_url', url: 'data:video/mp4;base64,AAAA', name: 'clip.mp4', mime: 'video/mp4' },
      ],
    }),
  ]
  return resolveAttachment(messages[0].attachments[0]).then((resolved) => {
    const md = toMarkdown(
      [{ ...messages[0], _resolvedAttachments: [resolved] }] as any,
      's',
    )
    assert.match(md, /\[🎬 clip\.mp4\]\(data:video\/mp4/)
  })
})

test('toMarkdown renders text attachments as a code block', () => {
  const messages = [
    msg({
      role: 'user',
      content: '',
      attachments: [
        { type: 'text', text: 'name,age\nalice,30\nbob,25', name: 'people.csv', mime: 'text/csv' },
      ],
    }),
  ]
  return resolveAttachment(messages[0].attachments[0]).then((resolved) => {
    const md = toMarkdown(
      [{ ...messages[0], _resolvedAttachments: [resolved] }] as any,
      's',
    )
    assert.match(md, /```csv/)
    assert.match(md, /alice,30/)
    assert.match(md, /bob,25/)
    assert.match(md, /\*— people\.csv\*/)
  })
})

test('toMarkdown renders the placeholder screenshot as a marker, not the literal text', () => {
  const messages = [
    msg({
      role: 'user',
      content: '看这里',
      attachments: [
        { type: 'image_url', url: PLACEHOLDER_SCREENSHOT, name: 'gone.png', mime: 'image/png' },
      ],
    }),
  ]
  return resolveAttachment(messages[0].attachments[0]).then((resolved) => {
    const md = toMarkdown(
      [{ ...messages[0], _resolvedAttachments: [resolved] }] as any,
      's',
    )
    // Marker present, literal placeholder absent from output.
    assert.match(md, /_\(截图已省略: gone\.png\)_/)
    assert.equal(md.includes(PLACEHOLDER_SCREENSHOT), false)
  })
})

// === JSON envelope ===============================================

test('toJSON emits the v2 envelope with version /2', () => {
  const messages = [
    msg({ role: 'user', content: 'hi' }),
    msg({ role: 'assistant', content: 'hello' }),
  ]
  const j = JSON.parse(toJSON(messages as any, 'session-x'))
  assert.equal(j.version, 'pchat-frontend-export/2')
  assert.equal(j.session.title, 'session-x')
  assert.equal(j.messages.length, 2)
  assert.equal(j.messages[0].role, 'user')
  assert.equal(j.messages[0].content, 'hi')
  assert.equal(j.messages[0].index, 1)
  assert.equal(j.messages[1].index, 2)
  // v2: attachments is an array (may be empty) on every
  // message — never omitted, so consumers can rely on
  // its presence.
  assert.ok(Array.isArray(j.messages[0].attachments))
  // SQLite row id intentionally not exported.
  assert.equal('id' in j.messages[0], false)
})

test('toJSON carries resolved attachments under the v2 attachments field', () => {
  const messages = [
    msg({
      role: 'user',
      content: '看',
      attachments: [
        { type: 'image_url', url: 'data:image/png;base64,ABCD', name: 'a.png', mime: 'image/png' },
      ],
    }),
  ]
  return resolveAttachment(messages[0].attachments[0]).then((resolved) => {
    const j = JSON.parse(
      toJSON([{ ...messages[0], _resolvedAttachments: [resolved] }] as any, 's'),
    )
    assert.equal(j.messages[0].attachments.length, 1)
    assert.equal(j.messages[0].attachments[0].type, 'image_url')
    assert.equal(j.messages[0].attachments[0].name, 'a.png')
    assert.equal(j.messages[0].attachments[0].url, 'data:image/png;base64,ABCD')
  })
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
  const j = JSON.parse(toJSON(messages as any, 's'))
  assert.equal(j.messages[0].content, 'ab')
})

// === exportMessages dispatcher (async) ===========================

test('exportMessages dispatches by format (async)', async () => {
  const messages = [msg({ role: 'user', content: 'x' })]
  // We can't assert byte-for-byte equality with the
  // synchronous helpers because exportMessages is now
  // async and the timestamp in the header changes
  // between the two calls. Assert structural
  // equivalence instead: same content body, same role
  // header, same envelope keys.
  const md = await exportMessages(messages as any, 'markdown', 's')
  const j = await exportMessages(messages as any, 'json', 's')
  assert.match(md, /^# s\b/)
  assert.match(md, /## 🧑 user · msg #1/)
  assert.match(md, /\bx\b/)
  const parsed = JSON.parse(j)
  assert.equal(parsed.version, 'pchat-frontend-export/2')
  assert.equal(parsed.session.title, 's')
  assert.equal(parsed.messages.length, 1)
  assert.equal(parsed.messages[0].role, 'user')
})

test('exportMessages with empty message list still produces valid output', async () => {
  for (const fmt of ['markdown', 'json'] as ExportFormat[]) {
    const body = await exportMessages([], fmt, 'empty')
    assert.ok(body.length > 0, `format ${fmt} produced empty body`)
    if (fmt === 'json') {
      const j = JSON.parse(body)
      assert.equal(j.messages.length, 0)
    } else if (fmt === 'markdown') {
      assert.match(body, /\*\*Messages\*\*: 0/)
    }
  }
})

// === attachment resolver (no DOM) ================================

test('isDataURL / isBlobURL / isPlaceholderScreenshot predicates', () => {
  assert.equal(isDataURL('data:image/png;base64,XXX'), true)
  assert.equal(isDataURL('blob:http://localhost/abc'), false)
  assert.equal(isDataURL('https://example.com/x.png'), false)
  assert.equal(isDataURL(''), false)
  assert.equal(isDataURL(undefined), false)

  assert.equal(isBlobURL('blob:http://localhost/abc'), true)
  assert.equal(isBlobURL('data:image/png;base64,XXX'), false)

  assert.equal(isPlaceholderScreenshot(PLACEHOLDER_SCREENSHOT), true)
  assert.equal(isPlaceholderScreenshot('something else'), false)
})

test('resolveAttachment passes a data: URL through unchanged', async () => {
  const r = await resolveAttachment({
    type: 'image_url',
    url: 'data:image/png;base64,ABCD',
    name: 'a.png',
    mime: 'image/png',
  } as any)
  assert.equal(r.url, 'data:image/png;base64,ABCD')
  assert.equal(r.resolvedFromBlob, false)
})

test('resolveAttachment keeps the placeholder screenshot verbatim', async () => {
  const r = await resolveAttachment({
    type: 'image_url',
    url: PLACEHOLDER_SCREENSHOT,
    name: 'gone.png',
    mime: 'image/png',
  } as any)
  assert.equal(r.url, PLACEHOLDER_SCREENSHOT)
  assert.equal(r.resolvedFromBlob, false)
})

test('resolveAttachment keeps https URLs verbatim (no fetch in tests)', async () => {
  const r = await resolveAttachment({
    type: 'image_url',
    url: 'https://example.com/cat.png',
    name: 'cat.png',
    mime: 'image/png',
  } as any)
  assert.equal(r.url, 'https://example.com/cat.png')
  assert.equal(r.resolvedFromBlob, false)
})

test('resolveAttachment routes text attachments into the url field', async () => {
  const r = await resolveAttachment({
    type: 'text',
    text: 'hello world',
    name: 'a.txt',
    mime: 'text/plain',
  } as any)
  assert.equal(r.url, 'hello world')
  assert.equal(r.type, 'text')
})

// === resultSniff =================================================

test('sniffResult classifies the four kinds correctly', () => {
  assert.equal(sniffResult('data:image/png;base64,ABCD'), 'image')
  assert.equal(
    sniffResult('https://example.com/cat.png'),
    'image',
  )
  assert.equal(sniffResult('{"foo": 1, "bar": [2,3]}'), 'json')
  assert.equal(sniffResult('function f() {\n  return 1\n}'), 'code')
  assert.equal(sniffResult('hello world'), 'text')
  assert.equal(sniffResult(''), 'text')
  assert.equal(sniffResult(undefined), 'text')
})

test('formatResultForMarkdown renders a base64 image as a markdown image', () => {
  const out = formatResultForMarkdown('data:image/png;base64,ABCD')
  assert.match(out, /!\[tool result\]\(data:image\/png;base64,ABCD\)/)
})

test('formatResultForMarkdown renders JSON with a json fence', () => {
  const out = formatResultForMarkdown('{"a":1}')
  assert.match(out, /```json/)
  assert.match(out, /"a":1/)
})

test('formatResultForMarkdown renders multi-line text as a blockquote', () => {
  const out = formatResultForMarkdown('line1\nline2')
  assert.match(out, /^> line1/m)
  assert.match(out, /^> line2/m)
})
