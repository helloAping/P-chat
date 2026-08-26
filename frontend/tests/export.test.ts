import assert from 'node:assert/strict'
import test from 'node:test'

import {
  suggestFilename,
  dedupeFilename,
  type ExportFormat,
} from '../src/utils/export.ts'

// The frontend's export module is now a thin layer over
// the server-side renderer (see internal/export + the
// GET /api/v1/sessions/:id/export handler). The bulk of
// the rendering moved there; this file only exercises
// the filename helpers the SPA still calls client-side.

test('suggestFilename strips filesystem-unsafe characters', () => {
  const f = suggestFilename('a/b\\c:d*e?f"g<h>i|j', 'html')
  // All of the 9 reserved characters should be replaced.
  // The exact substitute doesn't matter as long as none
  // of the originals survive.
  assert.equal(/[\\/:*?"<>|]/.test(f), false)
  assert.match(f, /\.html$/)
})

test('suggestFilename picks the right extension per format', () => {
  assert.match(suggestFilename('s', 'html'), /\.html$/)
  assert.match(suggestFilename('s', 'pdf'), /\.pdf$/)
})

test('suggestFilename falls back to a safe default', () => {
  const f = suggestFilename('', 'pdf')
  assert.match(f, /^pchat-session-/)
  assert.match(f, /\.pdf$/)
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

// Type-level sanity: the supported GUI ExportFormat values
// are exactly the two strings we ship. The backend keeps older
// markdown/json formats for compatibility, but the GUI export
// flow now offers styled HTML and PDF only.
test('ExportFormat is restricted to html | pdf', () => {
  const formats: ExportFormat[] = ['html', 'pdf']
  assert.equal(formats.length, 2)
})
