import assert from 'node:assert/strict'
import test from 'node:test'

import { isCurrentStream } from '../src/stores/streamLifecycle.ts'

test('a stopped stream cannot own a replacement stream', () => {
  const stopped = new AbortController()
  const replacement = new AbortController()

  assert.equal(isCurrentStream({ ctrl: replacement }, stopped), false)
  assert.equal(isCurrentStream({ ctrl: replacement }, replacement), true)

  replacement.abort()
  assert.equal(isCurrentStream({ ctrl: replacement }, replacement), false)
})
