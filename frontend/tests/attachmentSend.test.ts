import assert from 'node:assert/strict'
import test from 'node:test'

import type { PendingAttachment } from '../src/stores/chat.ts'
import { planAttachmentSend } from '../src/utils/attachmentSend.ts'

function attachment(name: string, id: string, error = false): PendingAttachment {
  return { name, id, kind: 'file', mime: 'application/pdf', size: 1, _uploading: false, _error: error, _previewURL: '' }
}

test('failed PDFs require explicit consent before a text-only send', async () => {
  const failed = attachment('bad.pdf', '', true)
  const good = attachment('good.pdf', 'abcd1234567890ab')
  let confirmationNames: string[] = []
  const plan = await planAttachmentSend([failed, good], true, async docs => {
    confirmationNames = docs.map(doc => doc.name)
    return true
  })
  assert.deepEqual(confirmationNames, ['bad.pdf'])
  assert.equal(plan.status, 'ready')
  if (plan.status === 'ready') {
    assert.deepEqual(plan.attachments, [good])
    assert.deepEqual(plan.omitted, [failed])
  }
})

test('cancelling preserves the draft and no attachment is sent', async () => {
  const failed = attachment('bad.pdf', '', true)
  const plan = await planAttachmentSend([failed], true, async () => false)
  assert.equal(plan.status, 'cancelled')
})

test('failed PDF alone remains blocked without a text message', async () => {
  const failed = attachment('bad.pdf', '', true)
  let asked = false
  const plan = await planAttachmentSend([failed], false, async () => { asked = true; return true })
  assert.equal(plan.status, 'blocked')
  assert.equal(asked, false)
})
