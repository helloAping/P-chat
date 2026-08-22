import assert from 'node:assert/strict'
import test from 'node:test'

import { dedupMessagesByKey } from '../src/utils/messageDedup.ts'
import type { Message } from '../src/api/client.ts'

test('dedupMessagesByKey merges optimistic user row with later seq-bearing row', () => {
  const optimisticID = 1770000000000123
  const existing: Message[] = [
    { id: 1, seq: 1, role: 'assistant', content: 'older' },
    { id: optimisticID, role: 'user', content: 'launch background work' },
  ]
  const incoming: Message[] = [
    { id: optimisticID, seq: 2, role: 'user', content: 'launch background work' },
    { id: 2, seq: 3, role: 'assistant', content: 'background hook', parts: [] },
  ]

  const merged = dedupMessagesByKey(existing, incoming)

  assert.equal(merged.filter((m) => m.id === optimisticID).length, 1)
  assert.deepEqual(merged.map((m) => m.content), [
    'older',
    'launch background work',
    'background hook',
  ])
  assert.equal(merged[1].seq, 2)
})

test('dedupMessagesByKey keeps seq-less live placeholders after loaded history', () => {
  const existing: Message[] = [
    { id: 1, seq: 1, role: 'assistant', content: 'older' },
    { role: 'assistant', content: '', parts: [] },
  ]
  const incoming: Message[] = [
    { id: 2, seq: 2, role: 'assistant', content: 'persisted hook', parts: [] },
  ]

  const merged = dedupMessagesByKey(existing, incoming)

  assert.deepEqual(merged.map((m) => m.content), ['older', 'persisted hook', ''])
})
