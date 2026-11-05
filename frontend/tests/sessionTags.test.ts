import assert from 'node:assert/strict'
import test from 'node:test'

import { normalizeSessionTags } from '../src/utils/sessionTags.ts'

test('session tags trim blanks and keep the first copy', () => {
  assert.deepEqual(normalizeSessionTags([' 排查 ', '前端', '排查', ' ']), { tags: ['排查', '前端'] })
})

test('session tags reject an overlong label and a ninth tag', () => {
  assert.equal(normalizeSessionTags(['一二三四五六七八九十一二三四五六七']).error?.includes('16'), true)
  const nine = ['1', '2', '3', '4', '5', '6', '7', '8', '9']
  assert.equal(normalizeSessionTags(nine).error?.includes('8'), true)
})
