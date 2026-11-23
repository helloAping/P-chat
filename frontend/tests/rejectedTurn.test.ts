import assert from 'node:assert/strict'
import test from 'node:test'

import type { Message } from '../src/api/client.ts'
import type { PendingAttachment } from '../src/stores/chat.ts'
import { mergeRejectedDraft, removeUnacceptedLocalTurn, restoreRejectedAttachments } from '../src/utils/rejectedTurn.ts'

test('initial attachment rejection removes only the optimistic turn and empty assistant placeholder', () => {
  const prior = { id: 1, role: 'user', content: 'earlier' } as Message
  const optimistic = { id: 123, role: 'user', content: 'remember PDFs', attachments: [{ type: 'text', url: 'blob:local-pdf' }] } as Message
  const placeholder = { role: 'assistant', content: '', parts: [] } as Message
  const messages = [prior, optimistic, placeholder]

  assert.equal(removeUnacceptedLocalTurn(messages, 123), true)
  assert.deepEqual(messages, [prior])
  assert.equal(removeUnacceptedLocalTurn(messages, 123), false)
})

test('rejected turn restores durable attachment IDs and a usable local preview', () => {
  const file = new File(['pdf bytes'], 'broken.pdf', { type: 'application/pdf' })
  const staleURL = URL.createObjectURL(file)
  URL.revokeObjectURL(staleURL)
  const staged: PendingAttachment = {
    id: 'abcd1234567890ab', name: 'broken.pdf', size: file.size,
    mime: 'application/pdf', kind: 'file', _file: file, _blobURL: staleURL,
    _previewURL: staleURL, _uploading: false, _error: false,
  }
  const addedLater: PendingAttachment = {
    id: 'abcd1234567890ac', name: 'later.pdf', size: 1,
    mime: 'application/pdf', kind: 'file', _previewURL: '',
    _uploading: false, _error: false,
  }
  const restored = restoreRejectedAttachments([staged], [addedLater], '以下附件无效："broken.pdf"：PDF 文件不完整')
  assert.equal(restored.length, 2)
  assert.equal(restored[0].id, staged.id)
  assert.equal(restored[1], addedLater)
  assert.notEqual(restored[0]._blobURL, staleURL)
  assert.equal(restored[0]._previewURL, restored[0]._blobURL)
  assert.equal(restored[0]._error, true)
  assert.match(restored[0]._errorMessage || '', /重新上传/)
  assert.equal(restored[1]._error, false)
  if (restored[0]._blobURL) URL.revokeObjectURL(restored[0]._blobURL)
})

test('rejected text is restored without overwriting anything typed while the request was pending', () => {
  assert.equal(mergeRejectedDraft('remember PDFs', ''), 'remember PDFs')
  assert.equal(mergeRejectedDraft('remember PDFs', 'new thought'), 'remember PDFs\nnew thought')
  assert.equal(mergeRejectedDraft('', 'new thought'), 'new thought')
})
