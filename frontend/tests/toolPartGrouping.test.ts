import assert from 'node:assert/strict'
import test from 'node:test'

import {
  groupConsecutiveToolParts,
  isRenderablePart,
} from '../src/utils/toolPartGrouping.ts'
import type { MessagePart } from '../src/api/client.ts'

test('isRenderablePart drops blank prose parts', () => {
  assert.equal(isRenderablePart({ kind: 'text', text: '' }), false)
  assert.equal(isRenderablePart({ kind: 'text', text: ' \n\t ' }), false)
  assert.equal(isRenderablePart({ kind: 'thinking', text: '\n' }), false)
  assert.equal(isRenderablePart({ kind: 'text', text: 'result' }), true)
})

test('groupConsecutiveToolParts skips blank text without breaking tool groups', () => {
  const parts: MessagePart[] = [
    { kind: 'tool', name: 'read_file', status: 'ok' },
    { kind: 'text', text: '\n\n' },
    { kind: 'tool', name: 'exec_command', status: 'ok' },
  ]

  const entries = groupConsecutiveToolParts(parts.map((part, index) => ({ part, index })))

  assert.equal(entries.length, 1)
  assert.equal(entries[0].kind, 'tool_group')
  if (entries[0].kind === 'tool_group') {
    assert.deepEqual(entries[0].indexes, [0, 2])
  }
})
