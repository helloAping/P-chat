import assert from 'node:assert/strict'
import test from 'node:test'

import { dataURLToBlobURL } from '../src/utils/mediaPreview.ts'

test('data URL preview strips encoding metadata from the Blob MIME type', async () => {
  const url = dataURLToBlobURL('data:image/png;base64,aGVsbG8=')

  assert.ok(url?.startsWith('blob:'))
  const blob = await (await fetch(url)).blob()
  assert.equal(blob.type, 'image/png')
  assert.equal(await blob.text(), 'hello')
  URL.revokeObjectURL(url)
})

test('media preview leaves ordinary URLs and malformed data URLs untouched', () => {
  assert.equal(dataURLToBlobURL('/api/v1/uploads/demo'), '/api/v1/uploads/demo')
  assert.equal(dataURLToBlobURL('data:image/png;base64'), 'data:image/png;base64')
})
