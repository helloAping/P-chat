import assert from 'node:assert/strict'
import test from 'node:test'

import { renderMarkdown } from '../src/utils/markdownCache.ts'

test('renderMarkdown returns empty string for empty input', () => {
  assert.equal(renderMarkdown(''), '')
  assert.equal(renderMarkdown(undefined as unknown as string), '')
})

test('renderMarkdown renders markdown to HTML', () => {
  const html = renderMarkdown('**bold** and `code`')
  assert.match(html, /<strong>bold<\/strong>/)
  assert.match(html, /<code>code<\/code>/)
})

test('renderMarkdown is idempotent for the same text (cache path)', () => {
  const text = '# Heading\n\nparagraph\n\n- a\n- b\n\n> quote'
  assert.equal(renderMarkdown(text), renderMarkdown(text))
  // A different text must not poison earlier results.
  renderMarkdown('totally different content')
  assert.equal(renderMarkdown(text), renderMarkdown(text))
})

test('renderMarkdown honors breaks: true (single newline renders <br>)', () => {
  const html = renderMarkdown('line1\nline2')
  assert.ok(html.includes('<br>'), 'breaks option should emit <br> for a single newline')
})

test('renderMarkdown survives a large pathological payload', () => {
  const big = 'word '.repeat(80_000)
  const html = renderMarkdown(big)
  assert.ok(typeof html === 'string' && html.length > 0, 'should not throw on very large text')
})
